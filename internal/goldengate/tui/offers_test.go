package tui

import (
	"strings"
	"testing"
)

func sampleOffer() RuleOffer {
	return RuleOffer{ID: "o_1a2b3c4d", Question: "Share sessions from ~/code/app automatically?",
		Because: "You sent 6 sessions from there by hand in the last 30 days.",
		Effect: []string{`adds to project "App" the rule: approve sessions in ~/code/app`,
			"sets the project's trust level to auto, so sessions the rule matches are sent on the next sync"}}
}

func openInboxWithOffer(t *testing.T) *harness {
	t.Helper()
	be := newFake()
	be.inbox = sampleInbox()
	be.inbox.Offers = []RuleOffer{sampleOffer()}
	h := newHarnessWith(t, be, 120, 40)
	h.key("i")
	return h
}

func TestInboxShowsARuleOfferAndNeverActsWithoutAYes(t *testing.T) {
	h := openInboxWithOffer(t)
	h.contains("Offer: Share sessions from ~/code/app automatically? Press o to see what it would do.", "o offer")
	if len(h.be.offerDecs) != 0 {
		t.Fatal("an offer is only shown")
	}
	// o shows the exact rule and what else changes
	h.key("o")
	h.contains("Share sessions from ~/code/app automatically?", "You sent 6 sessions from there by hand in the last 30 days.",
		"Saying yes:", `the rule: approve sessions in ~/code/app`, "trust level to auto", "Not now", "Ask me later")
	h.golden("inbox_offer")
	// esc: ask me later. Nothing is decided and the offer stays.
	h.key("esc")
	if len(h.be.offerDecs) != 0 {
		t.Fatalf("closing decides nothing: %v", h.be.offerDecs)
	}
	h.contains("Offer: Share sessions from ~/code/app automatically?")
	// y accepts
	h.key("o", "y")
	if strings.Join(h.be.offerDecs, " ") != "accept:o_1a2b3c4d" {
		t.Fatalf("accept: %v", h.be.offerDecs)
	}
	h.contains("added rule r_0a1b2c3d; trust level is now auto")
	h.lacks("Offer:")
	h.key("o")
	h.contains("No offers.")
}

func TestNotNowDismissesTheOffer(t *testing.T) {
	h := openInboxWithOffer(t)
	h.key("o", "n")
	if strings.Join(h.be.offerDecs, " ") != "dismiss:o_1a2b3c4d" {
		t.Fatalf("not now: %v", h.be.offerDecs)
	}
	h.contains("not now: the offer will not be made again")
	h.lacks("Offer:")
}
