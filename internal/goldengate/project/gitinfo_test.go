package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGitInfoReadsRemoteAndBranch(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	write(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/fix/lin-412-auth\n")
	write(t, filepath.Join(repo, ".git", "config"), "[core]\n\turl = not-a-remote\n[remote \"upstream\"]\n\turl = https://example.com/up/app.git\n"+
		"[remote \"origin\"]\n\turl = git@github.com:acme/app.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"main\"]\n\tremote = origin\n")
	if remote, branch := GitInfo(repo); remote != "git@github.com:acme/app.git" || branch != "fix/lin-412-auth" {
		t.Errorf("repository: %q %q", remote, branch)
	}

	// a linked worktree: its own HEAD, the main repository's config
	wt := filepath.Join(root, "app-wt")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n")
	write(t, filepath.Join(repo, ".git", "worktrees", "wt", "HEAD"), "ref: refs/heads/feature/x\n")
	write(t, filepath.Join(repo, ".git", "worktrees", "wt", "commondir"), "../..\n")
	if remote, branch := GitInfo(wt); remote != "git@github.com:acme/app.git" || branch != "feature/x" {
		t.Errorf("worktree: %q %q", remote, branch)
	}

	// no origin: the first remote; a detached HEAD has no branch
	other := filepath.Join(root, "other")
	write(t, filepath.Join(other, ".git", "HEAD"), "0123456789abcdef0123456789abcdef01234567\n")
	write(t, filepath.Join(other, ".git", "config"), "[remote \"backup\"]\n  url = https://example.com/b.git\n")
	if remote, branch := GitInfo(other); remote != "https://example.com/b.git" || branch != "" {
		t.Errorf("first remote, detached: %q %q", remote, branch)
	}

	// not a repository, and metadata that is too large or not a plain file
	if remote, branch := GitInfo(filepath.Join(root, "nothing")); remote != "" || branch != "" {
		t.Errorf("no repository: %q %q", remote, branch)
	}
	big := filepath.Join(root, "big")
	write(t, filepath.Join(big, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(big, ".git", "config"), "[remote \"origin\"]\n\turl = x\n"+strings.Repeat("# pad\n", 20000))
	if remote, _ := GitInfo(big); remote != "" {
		t.Errorf("an oversized config is not read: %q", remote)
	}
	link := filepath.Join(root, "link")
	write(t, filepath.Join(link, ".git", "HEAD"), "ref: refs/heads/main\n")
	if err := os.Symlink(filepath.Join(repo, ".git", "config"), filepath.Join(link, ".git", "config")); err == nil {
		if remote, _ := GitInfo(link); remote != "" {
			t.Errorf("a symlinked config is not followed: %q", remote)
		}
	}
}
