package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	gosync "sync"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/summarize"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// State is the local sync database: what was pushed, and which projects have
// been un-shared (tombstones). It lives next to the config and is mode 0600.
type State struct{ db *sql.DB }

// Entry is one pushed session as remembered locally.
type Entry struct {
	Source, SessionID, Project, ContentHash string
	Size                                    int64
	MTimeNS                                 int64
	SyncedAt                                time.Time
	// Payload is the JSON that was pushed (already redacted, exactly what the
	// server received). It lets the TUI show what changed since the last sync.
	Payload []byte
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions(
  source TEXT NOT NULL, session_id TEXT NOT NULL, project TEXT NOT NULL,
  content_hash TEXT NOT NULL, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, synced_at INTEGER NOT NULL,
  payload TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(source, session_id));
CREATE INDEX IF NOT EXISTS sessions_project ON sessions(project);
CREATE TABLE IF NOT EXISTS model_skipped(
  source TEXT NOT NULL, session_id TEXT NOT NULL, model TEXT NOT NULL, content_hash TEXT NOT NULL,
  PRIMARY KEY(source, session_id));
CREATE TABLE IF NOT EXISTS tombstones(project TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS model_summaries(key TEXT PRIMARY KEY, answer TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS decisions(
  source TEXT NOT NULL, session_id TEXT NOT NULL, state TEXT NOT NULL, content_hash TEXT NOT NULL,
  decided_at INTEGER NOT NULL, project TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(source, session_id));
CREATE TABLE IF NOT EXISTS edits(
  source TEXT NOT NULL, session_id TEXT NOT NULL, content_hash TEXT NOT NULL, edit TEXT NOT NULL, edited_at INTEGER NOT NULL,
  PRIMARY KEY(source, session_id));
CREATE TABLE IF NOT EXISTS receipts(id TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS receipt_items(
  receipt_id TEXT NOT NULL, source TEXT NOT NULL, session_id TEXT NOT NULL, project TEXT NOT NULL,
  project_id TEXT NOT NULL DEFAULT '', project_name TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '',
  rule_id TEXT NOT NULL DEFAULT '', why TEXT NOT NULL DEFAULT '', retracted_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(receipt_id, source, session_id));
CREATE INDEX IF NOT EXISTS receipt_items_session ON receipt_items(source, session_id);
CREATE TABLE IF NOT EXISTS import_stats(import_id INTEGER PRIMARY KEY, total INTEGER NOT NULL, added INTEGER NOT NULL, changed INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS offers_dismissed(key TEXT PRIMARY KEY, dismissed_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS imports(
  id INTEGER PRIMARY KEY AUTOINCREMENT, source TEXT NOT NULL, path TEXT NOT NULL, added_at INTEGER NOT NULL, UNIQUE(source, path));
` + planCacheSchema + metaSchema

// OpenState opens (creating if needed) the state database at path. Use
// ":memory:" in tests.
func OpenState(path string) (*State, error) {
	dsn := path
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("state: mkdir: %w", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("state: create %s: %w", path, err)
		}
		_ = f.Close()
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("state: open: %w", err)
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1) // each :memory: connection is its own database
	}
	if err := prepareState(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &State{db: db}, nil
}

// prepareMu serializes creating and upgrading the schema inside this process:
// the web UI opens the state database once per request, and two requests on
// a first run would otherwise both switch a new file to WAL at once (#140).
var prepareMu gosync.Mutex

// prepareTimeout bounds how long opening waits for another process (the
// terminal UI, a background sync) that holds the database while it does the same.
const prepareTimeout = 10 * time.Second

// prepareState creates the schema and runs the migrations. SQLite answers
// SQLITE_BUSY at once, without waiting on busy_timeout, when two connections
// race to change the journal mode or to upgrade a read lock to a write lock;
// that is retried until prepareTimeout.
func prepareState(db *sql.DB) error {
	prepareMu.Lock()
	defer prepareMu.Unlock()
	ctx := context.Background()
	deadline := time.Now().Add(prepareTimeout)
	wait := 10 * time.Millisecond
	for {
		err := func() error {
			if _, err := db.ExecContext(ctx, schema); err != nil {
				return fmt.Errorf("state: schema: %w", err)
			}
			return migrateState(ctx, db)
		}()
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
		wait = min(wait*2, 200*time.Millisecond)
	}
}

// isBusy reports whether err is SQLite's "database is locked" or "table is
// locked", in any of their extended forms.
func isBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

// migrateState upgrades state databases created before payloads were kept.
func migrateState(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(sessions)`)
	if err != nil {
		return fmt.Errorf("state: migrate: %w", err)
	}
	have := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("state: migrate: %w", err)
		}
		if name == "payload" {
			have = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("state: migrate: %w", err)
	}
	if !have {
		if _, err := db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN payload TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("state: migrate: %w", err)
		}
	}
	return nil
}

// Close closes the database.
func (s *State) Close() error { return s.db.Close() }

// Get returns the stored entry for a session, if any.
func (s *State) Get(ctx context.Context, source, id string) (Entry, bool, error) {
	var e Entry
	var at int64
	var payload string
	err := s.db.QueryRowContext(ctx,
		`SELECT source, session_id, project, content_hash, size, mtime_ns, synced_at, payload FROM sessions WHERE source=? AND session_id=?`,
		source, id).Scan(&e.Source, &e.SessionID, &e.Project, &e.ContentHash, &e.Size, &e.MTimeNS, &at, &payload)
	if err == sql.ErrNoRows {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("state: get: %w", err)
	}
	e.SyncedAt = time.Unix(at, 0)
	e.Payload = []byte(payload)
	return e, true, nil
}

// Put records a pushed session.
func (s *State) Put(ctx context.Context, e Entry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(source, session_id, project, content_hash, size, mtime_ns, synced_at, payload) VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(source, session_id) DO UPDATE SET project=excluded.project, content_hash=excluded.content_hash,
		   size=excluded.size, mtime_ns=excluded.mtime_ns, synced_at=excluded.synced_at, payload=excluded.payload`,
		e.Source, e.SessionID, e.Project, e.ContentHash, e.Size, e.MTimeNS, e.SyncedAt.Unix(), string(e.Payload))
	if err != nil {
		return fmt.Errorf("state: put: %w", err)
	}
	return nil
}

// Tombstone marks a project as un-shared and forgets its pushed sessions, so
// a later re-share pushes everything again.
func (s *State) Tombstone(ctx context.Context, project string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: tombstone: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO tombstones(project, created_at) VALUES(?,?)`, project, now.Unix()); err != nil {
		return fmt.Errorf("state: tombstone: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE project=?`, project); err != nil {
		return fmt.Errorf("state: tombstone: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plan_cache WHERE project=?`, project); err != nil {
		return fmt.Errorf("state: tombstone: %w", err)
	}
	return tx.Commit()
}

// ClearTombstone removes a tombstone (the project was shared again).
func (s *State) ClearTombstone(ctx context.Context, project string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM tombstones WHERE project=?`, project); err != nil {
		return fmt.Errorf("state: clear tombstone: %w", err)
	}
	return nil
}

// Tombstoned reports whether a project is tombstoned.
func (s *State) Tombstoned(ctx context.Context, project string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tombstones WHERE project=?`, project).Scan(&n); err != nil {
		return false, fmt.Errorf("state: tombstoned: %w", err)
	}
	return n > 0, nil
}

// Count returns the number of remembered sessions.
func (s *State) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		return 0, fmt.Errorf("state: count: %w", err)
	}
	return n, nil
}

