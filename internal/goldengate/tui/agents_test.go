package tui

import (
	"errors"
	"strings"
	"testing"
)

// openAgentsGrid goes to the Agents screen and enters the admin secret.
func openAgentsGrid(t *testing.T, w, hh int) *harness {
	t.Helper()
	h := newHarness(t, w, hh)
	h.key("5")
	if h.m.scr != scrAgents || h.m.ag.phase != agAsk {
		t.Fatalf("5 opens the Agents screen at the secret prompt: %v %v", h.m.scr, h.m.ag.phase)
	}
	h.typeText(fakeAdmin)
	if strings.Contains(h.view(), fakeAdmin) {
		t.Fatal("the admin secret is masked while typed")
	}
	h.key("enter")
	if h.m.ag.phase != agGrid {
		t.Fatalf("the right secret opens the grid: %v %q", h.m.ag.phase, h.m.ag.err)
	}
	return h
}

func TestAgentsAsksForTheAdminSecretOncePerRun(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("5")
	h.contains("Which assistant sees which project", "Admin secret", "never written anywhere")
	h.checkGeometry(100, 30)
	h.golden("agents_ask")

	// letters are text here, not commands
	h.typeText("q12")
	if h.m.scr != scrAgents {
		t.Fatal("typing the secret must not run commands")
	}
	h.key("enter")
	if h.m.ag.phase != agAsk || h.m.ag.secret != "" {
		t.Fatal("a wrong secret is not kept")
	}
	h.contains("the server refused the admin token")
	h.key("enter")
	h.contains("Type the admin secret")

	h.typeText(fakeAdmin)
	h.key("enter")
	h.contains("Every project, including future ones", "Pharmfindr", "ChatGPT")
	// leaving and coming back does not ask again
	h.key("esc", "5")
	if h.m.ag.phase != agGrid {
		t.Fatalf("the secret is remembered for the run: %v", h.m.ag.phase)
	}
	for _, s := range h.be.ag.secrets[2:] {
		if s != fakeAdmin {
			t.Errorf("every call carries the secret from memory: %q", s)
		}
	}
	// L forgets it
	h.key("L")
	if h.m.ag.secret != "" || h.m.ag.phase != agAsk {
		t.Fatal("L forgets the secret and asks again")
	}
	h.key("esc")
	if h.m.scr != scrProjects {
		t.Fatal("esc leaves the prompt")
	}
}

func TestAgentsGridSnapshots(t *testing.T) {
	for _, sz := range []struct {
		name string
		w, h int
	}{{"agents_80x24", 80, 24}, {"agents_120x40", 120, 40}} {
		h := openAgentsGrid(t, sz.w, sz.h)
		h.key("down", "right", "space") // ChatGPT gains Forge
		h.key("down", "space")          // and loses Pharmfindr
		h.contains("Default", "ChatGPT", "[x]", "[*]", "2 changes not applied yet", "[enter Review 2 changes]")
		h.checkGeometry(sz.w, sz.h)
		h.golden(sz.name)
	}
}

