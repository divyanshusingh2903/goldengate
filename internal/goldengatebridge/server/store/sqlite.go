package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)

	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

// SQLite is the self-host Store: one file on one volume. Search uses FTS5.
type SQLite struct{ db *sql.DB }

// In every table the project column holds a project id (projects.id). For
// data stored before projects had ids, the id is the old project key.
// sessions.member is the key of the folder or chat grouping a session came
// from; it is added by migrate so databases created earlier upgrade in place.
const sqliteSchema = `
CREATE TABLE IF NOT EXISTS projects(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sessions(
  source TEXT NOT NULL, session_id TEXT NOT NULL, project TEXT NOT NULL,
  updated_at INTEGER NOT NULL, ingested_at INTEGER NOT NULL, payload TEXT NOT NULL,
  PRIMARY KEY(source, session_id));
CREATE INDEX IF NOT EXISTS sessions_project_updated ON sessions(project, updated_at DESC);
CREATE INDEX IF NOT EXISTS sessions_updated ON sessions(updated_at DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(
  text, source UNINDEXED, session_id UNINDEXED, project UNINDEXED, tokenize='unicode61 remove_diacritics 2');
CREATE TABLE IF NOT EXISTS work_items(
  project TEXT NOT NULL, kind TEXT NOT NULL, provider TEXT NOT NULL, key TEXT NOT NULL,
  item TEXT NOT NULL, status TEXT NOT NULL, observed_at INTEGER NOT NULL, first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
  PRIMARY KEY(project, kind, provider, key));
CREATE INDEX IF NOT EXISTS work_items_observed ON work_items(observed_at DESC);
CREATE TABLE IF NOT EXISTS work_item_sessions(
  project TEXT NOT NULL, kind TEXT NOT NULL, provider TEXT NOT NULL, key TEXT NOT NULL,
  source TEXT NOT NULL, session_id TEXT NOT NULL,
  PRIMARY KEY(project, kind, provider, key, source, session_id));
CREATE INDEX IF NOT EXISTS wis_session ON work_item_sessions(source, session_id);
CREATE TABLE IF NOT EXISTS tokens(
  id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, scope TEXT NOT NULL, label TEXT NOT NULL,
  created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL,
  client_id TEXT NOT NULL DEFAULT '', family TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS oauth_clients(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, secret_hash TEXT NOT NULL, redirect_uris TEXT NOT NULL,
  created_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS oauth_txns(hash TEXT PRIMARY KEY, data TEXT NOT NULL, client_id TEXT NOT NULL, expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS oauth_codes(
  hash TEXT PRIMARY KEY, data TEXT NOT NULL, client_id TEXT NOT NULL, family TEXT NOT NULL, expires_at INTEGER NOT NULL, used INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS oauth_refresh(
  hash TEXT PRIMARY KEY, data TEXT NOT NULL, client_id TEXT NOT NULL, family TEXT NOT NULL, expires_at INTEGER NOT NULL, used INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS oauth_refresh_family ON oauth_refresh(family);
CREATE TABLE IF NOT EXISTS agents(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at INTEGER NOT NULL, max_days INTEGER NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX IF NOT EXISTS agents_name ON agents(name COLLATE NOCASE);
CREATE TABLE IF NOT EXISTS agent_grants(
  agent_id TEXT NOT NULL, project_id TEXT NOT NULL, created_at INTEGER NOT NULL,
  PRIMARY KEY(agent_id, project_id));
CREATE INDEX IF NOT EXISTS agent_grants_project ON agent_grants(project_id);
CREATE TABLE IF NOT EXISTS pairings(
  hash TEXT PRIMARY KEY, expires_at INTEGER NOT NULL, failures INTEGER NOT NULL DEFAULT 0, burned INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS audit(
  id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, token_id TEXT NOT NULL, scope TEXT NOT NULL,
  action TEXT NOT NULL, project TEXT NOT NULL, ip TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS audit_at ON audit(at);
`

