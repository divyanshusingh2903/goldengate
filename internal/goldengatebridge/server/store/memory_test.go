package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"

	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/store"
	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/store/storetest"
)

func TestMemoryContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return store.NewMemory() })
}

func TestSQLiteContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := store.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "bridge.db"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestSQLitePersistsAndIsPrivate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "d", "bridge.db")
	s, err := store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, store.Token{ID: "i", Hash: "h", Scope: "read", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("db perms %v", fi.Mode().Perm())
		}
	}
	s2, err := store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if _, err := s2.TokenByHash(ctx, "h"); err != nil {
		t.Fatalf("token lost across reopen: %v", err)
	}
}

func TestSQLiteMigratesPreOAuthDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// the tokens table as it was before OAuth support
	if _, err := db.ExecContext(ctx, `CREATE TABLE tokens(id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, scope TEXT NOT NULL, label TEXT NOT NULL,
		created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL);
		INSERT INTO tokens VALUES('tok_old','hold','read','legacy',1,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s, err := store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("opening an old database: %v", err)
	}
	defer func() { _ = s.Close() }()
	old, err := s.TokenByHash(ctx, "hold")
	if err != nil || old.Label != "legacy" || old.ClientID != "" || old.Family != "" {
		t.Fatalf("legacy token lost or changed: %+v %v", old, err)
	}
	if err := s.CreateToken(ctx, store.Token{ID: "tok_new", Hash: "hnew", Scope: "read", CreatedAt: time.Now(), ClientID: "c", Family: "f"}); err != nil {
		t.Fatalf("new columns unusable after migration: %v", err)
	}
	s2, err := store.OpenSQLite(ctx, path) // idempotent
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	_ = s2.Close()
}

// The work tables as they were before projects had ids: everything keyed by
// the project key, no projects table, no member column.
const preProjectsSchema = `
CREATE TABLE sessions(
  source TEXT NOT NULL, session_id TEXT NOT NULL, project TEXT NOT NULL,
  updated_at INTEGER NOT NULL, ingested_at INTEGER NOT NULL, payload TEXT NOT NULL,
  PRIMARY KEY(source, session_id));
CREATE VIRTUAL TABLE sessions_fts USING fts5(
  text, source UNINDEXED, session_id UNINDEXED, project UNINDEXED, tokenize='unicode61 remove_diacritics 2');
CREATE TABLE work_items(
  project TEXT NOT NULL, kind TEXT NOT NULL, provider TEXT NOT NULL, key TEXT NOT NULL,
  item TEXT NOT NULL, status TEXT NOT NULL, observed_at INTEGER NOT NULL, first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
  PRIMARY KEY(project, kind, provider, key));
CREATE TABLE work_item_sessions(
  project TEXT NOT NULL, kind TEXT NOT NULL, provider TEXT NOT NULL, key TEXT NOT NULL,
  source TEXT NOT NULL, session_id TEXT NOT NULL,
  PRIMARY KEY(project, kind, provider, key, source, session_id));
CREATE TABLE audit(
  id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, token_id TEXT NOT NULL, scope TEXT NOT NULL,
  action TEXT NOT NULL, project TEXT NOT NULL, ip TEXT NOT NULL);`

func TestSQLiteUpgradesAPreProjectsDatabaseInPlace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, preProjectsSchema); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	item := `{"kind":"pull_request","provider":"github","key":"o/r#1","status":"created","observed_at":"2026-01-05T09:00:00Z","evidence":[{"session_id":"a","via":"tool_result","at":"2026-01-05T09:00:00Z"}]}`
	for i, row := range []struct{ id, project, title string }{{"a", "~/code/app", "Alpha widget"}, {"b", "~/code/app", "Beta"}, {"c", "~/code/site", "Gamma"}} {
		at := t0.Add(time.Duration(i) * time.Hour)
		body := `{"source":"claude-code","session_id":"` + row.id + `","project":"` + row.project + `","level":"summary","started_at":"2026-01-05T08:00:00Z","updated_at":"` +
			at.Format(time.RFC3339) + `","summary":{"title":"` + row.title + `","turn_count":1,"duration_sec":1},"content_hash":"` + strings.Repeat("b", 64) + `","redactions":{}}`
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions VALUES('claude-code',?,?,?,?,?)`, row.id, row.project, at.UnixNano(), t0.UnixNano(), body); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions_fts(text, source, session_id, project) VALUES(?,?,?,?)`, row.project+" "+row.title, "claude-code", row.id, row.project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items VALUES('~/code/app','pull_request','github','o/r#1',?,'created',?,?,?);
		INSERT INTO work_item_sessions VALUES('~/code/app','pull_request','github','o/r#1','claude-code','a');
		INSERT INTO audit(at, token_id, scope, action, project, ip) VALUES(1,'tok','ingest','ingest','~/code/app','')`, item, t0.UnixNano(), t0.UnixNano(), t0.UnixNano()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	check := func(s *store.SQLite) {
		t.Helper()
		ps, err := s.ListProjects(ctx)
		if err != nil || len(ps) != 2 || ps[0].ID != "~/code/site" || ps[0].Project != "~/code/site" || ps[1].ID != "~/code/app" || ps[1].Project != "~/code/app" || ps[1].Sessions != 2 {
			t.Fatalf("one project per old key, named after it: %+v %v", ps, err)
		}
		got, err := s.Sessions(ctx, store.SessionQuery{Project: "~/code/app"})
		if err != nil || len(got) != 2 || got[0].Payload.SessionID != "b" || got[0].ProjectID != "~/code/app" || got[0].ProjectName != "~/code/app" {
			t.Fatalf("the same sessions under the same key: %+v %v", got, err)
		}
		items, err := s.WorkItems(ctx, store.WorkItemQuery{Project: "~/code/app"})
		if err != nil || len(items) != 1 || items[0].Project != "~/code/app" || len(items[0].Sessions) != 1 {
			t.Fatalf("work items survive: %+v %v", items, err)
		}
		if hits, err := s.Search(ctx, "widget", "~/code/app", 10); err != nil || len(hits) != 1 || hits[0].Payload.SessionID != "a" {
			t.Fatalf("search survives: %+v %v", hits, err)
		}
		if p, err := s.ResolveProject(ctx, "~/code/app"); err != nil || p.ID != "~/code/app" || !p.CreatedAt.Equal(t0) {
			t.Fatalf("resolve: %+v %v", p, err)
		}
		if ev, err := s.ListAudit(ctx, 10); err != nil || len(ev) != 1 || ev[0].Project != "~/code/app" {
			t.Fatalf("the audit log still names the project: %+v %v", ev, err)
		}
	}
	s, err := store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("opening a pre-projects database: %v", err)
	}
	check(s)
	_ = s.Close()
	s, err = store.OpenSQLite(ctx, path) // the migration is idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	check(s)

	// A client that knows about named projects re-sends a session with an
	// id: it moves, and deleting by the old key still removes what is left.
	p := &model.SessionPayload{Version: model.PayloadV2, Source: "claude-code", SessionID: "a", Project: "~/code/app", ProjectID: "p_app1", ProjectName: "App",
		Level: model.LevelSummary, StartedAt: t0.Add(-time.Hour), UpdatedAt: t0, ContentHash: strings.Repeat("b", 64), Summary: &model.Summary{Title: "Alpha widget"}}
	if err := s.PutSession(ctx, p, t0, store.Limits{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Sessions(ctx, store.SessionQuery{Project: "p_app1"}); len(got) != 1 || got[0].ProjectName != "App" {
		t.Fatalf("re-filed under the new id: %+v", got)
	}
	if items, _ := s.WorkItems(ctx, store.WorkItemQuery{}); len(items) != 0 {
		t.Fatalf("the work item only that session carried went with its old link: %+v", items)
	}
	// "~/code/app" is still a project id (session b), so this deletes that project.
	if n, err := s.DeleteProject(ctx, "~/code/app"); err != nil || n != 1 {
		t.Fatalf("delete by the old key: %d %v", n, err)
	}
	// Now it is only a member key (session a, inside p_app1).
	if n, err := s.DeleteProject(ctx, "~/code/app"); err != nil || n != 1 {
		t.Fatalf("delete by member key: %d %v", n, err)
	}
	if ps, _ := s.ListProjects(ctx); len(ps) != 1 || ps[0].ID != "~/code/site" {
		t.Fatalf("only the untouched project is left: %+v", ps)
	}
}
