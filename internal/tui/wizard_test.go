package tui

import (
	"strings"
	"testing"
)

func firstRun() *fakeBackend {
	be := newFake()
	be.firstRun = true
	be.server = ""
	for i := range be.rows { // nothing is shared on a first run
		be.rows[i].State = NotShared
		be.rows[i].Pending = 0
	}
	return be
}

func TestWizardStartsOnFirstRunOnly(t *testing.T) {
	h := newHarnessWith(t, firstRun(), 100, 30)
	if h.m.scr != scrWizard {
		t.Fatal("a first run opens the wizard")
	}
	h.contains("Welcome to Golden Gate", "Nothing is shared until you say so", "Found on this computer", "● Claude Code", "142 session(s)", "○ OpenCode", "not found", "Setup 1/6")
	h.checkGeometry(100, 30)
	h.golden("wizard_welcome")

	h2 := newHarness(t, 100, 30) // not a first run
	if h2.m.scr != scrProjects {
		t.Fatal("no wizard on later runs")
	}
}

func TestWizardCanBeSkipped(t *testing.T) {
	h := newHarnessWith(t, firstRun(), 100, 30)
	h.key("esc")
	if h.m.scr != scrProjects {
		t.Fatal("esc skips setup")
	}
	if h.m.wz.step != wizWelcome {
		t.Error("skipping must not run any step")
	}
	if len(h.be.saved) != 1 || h.be.saved[0].Level != "summary" {
		t.Errorf("skipping writes the default config once, so setup is not offered at every start: %+v", h.be.saved)
	}
}

func TestWizardConnectsWithAdminTokenAndForgetsIt(t *testing.T) {
	be := firstRun()
	h := newHarnessWith(t, be, 100, 30)
	h.key("enter") // welcome -> server
	h.contains("Where do the summaries go?", "(•) Connect with the admin token", "Admin token")
	h.golden("wizard_server")
	h.key("tab", "tab") // cycle the mode all the way around
	if h.m.wz.mode != 2 {
		t.Fatalf("tab cycles modes: %d", h.m.wz.mode)
	}
	h.key("tab")
	h.typeText("https://bridge.example.com")
	h.key("down")
	h.typeText("S3CR3T-ADMIN-TOKEN")
	h.lacks("S3CR3T-ADMIN-TOKEN") // typed input is masked
	h.key("enter")
	if len(be.inits) != 1 || be.inits[0].ServerURL != "https://bridge.example.com" || be.inits[0].AdminToken != "S3CR3T-ADMIN-TOKEN" {
		t.Fatalf("init params: %+v", be.inits)
	}
	if h.m.wz.admin.Value() != "" {
		t.Fatal("the admin token must be wiped from the UI after use")
	}
	h.contains("Connected.", "Ingest token stored in the keyring", "The admin token was not saved", "ggb_read_EXAMPLE", "shown only now", "https://bridge.example.com/mcp", "OAuth is enabled")
	h.golden("wizard_connected")
	if strings.Contains(h.view(), "S3CR3T") {
		t.Fatal("secret leaked into the view")
	}
	h.key("enter") // -> level
	h.contains("What should be sent for each session?", "(•) summary", "( ) titles-only")
	h.golden("wizard_level")
}

func TestWizardPairingCode(t *testing.T) {
	be := firstRun()
	h := newHarnessWith(t, be, 100, 30)
	h.key("enter", "tab") // pairing mode
	h.contains("Pairing code", "goldengatebridge pair")
	h.typeText("https://bridge.example.com")
	h.key("down")
	h.typeText("ABCD-EFGH-JKLM")
	h.key("enter")
	if len(be.inits) != 1 || be.inits[0].PairCode != "ABCD-EFGH-JKLM" || be.inits[0].AdminToken != "" {
		t.Fatalf("%+v", be.inits)
	}
	h.contains("Connected.")
	h.lacks("Read token for your assistant") // pairing yields an ingest token only
}

func TestWizardValidationAndErrors(t *testing.T) {
	be := firstRun()
	be.initErr = fmtErr("pairing failed: invalid, expired or already used pairing code")
	h := newHarnessWith(t, be, 100, 30)
	h.key("enter", "enter")
	h.contains("enter the server URL")
	h.typeText("https://bridge.example.com")
	h.key("enter")
	h.contains("enter the admin token")
	h.key("down")
	h.typeText("tok")
	h.key("enter")
	h.contains("pairing failed")
	if h.m.wz.done {
		t.Fatal("a failed connection must not advance")
	}
	if h.m.wz.admin.Value() != "" {
		t.Fatal("even a failed attempt wipes the admin token")
	}
}

func TestWizardStayLocalSkipsTheServer(t *testing.T) {
	be := firstRun()
	h := newHarnessWith(t, be, 100, 30)
	h.key("enter", "tab", "tab") // mode 2
	h.contains("Stay local for now", "connect later from the Server screen")
	h.key("enter")
	if len(be.inits) != 0 {
		t.Fatal("staying local must not contact any server")
	}
	if h.m.wz.step != wizLevel {
		t.Fatalf("goes on to the level: %v", h.m.wz.step)
	}
}

func TestWizardFullFlow(t *testing.T) {
	be := firstRun()
	h := newHarnessWith(t, be, 100, 30)
	h.key("enter", "tab", "tab", "enter") // local only -> level
	h.key("right", "enter")               // titles-only
	if len(be.saved) == 0 || be.saved[len(be.saved)-1].Level != "titles-only" {
		t.Fatalf("level saved: %+v", be.saved)
	}
	if h.m.wz.step != wizProjects {
		t.Fatalf("step: %v", h.m.wz.step)
	}
	h.contains("Setup 4/6", "Choose what to share", "tab next step")
	h.checkGeometry(100, 30)
	h.golden("wizard_projects")
	h.key("space") // share the first project from within the wizard
	if len(be.actions) != 1 || be.actions[0].Kind != ActAllow {
		t.Fatalf("%+v", be.actions)
	}
	h.key("tab")
	if h.m.wz.step != wizReview {
		t.Fatalf("tab advances: %v", h.m.wz.step)
	}
	h.contains("Dry run: this is exactly what the first sync would send", "7 session(s) in 3 project(s)", "2 redaction(s) applied")
	h.golden("wizard_review")
	h.key("enter") // -> sync step
	if h.m.wz.step != wizSync {
		t.Fatalf("step: %v", h.m.wz.step)
	}
	h.key("enter") // run (a dry run: no server)
	if h.m.sy.phase != syncDone || !be.syncs[0] {
		t.Fatalf("no server, so the first sync is a dry run: %v %v", h.m.sy.phase, be.syncs)
	}
	h.key("enter") // finish
	if h.m.scr != scrProjects {
		t.Fatal("the wizard ends on the project picker")
	}
}

func TestWizardBackFromProjects(t *testing.T) {
	be := firstRun()
	h := newHarnessWith(t, be, 100, 30)
	h.key("enter", "tab", "tab", "enter", "enter") // to projects
	h.key("shift+tab")
	if h.m.wz.step != wizLevel {
		t.Fatalf("shift+tab goes back: %v", h.m.wz.step)
	}
}

func TestWizardAt80x24(t *testing.T) {
	h := newHarnessWith(t, firstRun(), 80, 24)
	for _, k := range []string{"enter", "tab", "tab", "enter", "enter"} {
		h.key(k)
		h.checkGeometry(80, 24)
	}
}

func TestServerScreenCanLaunchTheWizard(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("3", "i")
	if h.m.scr != scrWizard {
		t.Fatal("i on the server screen opens setup")
	}
}