// OpenSQLite opens (creating, mode 0600) the database at path.
func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("sqlite store: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: create %s: %w", path, err)
	}
	_ = f.Close()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("sqlite store: open: %w", err)
	}
	db.SetMaxOpenConns(1) // one writer; keeps merges serialized without extra locking
	if _, err := db.ExecContext(ctx, sqliteSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite store: schema: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

// migrate upgrades databases created by earlier versions in place.
func migrate(ctx context.Context, db *sql.DB) error {
	have, err := columns(ctx, db, "tokens")
	if err != nil {
		return err
	}
	for _, col := range []string{"client_id", "family", "agent_id"} {
		if !have[col] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE tokens ADD COLUMN `+col+` TEXT NOT NULL DEFAULT ''`); err != nil { //nolint:gosec // G202: column names are constants
				return fmt.Errorf("sqlite store: migrate: %w", err)
			}
		}
	}
	// Named projects: every distinct project key that sessions were stored
	// under becomes a project whose id and name are that key, and each
	// session's member is the key it was stored under. Both statements only
	// touch rows that still need it, so this is safe to run at every start.
	if have, err = columns(ctx, db, "sessions"); err != nil {
		return err
	}
	if !have["member"] {
		if _, err := db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN member TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("sqlite store: migrate: %w", err)
		}
	}
	for _, stmt := range []string{
		`UPDATE sessions SET member=project WHERE member=''`,
		`INSERT OR IGNORE INTO projects(id, name, created_at, updated_at)
			SELECT project, project, MIN(ingested_at), MIN(ingested_at) FROM sessions GROUP BY project`,
		`CREATE INDEX IF NOT EXISTS sessions_member ON sessions(member)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlite store: migrate: %w", err)
		}
	}
	return migrateAgents(ctx, db)
}

// migrateAgents gives readers that predate agents an owner. Read tokens and
// OAuth clients without an agent become the default agent's, and, because
// they could see everything until now, that agent is granted every project
// that exists at this moment. Afterwards no reader is without an agent, so
// this never grants anything again: projects that arrive later are granted
// to no one.
func migrateAgents(ctx context.Context, db *sql.DB) error {
	have, err := columns(ctx, db, "oauth_clients")
	if err != nil {
		return err
	}
	if !have["agent_id"] {
		if _, err := db.ExecContext(ctx, `ALTER TABLE oauth_clients ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("sqlite store: migrate: %w", err)
		}
	}
	var orphans int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM tokens WHERE scope='read' AND agent_id='') + (SELECT COUNT(*) FROM oauth_clients WHERE agent_id='')`).Scan(&orphans); err != nil {
		return fmt.Errorf("sqlite store: migrate: %w", err)
	}
	if orphans == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite store: migrate: %w", err)
	}
	now := time.Now().UnixNano()
	for _, q := range []struct {
		stmt string
		args []any
	}{
		{`INSERT OR IGNORE INTO agents(id, name, created_at, max_days) VALUES(?,?,?,0)`, []any{DefaultAgentID, DefaultAgentName, now}},
		{`INSERT OR IGNORE INTO agent_grants(agent_id, project_id, created_at) SELECT ?, id, ? FROM projects`, []any{DefaultAgentID, now}},
		{`UPDATE tokens SET agent_id=? WHERE scope='read' AND agent_id=''`, []any{DefaultAgentID}},
		{`UPDATE oauth_clients SET agent_id=? WHERE agent_id=''`, []any{DefaultAgentID}},
	} {
		if _, err := tx.ExecContext(ctx, q.stmt, q.args...); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlite store: migrate agents: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite store: migrate agents: %w", err)
	}
	return nil
}

