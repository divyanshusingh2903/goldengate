package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func openInboxScreen(t *testing.T, w, hh int) *harness {
	t.Helper()
	be := newFake()
	be.inbox = sampleInbox()
	h := newHarnessWith(t, be, w, hh)
	h.key("i")
	if h.m.scr != scrInbox {
		t.Fatalf("i opens the inbox from the projects screen: %v", h.m.scr)
	}
	return h
}

func TestInboxScreenSnapshots(t *testing.T) {
	for _, sz := range []struct {
		name string
		w, h int
	}{{"inbox_80x24", 80, 24}, {"inbox_120x40", 120, 40}} {
		h := openInboxScreen(t, sz.w, sz.h)
		h.contains("Waiting for your approval (2)", "Fix OAuth redirect", "Excluded: kept on this machine (1)")
		h.checkGeometry(sz.w, sz.h)
		h.golden(sz.name)
	}
	h := openInboxScreen(t, 120, 40)
	h.contains("Sent, or queued for the next send (2)", "Retracted: deleted from the server (1)", "changed since sent", "[REDACTED:aws-access-key]",
		"Approving records your decision; it goes out the next time you send.")
}

func TestInboxDecisionsOneSessionAtATime(t *testing.T) {
	h := openInboxScreen(t, 120, 40)
	if it, _ := h.m.ib.current(); it.ID != "s-pend-1" {
		t.Fatalf("the cursor starts on the first waiting session, not a header: %+v", it)
	}
	h.key("a")
	h.contains("approved \"Fix OAuth redirect mismatch\": it goes out the next time you send", "Waiting for your approval (1)")
	h.key("x") // now on the changed one
	h.contains("excluded \"Add gRPC health checks", "Excluded: kept on this machine (2)")
	h.lacks("Waiting for your approval")
	if got := strings.Join(h.be.inboxDecs, " "); got != "approve:s-pend-1 exclude:s-pend-2" {
		t.Fatalf("decisions: %s", got)
	}
	// the cursor never rests on a header while moving through every row
	for i := 0; i < 12; i++ {
		h.key("down")
		if _, ok := h.m.ib.current(); !ok {
			t.Fatalf("cursor on a header after %d downs", i+1)
		}
	}
	for i := 0; i < 12; i++ {
		h.key("up")
		if _, ok := h.m.ib.current(); !ok {
			t.Fatalf("cursor on a header after %d ups", i+1)
		}
	}

	// undo an exclude; approving something already sent or excluding something excluded says so
	it, _ := h.m.ib.current()
	if it.State != "excluded" {
		t.Fatalf("back at the top item: %+v", it)
	}
	h.key("x")
	h.contains("already kept on this machine")
	h.key("u")
	h.contains("decision removed", "Waiting for your approval (1)")
	if h.be.inboxDecs[len(h.be.inboxDecs)-1] != "reset:"+it.ID {
		t.Fatalf("reset: %v", h.be.inboxDecs)
	}
}

func TestInboxRetractAsksFirstAndOnlyForSentSessions(t *testing.T) {
	h := openInboxScreen(t, 120, 40)
	h.key("R")
	h.contains("Only a session that was sent can be retracted")
	if h.m.modal != nil {
		t.Fatal("a waiting session cannot be retracted")
	}
	for {
		it, _ := h.m.ib.current()
		if it.ID == "s-sent-1" {
			break
		}
		h.key("down")
	}
	h.key("a")
	h.contains("Nothing to approve: this session is already sent.")
	h.key("u")
	h.contains("Nothing to undo for this session.")
	h.key("R")
	h.contains("Retract from the server", "never send it again?", "Assistants lose it at once")
	h.key("n")
	if len(h.be.inboxDecs) != 0 {
		t.Fatal("retract asks first")
	}
	h.key("R", "y")
	if len(h.be.inboxDecs) != 1 || h.be.inboxDecs[0] != "retract:s-sent-1" {
		t.Fatalf("retract: %v", h.be.inboxDecs)
	}
	h.contains("retracted \"Refactor token store\": deleted from the server", "Retracted: deleted from the server (2)")

	// errors are shown, not swallowed
	h.be.inboxErr = errors.New("server said no")
	h.key("x")
	h.contains("server said no")
}

