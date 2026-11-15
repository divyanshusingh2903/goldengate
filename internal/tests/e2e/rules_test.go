package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Rules and trust levels from the command line: a rule does nothing at
// "ask", marks sessions at "suggest", sends them at "auto"; a folder nobody
// named is asked about first; an exclude rule keeps sessions local.
func TestRulesAndTrustLevelsFromTheCommandLine(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")

	// a rule on a project at "ask" says that it will not act yet
	out := l.must(nil, "rules", "add", "demo", "--source", "claude-code")
	id := regexp.MustCompile(`r_[0-9a-f]{8}`).FindString(out)
	if id == "" || !strings.Contains(out, "approve sessions from claude-code") || !strings.Contains(out, "goldengate projects trust") {
		t.Fatalf("rules add: %s", out)
	}
	l.must(nil, "sync")
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "widget") {
		t.Fatal("at ask, an approve rule sends nothing")
	}
	if out := l.must(nil, "inbox"); strings.Contains(out, "suggested") {
		t.Fatalf("at ask, nothing is suggested: %s", out)
	}

	// suggest: still nothing is sent, the inbox says which rule suggests it,
	// and one action approves what the rules suggest
	if out := l.must(nil, "projects", "trust", "demo", "suggest"); !strings.Contains(out, "nothing is sent until you approve") {
		t.Fatalf("trust suggest: %s", out)
	}
	l.errOut.Reset()
	l.must(nil, "sync")
	if !strings.Contains(l.errOut.String(), "1 waiting sessions are approved by your rules") || strings.Contains(everythingTheServerHolds(t, b), "widget") {
		t.Fatalf("suggest sends nothing and says what it suggests:\n%s", l.errOut)
	}
	if out := l.must(nil, "inbox"); !strings.Contains(out, "suggested by rule "+id) {
		t.Fatalf("inbox names the rule: %s", out)
	}
	if out := l.must(nil, "inbox", "approve", "--suggested"); !strings.Contains(out, "approved 1 session(s)") {
		t.Fatalf("approve --suggested: %s", out)
	}
	l.must(nil, "sync")
	if !strings.Contains(everythingTheServerHolds(t, b), "widget") {
		t.Fatal("the approved session is sent")
	}

	// auto: a new session the rule matches is sent on the next sync, and sync says so
	l.must(nil, "projects", "trust", "demo", "auto")
	l.addClaudeSession("33333333-3333-4333-8333-333333333333", "/home/user/projects/demo", "second thing AUTOSENT")
	l.errOut.Reset()
	l.must(nil, "sync")
	if !strings.Contains(everythingTheServerHolds(t, b), "AUTOSENT") || !strings.Contains(l.errOut.String(), "1 of them were sent by your projects' trust level and rules") {
		t.Fatalf("auto sends what the rule approves and reports it:\n%s", l.errOut)
	}

	// a folder under the shared one that nobody named is asked about first
	l.addClaudeSession("44444444-4444-4444-8444-444444444444", "/home/user/projects/demo/sub", "NEWFOLDER work")
	l.must(nil, "sync")
	if strings.Contains(everythingTheServerHolds(t, b), "NEWFOLDER") {
		t.Fatal("auto sent from a folder it had never sent from")
	}
	if out := l.must(nil, "inbox"); !strings.Contains(out, "first session from this folder or chat") {
		t.Fatalf("the inbox says why it waits: %s", out)
	}

	// an exclude rule keeps a folder's sessions local, whatever else matches
	out = l.must(nil, "rules", "add", "demo", "--exclude", "--folder", "~/projects/demo/sub")
	if !strings.Contains(out, "exclude sessions in ~/projects/demo/sub") {
		t.Fatalf("exclude rule: %s", out)
	}
	l.errOut.Reset()
	l.must(nil, "sync", "--approve")
	if strings.Contains(everythingTheServerHolds(t, b), "NEWFOLDER") || !strings.Contains(l.errOut.String(), "kept back by an exclude rule") {
		t.Fatalf("an excluded session is not sent even with --approve:\n%s", l.errOut)
	}

	// listing and removing
	out = l.must(nil, "rules", "list", "demo")
	if !strings.Contains(out, id) || !strings.Contains(out, "trust auto") || strings.Count(out, "r_") != 2 {
		t.Fatalf("rules list: %s", out)
	}
	if out := l.must(nil, "projects", "list"); !strings.Contains(out, "rule "+id) || !strings.Contains(out, "[auto: sessions its rules approve are sent]") {
		t.Fatalf("projects list shows trust and rules: %s", out)
	}
	l.must(nil, "rules", "remove", "demo", id)
	if out := l.must(nil, "rules", "list"); strings.Contains(out, id) {
		t.Fatalf("removed: %s", out)
	}
	if cfg := l.readConfig(t); !strings.Contains(cfg, "trust = 'auto'") || !strings.Contains(cfg, "action = 'exclude'") || strings.Contains(cfg, "auto_approve") {
		t.Fatalf("the config holds trust and rules:\n%s", cfg)
	}

	for _, bad := range [][]string{
		{"rules", "add", "nope", "--source", "codex"},
		{"rules", "add", "demo", "--exclude"},
		{"rules", "remove", "demo", "r_00000000"},
		{"projects", "trust", "demo", "always"},
		{"projects", "trust", "nope", "auto"},
		{"inbox", "approve", "--all", "--suggested"},
	} {
		if err := l.run(nil, bad...); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
	// the older switch still works and maps onto trust
	l.must(nil, "projects", "auto", "demo", "off")
	if out := l.must(nil, "projects", "list"); !strings.Contains(out, "[ask: new sessions wait in the inbox]") {
		t.Fatalf("auto off is ask: %s", out)
	}
}