// columns lists a table's column names.
func columns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`) //nolint:gosec // G202: table names are constants
	if err != nil {
		return nil, fmt.Errorf("sqlite store: migrate: %w", err)
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("sqlite store: migrate: %w", err)
		}
		have[name] = true
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("sqlite store: migrate: %w", err)
	}
	return have, nil
}

// Close implements Store.
func (s *SQLite) Close() error { return s.db.Close() }

func nano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

func (s *SQLite) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite store: commit: %w", err)
	}
	return nil
}

// PutSession implements Store.
func (s *SQLite) PutSession(ctx context.Context, p *model.SessionPayload, now time.Time, lim Limits) error {
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("sqlite store: encode: %w", err)
	}
	pid, name := p.ProjectRef(), p.ProjectLabel()
	return s.tx(ctx, func(tx *sql.Tx) error {
		var oldProject string
		err := tx.QueryRowContext(ctx, `SELECT project FROM sessions WHERE source=? AND session_id=?`, p.Source, p.SessionID).Scan(&oldProject)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlite store: lookup: %w", err)
		}
		if !exists {
			var perProject, total int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE project=?`, pid).Scan(&perProject); err != nil {
				return fmt.Errorf("sqlite store: quota: %w", err)
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&total); err != nil {
				return fmt.Errorf("sqlite store: quota: %w", err)
			}
			if (lim.MaxSessionsPerProject > 0 && perProject >= lim.MaxSessionsPerProject) || (lim.MaxSessionsTotal > 0 && total >= lim.MaxSessionsTotal) {
				return ErrQuota
			}
		}
		if exists && oldProject != pid {
			if err := unlinkSession(ctx, tx, p.Source, p.SessionID); err != nil {
				return err
			}
		}
		// The project row: created at first sight, renamed when the payload
		// carries another name.
		var one int
		isNew := errors.Is(tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=?`, pid).Scan(&one), sql.ErrNoRows)
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, name, created_at, updated_at) VALUES(?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at WHERE projects.name<>excluded.name`,
			pid, name, nano(now), nano(now)); err != nil {
			return fmt.Errorf("sqlite store: upsert project: %w", err)
		}
		// A session that moves into a project this request creates takes
		// the old project's grants along: what an agent could already see
		// stays visible when a project gets an id or is split off. Moving
		// into a project that already exists changes no grants.
		if exists && oldProject != pid && isNew {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_grants(agent_id, project_id, created_at)
				SELECT agent_id, ?, ? FROM agent_grants WHERE project_id=?`, pid, nano(now), oldProject); err != nil {
				return fmt.Errorf("sqlite store: inherit grants: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(source, session_id, project, member, updated_at, ingested_at, payload) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(source, session_id) DO UPDATE SET project=excluded.project, member=excluded.member, updated_at=excluded.updated_at, ingested_at=excluded.ingested_at, payload=excluded.payload`,
			p.Source, p.SessionID, pid, p.Project, nano(p.UpdatedAt), nano(now), string(body)); err != nil {
			return fmt.Errorf("sqlite store: upsert session: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions_fts WHERE source=? AND session_id=?`, p.Source, p.SessionID); err != nil {
			return fmt.Errorf("sqlite store: fts delete: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions_fts(text, source, session_id, project) VALUES(?,?,?,?)`,
			SearchText(p), p.Source, p.SessionID, pid); err != nil {
			return fmt.Errorf("sqlite store: fts insert: %w", err)
		}
		for _, it := range p.WorkItems {
			if err := mergeWorkItem(ctx, tx, p, pid, it, now); err != nil {
				return err
			}
		}
		if exists && oldProject != pid {
			return dropEmptyProjects(ctx, tx)
		}
		return nil
	})
}

// dropEmptyProjects removes projects that no session is filed under any more.
func dropEmptyProjects(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE NOT EXISTS (SELECT 1 FROM sessions s WHERE s.project=projects.id)`); err != nil {
		return fmt.Errorf("sqlite store: project cleanup: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_grants WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id=agent_grants.project_id)`); err != nil {
		return fmt.Errorf("sqlite store: grant cleanup: %w", err)
	}
	return nil
}

