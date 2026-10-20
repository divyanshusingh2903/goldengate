package sync

import (
	"context"
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
