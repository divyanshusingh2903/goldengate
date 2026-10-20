package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
)

// --- sync ---

func TestSyncReviewThenSend(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("s")
	if h.m.scr != scrSync || h.m.sy.phase != syncReady || !h.m.sy.checked {
		t.Fatalf("s opens the sync screen and reviews at once: scr=%v phase=%v", h.m.scr, h.m.sy.phase)
	}
	if len(h.be.syncs) != 1 || !h.be.syncs[0] {
		t.Fatalf("the review is a dry pass: %v", h.be.syncs)
	}
	h.contains("✓ Review", "② Send", "✓ Ready to send", "nothing has left this machine yet",
		"~/code/pharmfindr", "7 sessions · 3 projects · 2 secrets redacted", "120 sessions in projects you don't share stay on this machine",
		"[enter Send 7 sessions]")
	h.checkGeometry(100, 30)
	h.golden("sync_plan")
	h.key("enter") // step 2
	if len(h.be.syncs) != 2 || h.be.syncs[1] {
		t.Fatalf("enter sends for real: %v", h.be.syncs)
	}
	h.contains("✓ Sent 7 sessions to bridge.example.com", "[enter Done]")
	h.golden("sync_done")
	h.key("enter")
	if h.m.scr != scrProjects {
		t.Fatal("Done returns to the projects")
	}
}

func TestSyncNothingToSend(t *testing.T) {
	be := newFake()
	h := newHarnessWith(t, be, 100, 30)
	h.m.sy.plan = PlanInfo{}
	h.m.onPlan(planMsg{plan: PlanInfo{Unchanged: 12, Server: be.server}})
	h.m.scr = scrSync
	h.contains("✓ Everything is up to date", "12 sessions")
	if len(be.syncs) != 0 {
		t.Fatal("nothing to review, nothing run")
	}
}

func TestSyncWithoutServerOnlyReviews(t *testing.T) {
	be := newFake()
	be.server = ""
	h := newHarnessWith(t, be, 100, 30)
	h.key("s")
	h.contains("No server is connected, so this can only be a review.", "[enter Connect a server]", "nowhere yet")
	h.key("enter")
	if len(be.syncs) != 1 || h.m.scr != scrServer {
		t.Fatalf("enter goes to the server screen instead of sending: %v scr=%v", be.syncs, h.m.scr)
	}
}

func TestSyncFailureAndRetry(t *testing.T) {
	be := newFake()
	be.syncFail["~/code/forge"] = "server returned 503: try later"
	h := newHarnessWith(t, be, 100, 30)
	h.key("s", "enter")
	h.contains("✗ 1 project could not be sent", "✗  ~/code/forge", "503", "6 sessions were sent", "[enter Retry]")
	h.golden("sync_failed")
	delete(be.syncFail, "~/code/forge")
	h.key("enter") // retry reviews again first
	if !h.m.sy.canSend() {
		t.Fatalf("retry runs the review: phase %v", h.m.sy.phase)
	}
	h.key("enter")
	h.contains("✓ Sent 7 sessions")
}

func TestQuitWhileSyncRunningAsksFirst(t *testing.T) {
	be := newFake()
	be.syncBlock = make(chan struct{})
	h := newHarnessWith(t, be, 100, 30)
	h.key("s")                                           // the review finishes; the send blocks
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
	h2.contains("✗ unreachable")
	h2.key("3")
	h2.contains("✗ unreachable", "connection refused")
}

// --- settings ---

func TestSettingsScreen(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4")
	h.contains("Share level", "summary", "Look back", "90d", "Source CC Claude Code", "on", "Source AI claude.ai", "not found on this computer", "Verify commits with local git", "not available yet")
	h.checkGeometry(100, 30)
	h.golden("settings")
}

