package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
)

// --- sync ---

func TestSyncDryRunFlow(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("s")
	if h.m.scr != scrSync || h.m.sy.phase != syncReady {
		t.Fatalf("s opens the sync screen with a plan: scr=%v phase=%v", h.m.scr, h.m.sy.phase)
	}
	h.contains("7 session(s) in 3 project(s) would be sent", "120 session(s) not selected", "2 redaction(s) applied", "dry run")
	h.checkGeometry(100, 30)
	h.golden("sync_plan")
	h.key("enter")
	if h.m.sy.phase != syncDone {
		t.Fatalf("the fake finishes immediately: %v", h.m.sy.phase)
	}
	if len(h.be.syncs) != 1 || !h.be.syncs[0] {
		t.Fatalf("dry run by default: %v", h.be.syncs)
	}
	h.contains("7/7 sessions", "[ok] ~/code/pharmfindr", "done", "dry run finished: 7 session(s)")
	h.golden("sync_done")
}

func TestRealSyncNeedsExplicitSwitch(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("s", "D")
	h.contains("REAL SYNC")
	h.key("enter")
	if len(h.be.syncs) != 1 || h.be.syncs[0] {
		t.Fatalf("a real sync only after D: %v", h.be.syncs)
	}
	h.contains("synced: 7 session(s)")
}

func TestSyncWithoutServerStaysDry(t *testing.T) {
	be := newFake()
	be.server = ""
	h := newHarnessWith(t, be, 100, 30)
	h.key("s")
	h.contains("No server configured: only a dry run is available.")
	h.key("D")
	if !h.m.sy.dry {
		t.Fatal("without a server D must not switch to a real sync")
	}
	h.contains("no server is configured")
}

func TestSyncFailureAndRetry(t *testing.T) {
	be := newFake()
	be.syncFail["~/code/forge"] = "server returned 503: try later"
	h := newHarnessWith(t, be, 100, 30)
	h.key("s", "D", "enter")
	h.contains("[!!] ~/code/forge", "503", "1 project(s) failed", "6/7 sessions")
	h.golden("sync_failed")
	delete(be.syncFail, "~/code/forge")
	h.key("r") // replan, then run again
	if h.m.sy.phase != syncReady {
		t.Fatalf("r replans: %v", h.m.sy.phase)
	}
	h.key("enter")
	h.contains("7/7 sessions", "done")
}

func TestQuitWhileSyncRunningAsksFirst(t *testing.T) {
	be := newFake()
	be.syncBlock = make(chan struct{})
	h := newHarnessWith(t, be, 100, 30)
	h.key("s")
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // start, but do not drain (it blocks on purpose)
	_ = cmd
	time.Sleep(30 * time.Millisecond)
	if h.m.sy.phase != syncRunning {
		t.Fatalf("running: %v", h.m.sy.phase)
	}
	_, quit := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit != nil || h.m.modal == nil {
		t.Fatal("ctrl+c during a sync asks before quitting")
	}
	h.contains("A sync is in progress.")
	_, none := h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if none != nil || h.m.modal != nil {
		t.Fatal("n keeps running")
	}
	h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	_, quit = h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if quit == nil {
		t.Fatal("y quits")
	}
	if msg := quit(); msg != tea.Quit() {
		t.Errorf("expected tea.Quit, got %T", msg)
	}
	close(be.syncBlock)
}

func TestEscDoesNotAbandonARunningSync(t *testing.T) {
	be := newFake()
	be.syncBlock = make(chan struct{})
	h := newHarnessWith(t, be, 100, 30)
	h.key("s")
	h.m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	time.Sleep(30 * time.Millisecond)
	h.m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.scr != scrSync {
		t.Fatal("esc must not leave a sync half way")
	}
	close(be.syncBlock)
}

