package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OWNER/goldengate/internal/model"
)

func TestRoot(t *testing.T) {
	r := Resolver{}
	cases := []struct {
		name string
		ref  model.SessionRef
		want string
	}{
		{"plain folder", model.SessionRef{ProjectKey: "/home/u/code/app"}, ""},
		{"claude worktree", model.SessionRef{ProjectKey: "/home/u/code/app/.claude/worktrees/happy-fox"}, "/home/u/code/app"},
		{"claude worktree subfolder", model.SessionRef{ProjectKey: "/home/u/code/app/.claude/worktrees/happy-fox/packages/x"}, "/home/u/code/app"},
		{"marker without a name", model.SessionRef{ProjectKey: "/home/u/code/app/.claude/worktrees/"}, ""},
		{"marker at the very start", model.SessionRef{ProjectKey: "/.claude/worktrees/x"}, ""},
		{"tool-recorded root", model.SessionRef{ProjectKey: "/home/u/code/app-wt", RepoRoot: "/home/u/code/app"}, "/home/u/code/app"},
		{"root equals folder", model.SessionRef{ProjectKey: "/home/u/code/app", RepoRoot: "/home/u/code/app"}, ""},
		{"global project", model.SessionRef{ProjectKey: "/home/u/other", RepoRoot: "/"}, ""},
		{"relative root is ignored", model.SessionRef{ProjectKey: "/home/u/x", RepoRoot: "app"}, ""},
		{"chat project", model.SessionRef{ProjectKey: "claude-ai:Chats"}, ""},
		{"empty", model.SessionRef{}, ""},
	}
	for _, c := range cases {
		if got := r.Root(c.ref); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestGitPointerIsOptIn(t *testing.T) {
	main := t.TempDir()
	wt := filepath.Join(t.TempDir(), "feature")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	pointer := "gitdir: " + filepath.Join(main, ".git", "worktrees", "feature") + "\n"
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(pointer), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := model.SessionRef{ProjectKey: wt}
	if got := (Resolver{}).Root(ref); got != "" {
		t.Errorf("without enrich.git no file may be read, got %q", got)
	}
	mainReal, _ := filepath.EvalSymlinks(main)
	if got := (Resolver{ReadGitPointers: true}).Root(ref); got != mainReal {
		t.Errorf("with enrich.git: got %q want %q", got, mainReal)
	}
	// a normal repository (.git is a directory) and junk pointers resolve to nothing
	repo := t.TempDir()
	_ = os.Mkdir(filepath.Join(repo, ".git"), 0o755)
	junk := t.TempDir()
	_ = os.WriteFile(filepath.Join(junk, ".git"), []byte("not a pointer\n"), 0o600)
	huge := t.TempDir()
	_ = os.WriteFile(filepath.Join(huge, ".git"), make([]byte, 10000), 0o600)
	for _, d := range []string{repo, junk, huge, filepath.Join(repo, "missing")} {
		if got := (Resolver{ReadGitPointers: true}).Root(model.SessionRef{ProjectKey: d}); got != "" {
			t.Errorf("%s: %q", d, got)
		}
	}
}