func mergeWorkItem(ctx context.Context, tx *sql.Tx, p *model.SessionPayload, pid string, it model.WorkItem, now time.Time) error {
	var raw string
	var first int64
	err := tx.QueryRowContext(ctx, `SELECT item, first_seen FROM work_items WHERE project=? AND kind=? AND provider=? AND key=?`,
		pid, it.Kind, it.Provider, it.Key).Scan(&raw, &first)
	merged := it
	switch {
	case err == nil:
		var cur model.WorkItem
		if err := json.Unmarshal([]byte(raw), &cur); err != nil {
			return fmt.Errorf("sqlite store: decode work item: %w", err)
		}
		merged = workitems.Merge(cur, it)
	case errors.Is(err, sql.ErrNoRows):
		first = nano(now)
	default:
		return fmt.Errorf("sqlite store: work item lookup: %w", err)
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("sqlite store: encode work item: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_items(project, kind, provider, key, item, status, observed_at, first_seen, last_seen) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project, kind, provider, key) DO UPDATE SET item=excluded.item, status=excluded.status, observed_at=excluded.observed_at, last_seen=excluded.last_seen`,
		pid, merged.Kind, merged.Provider, merged.Key, string(b), merged.Status, nano(merged.ObservedAt), first, nano(now)); err != nil {
		return fmt.Errorf("sqlite store: upsert work item: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_item_sessions(project, kind, provider, key, source, session_id) VALUES(?,?,?,?,?,?)`,
		pid, merged.Kind, merged.Provider, merged.Key, p.Source, p.SessionID); err != nil {
		return fmt.Errorf("sqlite store: link work item: %w", err)
	}
	return nil
}

func unlinkSession(ctx context.Context, tx *sql.Tx, source, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_item_sessions WHERE source=? AND session_id=?`, source, id); err != nil {
		return fmt.Errorf("sqlite store: unlink: %w", err)
	}
	return dropOrphans(ctx, tx)
}

func dropOrphans(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_items WHERE NOT EXISTS (
		SELECT 1 FROM work_item_sessions l WHERE l.project=work_items.project AND l.kind=work_items.kind AND l.provider=work_items.provider AND l.key=work_items.key)`); err != nil {
		return fmt.Errorf("sqlite store: orphan cleanup: %w", err)
	}
	return nil
}

// ResolveProject implements Store.
func (s *SQLite) ResolveProject(ctx context.Context, ref string) (Project, error) {
	return resolveProject(ctx, s.db, ref)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func resolveProject(ctx context.Context, q queryer, ref string) (Project, error) {
	for _, stmt := range []string{
		`SELECT id, name, created_at, updated_at FROM projects WHERE id=?`,
		`SELECT p.id, p.name, p.created_at, p.updated_at FROM projects p WHERE p.name=? COLLATE NOCASE
			ORDER BY (SELECT MAX(updated_at) FROM sessions s WHERE s.project=p.id) DESC, p.id LIMIT 1`,
		`SELECT p.id, p.name, p.created_at, p.updated_at FROM sessions s JOIN projects p ON p.id=s.project WHERE s.member=?
			ORDER BY s.updated_at DESC LIMIT 1`,
	} {
		var p Project
		var c, u int64
		err := q.QueryRowContext(ctx, stmt, ref).Scan(&p.ID, &p.Name, &c, &u)
		if err == nil {
			p.CreatedAt, p.UpdatedAt = fromNano(c), fromNano(u)
			return p, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Project{}, fmt.Errorf("sqlite store: resolve project: %w", err)
		}
	}
	return Project{}, ErrNotFound
}

// DeleteProject implements Store.
func (s *SQLite) DeleteProject(ctx context.Context, ref string) (int, error) {
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// What to delete: the whole project when ref is its id; else the
		// sessions that came from the member ref; else the project named ref.
		col, val := "project", ref
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=?`, ref).Scan(&one); {
		case err == nil:
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("sqlite store: delete project: %w", err)
		default:
			switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE member=? LIMIT 1`, ref).Scan(&one); {
			case err == nil:
				col = "member"
			case !errors.Is(err, sql.ErrNoRows):
				return fmt.Errorf("sqlite store: delete project: %w", err)
			default:
				p, err := resolveProject(ctx, tx, ref)
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				if err != nil {
					return err
				}
				val = p.ID
			}
		}
		for _, stmt := range []string{
			`DELETE FROM sessions_fts WHERE EXISTS (SELECT 1 FROM sessions s WHERE s.source=sessions_fts.source AND s.session_id=sessions_fts.session_id AND s.` + col + `=?)`,
			`DELETE FROM work_item_sessions WHERE EXISTS (SELECT 1 FROM sessions s WHERE s.source=work_item_sessions.source AND s.session_id=work_item_sessions.session_id AND s.` + col + `=?)`,
		} {
			if _, err := tx.ExecContext(ctx, stmt, val); err != nil { //nolint:gosec // G202: col is one of two constants
				return fmt.Errorf("sqlite store: delete project: %w", err)
			}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE `+col+`=?`, val) //nolint:gosec // G202: col is one of two constants
		if err != nil {
			return fmt.Errorf("sqlite store: delete project: %w", err)
		}
		c, _ := res.RowsAffected()
		n = int(c)
		if err := dropOrphans(ctx, tx); err != nil {
			return err
		}
		return dropEmptyProjects(ctx, tx)
	})
	return n, err
}

// DeleteSession implements Store.
func (s *SQLite) DeleteSession(ctx context.Context, source, id string) (int, error) {
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		q, args := `FROM sessions WHERE session_id=?`, []any{id}
		if source != "" {
			q, args = q+` AND source=?`, append(args, source)
		}
		rows, err := tx.QueryContext(ctx, `SELECT source `+q, args...) //nolint:gosec // G202: only fixed predicates are concatenated; every value is a bound parameter
		if err != nil {
			return fmt.Errorf("sqlite store: delete session: %w", err)
		}
		var sources []string
		for rows.Next() {
			var src string
			if err := rows.Scan(&src); err != nil {
				_ = rows.Close()
				return fmt.Errorf("sqlite store: delete session: %w", err)
			}
			sources = append(sources, src)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("sqlite store: delete session: %w", err)
		}
		for _, src := range sources {
			for _, stmt := range []string{`DELETE FROM sessions_fts WHERE source=? AND session_id=?`, `DELETE FROM sessions WHERE source=? AND session_id=?`} {
				if _, err := tx.ExecContext(ctx, stmt, src, id); err != nil {
					return fmt.Errorf("sqlite store: delete session: %w", err)
				}
			}
			if err := unlinkSession(ctx, tx, src, id); err != nil {
				return err
			}
			n++
		}
		return dropEmptyProjects(ctx, tx)
	})
	return n, err
}

// ListProjects implements Store.
func (s *SQLite) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.project, COALESCE(p.name, s.project), s.source, COUNT(*), MAX(s.updated_at)
		FROM sessions s LEFT JOIN projects p ON p.id=s.project GROUP BY s.project, s.source`)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	by := map[string]*ProjectInfo{}
	var order []string
	for rows.Next() {
		var project, name, source string
		var n int
		var last int64
		if err := rows.Scan(&project, &name, &source, &n, &last); err != nil {
			return nil, fmt.Errorf("sqlite store: list projects: %w", err)
		}
		pi, ok := by[project]
		if !ok {
			pi = &ProjectInfo{ID: project, Project: name, Sources: map[string]int{}}
			by[project] = pi
			order = append(order, project)
		}
		pi.Sessions += n
		pi.Sources[source] += n
		if t := fromNano(last); t.After(pi.LastActivity) {
			pi.LastActivity = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite store: list projects: %w", err)
	}
	out := make([]ProjectInfo, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	sortProjects(out)
	return out, nil
}

func sortProjects(out []ProjectInfo) {
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j], out[j-1]
			if a.LastActivity.After(b.LastActivity) || (a.LastActivity.Equal(b.LastActivity) && (a.Project < b.Project || (a.Project == b.Project && a.ID < b.ID))) {
				out[j], out[j-1] = b, a
			} else {
				break
			}
		}
	}
}

