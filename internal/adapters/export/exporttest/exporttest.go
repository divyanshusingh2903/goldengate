// Package exporttest builds export zips from fixture directories for tests.
package exporttest

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// ZipDir zips every file of dir (non-recursive) into a temp file and returns its path.
func ZipDir(t *testing.T, dir string) string {
	t.Helper()
	return ZipDirAs(t, dir, nil)
}

// ZipDirAs is ZipDir with per-file content overrides, keyed by file name.
func ZipDirAs(t *testing.T, dir string, override map[string][]byte) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "export.zip")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, ok := override[e.Name()]
		if !ok {
			if b, err = os.ReadFile(filepath.Join(dir, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
		w, err := zw.Create(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}