// MarkModelSkipped remembers that the model was asked to summarize this
// content and left the rules' summary (too little conversation, say), so the
// session is not offered for re-summarizing again.
func (s *State) MarkModelSkipped(ctx context.Context, source, id, hash, by string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO model_skipped(source, session_id, model, content_hash) VALUES(?,?,?,?)
		ON CONFLICT(source, session_id) DO UPDATE SET model=excluded.model, content_hash=excluded.content_hash`, source, id, by, hash)
}

// ModelSkipped reports whether MarkModelSkipped was called for this very
// content and model.
func (s *State) ModelSkipped(ctx context.Context, source, id, hash, by string) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_skipped WHERE source=? AND session_id=? AND model=? AND content_hash=?`, source, id, by, hash).Scan(&n)
	return err == nil && n > 0
}

// SummaryCache stores model answers in the state database, so a session is
// summarized once per content and model, across runs and previews.
func (s *State) SummaryCache(ctx context.Context, now func() time.Time) summarize.Cache {
	return stateCache{s, ctx, now}
}

type stateCache struct {
	st  *State
	ctx context.Context
	now func() time.Time
}

func (c stateCache) Get(key string) ([]byte, bool) {
	var v string
	if err := c.st.db.QueryRowContext(c.ctx, `SELECT answer FROM model_summaries WHERE key=?`, key).Scan(&v); err != nil {
		return nil, false
	}
	return []byte(v), true
}

func (c stateCache) Put(key string, value []byte) {
	_, _ = c.st.db.ExecContext(c.ctx, `INSERT INTO model_summaries(key, answer, created_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET answer=excluded.answer`,
		key, string(value), c.now().Unix()) // a lost cache entry only costs a call
}

// ProjectStat is what the server already holds for a project, as far as this
// machine knows: how many sessions were pushed and when the latest push was.
type ProjectStat struct {
	Synced   int
	LastSync time.Time
}

// ProjectStats returns a ProjectStat for every project with pushed sessions.
func (s *State) ProjectStats(ctx context.Context) (map[string]ProjectStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project, COUNT(*), MAX(synced_at) FROM sessions GROUP BY project`)
	if err != nil {
		return nil, fmt.Errorf("state: project stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ProjectStat{}
	for rows.Next() {
		var p string
		var n int
		var at int64
		if err := rows.Scan(&p, &n, &at); err != nil {
			return nil, fmt.Errorf("state: project stats: %w", err)
		}
		out[p] = ProjectStat{Synced: n, LastSync: time.Unix(at, 0)}
	}
	return out, rows.Err()
}

// ForgetProject drops the remembered sessions of a project (after a remote
// purge) so that the next sync uploads them again if the project is still shared.
func (s *State) ForgetProject(ctx context.Context, project string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE project=?`, project); err != nil {
		return fmt.Errorf("state: forget project: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM plan_cache WHERE project=?`, project); err != nil {
		return fmt.Errorf("state: forget project: %w", err)
	}
	return nil
}