// sessionCols selects what scanSessions reads: the payload, its arrival time,
// and the project it is filed under with its current name.
const sessionCols = `SELECT s.payload, s.ingested_at, s.project, COALESCE(p.name, s.project) FROM sessions s LEFT JOIN projects p ON p.id=s.project`

func scanSessions(rows *sql.Rows) ([]SessionRecord, error) {
	defer func() { _ = rows.Close() }()
	var out []SessionRecord
	for rows.Next() {
		var body string
		var ing int64
		var r SessionRecord
		if err := rows.Scan(&body, &ing, &r.ProjectID, &r.ProjectName); err != nil {
			return nil, fmt.Errorf("sqlite store: scan: %w", err)
		}
		if err := json.Unmarshal([]byte(body), &r.Payload); err != nil {
			return nil, fmt.Errorf("sqlite store: decode session: %w", err)
		}
		r.IngestedAt = fromNano(ing)
		out = append(out, r)
	}
	return out, rows.Err()
}

func limitClause(limit, offset int) string {
	if limit <= 0 {
		limit = -1
	}
	return fmt.Sprintf(" LIMIT %d OFFSET %d", limit, max(offset, 0))
}

// Sessions implements Store.
func (s *SQLite) Sessions(ctx context.Context, q SessionQuery) ([]SessionRecord, error) {
	where, args := []string{"1=1"}, []any{}
	if q.Project != "" {
		where, args = append(where, "s.project=?"), append(args, q.Project)
	}
	if q.Source != "" {
		where, args = append(where, "s.source=?"), append(args, q.Source)
	}
	if !q.Since.IsZero() {
		where, args = append(where, "s.updated_at>=?"), append(args, nano(q.Since))
	}
	if !q.Until.IsZero() {
		where, args = append(where, "s.updated_at<?"), append(args, nano(q.Until))
	}
	rows, err := s.db.QueryContext(ctx, sessionCols+` WHERE `+strings.Join(where, " AND ")+ //nolint:gosec // G202: only fixed predicates are concatenated; every value is a bound parameter
		` ORDER BY s.updated_at DESC, s.source, s.session_id`+limitClause(q.Limit, q.Offset), args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: sessions: %w", err)
	}
	return scanSessions(rows)
}

