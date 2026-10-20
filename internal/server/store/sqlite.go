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

	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/workitems"
)

// SQLite is the self-host Store: one file on one volume. Search uses FTS5.
type SQLite struct{ db *sql.DB }

const sqliteSchema = `
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
  created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL);
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
	return &SQLite{db: db}, nil
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
	return s.tx(ctx, func(tx *sql.Tx) error {
		var oldProject string
		err := tx.QueryRowContext(ctx, `SELECT project FROM sessions WHERE source=? AND session_id=?`, p.Source, p.SessionID).Scan(&oldProject)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlite store: lookup: %w", err)
		}
		if !exists {
			var perProject, total int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE project=?`, p.Project).Scan(&perProject); err != nil {
				return fmt.Errorf("sqlite store: quota: %w", err)
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&total); err != nil {
				return fmt.Errorf("sqlite store: quota: %w", err)
			}
			if (lim.MaxSessionsPerProject > 0 && perProject >= lim.MaxSessionsPerProject) || (lim.MaxSessionsTotal > 0 && total >= lim.MaxSessionsTotal) {
				return ErrQuota
			}
		}
		if exists && oldProject != p.Project {
			if err := unlinkSession(ctx, tx, p.Source, p.SessionID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(source, session_id, project, updated_at, ingested_at, payload) VALUES(?,?,?,?,?,?)
			ON CONFLICT(source, session_id) DO UPDATE SET project=excluded.project, updated_at=excluded.updated_at, ingested_at=excluded.ingested_at, payload=excluded.payload`,
			p.Source, p.SessionID, p.Project, nano(p.UpdatedAt), nano(now), string(body)); err != nil {
			return fmt.Errorf("sqlite store: upsert session: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions_fts WHERE source=? AND session_id=?`, p.Source, p.SessionID); err != nil {
			return fmt.Errorf("sqlite store: fts delete: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions_fts(text, source, session_id, project) VALUES(?,?,?,?)`,
			SearchText(p), p.Source, p.SessionID, p.Project); err != nil {
			return fmt.Errorf("sqlite store: fts insert: %w", err)
		}
		for _, it := range p.WorkItems {
			if err := mergeWorkItem(ctx, tx, p, it, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func mergeWorkItem(ctx context.Context, tx *sql.Tx, p *model.SessionPayload, it model.WorkItem, now time.Time) error {
	var raw string
	var first int64
	err := tx.QueryRowContext(ctx, `SELECT item, first_seen FROM work_items WHERE project=? AND kind=? AND provider=? AND key=?`,
		p.Project, it.Kind, it.Provider, it.Key).Scan(&raw, &first)
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
		p.Project, merged.Kind, merged.Provider, merged.Key, string(b), merged.Status, nano(merged.ObservedAt), first, nano(now)); err != nil {
		return fmt.Errorf("sqlite store: upsert work item: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_item_sessions(project, kind, provider, key, source, session_id) VALUES(?,?,?,?,?,?)`,
		p.Project, merged.Kind, merged.Provider, merged.Key, p.Source, p.SessionID); err != nil {
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

// DeleteProject implements Store.
func (s *SQLite) DeleteProject(ctx context.Context, project string) (int, error) {
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions_fts WHERE project=?`, project); err != nil {
			return fmt.Errorf("sqlite store: fts delete: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM work_item_sessions WHERE project=?`, project); err != nil {
			return fmt.Errorf("sqlite store: unlink: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE project=?`, project)
		if err != nil {
			return fmt.Errorf("sqlite store: delete project: %w", err)
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return dropOrphans(ctx, tx)
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
		return nil
	})
	return n, err
}

// ListProjects implements Store.
func (s *SQLite) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project, source, COUNT(*), MAX(updated_at) FROM sessions GROUP BY project, source`)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	by := map[string]*ProjectInfo{}
	var order []string
	for rows.Next() {
		var project, source string
		var n int
		var last int64
		if err := rows.Scan(&project, &source, &n, &last); err != nil {
			return nil, fmt.Errorf("sqlite store: list projects: %w", err)
		}
		pi, ok := by[project]
		if !ok {
			pi = &ProjectInfo{Project: project, Sources: map[string]int{}}
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
			if a.LastActivity.After(b.LastActivity) || (a.LastActivity.Equal(b.LastActivity) && a.Project < b.Project) {
				out[j], out[j-1] = b, a
			} else {
				break
			}
		}
	}
}

func scanSessions(rows *sql.Rows) ([]SessionRecord, error) {
	defer func() { _ = rows.Close() }()
	var out []SessionRecord
	for rows.Next() {
		var body string
		var ing int64
		if err := rows.Scan(&body, &ing); err != nil {
			return nil, fmt.Errorf("sqlite store: scan: %w", err)
		}
		var r SessionRecord
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
		where, args = append(where, "project=?"), append(args, q.Project)
	}
	if q.Source != "" {
		where, args = append(where, "source=?"), append(args, q.Source)
	}
	if !q.Since.IsZero() {
		where, args = append(where, "updated_at>=?"), append(args, nano(q.Since))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload, ingested_at FROM sessions WHERE `+strings.Join(where, " AND ")+ //nolint:gosec // G202: only fixed predicates are concatenated; every value is a bound parameter
		` ORDER BY updated_at DESC, source, session_id`+limitClause(q.Limit, q.Offset), args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: sessions: %w", err)
	}
	return scanSessions(rows)
}

// GetSession implements Store.
func (s *SQLite) GetSession(ctx context.Context, source, id string) (*SessionRecord, error) {
	q, args := `SELECT payload, ingested_at FROM sessions WHERE session_id=?`, []any{id}
	if source != "" {
		q, args = q+` AND source=?`, append(args, source)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY updated_at DESC LIMIT 1`, args...)
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
	q := `SELECT s.payload, s.ingested_at FROM sessions_fts f JOIN sessions s ON s.source=f.source AND s.session_id=f.session_id
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
	add("project", q.Project)
	add("kind", q.Kind)
	add("provider", q.Provider)
	add("status", q.Status)
	if !q.Since.IsZero() {
		where, args = append(where, "observed_at>=?"), append(args, nano(q.Since))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT project, kind, provider, key, item, first_seen, last_seen FROM work_items WHERE `+strings.Join(where, " AND ")+ //nolint:gosec // G202: only fixed predicates are concatenated; every value is a bound parameter
		` ORDER BY observed_at DESC, project, key`+limitClause(q.Limit, q.Offset), args...)
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
		if err := rows.Scan(&r.proj, &r.kind, &r.provider, &r.key, &raw, &first, &last); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("sqlite store: work items: %w", err)
		}
		if err := json.Unmarshal([]byte(raw), &r.rec.Item); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("sqlite store: decode work item: %w", err)
		}
		r.rec.Project, r.rec.FirstSeen, r.rec.LastSeen = r.proj, fromNano(first), fromNano(last)
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens(id, hash, scope, label, created_at, expires_at, revoked_at, last_used_at) VALUES(?,?,?,?,?,?,?,?)`,
		t.ID, t.Hash, t.Scope, t.Label, nano(t.CreatedAt), nano(t.ExpiresAt), nano(t.RevokedAt), nano(t.LastUsedAt))
	if isUnique(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("sqlite store: create token: %w", err)
	}
	return nil
}

const tokenCols = `id, hash, scope, label, created_at, expires_at, revoked_at, last_used_at` //nolint:gosec // G101: column names, not a credential

type rowScanner interface{ Scan(...any) error }

func scanToken(r rowScanner) (Token, error) {
	var t Token
	var c, e, rv, lu int64
	if err := r.Scan(&t.ID, &t.Hash, &t.Scope, &t.Label, &c, &e, &rv, &lu); err != nil {
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