func TestInboxOpensFromSyncAndMouseWorks(t *testing.T) {
	be := newFake()
	be.inbox = sampleInbox()
	h := newHarnessWith(t, be, 120, 40)
	h.key("2", "i")
	if h.m.scr != scrInbox {
		t.Fatal("i opens the inbox from the sync review")
	}
	rows := strings.Split(h.view(), "\n")
	for y, r := range rows {
		if strings.Contains(r, "Salary spreadsheet cleanup") {
			h.clickAt(10, y, false)
		}
	}
	if it, _ := h.m.ib.current(); it.ID != "s-excl-1" {
		t.Fatalf("a click selects the row: %+v", it)
	}
	h.click("u undo")
	if len(h.be.inboxDecs) != 1 || h.be.inboxDecs[0] != "reset:s-excl-1" {
		t.Fatalf("the hint bar is clickable: %v", h.be.inboxDecs)
	}
	h.key("esc")
	if h.m.scr != scrProjects {
		t.Fatal("esc leaves")
	}
	// typing i into the search box is text, not a command
	h.key("/")
	h.typeText("i")
	if h.m.scr != scrProjects {
		t.Fatal("i in the search box must not open the inbox")
	}
}

func TestInboxEmptyAndFailed(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("i")
	h.contains("No sessions yet", "wait here for your approval")
	h.checkGeometry(100, 30)
}

func TestInboxEditsASummaryInTheUsersEditor(t *testing.T) {
	h := openInboxScreen(t, 120, 40)
	var opened []string
	h.m.opts.EditFile = func(path string, done func(error) tea.Msg) tea.Cmd {
		opened = append(opened, path)
		return func() tea.Msg { return done(nil) }
	}
	h.key("e")
	if len(opened) != 1 || opened[0] != "/fake/edit/s-pend-1.txt" || len(h.be.editBegun) != 1 {
		t.Fatalf("e writes the form and opens the editor on it: %v %v", opened, h.be.editBegun)
	}
	if len(h.be.editFinished) != 1 || h.be.editFinished[0] != "s-pend-1@/fake/edit/s-pend-1.txt" {
		t.Fatalf("closing the editor reads the form back: %v", h.be.editFinished)
	}
	h.contains("saved your version; it is what will be sent for this session", "My edited title", "pending, edited")

	// an editor that fails saves nothing, and the form is still cleaned up
	h.m.opts.EditFile = func(_ string, done func(error) tea.Msg) tea.Cmd {
		return func() tea.Msg { return done(errors.New("exit status 1")) }
	}
	h.key("e")
	h.contains("the editor did not finish cleanly: exit status 1")
	if last := h.be.editFinished[len(h.be.editFinished)-1]; last != "s-pend-1@" {
		t.Fatalf("a failed editor only cleans up: %v", h.be.editFinished)
	}
	// D removes the edit; on a session without one it says so
	h.key("D")
	h.contains("removed your edit of \"My edited title\"")
	h.key("D")
	h.contains("This session's summary is not edited.")
	h.key("K")
	h.contains("this one has no such edit")
	// an excluded session is not edited
	for {
		it, _ := h.m.ib.current()
		if it.State == "excluded" {
			break
		}
		h.key("down")
	}
	n := len(h.be.editBegun)
	h.key("e")
	h.contains("press u first")
	if len(h.be.editBegun) != n {
		t.Fatal("no form for an excluded session")
	}
}

func TestInboxHoldsASessionWhoseEditNeedsReview(t *testing.T) {
	be := newFake()
	be.inbox = Inbox{Pending: []InboxItem{{Source: "claude-code", ID: "s-conf", Project: "Pharmfindr", Title: "New generated title", Updated: ago(1 * time.Hour),
		State: InboxStateConflict, Edited: true, Changed: true, Hash: "h9"}}}
	h := newHarnessWith(t, be, 120, 30)
	h.key("i")
	h.contains("edit needs review", "This session changed after you edited its summary. e: edit again · K: keep your version · D: use the new one")
	h.checkGeometry(120, 30)
	h.golden("inbox_conflict")
	h.key("a") // approving does not release a held session (#119)
	if len(be.inboxDecs) != 0 {
		t.Fatalf("a must not record an approval for a held session: %v", be.inboxDecs)
	}
	h.contains("Held: this session changed after you edited its summary")
	h.key("K")
	if len(be.inboxDecs) != 1 || be.inboxDecs[0] != "keep-edit:s-conf" {
		t.Fatalf("K keeps the edit: %v", be.inboxDecs)
	}
	h.contains("kept your version of \"New generated title\"")
}