// GetSession implements Store.
func (s *SQLite) GetSession(ctx context.Context, source, id string) (*SessionRecord, error) {
	q, args := sessionCols+` WHERE s.session_id=?`, []any{id}
	if source != "" {
		q, args = q+` AND s.source=?`, append(args, source)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY s.updated_at DESC LIMIT 1`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: get session: %w", err)
	}
	recs, err := scanSessions(rows)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, ErrNotFound
	}
	return &recs[0], nil
}

// ftsQuery turns free text into a safe FTS5 expression: quoted prefix terms
// ANDed together. FTS operators in user input are never interpreted.
func ftsQuery(q string) string {
	words := Tokenize(q)
	for i, w := range words {
		words[i] = `"` + w + `"*`
	}
	return strings.Join(words, " ")
}

// Search implements Store.
func (s *SQLite) Search(ctx context.Context, query, project string, limit int) ([]SessionRecord, error) {
	fq := ftsQuery(query)
	if fq == "" {
		return nil, nil
	}
	q := `SELECT s.payload, s.ingested_at, s.project, COALESCE(p.name, s.project) FROM sessions_fts f
		JOIN sessions s ON s.source=f.source AND s.session_id=f.session_id LEFT JOIN projects p ON p.id=s.project
		WHERE sessions_fts MATCH ?`
	args := []any{fq}
	if project != "" {
		q, args = q+` AND s.project=?`, append(args, project)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY s.updated_at DESC, s.source, s.session_id`+limitClause(limit, 0), args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: search: %w", err)
	}
	return scanSessions(rows)
}

