package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/store"
)

// retitle returns an editor that replaces the title line.
func retitle(title string) func(string) string {
	return func(form string) string {
		var out []string
		for _, l := range strings.Split(form, "\n") {
			if strings.HasPrefix(l, "title:") {
				l = "title: " + title
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n")
	}
}

func TestEditedSummariesStayEdited(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	fake := &fakeChatServer{}
	ts := fake.start(t)
	ctx := context.Background()
	key := map[string]string{"GOLDENGATE_SUMMARIZER_KEY": "sk-local"}
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")

	// closing the editor without changes keeps the generated summary
	if out := l.must(nil, "sessions", "edit", demoSession); !strings.Contains(out, "nothing changed") {
		t.Fatalf("no change: %s", out)
	}
	if len(l.forms) != 1 || !strings.Contains(l.forms[0], "title: please ship the widget") || !strings.Contains(l.forms[0], "[work items]") || !strings.Contains(l.forms[0], "# Edit this session's summary") {
		t.Fatalf("the form shows the generated summary:\n%v", l.forms)
	}
	// a form that cannot be read back is refused and nothing is stored
	l.edit = func(string) string { return "garbage without a title\n" }
	if err := l.run(nil, "sessions", "edit", demoSession); err == nil || !strings.Contains(err.Error(), "not saved") {
		t.Fatalf("an unreadable form: %v", err)
	}

	// edit: a new title with a planted secret, a struck outcome, an unlinked work item
	planted := "AKIA" + "IOSFODNN7EXAMPLE"
	l.edit = func(form string) string {
		var out []string
		work := false
		for _, line := range strings.Split(retitle("Shipped the widget, key "+planted)(form), "\n") {
			if line == "[work items]" {
				work = true
			}
			if line == "- PR opened" || (work && strings.HasPrefix(line, "- pull_request ")) {
				continue
			}
			out = append(out, line)
		}
		return strings.Join(out, "\n")
	}
	if out := l.must(nil, "sessions", "edit", demoSession); !strings.Contains(out, "saved your version") {
		t.Fatalf("edit: %s", out)
	}
	if out := l.must(nil, "sessions", "list"); !strings.Contains(out, "edited") {
		t.Fatalf("the list marks the edited session: %s", out)
	}
	// the preview is the edited payload, exactly, and redaction ran on the user's text
	dry := l.must(nil, "sync", "--dry-run", "--approve")
	if !strings.Contains(dry, "Shipped the widget, key [REDACTED:aws-access-key]") || strings.Contains(dry, planted) || !strings.Contains(dry, `"edited": true`) ||
		strings.Contains(dry, "PR opened") || strings.Contains(dry, `"kind": "pull_request"`) {
		t.Fatalf("the dry run shows the edited, redacted payload:\n%s", dry)
	}
	l.must(nil, "sync", "--approve")
	held := everythingTheServerHolds(t, b)
	if !strings.Contains(held, "Shipped the widget, key [REDACTED:aws-access-key]") || strings.Contains(held, planted) || strings.Contains(held, "please ship the widget\",\"goals") {
		t.Fatalf("the server holds the user's version:\n%s", held)
	}
	if items, _ := b.st.WorkItems(ctx, store.WorkItemQuery{Kind: "pull_request"}); len(items) != 0 {
		t.Fatalf("the unlinked work item was not sent: %+v", items)
	}

	// it survives a re-sync and a re-summarize with a model, which is never asked about it
	sent := countAction(t, b, "ingest")
	l.must(nil, "sync")
	l.must(nil, "summarizer", "use", "custom", "--model", "tiny", "--base-url", ts.URL+"/v1")
	fake.bodies = nil
	l.must(key, "sync", "--resummarize", "--approve")
	if got := countAction(t, b, "ingest"); got != sent || len(fake.bodies) != 0 {
		t.Fatalf("an edited summary is not re-summarized or re-sent: %d -> %d ingests, %d model calls", sent, got, len(fake.bodies))
	}
	if strings.Contains(l.errOut.String(), "still have summaries not written by") {
		t.Errorf("an edited session is not offered for re-summarizing:\n%s", l.errOut)
	}
	l.must(nil, "summarizer", "use", "heuristic")

	// editing again starts from the user's version; a second edit of sent content is re-sent by a plain sync
	l.edit = retitle("Second version")
	l.must(nil, "sessions", "edit", demoSession)
	if f := l.forms[len(l.forms)-1]; !strings.Contains(f, "title: Shipped the widget, key [REDACTED:aws-access-key]") || !strings.Contains(f, "This is your edited version") {
		t.Fatalf("the form starts from the edited version:\n%s", f)
	}
	l.must(nil, "sync")
	if held := everythingTheServerHolds(t, b); !strings.Contains(held, "Second version") {
		t.Fatal("an edit of a session that is on the server is sent by the next sync")
	}

	// the session continues: the edit is neither applied silently nor overwritten
	sent = countAction(t, b, "ingest")
	l.addClaudeTurn(demoSession, "/home/user/projects/demo", "and one more request")
	for _, args := range [][]string{{"sync"}, {"sync", "--approve"}} {
		l.must(nil, args...)
	}
	l.must(nil, "projects", "auto", "demo", "on")
	l.must(nil, "sync")
	if got := countAction(t, b, "ingest"); got != sent {
		t.Fatalf("a session whose edit needs review is held, even with --approve or auto-approve: %d -> %d", sent, got)
	}
	if held := everythingTheServerHolds(t, b); !strings.Contains(held, "Second version") {
		t.Fatal("the server keeps the user's last version meanwhile")
	}
	if out := l.must(nil, "sessions", "list"); !strings.Contains(out, "[edit needs review]") {
		t.Fatalf("the list says so: %s", out)
	}
	if dry := l.must(nil, "sync", "--dry-run", "--approve"); strings.Contains(dry, demoSession) {
		t.Fatal("a held session is in no dry run")
	}
	// opening the editor shows the new generated summary and the earlier version side by side
	l.edit = nil
	l.must(nil, "sessions", "edit", demoSession)
	f := l.forms[len(l.forms)-1]
	if !strings.Contains(f, "NEW generated summary") || !strings.Contains(f, "#   yours  title: Second version") || !strings.Contains(f, "#   new    title: please ship the widget") || !strings.Contains(f, "\ntitle: please ship the widget") {
		t.Fatalf("the conflict form:\n%s", f)
	}
	// closing it unchanged accepts the new generated summary and removes the edit
	if out := l.out.String(); !strings.Contains(out, "your edit was removed") {
		t.Fatalf("accepting the new summary: %s", out)
	}
	l.must(nil, "sync")
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "Second version") || !strings.Contains(held, `"title":"please ship the widget"`) {
		t.Fatalf("the generated summary is sent once the user accepted it:\n%s", held)
	}

	// --keep carries an edit over to changed content; --discard drops one
	l.edit = retitle("Kept across a change")
	l.must(nil, "sessions", "edit", demoSession)
	l.must(nil, "sync")
	l.addClaudeTurn(demoSession, "/home/user/projects/demo", "yet another request")
	if err := l.run(nil, "sessions", "edit", demoSession, "--keep", "--discard"); err == nil {
		t.Error("--keep and --discard together")
	}
	if out := l.must(nil, "sessions", "edit", demoSession, "--keep"); !strings.Contains(out, "kept your version") {
		t.Fatalf("keep: %s", out)
	}
	l.must(nil, "sync")
	if held := everythingTheServerHolds(t, b); !strings.Contains(held, "Kept across a change") {
		t.Fatal("the kept edit is sent with the changed session")
	}
	if out := l.must(nil, "sessions", "edit", demoSession, "--discard"); !strings.Contains(out, "your edit was removed") {
		t.Fatalf("discard: %s", out)
	}
	l.must(nil, "sync")
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "Kept across a change") {
		t.Fatal("after --discard the generated summary replaces the edit")
	}
	if err := l.run(nil, "sessions", "edit", demoSession, "--discard"); err == nil {
		t.Error("nothing left to discard")
	}
	if err := l.run(nil, "sessions", "edit", "no-such-session"); err == nil {
		t.Error("unknown session")
	}
	// the form file does not stay behind
	if left, _ := filepath.Glob(filepath.Join(l.root, "state", "goldengate", "edit", "*")); len(left) != 0 {
		t.Errorf("edit forms left on disk: %v", left)
	}
}

