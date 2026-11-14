package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
)

const demoSession = "11111111-1111-4111-8111-111111111111"

// A project shared now waits for approval: nothing leaves until the user
// approves it, from the CLI or by sending a reviewed plan.
func TestInboxFromTheCommandLine(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	ctx := context.Background()
	l.must(l.initEnv(b), "init", "--non-interactive")
	out := l.must(nil, "share", "add", "~/projects/demo")
	if !strings.Contains(out, "wait for your approval") {
		t.Fatalf("sharing says the sessions wait: %s", out)
	}
	if out := l.must(nil, "projects", "list"); !strings.Contains(out, "[new sessions wait in the inbox]") {
		t.Fatalf("projects list shows the mode: %s", out)
	}

	// sync sends nothing and says why
	if out := l.must(nil, "sync", "--dry-run"); strings.Contains(out, "please ship the widget") {
		t.Fatalf("a pending session must not be in a dry run: %s", out)
	}
	l.must(nil, "sync")
	if !strings.Contains(l.errOut.String(), "1 sessions wait in the inbox and were not sent") {
		t.Fatalf("sync reports what waits:\n%s", l.errOut)
	}
	if ps, _ := b.st.ListProjects(ctx); len(ps) != 0 {
		t.Fatalf("nothing reached the server: %+v", ps)
	}
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "widget") {
		t.Fatal("pending content reached the server")
	}

	// the inbox lists it; looking first prints the payload without sending or approving
	out = l.must(nil, "inbox")
	if !strings.Contains(out, "1 session(s) waiting for approval") || !strings.Contains(out, demoSession) || !strings.Contains(out, "please ship the widget") || !strings.Contains(out, "(new)") {
		t.Fatalf("inbox: %s", out)
	}
	if out := l.must(nil, "sync", "--dry-run", "--approve"); !strings.Contains(out, "please ship the widget") {
		t.Fatalf("--dry-run --approve shows what approving would send: %s", out)
	}
	if out := l.must(nil, "inbox", "list"); !strings.Contains(out, "1 session(s) waiting") {
		t.Fatalf("looking approves nothing: %s", out)
	}
	if err := l.run(nil, "inbox", "approve"); err == nil {
		t.Error("approve needs ids or --all")
	}
	if err := l.run(nil, "inbox", "approve", "no-such-session"); err == nil || !strings.Contains(err.Error(), "not waiting in the inbox") {
		t.Errorf("unknown id: %v", err)
	}

	// approve, then sync sends exactly that
	if out := l.must(nil, "inbox", "approve", demoSession); !strings.Contains(out, "approved 1 session(s)") {
		t.Fatalf("approve: %s", out)
	}
	if out := l.must(nil, "inbox"); !strings.Contains(out, "nothing is waiting") {
		t.Fatalf("approved sessions leave the inbox: %s", out)
	}
	l.must(nil, "sync")
	if ps, _ := b.st.ListProjects(ctx); len(ps) != 1 || ps[0].Sessions != 1 {
		t.Fatalf("the approved session was sent: %+v", ps)
	}

	// the session continues: it comes back as changed and is not re-sent on the old approval
	// (the summary need not mention the new message, so sends are counted)
	sent := countAction(t, b, "ingest")
	l.addClaudeTurn(demoSession, "/home/user/projects/demo", "one more thing, later")
	l.must(nil, "sync")
	if got := countAction(t, b, "ingest"); got != sent {
		t.Fatal("changed content was sent without a new approval")
	}
	if out := l.must(nil, "inbox"); !strings.Contains(out, "changed since you approved or sent it") {
		t.Fatalf("the changed session is back: %s", out)
	}

	// excluding keeps it local for good, also with --approve; reset undoes that
	if out := l.must(nil, "inbox", "exclude", "--all"); !strings.Contains(out, "excluded 1 session(s)") {
		t.Fatalf("exclude: %s", out)
	}
	l.must(nil, "sync", "--approve")
	if got := countAction(t, b, "ingest"); got != sent {
		t.Fatal("an excluded session was sent")
	}
	if out := l.must(nil, "inbox"); !strings.Contains(out, "1 excluded session(s), never sent") || !strings.Contains(out, demoSession) {
		t.Fatalf("excluded sessions are listed: %s", out)
	}
	l.must(nil, "inbox", "reset", demoSession)
	if err := l.run(nil, "inbox", "reset", demoSession); err == nil {
		t.Error("resetting twice reports that nothing is recorded")
	}
	l.must(nil, "sync", "--approve")
	if got := countAction(t, b, "ingest"); got != sent+1 {
		t.Fatalf("sync --approve sends what waits: %d -> %d", sent, got)
	}

	// auto-approve: from now on changes go without asking
	if out := l.must(nil, "projects", "auto", "demo", "on"); !strings.Contains(out, "without asking") {
		t.Fatalf("auto on: %s", out)
	}
	l.addClaudeTurn(demoSession, "/home/user/projects/demo", "and one more")
	l.must(nil, "sync")
	if got := countAction(t, b, "ingest"); got != sent+2 {
		t.Fatalf("an auto-approved project sends changes: %d -> %d", sent, got)
	}
	if err := l.run(nil, "projects", "auto", "demo", "maybe"); err == nil {
		t.Error("on or off only")
	}
	l.must(nil, "projects", "auto", "demo", "off")
	if out := l.must(nil, "projects", "list"); !strings.Contains(out, "[new sessions wait in the inbox]") {
		t.Fatalf("auto off: %s", out)
	}
}