func TestSettingsChangesAreSavedImmediately(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4", "down", "space") // level
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
	h.key("down", "down", "space") // first source off (past the Summarizer row)
	if s := h.be.saved[len(h.be.saved)-1].Sources[0]; s.Enabled {
		t.Fatalf("source toggled: %+v", s)
	}
	h.contains("Source CC Claude Code", "off")
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
	h.key("4", "down", "space")
	h.contains(`invalid since window "soon"`)
	h.contains("summary") // the shown value reverts to what is really saved
}

func TestSettingsEditEscapeCancels(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4", "down", "down", "enter")
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
	h.contains("Keys", "fuzzy search", "paranoid", "[x] shared")
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

func TestSyncProgressFollowsEachStep(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("s", "enter") // review, then send; now replay a slow send by hand
	m := h.m
	m.sy.plan.Sessions = 120
	m.sy.phase, m.sy.dry, m.sy.done, m.sy.total, m.sy.prepared = syncRunning, false, 0, 0, 0
	m.sy.order, m.sy.proj = nil, map[string]*projProgress{}
	m.sy.ch = make(chan tea.Msg, 1)
	ev := func(e SyncEvent) { m.onSyncEvent(syncEventMsg{e}) }
	for i := 0; i < 45; i++ {
		ev(SyncEvent{Kind: SyncPrepared, Sessions: 1})
	}
	h.contains("preparing 45/120 sessions")
	ev(SyncEvent{Kind: SyncPlanned, Sessions: 120})
	ev(SyncEvent{Kind: SyncProjectStart, Project: "~/code/forge", Sessions: 116})
	ev(SyncEvent{Kind: SyncProgress, Project: "~/code/forge", Sessions: 10})
	ev(SyncEvent{Kind: SyncProgress, Project: "~/code/forge", Sessions: 10})
	h.contains("sent 20/120 sessions", "20/116")
	ev(SyncEvent{Kind: SyncProjectDone, Project: "~/code/forge", Sessions: 116})
	if m.sy.done != 116 {
		t.Fatalf("done counts each session once: %d", m.sy.done)
	}
}

func TestSummarizerDropdownDoesEverythingInTheTUI(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.key("4")
	for i, r := range h.m.settingRows() {
		if r.kind == setSummarizer {
			h.m.st.cursor = i
		}
	}
	h.contains("Summarizer", "Local heuristic", "nothing is sent to a model")
	h.key("enter") // opens the dropdown
	if h.m.st.dd != menuSummarizer {
		t.Fatal("enter opens the summarizer dropdown")
	}
	h.contains("┌ Summarizer", "✓ Local heuristic", "Anthropic API (Claude)", "OpenAI API (ChatGPT)", "Google Gemini API", "OpenCode Zen API", "Custom (OpenAI-compatible)")
	h.golden("summarizer_menu")
	h.key("esc")
	if h.m.st.dd != menuNone || len(h.be.saved) != 0 {
		t.Fatal("esc closes without a change")
	}

	// choose Gemini: asks first, then lands on the API key
	h.key("enter", "down", "down", "down", "enter")
	if h.m.modal == nil || len(h.be.saved) != 0 {
		t.Fatal("choosing a model asks before saving")
	}
	h.contains("Summarize with Google Gemini API?", "redacted conversation", "Tool output and file contents are never sent")
	h.key("y")
	if s := h.be.saved[len(h.be.saved)-1].Summarize; s.Provider != "gemini" || s.Model != "" {
		t.Fatalf("gemini with its defaults: %+v", s)
	}
	h.contains("Google Gemini API", "gemini-3.5-flash-lite (default)", "generativelanguage.googleapis.com", "missing · enter to paste your key", "[enter Test connection]")
	if r := h.m.settingRows()[h.m.st.cursor]; r.kind != setSumKey {
		t.Fatalf("after choosing, the cursor is on the API key row: %v", r.kind)
	}

	// paste a key: hidden while typing, stored through the backend, never in settings
	h.key("enter")
	h.typeText("sk-secret-123")
	if strings.Contains(h.view(), "sk-secret-123") {
		t.Fatal("the key is masked while typed")
	}
	h.contains("API key for Google Gemini API (saved in the OS keyring")
	h.key("enter")
	if h.be.keys["gemini"] != "sk-secret-123" {
		t.Fatalf("the key went to the keyring: %v", h.be.keys)
	}
	for _, s := range h.be.saved {
		if strings.Contains(fmt.Sprintf("%+v", s), "sk-secret") {
			t.Fatal("the key never goes into the settings/config")
		}
	}
	h.contains("✓ keyring")

	// change the model, then test the connection
	for i, r := range h.m.settingRows() {
		if r.kind == setSumModel {
			h.m.st.cursor = i
		}
	}
	h.be.models = map[string][]string{"gemini": {"gemini-3.5-flash-lite", "gemini-3.6-flash"}}
	h.key("enter") // the model picker lists what the provider offers
	if h.m.st.dd != menuModel {
		t.Fatal("enter on Model opens the model picker")
	}
	h.contains("┌ Model", "gemini-3.6-flash", "✎ Type a model name…")
	h.click("gemini-3.6-flash")
	if s := h.be.saved[len(h.be.saved)-1].Summarize; s.Model != "gemini-3.6-flash" {
		t.Fatalf("model: %+v", s)
	}
	h.key("enter", "end", "enter") // or type one
	h.m.st.input.SetValue("gemini-3.7-flash-lite")
	h.key("enter")
	if s := h.be.saved[len(h.be.saved)-1].Summarize; s.Model != "gemini-3.7-flash-lite" {
		t.Fatalf("typed model: %+v", s)
	}
	for i, r := range h.m.settingRows() {
		if r.kind == setSumTest {
			h.m.st.cursor = i
		}
	}
	h.key("enter")
	if h.be.tests != 1 {
		t.Fatal("enter on Test runs it")
	}
	h.contains("✓ gemini:model ok in 812ms")

	// a custom endpoint, chosen by mouse
	for i, r := range h.m.settingRows() {
		if r.kind == setSummarizer {
			h.m.st.cursor = i
		}
	}
	h.key("enter")
	h.click("Custom (OpenAI-compatible)")
	h.key("y") // a local provider goes straight to its model picker; this endpoint is down
	if h.m.st.dd != menuModel {
		t.Fatal("after choosing a keyless provider, its model picker opens")
	}
	h.contains("couldn't list models", "connection refused", "✎ Type a model name…")
	h.key("esc")
	h.contains("(choose one)", "localhost:1234", "none (fine for a local server)")

	// back to the heuristic: no question, provider rows go away
	for i, r := range h.m.settingRows() {
		if r.kind == setSummarizer {
			h.m.st.cursor = i
		}
	}
	h.key("enter", "home", "enter")
	if s := h.be.saved[len(h.be.saved)-1].Summarize; s.Provider != "" || h.m.modal != nil {
		t.Fatalf("the heuristic needs no confirmation: %+v", s)
	}
	if strings.Contains(h.view(), "Test connection") {
		t.Error("the model rows go away with the heuristic")
	}
}
func TestSyncOffersToResummarizeHeuristicSessions(t *testing.T) {
	be := newFake()
	be.stale = 5
	h := newHarnessWith(t, be, 110, 34)
	h.key("s")
	h.contains("5 synced sessions have rule-based summaries", "anthropic:claude-haiku-5-5", "[m Include them in this sync]")
	h.click("[m Include them in this sync]")
	if n := len(be.planOpts); n < 2 || !be.planOpts[n-1].Resummarize {
		t.Fatalf("the button plans again with the flag: %+v", be.planOpts)
	}
	if n := len(be.syncOpts); n < 2 || !be.syncOpts[n-1].Resummarize || !be.syncs[n-1] {
		t.Fatalf("and reviews again (dry) with it: %+v %v", be.syncOpts, be.syncs)
	}
	h.lacks("[m Include them in this sync]")
	h.contains("Includes 5 synced sessions re-summarized by anthropic:claude-haiku-5-5")
	h.key("enter")
	if n := len(be.syncOpts); be.syncs[n-1] || !be.syncOpts[n-1].Resummarize {
		t.Fatalf("the send keeps the flag: %+v %v", be.syncOpts, be.syncs)
	}
	// leaving and coming back starts without it
	h.key("enter")
	h.key("s")
	if n := len(be.planOpts); be.planOpts[n-1].Resummarize {
		t.Fatal("a fresh sync screen does not re-summarize unasked")
	}
}

func TestSyncNoResummarizeOfferWithoutAModel(t *testing.T) {
	h := newHarnessWith(t, newFake(), 110, 34)
	h.key("s")
	h.lacks("rule-based", "Include them")
}

func TestOllamaOutOfTheBox(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.be.models = map[string][]string{"ollama": {"llama3.2:latest", "qwen3:8b"}}
	h.key("4")
	for i, r := range h.m.settingRows() {
		if r.kind == setSummarizer {
			h.m.st.cursor = i
		}
	}
	h.key("enter") // the menu says whether Ollama runs here
	h.contains("Ollama (this machine)", "running here · 2 models installed")
	h.click("Ollama (this machine)")
	h.contains("Summarize with Ollama (this machine)?", "Ollama runs on this machine: nothing leaves it.")
	h.key("y")
	if h.m.st.dd != menuModel {
		t.Fatal("the installed models are offered right away")
	}
	h.contains("llama3.2:latest", "qwen3:8b")
	h.click("qwen3:8b")
	s := h.be.saved[len(h.be.saved)-1].Summarize
	if s.Provider != "ollama" || s.Model != "qwen3:8b" {
		t.Fatalf("ollama with an installed model: %+v", s)
	}
	h.contains("qwen3:8b ▾", "localhost:11434", "none (fine for a local server)")
	if len(h.be.keys) != 0 {
		t.Error("Ollama needs no key")
	}
}

func TestOllamaNotRunningSaysSo(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.key("4")
	for i, r := range h.m.settingRows() {
		if r.kind == setSummarizer {
			h.m.st.cursor = i
		}
	}
	h.key("enter")
	h.contains("not running here · install from ollama.com")
}

func TestReviewShowsWhatHappensWhileItSummarizes(t *testing.T) {
	h := newHarness(t, 110, 34)
	h.key("2") // the fake review finishes at once; replay a slow one by hand
	m := h.m
	m.snap.Summarizer = "ollama:gemma2:2b"
	m.sy.phase, m.sy.dry, m.sy.total, m.sy.prepared, m.sy.expect = syncRunning, true, 0, 0, 6
	m.sy.recent, m.sy.fallbacks, m.sy.started = nil, 0, clock.Add(-30*time.Second)
	m.sy.ch = make(chan tea.Msg, 1)
	h.contains("Step 1 · Review", "nothing is sent in this step", "read from its tool's log",
		"redacted: secrets, keys and paths", "summarized by ollama:gemma2:2b on this machine", "starting · the first answer can be slow")
	ev := func(title, by string) {
		m.onSyncEvent(syncEventMsg{SyncEvent{Kind: SyncPrepared, Sessions: 1, Project: "~/code/app", Title: title, By: by}})
	}
	ev("Fix the theme dropdown", "ollama:gemma2:2b")
	ev("Add Ollama support", "ollama:gemma2:2b")
	ev("Long refactor", "heuristic") // the model failed on this one
	h.contains("3 of ~6 sessions", "~10s each", "about 30s left", "Just summarized", "✓ Add Ollama support",
		"⚠ Long refactor", "rules (model failed)", "1 session kept the rule-based summary", "Model answers are cached")
	h.checkGeometry(110, 34)
	h.golden("sync_preparing")
	if strings.Contains(h.view(), "nowhere yet") {
		t.Error("the destination is known from the snapshot while the plan builds")
	}
}