// The same editing through the backend the TUI inbox uses.
func TestEditThroughTheBackend(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	ctx := context.Background()
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	be := backend(l)
	in, _ := be.Inbox(ctx)
	item := in.Pending[0]

	path, err := be.EditBegin(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	form, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(form), "title: please ship the widget") {
		t.Fatalf("the form file: %v\n%s", err, form)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("the form is private: %v", fi.Mode().Perm())
	}
	if _, err := be.EditFinish(ctx, item, "/etc/passwd"); err == nil {
		t.Fatal("only this session's own form file is read back")
	}
	path, _ = be.EditBegin(ctx, item)
	if err := os.WriteFile(path, []byte(retitle("Edited in the TUI")(string(form))), 0o600); err != nil {
		t.Fatal(err)
	}
	msg, err := be.EditFinish(ctx, item, path)
	if err != nil || !strings.Contains(msg, "saved your version") {
		t.Fatalf("finish: %q %v", msg, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the form is deleted afterwards")
	}
	in, _ = be.Inbox(ctx)
	if len(in.Pending) != 1 || !in.Pending[0].Edited || in.Pending[0].Title != "Edited in the TUI" || in.Pending[0].State != "pending" {
		t.Fatalf("still waiting for approval, now edited: %+v", in.Pending)
	}
	// the preview shows exactly the edited payload
	pv, err := be.Preview(ctx, "/home/user/projects/demo", 5)
	if err != nil || len(pv.Items) == 0 || pv.Items[0].Payload.Summary.Title != "Edited in the TUI" || !pv.Items[0].Payload.Summary.Edited {
		t.Fatalf("preview: %+v %v", pv, err)
	}
	// review and send: the edited version reaches the server
	if err := be.Sync(ctx, false, tui.SyncOpts{}, func(tui.SyncEvent) {}); err != nil {
		t.Fatal(err)
	}
	if held := everythingTheServerHolds(t, b); !strings.Contains(held, "Edited in the TUI") {
		t.Fatal("the edited summary was sent")
	}
	// the session continues: held even from the TUI's review, until the user decides
	l.addClaudeTurn(demoSession, "/home/user/projects/demo", "more work")
	plan, err := be.Plan(ctx, tui.SyncOpts{})
	if err != nil || plan.Sessions != 0 {
		t.Fatalf("a session whose edit needs review is in no plan: %+v %v", plan, err)
	}
	in, _ = be.Inbox(ctx)
	if len(in.Pending) != 1 || in.Pending[0].State != tui.InboxStateConflict {
		t.Fatalf("it is listed as needing review: %+v", in)
	}
	if err := be.InboxDecide(ctx, in.Pending, tui.InboxKeepEdit); err != nil {
		t.Fatal(err)
	}
	if plan, _ := be.Plan(ctx, tui.SyncOpts{}); plan.Sessions != 1 {
		t.Fatalf("after keeping the edit it can be sent: %+v", plan)
	}
	if err := be.InboxDecide(ctx, in.Pending, tui.InboxDropEdit); err != nil {
		t.Fatal(err)
	}
	if in, _ = be.Inbox(ctx); len(in.Pending) != 1 || in.Pending[0].Edited {
		t.Fatalf("after dropping the edit: %+v", in.Pending)
	}
}
