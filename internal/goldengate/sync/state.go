package sync

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
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
CREATE TABLE IF NOT EXISTS tombstones(project TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS imports(
  id INTEGER PRIMARY KEY AUTOINCREMENT, source TEXT NOT NULL, path TEXT NOT NULL, added_at INTEGER NOT NULL, UNIQUE(source, path));
`

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
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("state: schema: %w", err)
	}
	if err := migrateState(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &State{db: db}, nil
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

// ForgetProject drops the remembered sessions of a project (after a remote
// purge) so that the next sync uploads them again if the project is still shared.
func (s *State) ForgetProject(ctx context.Context, project string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE project=?`, project); err != nil {
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
}

// AddImport remembers an archive. Adding the same one again is a no-op.
func (s *State) AddImport(ctx context.Context, source, path string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO imports(source, path, added_at) VALUES(?,?,?)`, source, path, now.Unix()); err != nil {
		return fmt.Errorf("state: add import: %w", err)
	}
	return nil
}

// ListImports returns the registered archives, oldest first.
func (s *State) ListImports(ctx context.Context) ([]Import, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, source, path, added_at FROM imports ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("state: list imports: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Import
	for rows.Next() {
		var i Import
		var at int64
		if err := rows.Scan(&i.ID, &i.Source, &i.Path, &at); err != nil {
			return nil, fmt.Errorf("state: list imports: %w", err)
		}
		i.AddedAt = time.Unix(at, 0)
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

// ForgetUnder drops the remembered sessions of a project and of everything
// filed beneath it (its worktrees), after the server project was deleted.
func (s *State) ForgetUnder(ctx context.Context, project string) error {
	prefix := project + "/"
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE project=? OR substr(project,1,?)=?`, project, len(prefix), prefix); err != nil {
		return fmt.Errorf("state: forget under: %w", err)
	}
	return nil
}
