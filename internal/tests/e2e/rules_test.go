package e2e

import (
	"fmt"
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

// The unattended sync (a timer or a hook runs `goldengate sync --auto`)
// follows the inbox: it never sends what waits, says what it left, and
// lists everything it did send in a receipt.
func TestAutoSyncSendsOnlyWhatIsApproved(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")

	// a pending session and no rule: nothing is sent, and it says so
	l.errOut.Reset()
	l.must(nil, "sync", "--auto")
	line := strings.TrimSpace(l.errOut.String())
	if line != "goldengate auto sync: nothing new was sent; 1 session(s) wait in the inbox and were not sent (goldengate inbox)" {
		t.Fatalf("report: %q", line)
	}
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "widget") {
		t.Fatal("--auto sent a session that waits in the inbox")
	}
	if out := l.must(nil, "receipts"); !strings.Contains(out, "no receipts") {
		t.Fatalf("nothing sent, no receipt: %s", out)
	}
	// it cannot be told to approve
	for _, bad := range [][]string{{"sync", "--auto", "--approve"}, {"sync", "--auto", "--resummarize"}} {
		if err := l.run(nil, bad...); err == nil || !strings.Contains(err.Error(), "unattended") {
			t.Errorf("%v must be refused: %v", bad, err)
		}
	}
	// --quiet prints nothing at all
	l.errOut.Reset()
	l.must(nil, "sync", "--auto", "--quiet")
	if l.errOut.Len() != 0 || l.out.Len() != 0 {
		t.Fatalf("--auto --quiet is silent: %q %q", l.errOut, l.out)
	}

	// what the user approved is sent by the next unattended run, and the
	// receipt says why it left
	l.must(nil, "inbox", "approve", "--all")
	l.errOut.Reset()
	l.must(nil, "sync", "--auto")
	line = strings.TrimSpace(l.errOut.String())
	id := regexp.MustCompile(`rc_[0-9a-f]{8}`).FindString(line)
	if id == "" || !strings.HasPrefix(line, "goldengate auto sync: 1 session(s) sent, listed in receipt "+id) {
		t.Fatalf("report: %q", line)
	}
	if !strings.Contains(everythingTheServerHolds(t, b), "widget") {
		t.Fatal("the approved session is sent")
	}
	if out := l.must(nil, "receipts", "show", id); !strings.Contains(out, demoSession) || !strings.Contains(out, "why: you approved it in the inbox") {
		t.Fatalf("the receipt lists what the unattended run sent: %s", out)
	}

	// a rule at trust auto: the next session goes on its own; one that the
	// rule does not match still waits
	l.must(nil, "rules", "add", "demo", "--folder", "~/projects/demo")
	l.must(nil, "projects", "trust", "demo", "auto")
	l.addClaudeSession("33333333-3333-4333-8333-333333333333", "/home/user/projects/demo", "second thing AUTOSENT")
	l.addClaudeSession("44444444-4444-4444-8444-444444444444", "/home/user/projects/demo/sub", "NEWFOLDER work")
	l.errOut.Reset()
	l.must(nil, "sync", "--auto")
	line = strings.TrimSpace(l.errOut.String())
	if !strings.Contains(line, "1 session(s) sent, listed in receipt") || !strings.Contains(line, "1 session(s) wait in the inbox and were not sent") {
		t.Fatalf("report: %q", line)
	}
	held := everythingTheServerHolds(t, b)
	if !strings.Contains(held, "AUTOSENT") || strings.Contains(held, "NEWFOLDER") {
		t.Fatal("the rule's match is sent, the session from a new folder is not")
	}
	// a second run has nothing to do
	l.errOut.Reset()
	l.must(nil, "sync", "--auto")
	if line := strings.TrimSpace(l.errOut.String()); !strings.HasPrefix(line, "goldengate auto sync: nothing new was sent") {
		t.Fatalf("idle run: %q", line)
	}
}

