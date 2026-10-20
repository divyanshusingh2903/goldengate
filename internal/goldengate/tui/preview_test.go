package tui

import (
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

func TestPreviewScreen(t *testing.T) {
	h := newHarness(t, 100, 50)
	h.key("p")
	if h.m.scr != scrPreview {
		t.Fatal("p opens the preview")
	}
	h.contains("~/code/pharmfindr", "level summary", "Fix OAuth redirect mismatch", "Since the last sync:",
		"+ file auth/redirect_test.go", "[REDACTED:aws-access-key]", "pull_request example/pharmfindr#7 [mentioned] (text)",
		"commit pharmfindr@abc1234 [created] (tool_result)", "2 redactions", "showing the newest 3 of 47 sessions")
	h.contains("Goals: The login redirect fails on staging", "Outcomes:", "• tests passed", "Summarized by local heuristic")
	h.checkGeometry(100, 50)
	h.golden("preview_summary")
	h.key("esc")
	if h.m.scr != scrProjects {
		t.Fatal("esc returns")
	}
}

func TestPreviewAt80x24Scrolls(t *testing.T) {
	h := newHarness(t, 80, 24)
	h.key("p")
	h.checkGeometry(80, 24)
	before := h.view()
	h.key("pgdown")
	if h.view() == before {
		t.Error("the preview scrolls")
	}
	h.checkGeometry(80, 24)
}

func TestParanoidShowsEveryByte(t *testing.T) {
	h := newHarness(t, 100, 40)
	h.key("p", "P")
	h.contains("PARANOID", "\"session_id\": \"s-0001\"", "\"title\": \"Fix OAuth redirect mismatch\"")
	h.lacks("Since the last sync:") // raw payloads, not the friendly summary
	h.golden("preview_paranoid")
	h.key("P")
	h.contains("Title: Fix OAuth")
}

func TestPreviewForUnreadProjectLoads(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("down", "down", "down", "p") // vihayas has no canned preview
	h.contains("No sessions in the window for this project.")
}

func TestRedactionMarkersAreHighlightedAndCounted(t *testing.T) {
	h := newHarness(t, 100, 30)
	if got := h.m.highlight("key [REDACTED:aws-access-key] and [REDACTED:path]"); !strings.Contains(got, "[REDACTED:aws-access-key]") {
		t.Errorf("markers must survive highlighting: %q", got)
	}
	h.contains("2 secret(s) redacted")
}

func TestDiffPayload(t *testing.T) {
	prev := &model.SessionPayload{Level: "summary", Summary: &model.Summary{Title: "Old", Outcome: "tests failing", FilesTouched: []string{"a.go", "b.go"}, Commands: []string{"go test"}, TurnCount: 2},
		WorkItems: []model.WorkItem{{Kind: "pull_request", Key: "o/r#1", Status: "created"}, {Kind: "commit", Key: "r@abc", Status: "created"}}}
	cur := &model.SessionPayload{Level: "summary", Summary: &model.Summary{Title: "New", Outcome: "tests passed", FilesTouched: []string{"a.go", "c.go"}, Commands: []string{"go test", "git commit"}, TurnCount: 3},
		WorkItems: []model.WorkItem{{Kind: "pull_request", Key: "o/r#1", Status: "merged"}, {Kind: "issue", Key: "linear:ENG-5", Status: "created"}}}
	got := strings.Join(DiffPayload(prev, cur), "\n")
	for _, want := range []string{`title: "Old" -> "New"`, "+ outcome tests passed", "- outcome tests failing", "+ file c.go", "- file b.go", "+ command git commit",
		"turns: 2 -> 3", "~ pull_request o/r#1: created -> merged", "+ issue linear:ENG-5 [created]", "- commit r@abc"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff is missing %q:\n%s", want, got)
		}
	}
	if d := DiffPayload(cur, cur); len(d) != 0 {
		t.Errorf("identical payloads have no diff: %v", d)
	}
	if DiffPayload(nil, cur) != nil || DiffPayload(prev, nil) != nil {
		t.Error("no previous payload, no diff")
	}
}