// WorkItems implements Store.
func (s *SQLite) WorkItems(ctx context.Context, q WorkItemQuery) ([]WorkItemRecord, error) {
	where, args := []string{"1=1"}, []any{}
	add := func(col, v string) {
		if v != "" {
			where, args = append(where, col+"=?"), append(args, v)
		}
	}
	add("w.project", q.Project)
	add("w.kind", q.Kind)
	add("w.provider", q.Provider)
	add("w.status", q.Status)
	if !q.Since.IsZero() {
		where, args = append(where, "w.observed_at>=?"), append(args, nano(q.Since))
	}
	if !q.Until.IsZero() {
		where, args = append(where, "w.observed_at<?"), append(args, nano(q.Until))
	}
	const cols = `SELECT w.project, COALESCE(p.name, w.project), w.kind, w.provider, w.key, w.item, w.first_seen, w.last_seen
		FROM work_items w LEFT JOIN projects p ON p.id=w.project WHERE `
	rows, err := s.db.QueryContext(ctx, cols+strings.Join(where, " AND ")+ //nolint:gosec // G202: only fixed predicates are concatenated; every value is a bound parameter
		` ORDER BY w.observed_at DESC, w.project, w.key`+limitClause(q.Limit, q.Offset), args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: work items: %w", err)
	}
	type row struct {
		rec                       WorkItemRecord
		kind, provider, key, proj string
	}
	var out []row
	for rows.Next() {
		var r row
		var raw string
		var first, last int64
		if err := rows.Scan(&r.proj, &r.rec.Project, &r.kind, &r.provider, &r.key, &raw, &first, &last); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("sqlite store: work items: %w", err)
		}
		if err := json.Unmarshal([]byte(raw), &r.rec.Item); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("sqlite store: decode work item: %w", err)
		}
		r.rec.ProjectID, r.rec.FirstSeen, r.rec.LastSeen = r.proj, fromNano(first), fromNano(last)
		out = append(out, r)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("sqlite store: work items: %w", err)
	}
	recs := make([]WorkItemRecord, 0, len(out))
	for _, r := range out {
		lr, err := s.db.QueryContext(ctx, `SELECT source, session_id FROM work_item_sessions WHERE project=? AND kind=? AND provider=? AND key=? ORDER BY source, session_id`,
			r.proj, r.kind, r.provider, r.key)
		if err != nil {
			return nil, fmt.Errorf("sqlite store: work item sessions: %w", err)
		}
		for lr.Next() {
			var k SessionKey
			if err := lr.Scan(&k.Source, &k.ID); err != nil {
				_ = lr.Close()
				return nil, fmt.Errorf("sqlite store: work item sessions: %w", err)
			}
			r.rec.Sessions = append(r.rec.Sessions, k)
		}
		if err := lr.Close(); err != nil {
			return nil, fmt.Errorf("sqlite store: work item sessions: %w", err)
		}
		recs = append(recs, r.rec)
	}
	return recs, nil
}

// --- auth ---

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CreateToken implements AuthStore.
func (s *SQLite) CreateToken(ctx context.Context, t Token) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if t.Scope == "read" && t.AgentID == "" {
			// a read token always has an agent: its OAuth client's, else the default
			if t.ClientID != "" {
				if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM oauth_clients WHERE id=?`, t.ClientID).Scan(&t.AgentID); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("sqlite store: create token: %w", err)
				}
			}
			if t.AgentID == "" {
				if err := ensureDefaultAgent(ctx, tx, t.CreatedAt); err != nil {
					return err
				}
				t.AgentID = DefaultAgentID
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO tokens(id, hash, scope, label, created_at, expires_at, revoked_at, last_used_at, client_id, family, agent_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			t.ID, t.Hash, t.Scope, t.Label, nano(t.CreatedAt), nano(t.ExpiresAt), nano(t.RevokedAt), nano(t.LastUsedAt), t.ClientID, t.Family, t.AgentID)
		if isUnique(err) {
			return ErrDuplicate
		}
		if err != nil {
			return fmt.Errorf("sqlite store: create token: %w", err)
		}
		return nil
	})
}

const tokenCols = `id, hash, scope, label, created_at, expires_at, revoked_at, last_used_at, client_id, family, agent_id` //nolint:gosec // G101: column names, not a credential

type rowScanner interface{ Scan(...any) error }

func scanToken(r rowScanner) (Token, error) {
	var t Token
	var c, e, rv, lu int64
	if err := r.Scan(&t.ID, &t.Hash, &t.Scope, &t.Label, &c, &e, &rv, &lu, &t.ClientID, &t.Family, &t.AgentID); err != nil {
		return Token{}, err
	}
	t.CreatedAt, t.ExpiresAt, t.RevokedAt, t.LastUsedAt = fromNano(c), fromNano(e), fromNano(rv), fromNano(lu)
	return t, nil
}

// TokenByHash implements AuthStore.
func (s *SQLite) TokenByHash(ctx context.Context, hash string) (*Token, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE hash=?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite store: token: %w", err)
	}
	return &t, nil
}

// ListTokens implements AuthStore.
func (s *SQLite) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM tokens ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: list tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite store: list tokens: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLite) updateToken(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("sqlite store: update token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeToken implements AuthStore. An earlier existing revocation time is kept.
func (s *SQLite) RevokeToken(ctx context.Context, id string, at time.Time) error {
	return s.updateToken(ctx, `UPDATE tokens SET revoked_at = CASE WHEN revoked_at=0 OR ? < revoked_at THEN ? ELSE revoked_at END WHERE id=?`, nano(at), nano(at), id)
}

// SetTokenExpiry implements AuthStore.
func (s *SQLite) SetTokenExpiry(ctx context.Context, id string, at time.Time) error {
	return s.updateToken(ctx, `UPDATE tokens SET expires_at=? WHERE id=?`, nano(at), id)
}

// TouchToken implements AuthStore.
func (s *SQLite) TouchToken(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at=? WHERE id=?`, nano(at), id)
	if err != nil {
		return fmt.Errorf("sqlite store: touch token: %w", err)
	}
	return nil
}

