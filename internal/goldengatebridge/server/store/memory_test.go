package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