func TestAgentsChangesAreReviewedBeforeTheyAreApplied(t *testing.T) {
	h := openAgentsGrid(t, 120, 40)
	h.key("enter")
	h.contains("Nothing to apply")
	if h.m.modal != nil {
		t.Fatal("no review without changes")
	}

	h.key("down", "right", "space")     // + ChatGPT / Forge
	h.key("down", "space")              // - ChatGPT / Pharmfindr
	h.key("up", "up", "right", "space") // + Claude desktop: every project
	if len(h.be.ag.applied) != 0 {
		t.Fatal("toggling applies nothing")
	}
	h.contains("3 changes not applied yet")
	h.key("enter")
	if h.m.modal == nil {
		t.Fatal("enter opens the review")
	}
	h.contains("Review 3 changes", "+ ChatGPT can see Forge", "- ChatGPT can no longer see Pharmfindr", "+ Claude desktop: every project, including future ones", "Apply these changes?")
	h.golden("agents_review")
	h.key("n")
	if len(h.be.ag.applied) != 0 || len(h.m.ag.changes()) != 3 {
		t.Fatal("anything but y keeps the changes pending and applies nothing")
	}
	h.key("enter", "y")
	if len(h.be.ag.applied) != 1 || len(h.be.ag.applied[0]) != 3 {
		t.Fatalf("y applies exactly the reviewed changes: %+v", h.be.ag.applied)
	}
	got := h.be.ag.applied[0]
	if got[0] != (GrantChange{AgentID: "agt_gpt", AgentName: "ChatGPT", ProjectID: "p_forge", ProjectName: "Forge", Grant: true}) ||
		got[1].ProjectID != "p_pharm" || got[1].Grant || !got[2].All || !got[2].Grant || got[2].AgentID != "agt_claude" {
		t.Fatalf("changes: %+v", got)
	}
	h.contains("applied 3 changes")
	h.lacks("not applied yet")
	if len(h.m.ag.changes()) != 0 {
		t.Fatal("nothing is pending after applying")
	}

	// toggling a box twice is no change; u clears; leaving with changes asks
	h.key("down", "space", "space")
	if len(h.m.ag.changes()) != 0 {
		t.Fatal("toggling back cancels the change")
	}
	h.key("space", "u")
	h.contains("changes cleared; nothing was applied")
	h.key("space", "esc")
	h.contains("Leave without applying 1 change?")
	h.key("n")
	if h.m.scr != scrAgents || len(h.m.ag.changes()) != 1 {
		t.Fatal("declining stays on the grid with the change")
	}
	h.key("esc", "y")
	if h.m.scr != scrProjects || len(h.m.ag.changes()) != 0 || len(h.be.ag.applied) != 1 {
		t.Fatal("discarding leaves and applies nothing")
	}

	// a failed apply keeps the changes and says so
	h.key("5", "down", "space", "enter")
	h.be.ag.applyErr = errors.New("503")
	h.key("y")
	h.contains("could not apply every change: 503")
	if len(h.m.ag.changes()) != 1 {
		t.Fatal("a failed apply keeps what was not applied")
	}
}

func TestAgentsViewAsAgentShowsWhatItCanRead(t *testing.T) {
	h := openAgentsGrid(t, 100, 34)
	h.key("right", "v") // ChatGPT
	if h.m.ag.phase != agView {
		t.Fatal("v opens the agent's view")
	}
	h.contains("What ChatGPT can read right now", "Projects (1)", "Pharmfindr", "Summaries from the last 5 days (1)", "Work on Pharmfindr",
		"Work items from the last 5 days (1)", "pull_request example/p_pharm#7 [merged]", "[REDACTED:aws-access-key]", "sessions from another computer are not listed")
	h.lacks("Forge", "Website")
	h.checkGeometry(100, 34)
	h.golden("agents_view_as")
	h.key("esc")
	if h.m.ag.phase != agGrid {
		t.Fatal("esc returns to the grid")
	}
	// the Default agent sees every project; on a small window the view scrolls
	h = openAgentsGrid(t, 80, 24)
	h.key("v")
	h.contains("What Default can read right now", "Projects (4)")
	h.lacks("Work items from the last 5 days")
	h.key("pgdown")
	h.contains("Work items from the last 5 days (4)")
	h.checkGeometry(80, 24)
}

func TestAgentDetailsAndTokenRevoke(t *testing.T) {
	h := openAgentsGrid(t, 100, 30)
	h.key("right", "d")
	if h.m.ag.phase != agDetail {
		t.Fatal("d opens the details")
	}
	h.contains("ChatGPT", "agt_gpt", "Sees: Pharmfindr", "History: the last 30 days at most", "Last used: 26h ago", "Read tokens (2)", "chatgpt connector", "tok_aaaa1111", "(no label)", "never used", "[R Revoke]")
	h.checkGeometry(100, 30)
	h.golden("agents_detail")
	h.key("down", "R")
	h.contains("Revoke token", "loses access at once")
	h.key("n")
	if len(h.be.ag.revoked) != 0 {
		t.Fatal("revoking asks first")
	}
	h.key("R", "y")
	if len(h.be.ag.revoked) != 1 || h.be.ag.revoked[0] != "tok_bbbb2222" {
		t.Fatalf("the selected token is revoked: %v", h.be.ag.revoked)
	}
	h.contains("revoked tok_bbbb2222", "Read tokens (1)")
	h.key("esc", "right", "d")
	h.contains("Claude desktop", "OAuth applications: Claude", "Sees: Forge, Website", "Last used: never", "none: create one with")
}