// After the same manual choice several times, Golden Gate offers a rule. It
// only offers: nothing changes without a yes, the offer shows the exact rule
// and what it would have matched, and "not now" is remembered.
func TestRuleOffersFromManualChoices(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "share", "add", "~/projects/other")
	session := func(n int, folder, prompt string) {
		id := fmt.Sprintf("%08d-0000-4000-8000-000000000000", n)
		l.addClaudeSession(id, "/home/user/projects/"+folder, prompt)
	}
	// four sessions sent by hand from demo (one is there already): no offer yet
	for i := 1; i <= 3; i++ {
		session(i, "demo", fmt.Sprintf("demo task %d", i))
	}
	l.must(nil, "sync", "--approve")
	if out := l.must(nil, "rules", "offers"); !strings.Contains(out, "no offers") {
		t.Fatalf("four manual sends are not a pattern yet: %s", out)
	}
	// the fifth makes the offer, in the inbox and under rules offers
	session(4, "demo", "demo task 4")
	for i := 11; i <= 15; i++ {
		session(i, "other", fmt.Sprintf("other task %d", i))
	}
	l.must(nil, "sync", "--approve")
	out := l.must(nil, "inbox")
	ids := regexp.MustCompile(`offer (o_[0-9a-f]{8}): Share sessions from (~/projects/\w+) automatically\?`).FindAllStringSubmatch(out, -1)
	if len(ids) != 2 {
		t.Fatalf("two folders, two offers:\n%s", out)
	}
	offer := map[string]string{}
	for _, m := range ids {
		offer[m[2]] = m[1]
	}
	for _, want := range []string{
		"You sent 5 sessions from there by hand in the last 30 days.", "Such as:", "demo task",
		`adds to project "demo" the rule: approve sessions in ~/projects/demo`,
		"sets the project's trust level to auto",
		"goldengate rules accept " + offer["~/projects/demo"], "goldengate rules dismiss " + offer["~/projects/demo"],
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the offer lacks %q:\n%s", want, out)
		}
	}
	// an offer changes nothing by itself
	if cfg := l.readConfig(t); strings.Contains(cfg, "rules") || strings.Contains(cfg, "trust") {
		t.Fatalf("an offer wrote to the config:\n%s", cfg)
	}
	if out := l.must(nil, "rules", "offers"); !strings.Contains(out, "nothing changes unless you accept") || strings.Count(out, "offer o_") != 2 {
		t.Fatalf("rules offers: %s", out)
	}

	// not now is remembered, even as more sessions are sent by hand
	if out := l.must(nil, "rules", "dismiss", offer["~/projects/other"]); !strings.Contains(out, "will not be offered again") {
		t.Fatalf("dismiss: %s", out)
	}
	session(16, "other", "other task 16")
	l.must(nil, "sync", "--approve")
	if out := l.must(nil, "rules", "offers"); strings.Contains(out, "~/projects/other") || !strings.Contains(out, "~/projects/demo") {
		t.Fatalf("a dismissed offer came back, or the other one went: %s", out)
	}

	// yes adds exactly the rule shown and sets the trust level
	out = l.must(nil, "rules", "accept", offer["~/projects/demo"])
	if !strings.Contains(out, "approve sessions in ~/projects/demo") || !strings.Contains(out, "trust level is now auto") {
		t.Fatalf("accept: %s", out)
	}
	cfg := l.readConfig(t)
	if strings.Count(cfg, "[[projects.rules]]") != 1 || !strings.Contains(cfg, "folder = '~/projects/demo'") || strings.Count(cfg, "trust = 'auto'") != 1 {
		t.Fatalf("one rule and one trust level, for demo only:\n%s", cfg)
	}
	if out := l.must(nil, "rules", "offers"); !strings.Contains(out, "no offers") {
		t.Fatalf("an accepted offer is not made again: %s", out)
	}
	// the rule now does what the user kept doing by hand
	session(5, "demo", "demo task AUTOSENT")
	session(17, "other", "other task WAITS")
	l.must(nil, "sync")
	held := everythingTheServerHolds(t, b)
	if !strings.Contains(held, "AUTOSENT") || strings.Contains(held, "WAITS") {
		t.Fatal("demo sends on its own now; other still waits")
	}
	// sessions the rule sent are not counted as manual choices
	for _, bad := range [][]string{{"rules", "accept", "o_00000000"}, {"rules", "dismiss", offer["~/projects/other"]}} {
		if err := l.run(nil, bad...); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
}