// Receipts from the command line: an automatic send is listed with its rule,
// can be retracted from the receipt, and sessions approved by hand are not
// in it.
func TestReceiptsFromTheCommandLine(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	var notes []string
	l.notify = func(title, body string) error { notes = append(notes, title+": "+body); return nil }
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	if out := l.must(nil, "receipts"); !strings.Contains(out, "no receipts") {
		t.Fatalf("nothing yet: %s", out)
	}
	// approved by hand: sent, but no receipt
	l.must(nil, "sync", "--approve")
	if out := l.must(nil, "receipts"); !strings.Contains(out, "no receipts") {
		t.Fatalf("a manual approval leaves no receipt: %s", out)
	}

	out := l.must(nil, "rules", "add", "demo", "--source", "claude-code")
	rule := regexp.MustCompile(`r_[0-9a-f]{8}`).FindString(out)
	l.must(nil, "projects", "trust", "demo", "auto")
	l.addClaudeSession("33333333-3333-4333-8333-333333333333", "/home/user/projects/demo", "second thing AUTOSENT")
	l.errOut.Reset()
	l.must(nil, "sync", "--quiet")
	if len(notes) != 0 {
		t.Fatalf("notifications are off by default: %v", notes)
	}
	out = l.must(nil, "receipts")
	id := regexp.MustCompile(`rc_[0-9a-f]{8}`).FindString(out)
	if id == "" || !strings.Contains(out, "1 session(s) to demo") {
		t.Fatalf("receipts: %s", out)
	}
	out = l.must(nil, "receipts", "show")
	for _, want := range []string{"receipt " + id, "33333333-3333-4333-8333-333333333333", "AUTOSENT", "why: rule " + rule + ": approve sessions from claude-code", "goldengate receipts retract " + id} {
		if !strings.Contains(out, want) {
			t.Fatalf("receipts show lacks %q: %s", want, out)
		}
	}
	if strings.Contains(out, demoSession) {
		t.Fatalf("the session approved by hand is not in a receipt: %s", out)
	}
	// which assistants can read it, asked from the server with the admin token
	out = l.must(map[string]string{"GOLDENGATE_ADMIN_TOKEN": adminSecret}, "receipts", "show", id, "--agents")
	if !strings.Contains(out, "visible to: Default") {
		t.Fatalf("--agents names who can read the project: %s", out)
	}

	// retract from the receipt: gone from the server, marked, never sent again
	for _, bad := range [][]string{
		{"receipts", "retract", id},
		{"receipts", "retract", id, demoSession},
		{"receipts", "retract", "rc_00000000", "--all"},
		{"receipts", "show", "rc_00000000"},
	} {
		if err := l.run(nil, bad...); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
	l.must(nil, "receipts", "retract", id, "--all")
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "AUTOSENT") || !strings.Contains(held, "widget") {
		t.Fatal("only the retracted session leaves the server")
	}
	if out := l.must(nil, "receipts", "show", id); !strings.Contains(out, "[retracted ") || strings.Contains(out, "take back:") {
		t.Fatalf("the receipt shows the retraction: %s", out)
	}
	if out := l.must(nil, "receipts"); !strings.Contains(out, "(1 retracted)") {
		t.Fatalf("list: %s", out)
	}
	if out := l.must(nil, "receipts", "retract", id, "--all"); !strings.Contains(out, "already retracted") {
		t.Fatalf("nothing left: %s", out)
	}
	l.must(nil, "sync")
	if strings.Contains(everythingTheServerHolds(t, b), "AUTOSENT") {
		t.Fatal("a retracted session was sent again")
	}

	// the notification is opt-in and carries counts only
	cfg := l.readConfig(t)
	if err := os.WriteFile(filepath.Join(l.root, "cfg", "goldengate", "config.toml"), []byte(strings.Replace(cfg, "[ui]", "[ui]\nnotify_receipts = true", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	l.addClaudeSession("55555555-5555-4555-8555-555555555555", "/home/user/projects/demo", "third thing NOTIFIED")
	l.must(nil, "sync", "--quiet")
	if len(notes) != 1 || !strings.Contains(notes[0], "1 session(s) sent automatically to 1 project(s)") || strings.Contains(notes[0], "NOTIFIED") || strings.Contains(notes[0], "demo") {
		t.Fatalf("notification: %v", notes)
	}
}
