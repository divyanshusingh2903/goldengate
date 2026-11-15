package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gitRepo makes a folder that looks like a git repository with the given
// remote and branch (metadata files only).
func gitRepo(t *testing.T, dir, remote, branch string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[remote \"origin\"]\n\turl = "+remote+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A folder with sessions that is in no project gets a suggested project
// with the reason. It is only a suggestion: nothing is shared until the
// user accepts, accepting is one action, and "not now" is remembered.
func TestProjectSuggestionsForUnsharedFolders(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app, clone, issue, stranger, secret := filepath.Join(root, "app"), filepath.Join(root, "app-second-clone"), filepath.Join(root, "eng-work"),
		filepath.Join(root, "stranger"), filepath.Join(root, "denied")
	gitRepo(t, app, "git@github.com:acme/app.git", "main")
	gitRepo(t, clone, "https://github.com/acme/app", "main")           // same remote, written differently
	gitRepo(t, issue, "git@github.com:acme/tools.git", "fix/eng-12-x") // a branch named after an ENG issue
	gitRepo(t, stranger, "git@github.com:other/thing.git", "main")
	gitRepo(t, secret, "git@github.com:acme/app.git", "main")
	l.addClaudeSession("aaaaaaaa-0000-4000-8000-000000000001", app, "app work")
	l.addClaudeSession("aaaaaaaa-0000-4000-8000-000000000002", clone, "CLONEWORK one")
	l.addClaudeSession("aaaaaaaa-0000-4000-8000-000000000003", clone, "CLONEWORK two")
	l.addClaudeSession("aaaaaaaa-0000-4000-8000-000000000004", issue, "ISSUEWORK")
	l.addClaudeSession("aaaaaaaa-0000-4000-8000-000000000005", stranger, "STRANGERWORK")
	l.addClaudeSession("aaaaaaaa-0000-4000-8000-000000000006", secret, "DENIEDWORK")
	// a linked worktree of app, somewhere else on disk
	wt := filepath.Join(root, "app-worktree")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(app, ".git", "worktrees", "wt")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l.addClaudeSession("aaaaaaaa-0000-4000-8000-000000000007", wt, "WORKTREEWORK")

	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "projects", "create", "App", app)
	l.must(nil, "share", "deny", secret)

	// without enrich.git nothing about the folders' repositories is read
	out := l.must(nil, "projects", "suggest")
	if !strings.Contains(out, "no suggestions") || !strings.Contains(out, "git = true") {
		t.Fatalf("no git metadata is read unless enrich.git is on: %s", out)
	}
	cfg := l.readConfig(t)
	cfg = strings.Replace(cfg, "git = false", "git = true", 1)
	cfg = strings.Replace(cfg, "name = 'App'", "name = 'App'\nwork_items = ['linear:ENG']", 1)
	if err := os.WriteFile(filepath.Join(l.root, "cfg", "goldengate", "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	out = l.must(nil, "inbox")
	ids := map[string]string{}
	for _, m := range regexp.MustCompile(`suggestion (s_[0-9a-f]{8}): Add (\S+) to project "App"\?`).FindAllStringSubmatch(out, -1) {
		ids[m[2]] = m[1]
	}
	if len(ids) != 3 || ids[clone] == "" || ids[issue] == "" || ids[wt] == "" {
		t.Fatalf("the clone, the worktree and the issue folder are suggested, nothing else:\n%s", out)
	}
	for _, want := range []string{
		"2 session(s) there are in no project. It has the same repository remote as " + app + " (github.com/acme/app).",
		"1 session(s) there are in no project. Its branch fix/eng-12-x names ENG-12, which is under the project's work-item prefix linear:ENG.",
		"1 session(s) there are in no project. It is a worktree of " + app + ", which is in the project.",
		"adds " + clone + ` to project "App", which shares it`,
		"wait in the inbox for your approval; nothing is sent by accepting",
		"goldengate projects accept " + ids[clone], "goldengate projects dismiss " + ids[clone],
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the suggestion lacks %q:\n%s", want, out)
		}
	}
	// no session of an unshared folder was read, and nothing changed
	for _, leak := range []string{"CLONEWORK", "ISSUEWORK", "STRANGERWORK", "DENIEDWORK", "WORKTREEWORK", "stranger", "denied"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q appears in the inbox:\n%s", leak, out)
		}
	}
	if now := l.readConfig(t); strings.Contains(now, clone) {
		t.Fatal("a suggestion changed the config")
	}
	l.must(nil, "sync", "--approve")
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "CLONEWORK") || strings.Contains(held, "ISSUEWORK") {
		t.Fatal("a suggested folder was sent before it was accepted")
	}

	// not now is remembered
	if out := l.must(nil, "projects", "dismiss", ids[issue]); !strings.Contains(out, "will not be offered again") {
		t.Fatalf("dismiss: %s", out)
	}
	if out := l.must(nil, "projects", "suggest"); strings.Contains(out, issue) || !strings.Contains(out, clone) {
		t.Fatalf("after not now: %s", out)
	}

	// accepting is one action: the folder joins the project, its sessions
	// wait in the inbox, and nothing was sent by accepting
	if out := l.must(nil, "projects", "accept", ids[clone]); !strings.Contains(out, `"App": added `+clone+" (shared)") {
		t.Fatalf("accept: %s", out)
	}
	if out := l.must(nil, "projects", "list"); !strings.Contains(out, clone) {
		t.Fatalf("the folder is a member now: %s", out)
	}
	out = l.must(nil, "inbox")
	if !strings.Contains(out, "2 session(s) waiting for approval") || !strings.Contains(out, "CLONEWORK one") || strings.Contains(out, "suggestion "+ids[clone]) || !strings.Contains(out, "suggestion "+ids[wt]) {
		t.Fatalf("its sessions wait; its suggestion is gone and the worktree's is still there:\n%s", out)
	}
	l.must(nil, "sync")
	if strings.Contains(everythingTheServerHolds(t, b), "CLONEWORK") {
		t.Fatal("accepting a suggestion must not send anything by itself")
	}
	for _, bad := range [][]string{{"projects", "accept", "s_00000000"}, {"projects", "dismiss", ids[issue]}} {
		if err := l.run(nil, bad...); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
}