func TestSyncCancel(t *testing.T) {
	be := newFake()
	be.syncBlock = make(chan struct{})
	h := newHarnessWith(t, be, 100, 30)
	h.key("s")
	h.m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	time.Sleep(30 * time.Millisecond)
	_, _ = h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	time.Sleep(50 * time.Millisecond)
	if h.m.sy.cancel == nil && h.m.sy.phase == syncRunning {
		t.Fatal("cancel not wired")
	}
	// drain what the cancelled sync left on the channel
	for i := 0; i < 20 && h.m.sy.phase == syncRunning; i++ {
		select {
		case msg, ok := <-h.m.sy.ch:
			if !ok {
				i = 20
				break
			}
			h.m.Update(msg)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if h.m.sy.phase != syncDone || h.m.sy.err == "" {
		t.Errorf("a cancelled sync ends with an error shown: phase=%v err=%q", h.m.sy.phase, h.m.sy.err)
	}
}

func TestBar(t *testing.T) {
	for _, c := range []struct {
		done, total, w int
		want           string
	}{{0, 10, 12, "[----------]"}, {5, 10, 12, "[#####-----]"}, {10, 10, 12, "[##########]"}, {3, 0, 12, "[----------]"}, {99, 10, 12, "[##########]"}} {
		if got := bar(c.done, c.total, c.w); got != c.want {
			t.Errorf("bar(%d,%d,%d)=%q want %q", c.done, c.total, c.w, got, c.want)
		}
	}
}

// --- server ---

func TestServerScreen(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("3")
	h.contains("https://bridge.example.com", "Server ● reachable", "Ingest token ● accepted (keyring)", "2 project(s)", "~/code/pharmfindr", "44 sessions")
	h.checkGeometry(100, 30)
	h.golden("server")
}

func TestServerDeleteNeedsConfirmation(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("3", "D")
	h.contains("Delete ~/code/pharmfindr and all its sessions")
	h.key("n")
	if len(h.be.srvDels) != 0 {
		t.Fatal("n must not delete")
	}
	h.key("D", "y")
	if len(h.be.srvDels) != 1 || h.be.srvDels[0] != "~/code/pharmfindr" {
		t.Fatalf("deleted: %v", h.be.srvDels)
	}
	h.lacks("44 sessions")
	h.contains("deleted ~/code/pharmfindr from the server")
}

func TestServerScreenWithoutServerAndUnreachable(t *testing.T) {
	be := newFake()
	be.server = ""
	h := newHarnessWith(t, be, 100, 30)
	h.contains("server: ○ not configured")
	h.key("3")
	h.contains("No server configured", "goldengate init --server")

	be2 := newFake()
	be2.srvInfo = ServerInfo{URL: be2.server, Configured: true, Healthy: false, Err: "dial tcp: connection refused"}
	h2 := newHarnessWith(t, be2, 100, 30)
	h2.contains("server: ✗ unreachable")
	h2.key("3")
	h2.contains("✗ unreachable", "connection refused")
}

// --- settings ---

func TestSettingsScreen(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4")
	h.contains("Share level", "summary", "Look back", "90d", "Source Claude Code", "on", "Source claude.ai", "on (not found)", "Verify commits with local git", "not available yet")
	h.checkGeometry(100, 30)
	h.golden("settings")
}

func TestSettingsChangesAreSavedImmediately(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4", "space") // level
	if len(h.be.saved) != 1 || h.be.saved[0].Level != "titles-only" {
		t.Fatalf("level: %+v", h.be.saved)
	}
	h.contains("titles-only")
	h.key("down", "enter", "ctrl+a") // edit "since" ... then replace the text
	h.m.st.input.SetValue("30d")
	h.key("enter")
	if got := h.be.saved[len(h.be.saved)-1].Since; got != "30d" {
		t.Fatalf("since: %q", got)
	}
	h.key("down", "space") // first source off
	if s := h.be.saved[len(h.be.saved)-1].Sources[0]; s.Enabled {
		t.Fatalf("source toggled: %+v", s)
	}
	h.contains("Source Claude Code", "off")
}

func TestSettingsPatternsAddAndRemove(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4")
	// jump to "+ add a redaction pattern"
	for i, r := range h.m.settingRows() {
		if r.kind == setAddPattern {
			h.m.st.cursor = i
		}
	}
	h.key("enter")
	h.typeText(`ACME-\d{4}`)
	h.key("enter")
	if len(h.be.patterns) != 1 || h.be.patterns[0] != `ACME-\d{4}` {
		t.Fatalf("pattern added: %v", h.be.patterns)
	}
	h.contains(`ACME-\d{4}`)
	for i, r := range h.m.settingRows() {
		if r.kind == setPattern {
			h.m.st.cursor = i
		}
	}
	h.key("x")
	if len(h.be.patterns) != 0 {
		t.Fatalf("pattern removed: %v", h.be.patterns)
	}
}

func TestSettingsErrorsAreShownNotSwallowed(t *testing.T) {
	be := newFake()
	be.saveErr = fmtErr(`config: invalid since window "soon"`)
	h := newHarnessWith(t, be, 100, 30)
	h.key("4", "space")
	h.contains(`invalid since window "soon"`)
	h.contains("summary") // the shown value reverts to what is really saved
}

func TestSettingsEditEscapeCancels(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4", "down", "enter")
	h.typeText("zzz")
	h.key("esc")
	if len(h.be.saved) != 0 || h.m.st.editing {
		t.Fatal("esc cancels an edit without saving")
	}
	if h.m.scr != scrSettings {
		t.Fatal("esc while editing stays on the screen")
	}
}

// --- help, modal, navigation ---

func TestHelpOverlay(t *testing.T) {
	h := newHarness(t, 100, 40)
	h.key("?")
	h.contains("Keys", "fuzzy filter", "paranoid", "[x] shared")
	h.golden("help")
	h.key("x") // any key closes it and is NOT executed
	if h.m.help || len(h.be.actions) != 0 {
		t.Fatal("the key that closes help is swallowed")
	}
}

func TestHelpAt80x24(t *testing.T) {
	h := newHarness(t, 80, 24)
	h.key("?")
	h.contains("Keys")
	h.checkGeometry(80, 24)
}

func TestScreenNavigation(t *testing.T) {
	h := newHarness(t, 100, 30)
	for _, c := range []struct {
		key  string
		want screen
	}{{"2", scrSync}, {"3", scrServer}, {"4", scrSettings}, {"1", scrProjects}} {
		h.key(c.key)
		if h.m.scr != c.want {
			t.Errorf("%s -> %v, want %v", c.key, h.m.scr, c.want)
		}
	}
	h.key("tab")
	if h.m.scr != scrSync {
		t.Errorf("tab cycles: %v", h.m.scr)
	}
	h.key("esc")
	if h.m.scr != scrProjects {
		t.Error("esc returns to projects")
	}
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil || cmd() != tea.Quit() {
		t.Error("q quits from the projects screen")
	}
}

func TestQuitKeyIsTextWhileTyping(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("/")
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil && cmd() == tea.Quit() {
		t.Fatal("q typed into the filter must not quit")
	}
	if h.m.pk.query != "q" {
		t.Errorf("q went into the filter: %q", h.m.pk.query)
	}
}

func TestRemoteDeletePromptAfterUnsharing(t *testing.T) {
	be := newFake()
	be.remote = &RemotePrompt{Count: 1, Handle: "handle-1"}
	h := newHarnessWith(t, be, 100, 30)
	h.key("space") // un-share pharmfindr
	h.contains("1 project(s) are no longer shared.", "Delete their data from the server too?")
	h.key("n")
	if len(be.remoteDels) != 0 {
		t.Fatal("n keeps the server data")
	}
	h.key("space", "space")
	h.contains("Delete their data from the server too?")
	h.key("y")
	if len(be.remoteDels) != 1 || be.remoteDels[0] != "handle-1" {
		t.Fatalf("the opaque handle comes back: %v", be.remoteDels)
	}
	h.contains("deleted 1 project from the server")
}

func TestNoServerMeansNoRemotePrompt(t *testing.T) {
	be := newFake()
	be.server = ""
	be.remote = &RemotePrompt{Count: 1, Handle: "x"}
	h := newHarnessWith(t, be, 100, 30)
	h.key("space")
	if h.m.modal != nil {
		t.Fatal("nothing to offer without a server")
	}
}

func TestSnapshotErrorIsShown(t *testing.T) {
	be := newFake()
	be.snapshotFn = func() (Snapshot, error) { return Snapshot{}, fmtErr("config: parse error") }
	h := newHarnessWith(t, be, 100, 30)
	h.contains("could not read your projects: config: parse error")
}

func TestEmptyStateGuidesTheUser(t *testing.T) {
	be := newFake()
	be.rows = nil
	h := newHarnessWith(t, be, 100, 30)
	h.contains("No projects found.", "Is Claude Code, Codex or OpenCode installed?")
	h.key("space", "d", "p", "e") // nothing selected: nothing may happen or crash
	if len(be.actions) != 0 {
		t.Fatal("actions with no rows")
	}
}

// --- colors ---

func TestNoColorMode(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if p := defaultRenderer().ColorProfile(); p != termenv.Ascii {
		t.Fatalf("NO_COLOR must select the ASCII profile, got %v", p)
	}
	th := NewTheme(defaultRenderer())
	m := New(newFake(), Options{Theme: &th, Now: func() time.Time { return clock }, StaticCursor: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if msg := m.Init(); msg != nil {
		hs := &harness{t: t, m: m, be: newFake()}
		hs.run(msg)
	}
	if strings.Contains(m.View(), "\x1b[") {
		t.Error("NO_COLOR output must not contain color escape sequences")
	}
	// meaning never depends on color alone: state glyphs are text
	for _, g := range []string{"[x]", "[ ]", "[~]", "[-]"} {
		if !strings.Contains(m.View(), g) {
			t.Errorf("state glyph %s missing", g)
		}
	}
}

func TestEveryScreenFitsEverySize(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {60, 16}, {100, 30}, {120, 40}} {
		for _, key := range []string{"1", "2", "3", "4", "?"} {
			h := newHarness(t, sz[0], sz[1])
			h.key(key)
			h.checkGeometry(sz[0], sz[1])
		}
		h := newHarness(t, sz[0], sz[1])
		h.key("p")
		h.checkGeometry(sz[0], sz[1])
		h.key("esc", "t")
		h.checkGeometry(sz[0], sz[1])
	}
}

var _ = context.Background

func TestThemeOverride(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	for name, c := range map[string]struct {
		env      map[string]string
		dark, ok bool
	}{
		"unset: the terminal decides": {map[string]string{}, false, false},
		"light":                       {map[string]string{"GOLDENGATE_THEME": "light"}, false, true},
		"dark, any case":              {map[string]string{"GOLDENGATE_THEME": "DARK"}, true, true},
		"garbage is ignored":          {map[string]string{"GOLDENGATE_THEME": "purple"}, false, false},
	} {
		if dark, ok := themeOverride(env(c.env)); dark != c.dark || ok != c.ok {
			t.Errorf("%s: got (%v,%v) want (%v,%v)", name, dark, ok, c.dark, c.ok)
		}
	}
}