func TestAgentsMouse(t *testing.T) {
	h := openAgentsGrid(t, 120, 40)
	// find Website's row and click its cell under ChatGPT
	rows := strings.Split(h.view(), "\n")
	y, x := -1, -1
	for i, r := range rows {
		if strings.Contains(r, "Website") {
			y = i
		}
		if strings.Contains(r, "ChatGPT") && x < 0 {
			x = len([]rune(r[:strings.Index(r, "ChatGPT")]))
		}
	}
	if y < 0 || x < 0 {
		t.Fatalf("grid not found:\n%s", h.view())
	}
	h.clickAt(x+1, y, false)
	ch := h.m.ag.changes()
	if len(ch) != 1 || ch[0].AgentID != "agt_gpt" || ch[0].ProjectID != "p_site" || !ch[0].Grant {
		t.Fatalf("a click toggles the cell under it: %+v", ch)
	}
	h.click("[enter Review 1 change]")
	if h.m.modal == nil {
		t.Fatal("the review button opens the review")
	}
	h.key("y")
	if len(h.be.ag.applied) != 1 {
		t.Fatal("applied from the review")
	}
	h.click("Claude desktop") // the column header selects the agent
	if h.m.ag.col != 2 {
		t.Fatalf("clicking an agent's name selects its column: %d", h.m.ag.col)
	}
	h.click("d details")
	if h.m.ag.phase != agDetail {
		t.Fatal("the hint bar is clickable")
	}
	h.key("esc")
	h.click("5 Agents")
	h.click("1 Projects")
	if h.m.scr != scrProjects {
		t.Fatal("tabs are clickable")
	}
}

func TestAgentsWithoutAServerOrAgents(t *testing.T) {
	be := newFake()
	be.server = ""
	h := newHarnessWith(t, be, 100, 30)
	h.key("5")
	h.contains("No server configured", "goldengate init")
	h.typeText("q")
	if h.m.scr != scrProjects {
		t.Fatal("without a server there is no secret prompt to type into")
	}

	be = newFake()
	be.ag.info.Agents = nil
	h = newHarnessWith(t, be, 100, 30)
	h.key("5")
	h.typeText(fakeAdmin)
	h.key("enter")
	h.contains("No agents yet", "goldengate agents add")
	h.checkGeometry(100, 30)
}

func TestManyAgentsScrollSideways(t *testing.T) {
	be := newFake()
	for i := 0; i < 8; i++ {
		be.ag.info.Agents = append(be.ag.info.Agents, AgentInfo{ID: "agt_x" + string(rune('a'+i)), Name: "Extra " + string(rune('A'+i)), Grants: map[string]bool{}})
	}
	h := newHarnessWith(t, be, 80, 24)
	h.key("5")
	h.typeText(fakeAdmin)
	h.key("enter")
	for i := 0; i < 10; i++ {
		h.key("right")
	}
	h.contains("Extra H", "11/11")
	h.checkGeometry(80, 24)
	h.key("space")
	if ch := h.m.ag.changes(); len(ch) != 1 || ch[0].AgentName != "Extra H" || !ch[0].All {
		t.Fatalf("the visible column is the one toggled: %+v", ch)
	}
}

func TestSyncReviewSaysWhatSendingApproves(t *testing.T) {
	be := newFake()
	be.approving, be.excluded = 3, 1
	h := newHarnessWith(t, be, 110, 34)
	h.key("2")
	h.contains("3 sessions were waiting for your approval: sending approves what you reviewed here (i: decide one by one).", "1 session you excluded stays on this machine.")
	h.checkGeometry(110, 34)

	be = newFake()
	be.approving = 1
	h = newHarnessWith(t, be, 110, 34)
	h.key("2")
	h.contains("1 session was waiting for your approval")
	h.lacks("you excluded")
}
