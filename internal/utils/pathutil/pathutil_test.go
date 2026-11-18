package pathutil

import (
	"os"
	"path/filepath"
	"strings"
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

func TestScrubText(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "u")
	in := "cursor: unknown shape in " + filepath.Join(home, ".config", "Cursor", "state.vscdb") + "; " + filepath.Join(string(filepath.Separator), "home", "u2", "x")
	got := ScrubText(in, home)
	if strings.Contains(got, home+string(filepath.Separator)) || !strings.Contains(got, "~/") {
		t.Errorf("home directory not scrubbed: %q", got)
	}
	if !strings.Contains(got, filepath.Join(string(filepath.Separator), "home", "u2")) {
		t.Errorf("another user's directory must be left alone: %q", got)
	}
	if ScrubText("x", "") != "x" {
		t.Error("no home, no change")
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

func TestIsVirtualKey(t *testing.T) {
	for k, want := range map[string]bool{"claude-ai:Chats": true, "chatgpt:project-ab12": true, "opencode:x": true,
		`C:\Users\x`: false, "C:/x": false, "/home/u/a:b": false, "~/a:b": false, "": false, "plain": false, ":x": false, "a/b:c": false} {
		if got := IsVirtualKey(k); got != want {
			t.Errorf("%q: got %v want %v", k, got, want)
		}
	}
}
