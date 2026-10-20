package pathutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHomeAndScrub(t *testing.T) {
	sep := string(filepath.Separator)
	home := sep + "home" + sep + "u"
	if got := ExpandHome("~", home); got != home {
		t.Errorf("got %q", got)
	}
	if got := ExpandHome("~/code", home); got != filepath.Join(home, "code") {
		t.Errorf("got %q", got)
	}
	if got := ExpandHome("~other/x", home); got != "~other/x" {
		t.Errorf("only ~/ expands, got %q", got)
	}
	if got := Scrub(filepath.Join(home, "code", "a"), home); got != "~/code/a" {
		t.Errorf("got %q", got)
	}
	if got := Scrub(home+"x"+sep+"a", home); got != home+"x"+sep+"a" {
		t.Errorf("sibling dir with same prefix must not scrub: %q", got)
	}
	if got := Scrub(home, home); got != "~" {
		t.Errorf("got %q", got)
	}
}

func TestWithin(t *testing.T) {
	for _, c := range []struct {
		child, root string
		want        bool
	}{{"/a/b", "/a", true}, {"/a", "/a", true}, {"/ab", "/a", false}, {"/a/b", "/a/", true}, {"/", "/a", false}} {
		if got := Within(c.child, c.root); got != c.want {
			t.Errorf("Within(%q,%q)=%v", c.child, c.root, got)
		}
	}
}

func TestNormalizeNonexistentAndSymlink(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "deleted", "project")
	if got := Normalize(gone); got != gone {
		t.Errorf("deleted folders must normalize stably: %q", got)
	}
	real := filepath.Join(dir, "real")
	_ = os.Mkdir(real, 0o755)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if got := Normalize(filepath.Join(link, "sub")); got != filepath.Join(real, "sub") {
		t.Errorf("symlink not resolved through missing leaf: %q", got)
	}
	if Normalize("") != "" {
		t.Error("empty stays empty")
	}
}

func TestFold(t *testing.T) {
	if Fold("/A/B", true) != "/a/b" || Fold("/A/B", false) != "/A/B" {
		t.Error("fold")
	}
}