// CountActiveTokens implements AuthStore.
func (s *SQLite) CountActiveTokens(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens WHERE (revoked_at=0 OR revoked_at>?) AND (expires_at=0 OR expires_at>?)`, nano(now), nano(now)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlite store: count tokens: %w", err)
	}
	return n, nil
}

// CreatePairing implements AuthStore.
func (s *SQLite) CreatePairing(ctx context.Context, hash string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pairings(hash, expires_at) VALUES(?,?)`, hash, nano(expires))
	if isUnique(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("sqlite store: create pairing: %w", err)
	}
	return nil
}

// ConsumePairing implements AuthStore.
func (s *SQLite) ConsumePairing(ctx context.Context, hash string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE pairings SET burned=1 WHERE hash=? AND burned=0 AND expires_at>?`, hash, nano(now))
	if err != nil {
		return false, fmt.Errorf("sqlite store: consume pairing: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// PairingFailure implements AuthStore.
func (s *SQLite) PairingFailure(ctx context.Context, now time.Time, maxFailures int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pairings SET failures=failures+1, burned = CASE WHEN failures+1>=? THEN 1 ELSE burned END
		WHERE burned=0 AND expires_at>?`, maxFailures, nano(now))
	if err != nil {
		return fmt.Errorf("sqlite store: pairing failure: %w", err)
	}
	return nil
}

// AppendAudit implements AuthStore.
func (s *SQLite) AppendAudit(ctx context.Context, e AuditEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit(at, token_id, scope, action, project, ip) VALUES(?,?,?,?,?,?)`,
		nano(e.At), e.TokenID, e.Scope, e.Action, e.Project, e.IP)
	if err != nil {
		return fmt.Errorf("sqlite store: audit: %w", err)
	}
	return nil
}

// ListAudit implements AuthStore (newest first).
func (s *SQLite) ListAudit(ctx context.Context, limit int) ([]AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, token_id, scope, action, project, ip FROM audit ORDER BY id DESC`+limitClause(limit, 0)) //nolint:gosec // G202: limitClause formats integers only
	if err != nil {
		return nil, fmt.Errorf("sqlite store: list audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var at int64
		if err := rows.Scan(&at, &e.TokenID, &e.Scope, &e.Action, &e.Project, &e.IP); err != nil {
			return nil, fmt.Errorf("sqlite store: list audit: %w", err)
		}
		e.At = fromNano(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAudit implements AuthStore.
func (s *SQLite) PruneAudit(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit WHERE at<?`, nano(before))
	if err != nil {
		return 0, fmt.Errorf("sqlite store: prune audit: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
