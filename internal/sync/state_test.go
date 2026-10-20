package sync

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStateLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "state.db")
	st, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("state db must be 0600: %v %v", err, fi)
		}
	}
	e := Entry{Source: "s", SessionID: "1", Project: "/p", ContentHash: "h1", Size: 5, MTimeNS: 9}
	if _, ok, _ := st.Get(ctx, "s", "1"); ok {
		t.Fatal("empty db has entry")
	}
	if err := st.Put(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.ContentHash = "h2"
	if err := st.Put(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Get(ctx, "s", "1")
	if err != nil || !ok || got.ContentHash != "h2" {
		t.Fatalf("upsert failed: %+v %v %v", got, ok, err)
	}
	if n, _ := st.Count(ctx); n != 1 {
		t.Fatalf("count %d", n)
	}
	_ = st.Put(ctx, Entry{Source: "s", SessionID: "2", Project: "/other", ContentHash: "x"})
	if err := st.Tombstone(ctx, "/p", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.Count(ctx); n != 1 {
		t.Fatalf("tombstone must only forget that project: %d", n)
	}
	if dead, _ := st.Tombstoned(ctx, "/p"); !dead {
		t.Fatal("not tombstoned")
	}
	if dead, _ := st.Tombstoned(ctx, "/other"); dead {
		t.Fatal("wrong project tombstoned")
	}
	_ = st.ClearTombstone(ctx, "/p")
	if dead, _ := st.Tombstoned(ctx, "/p"); dead {
		t.Fatal("still tombstoned")
	}
}

func TestStatePersistsAcrossOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	a, _ := OpenState(path)
	_ = a.Put(ctx, Entry{Source: "s", SessionID: "1", Project: "/p", ContentHash: "h"})
	_ = a.Close()
	b, err := OpenState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, ok, _ := b.Get(ctx, "s", "1"); !ok {
		t.Fatal("state lost across reopen")
	}
}

func TestStateKeepsPushedPayloadAndMigratesOldDatabases(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	// a state database from before payloads were kept
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(ctx, `CREATE TABLE sessions(source TEXT NOT NULL, session_id TEXT NOT NULL, project TEXT NOT NULL,
		content_hash TEXT NOT NULL, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, synced_at INTEGER NOT NULL, PRIMARY KEY(source, session_id));
		INSERT INTO sessions VALUES('s','1','/p','h',1,2,3)`); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	st, err := OpenState(path)
	if err != nil {
		t.Fatalf("opening an old state db: %v", err)
	}
	defer func() { _ = st.Close() }()
	e, ok, err := st.Get(ctx, "s", "1")
	if err != nil || !ok || e.ContentHash != "h" || len(e.Payload) != 0 {
		t.Fatalf("old rows survive with no payload: %+v %v %v", e, ok, err)
	}
	e.Payload = []byte(`{"x":1}`)
	if err := st.Put(ctx, e); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.Get(ctx, "s", "1"); string(got.Payload) != `{"x":1}` {
		t.Errorf("payload round trip: %q", got.Payload)
	}
	keys, _ := st.SessionKeys(ctx, "/p")
	if len(keys) != 1 || keys[0] != [2]string{"s", "1"} {
		t.Errorf("session keys: %v", keys)
	}
}

func TestForgetUnderCoversWorktreesButNotSiblings(t *testing.T) {
	ctx := context.Background()
	st, err := OpenState(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for i, p := range []string{"/h/app", "/h/app/.claude/worktrees/fox", "/h/app-extra", "/h/other"} {
		_ = st.Put(ctx, Entry{Source: "s", SessionID: string(rune('a' + i)), Project: p, ContentHash: "h"})
	}
	if err := st.ForgetUnder(ctx, "/h/app"); err != nil {
		t.Fatal(err)
	}
	n, _ := st.Count(ctx)
	if n != 2 {
		t.Fatalf("only /h/app and what is filed beneath it go; /h/app-extra shares a prefix, not a folder: %d left", n)
	}
	if keys, _ := st.SessionKeys(ctx, "/h/app-extra"); len(keys) != 1 {
		t.Error("sibling with a common name prefix must survive")
	}
}
