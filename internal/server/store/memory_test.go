package store_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/OWNER/goldengate/internal/server/store"
	"github.com/OWNER/goldengate/internal/server/store/storetest"
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
