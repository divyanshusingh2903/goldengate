package tui

import (
	"testing"
	"time"
)

func sampleReceipts() []Receipt {
	at := time.Date(2026, 3, 2, 9, 30, 0, 0, time.Local)
	return []Receipt{
		{ID: "rc_0a1b2c3d", At: at, Items: []ReceiptItem{
			{Source: "claude-code", ID: "s-auto-1", Project: "Golden Gate", Title: "Fix OAuth redirect mismatch", Why: "rule r_1a2b3c4d: approve sessions on branch fix/*"},
			{Source: "codex", ID: "s-auto-2", Project: "Golden Gate", Title: "Add gRPC health checks", Why: "rule r_1a2b3c4d: approve sessions on branch fix/*"},
		}},
		{ID: "rc_9f8e7d6c", At: at.Add(-26 * time.Hour), Items: []ReceiptItem{
			{Source: "chatgpt", ID: "s-auto-3", Project: "Job search", Title: "Draft the cover letter", Why: "trust auto: every session of the project", Retracted: true},
		}},
	}
}

func TestReceiptsScreen(t *testing.T) {
	be := newFake()
	be.inbox = sampleInbox()
	be.receipts = sampleReceipts()
	h := newHarnessWith(t, be, 100, 30)
	h.key("i", "c")
	if h.m.scr != scrReceipts {
		t.Fatalf("c opens the receipts from the inbox: %v", h.m.scr)
	}
	h.contains("rc_0a1b2c3d", "2 sent automatically", "Fix OAuth redirect mismatch", "on the server",
		"rc_9f8e7d6c", "Draft the cover letter", "retracted",
		// the line under the title says why the selected session was sent
		"Sent because: rule r_1a2b3c4d: approve sessions on branch fix/*")
	h.checkGeometry(100, 30)
	h.golden("receipts")
	if it, ok := h.m.rc.current(); !ok || it.ID != "s-auto-1" {
		t.Fatalf("the cursor starts on a session, not a header: %+v", it)
	}

	// retract asks first, then deletes through the same retract as the inbox
	h.key("R")
	h.contains("Retract from the server", "never send it again?")
	h.key("n")
	if len(be.inboxDecs) != 0 {
		t.Fatal("retract asks first")
	}
	h.key("R", "y")
	if len(be.inboxDecs) != 1 || be.inboxDecs[0] != "retract:s-auto-1" {
		t.Fatalf("retract: %v", be.inboxDecs)
	}
	if it, _ := h.m.rc.current(); !it.Retracted {
		t.Fatalf("the receipt shows it as retracted: %+v", it)
	}
	h.key("R")
	h.contains("This session was already retracted.")

	// moving never rests on a receipt header
	for i := 0; i < 6; i++ {
		h.key("down")
		if _, ok := h.m.rc.current(); !ok {
			t.Fatalf("cursor on a header after %d downs", i+1)
		}
	}
	h.contains("Sent because: trust auto: every session of the project")
	h.key("esc")
	if h.m.scr != scrInbox {
		t.Fatalf("esc goes back to the inbox: %v", h.m.scr)
	}
}

func TestReceiptsScreenWhenNothingWasSentAutomatically(t *testing.T) {
	be := newFake()
	be.inbox = sampleInbox()
	h := newHarnessWith(t, be, 80, 24)
	h.key("i", "c")
	h.contains("No receipts", "Nothing was sent automatically", "trust <project> auto")
	h.key("R") // nothing to act on
	if len(be.inboxDecs) != 0 {
		t.Fatal("no session, no retract")
	}
}