// The TUI's review-then-send is the approval; the backend also lists and
// decides the inbox for a UI that wants to show it.
func TestInboxThroughTheBackend(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	ctx := context.Background()
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "share", "add", "~/projects/client-secret")
	be := backend(l)

	in, err := be.Inbox(ctx)
	if err != nil || len(in.Pending) != 2 || len(in.Excluded) != 0 {
		t.Fatalf("inbox: %+v %v", in, err)
	}
	var demo, secret tui.InboxItem
	for _, it := range in.Pending {
		if strings.Contains(it.Title, "widget") {
			demo = it
		} else {
			secret = it
		}
	}
	if demo.ID != demoSession || demo.Project != "demo" || demo.Hash == "" || demo.Changed {
		t.Fatalf("item: %+v", demo)
	}
	// exclude one; the review then holds the other and says it approves it
	if err := be.InboxDecide(ctx, []tui.InboxItem{secret}, tui.InboxExclude); err != nil {
		t.Fatal(err)
	}
	plan, err := be.Plan(ctx, tui.SyncOpts{})
	if err != nil || plan.Sessions != 1 || plan.Approving != 1 || plan.Excluded != 1 {
		t.Fatalf("the review: %+v %v", plan, err)
	}
	if err := be.Sync(ctx, false, tui.SyncOpts{}, func(tui.SyncEvent) {}); err != nil {
		t.Fatal(err)
	}
	held := everythingTheServerHolds(t, b)
	if !strings.Contains(held, "please ship the widget") || strings.Contains(held, "TOPSECRET") {
		t.Fatalf("sending the review sends what was reviewed, never the excluded session:\n%s", held)
	}
	in, _ = be.Inbox(ctx)
	if len(in.Pending) != 0 || len(in.Excluded) != 1 || in.Excluded[0].ID != secret.ID {
		t.Fatalf("after sending: %+v", in)
	}
	// reset: it waits again; approve: a plain CLI sync sends it
	if err := be.InboxDecide(ctx, in.Excluded, tui.InboxReset); err != nil {
		t.Fatal(err)
	}
	in, _ = be.Inbox(ctx)
	if len(in.Pending) != 1 {
		t.Fatalf("after a reset: %+v", in)
	}
	if err := be.InboxDecide(ctx, in.Pending, tui.InboxApprove); err != nil {
		t.Fatal(err)
	}
	if err := be.InboxDecide(ctx, in.Pending, "shrug"); err == nil {
		t.Error("unknown decisions are refused")
	}
	l.must(nil, "sync")
	if ps, _ := b.st.ListProjects(ctx); len(ps) != 2 {
		t.Fatalf("the approved session was sent by the CLI: %+v", ps)
	}
}

// addClaudeTurn continues an existing session with one more message from the
// user, as if the conversation went on later.
func (l *laptop) addClaudeTurn(id, cwd, text string) {
	l.t.Helper()
	l.turns++
	path := filepath.Join(l.claude, "projects", "-"+id, id+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		l.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	line := fmt.Sprintf(`{"isSidechain":false,"userType":"external","entrypoint":"cli","cwd":%q,"sessionId":"00000000-0000-4000-8000-0000000000aa","version":"2.1.260","gitBranch":"main","timestamp":"2026-01-05T09:%02d:00.000Z","type":"user","message":{"role":"user","content":%q}}`+"\n",
		cwd, 10+l.turns, text)
	if _, err := f.WriteString(line); err != nil {
		l.t.Fatal(err)
	}
}
