package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func exercise(t *testing.T, s Store) {
	t.Helper()
	if _, err := s.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty store: %v", err)
	}
	if err := s.Set("a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("b", "2"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("a"); err != nil || v != "1" {
		t.Fatalf("%q %v", v, err)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Fatal("not deleted")
	}
	if v, _ := s.Get("b"); v != "2" {
		t.Fatal("delete removed a neighbour")
	}
	if err := s.Delete("never-existed"); err != nil {
		t.Fatalf("deleting a missing secret is not an error: %v", err)
	}
}

func TestMemory(t *testing.T) { exercise(t, &Memory{}) }

func TestFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "secrets.json")
	f := &File{Path: p}
	exercise(t, f)
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("secrets file perms: %v %v", err, fi)
		}
		if fi, _ := os.Stat(filepath.Dir(p)); fi.Mode().Perm() != 0o700 {
			t.Errorf("dir perms %v", fi.Mode().Perm())
		}
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get("a"); err == nil {
		t.Error("corrupt file must be an error, not silently empty")
	}
}