// Import is a registered chat-export archive (claude.ai, ChatGPT). The archive
// stays where the user keeps it; only its path is remembered.
type Import struct {
	ID      int64
	Source  string
	Path    string
	AddedAt time.Time
	// What the archive brought in when it was registered: how many
	// conversations it holds, how many of them were new and how many were a
	// newer version of one already imported. Counted is false for archives
	// registered before this was recorded.
	Total, Added, Changed int
	Counted               bool
}

// AddImport remembers an archive. Adding the same one again is a no-op.
func (s *State) AddImport(ctx context.Context, source, path string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO imports(source, path, added_at) VALUES(?,?,?)`, source, path, now.Unix()); err != nil {
		return fmt.Errorf("state: add import: %w", err)
	}
	return nil
}

// RecordImport notes what a registered archive added at the time: total
// conversations, new ones, and changed ones. An earlier note for the same
// archive is kept, so re-running an import does not rewrite history.
func (s *State) RecordImport(ctx context.Context, source, path string, total, added, changed int) error {
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO import_stats(import_id, total, added, changed)
		SELECT id, ?, ?, ? FROM imports WHERE source=? AND path=?`, total, added, changed, source, path); err != nil {
		return fmt.Errorf("state: record import: %w", err)
	}
	return nil
}

// ListImports returns the registered archives, oldest first.
func (s *State) ListImports(ctx context.Context) ([]Import, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.id, i.source, i.path, i.added_at, s.total, s.added, s.changed
		FROM imports i LEFT JOIN import_stats s ON s.import_id = i.id ORDER BY i.id`)
	if err != nil {
		return nil, fmt.Errorf("state: list imports: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Import
	for rows.Next() {
		var i Import
		var at int64
		var total, added, changed sql.NullInt64
		if err := rows.Scan(&i.ID, &i.Source, &i.Path, &at, &total, &added, &changed); err != nil {
			return nil, fmt.Errorf("state: list imports: %w", err)
		}
		i.AddedAt = time.Unix(at, 0)
		if total.Valid {
			i.Total, i.Added, i.Changed, i.Counted = int(total.Int64), int(added.Int64), int(changed.Int64), true
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// RemoveImport forgets an archive (the file itself is left alone).
func (s *State) RemoveImport(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM imports WHERE id=?`, id)
	if err != nil {
		return false, fmt.Errorf("state: remove import: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM import_stats WHERE import_id=?`, id); err != nil {
		return false, fmt.Errorf("state: remove import: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SessionKeys lists the pushed sessions of a project, for per-session remote deletion.
func (s *State) SessionKeys(ctx context.Context, project string) ([][2]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, session_id FROM sessions WHERE project=? ORDER BY source, session_id`, project)
	if err != nil {
		return nil, fmt.Errorf("state: session keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out [][2]string
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			return nil, fmt.Errorf("state: session keys: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Sent lists every remembered (pushed) session, with the payload that was sent.
func (s *State) Sent(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, session_id, project, content_hash, size, mtime_ns, synced_at, payload FROM sessions ORDER BY synced_at DESC, source, session_id`)
	if err != nil {
		return nil, fmt.Errorf("state: sent: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var e Entry
		var at int64
		var payload string
		if err := rows.Scan(&e.Source, &e.SessionID, &e.Project, &e.ContentHash, &e.Size, &e.MTimeNS, &at, &payload); err != nil {
			return nil, fmt.Errorf("state: sent: %w", err)
		}
		e.SyncedAt, e.Payload = time.Unix(at, 0).UTC(), []byte(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Forget drops what is remembered about one pushed session.
func (s *State) Forget(ctx context.Context, source, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE source=? AND session_id=?`, source, id); err != nil {
		return fmt.Errorf("state: forget: %w", err)
	}
	return nil
}

// Payloads returns the payload last pushed for every remembered session that
// has one: the already-redacted JSON the server received.
func (s *State) Payloads(ctx context.Context) ([][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM sessions WHERE payload<>'' ORDER BY source, session_id`)
	if err != nil {
		return nil, fmt.Errorf("state: payloads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out [][]byte
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("state: payloads: %w", err)
		}
		out = append(out, []byte(p))
	}
	return out, rows.Err()
}

// ForgetUnder drops the remembered sessions of a project and of everything
// filed beneath it (its worktrees), after the server project was deleted.
func (s *State) ForgetUnder(ctx context.Context, project string) error {
	prefix := project + "/"
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE project=? OR substr(project,1,?)=?`, project, len(prefix), prefix); err != nil {
		return fmt.Errorf("state: forget under: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM plan_cache WHERE project=? OR substr(project,1,?)=?`, project, len(prefix), prefix); err != nil {
		return fmt.Errorf("state: forget under: %w", err)
	}
	return nil
}
