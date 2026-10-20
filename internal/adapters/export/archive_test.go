package export

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	body string
	mode os.FileMode
}

func makeZip(t *testing.T, entries ...entry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "export.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSafeName(t *testing.T) {
	good := []string{"conversations.json", "dir/file.json", "a/b/c.txt", "dir/"}
	bad := []string{"", "../x", "a/../../x", "/etc/passwd", "a/../b", `a\b`, "C:/x", "a//b", "./x", "x\x00y", "a/./b", "..", "a/.."}
	for _, n := range good {
		if !SafeName(n) {
			t.Errorf("%q should be safe", n)
		}
	}
	for _, n := range bad {
		if SafeName(n) {
			t.Errorf("%q should be unsafe", n)
		}
	}
}

func TestOpenAndRead(t *testing.T) {
	p := makeZip(t, entry{"conversations.json", `[{"a":1}]`, 0}, entry{"users.json", `{"email":"x"}`, 0}, entry{"sub/", "", os.ModeDir | 0o755})
	a, err := Open(p, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.Has("conversations.json") || a.Has("conversations.JSON") || a.Has("sub/") {
		t.Error("Has must be exact and list files only")
	}
	b, err := a.Read("conversations.json")
	if err != nil || string(b) != `[{"a":1}]` {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := a.Read("nope.json"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing entry: %v", err)
	}
	if len(a.Names()) != 2 {
		t.Errorf("names: %v", a.Names())
	}
}

func TestRefusesHostileArchives(t *testing.T) {
	lim := DefaultLimits()
	cases := map[string]string{
		"zip slip":          makeZip(t, entry{"conversations.json", "[]", 0}, entry{"../../../home/u/.bashrc", "evil", 0}),
		"absolute path":     makeZip(t, entry{"/etc/cron.d/x", "evil", 0}),
		"backslash":         makeZip(t, entry{`..\..\x`, "evil", 0}),
		"drive letter":      makeZip(t, entry{"C:/Windows/x", "evil", 0}),
		"symlink":           makeZip(t, entry{"conversations.json", "/etc/passwd", os.ModeSymlink | 0o777}),
		"duplicate entries": makeZip(t, entry{"conversations.json", "[]", 0}, entry{"conversations.json", "[1]", 0}),
		"control chars":     makeZip(t, entry{"a\x01b.json", "x", 0}),
	}
	for name, p := range cases {
		a, err := Open(p, lim)
		if err == nil {
			a.Close()
			t.Errorf("%s: archive accepted", name)
		}
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing.zip"), lim); err == nil {
		t.Error("missing file")
	}
	junk := filepath.Join(t.TempDir(), "junk.zip")
	_ = os.WriteFile(junk, []byte("this is not a zip"), 0o600)
	if _, err := Open(junk, lim); err == nil {
		t.Error("junk accepted")
	}
}

func TestZipBombDefenses(t *testing.T) {
	// a few KB of zeros inflate to tens of MB: caught by the ratio rule before anything is read
	bomb := makeZip(t, entry{"conversations.json", strings.Repeat("0", 24<<20), 0})
	if a, err := Open(bomb, DefaultLimits()); err == nil {
		a.Close()
		t.Error("high-ratio entry accepted")
	}
	// size limits: entry, total, count
	big := makeZip(t, entry{"conversations.json", strings.Repeat("abcdefghij", 1000), 0})
	lim := DefaultLimits()
	lim.MaxEntryBytes = 1000
	a, err := Open(big, lim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read("conversations.json"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized entry read: %v", err)
	}
	a.Close()
	lim = DefaultLimits()
	lim.MaxTotalBytes = 100
	if a, err := Open(big, lim); err == nil {
		a.Close()
		t.Error("total size limit")
	}
	lim = DefaultLimits()
	lim.MaxEntries = 1
	if a, err := Open(makeZip(t, entry{"a", "1", 0}, entry{"b", "2", 0}), lim); err == nil {
		a.Close()
		t.Error("entry count limit")
	}
	lim = DefaultLimits()
	lim.MaxArchiveBytes = 10
	if a, err := Open(big, lim); err == nil {
		a.Close()
		t.Error("archive size limit")
	}
	// a symlinked archive file is refused
	link := filepath.Join(t.TempDir(), "link.zip")
	if err := os.Symlink(big, link); err == nil {
		if a, err := Open(link, DefaultLimits()); err == nil {
			a.Close()
			t.Error("symlinked archive accepted")
		}
	}
}
