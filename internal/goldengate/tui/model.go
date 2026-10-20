package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type screen int

const (
	scrProjects screen = iota
	scrPreview
	scrSync
	scrServer
	scrSettings
	scrWizard
)

// Options configures New.
type Options struct {
	Context context.Context
	Now     func() time.Time
	Theme   *Theme
	// SkipWizard opens the project picker even on a first run.
	SkipWizard bool
	// StaticCursor turns off cursor blinking in text fields (tests, and anyone
	// who prefers no motion).
	StaticCursor bool
	// ThemeName is the configured color preset; GOLDENGATE_THEME overrides it.
	ThemeName string
	// SetColors paints the terminal's default colors (empty: restore them).
	SetColors func(fg, bg string)
}

// modal is a yes/no or info box over the current screen.
type modal struct {
	title string
	lines []string
	yes   func(m *Model) tea.Cmd // nil: informational, any key closes
}

// Model is the whole UI.
type Model struct {
	be   Backend
	rend *lipgloss.Renderer // nil when the theme is fixed (tests)
	ctx  context.Context
	now  func() time.Time
	th   Theme
	opts Options

	w, h   int
	scr    screen
	help   bool
	modal  *modal
	status string
	bad    bool // status is an error

	snap   Snapshot
	loaded bool

	ck clickState

	pk picker
	pv previewState
	sy syncState
	sv serverState
	st settingsState
	wz wizardState
}

// New builds the UI around a backend.
func New(be Backend, o Options) *Model {
	if o.Context == nil {
		o.Context = context.Background()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &Model{be: be, ctx: o.Context, now: o.Now, opts: o, w: 80, h: 24}
	if o.Theme != nil {
		m.th = *o.Theme
	} else {
		m.rend = defaultRenderer()
		name := o.ThemeName
		if env := os.Getenv("GOLDENGATE_THEME"); env != "" {
			name = env
		}
		m.applyTheme(name)
	}
	m.pk = newPicker(!o.StaticCursor)
	m.pv = newPreviewState()
	m.sy = newSyncState()
	m.st = newSettingsState(!o.StaticCursor)
	m.wz = newWizardState(!o.StaticCursor)
	return m
}

// rendStyle is a blank style on the UI's renderer.
func (m *Model) rendStyle() lipgloss.Style {
	if m.rend == nil {
		return m.th.Plain
	}
	return m.rend.NewStyle()
}

// applyTheme switches the color preset at once.
func (m *Model) applyTheme(name string) {
	if m.rend == nil {
		m.th.Name = name // fixed styles (tests); remember the choice only
		return
	}
	resolved, p := ResolveTheme(name)
	m.th = NewThemeFrom(m.rend, resolved, p)
	if m.opts.SetColors != nil && os.Getenv("NO_COLOR") == "" {
		if p.Bg == "" {
			m.opts.SetColors("", "")
		} else {
			m.opts.SetColors(p.Fg, p.Bg)
		}
	}
}

// --- messages ---

type (
	snapshotMsg struct {
		snap Snapshot
		err  error
	}
	previewMsg struct {
		key string
		pv  Preview
		err error
	}
	debounceMsg struct {
		key string
		seq int
	}
	actionMsg struct {
		res  ActionResult
		err  error
		verb string
	}
	remoteMsg struct {
		msg string
		err error
	}
	planMsg struct {
		plan PlanInfo
		err  error
	}
	syncEventMsg struct{ ev SyncEvent }
	syncDoneMsg  struct{ err error }
	serverMsg    struct {
		info ServerInfo
		err  error
	}
	serverDelMsg struct {
		label string
		err   error
	}
	settingsMsg struct {
		s   Settings
		err error
	}
	savedMsg  struct{ err error }
	detectMsg struct {
		tools []ToolStatus
		err   error
	}
	initMsg struct {
		res InitResult
		err error
	}
)

// Init loads the first snapshot.
func (m *Model) Init() tea.Cmd { return tea.Batch(m.loadSnapshot(), m.checkServer()) }

// checkServer refreshes the connection badge without changing screens.
func (m *Model) checkServer() tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		i, err := be.Server(ctx)
		return serverMsg{i, err}
	}
}

func (m *Model) loadSnapshot() tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		s, err := be.Snapshot(ctx)
		return snapshotMsg{s, err}
	}
}

func (m *Model) setStatus(s string, bad bool) { m.status, m.bad = s, bad }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.resize()
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	case tea.MouseMsg:
		return m.onMouse(msg)
	case snapshotMsg:
		return m, m.onSnapshot(msg)
	case previewMsg:
		m.onPreview(msg)
		return m, nil
	case debounceMsg:
		return m, m.onDebounce(msg)
	case actionMsg:
		return m, m.onAction(msg)
	case remoteMsg:
		if msg.err != nil {
			m.setStatus("could not delete from the server: "+msg.err.Error(), true)
		} else {
			m.setStatus(msg.msg, false)
		}
		return m, nil
	case planMsg:
		return m, m.onPlan(msg)
	case syncEventMsg:
		return m, m.onSyncEvent(msg)
	case syncDoneMsg:
		m.onSyncDone(msg)
		return m, nil
	case serverMsg:
		m.onServer(msg)
		return m, nil
	case serverDelMsg:
		return m, m.onServerDelete(msg)
	case settingsMsg:
		return m, m.onSettings(msg)
	case savedMsg:
		return m, m.onSaved(msg)
	case keySavedMsg:
		return m, m.onKeySaved(msg)
	case testDoneMsg:
		m.onTestDone(msg)
		return m, nil
	case modelsMsg:
		m.onModels(msg)
		return m, nil
	case detectMsg:
		m.onDetect(msg)
		return m, nil
	case initMsg:
		m.onInit(msg)
		return m, nil
	case wizLevelSaved:
		if msg.err != nil {
			m.wz.err = msg.err.Error()
			return m, nil
		}
		return m, tea.Batch(m.wizNext(), m.loadSnapshot())
	}
	return m, nil
}

func (m *Model) onSnapshot(msg snapshotMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("could not read your projects: "+msg.err.Error(), true)
		m.loaded = true
		return nil
	}
	m.snap, m.loaded = msg.snap, true
	m.pk.clamp(m)
	if msg.snap.FirstRun && !m.opts.SkipWizard && !m.wz.started {
		return m.startWizard()
	}
	return m.requestPreview()
}

// resize recomputes anything that depends on the window.
func (m *Model) resize() {
	bh := bodyHeight(m.h)
	m.pv.vp.Width, m.pv.vp.Height = max(m.w-2, 1), bh
	m.pv.refresh(m)
	m.pk.clamp(m)
}

// tooSmall reports whether the window is below the supported minimum.
func (m *Model) tooSmall() bool { return m.w < minW || m.h < minH }

// View implements tea.Model.
func (m *Model) View() string {
	if m.tooSmall() {
		return center(fmt.Sprintf("goldengate needs at least %dx%d (this window is %dx%d)", minW, minH, m.w, m.h), m.w, m.h)
	}
	m.ck.zones, m.ck.text = m.ck.zones[:0], m.ck.text[:0]
	var out string
	switch m.scr {
	case scrPreview:
		out = m.viewPreview()
	case scrSync:
		out = m.viewSync()
	case scrServer:
		out = m.viewServer()
	case scrSettings:
		out = m.viewSettings()
	case scrWizard:
		out = m.viewWizard()
	default:
		out = m.viewProjects()
	}
	if m.help || m.modal != nil {
		// an overlay takes every click; what lies under it is not clickable
		m.ck.zones, m.ck.text = m.ck.zones[:0], m.ck.text[:0]
	}
	if m.help {
		out = overlay(out, m.helpBox(), m.w, m.h)
	}
	if m.modal != nil {
		out = overlay(out, m.modalBox(), m.w, m.h)
	}
	m.resolveZones(out)
	return out
}

// hints is the hint bar; each single-key pair is also a click target.
func (m *Model) hints(w int, pairs ...string) string {
	for i := 0; i+1 < len(pairs); i += 2 {
		if clickable(pairs[i]) {
			m.clickKey(m.h-2, pairs[i]+" "+pairs[i+1], pairs[i])
		}
	}
	return hints(m.th, w, pairs...)
}

func (m *Model) modalBox() []string {
	lines := append([]string{}, m.modal.lines...)
	if m.modal.yes != nil {
		lines = append(lines, "", m.button(m.th.ButtonGo, -1, "y", "Yes")+"  "+m.button(m.th.Button, -1, "n", "No"))
	} else {
		lines = append(lines, "", m.button(m.th.Button, -1, "enter", "OK"))
	}
	return box(m.th, m.modal.title, clipLines(lines, m.w))
}

// --- common frame pieces ---

func (m *Model) serverBadge() string {
	switch {
	case m.snap.Server == "":
		return m.th.Dim.Render("server: ○ not configured")
	case m.sv.info.Configured && m.sv.info.Healthy:
		return m.th.Good.Render("● connected") + m.th.Dim.Render(" "+shortHost(m.snap.Server))
	case m.sv.info.Configured && m.sv.info.Err != "":
		return m.th.Bad.Render("✗ unreachable") + m.th.Dim.Render(" "+shortHost(m.snap.Server))
	}
	return "server: " + m.th.Dim.Render("● "+shortHost(m.snap.Server))
}

func shortHost(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	return u
}

func (m *Model) statusLine(hint string) string {
	if m.status == "" {
		return hint
	}
	st := m.th.Good
	if m.bad {
		st = m.th.Bad
	}
	return " " + st.Render(m.status)
}

func pretty(source string) string {
	switch source {
	case "claude-code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "claude-ai":
		return "claude.ai"
	case "chatgpt":
		return "ChatGPT"
	}
	return source
}

func (m *Model) sourcesLine() string {
	var parts []string
	for _, s := range m.snap.Sources {
		on := s.Installed && s.Enabled
		dot := m.th.Dim.Render("○")
		if on {
			dot = m.th.Accent.Render("●")
		}
		name := metaOf(s.Name).tag + " " + pretty(s.Name)
		if s.Installed {
			name += fmt.Sprintf(" %d", s.Sessions)
		}
		if on {
			name = m.tag(s.Name) + strings.TrimPrefix(name, metaOf(s.Name).tag)
		} else {
			name = m.th.Dim.Render(name)
		}
		p := dot + " " + name
		parts = append(parts, p)
	}
	return m.th.Label.Render("Sources ") + strings.Join(parts, "  ")
}

func newInput(placeholder string, secret, blink bool) textinput.Model {
	ti := textinput.New()
	if !blink {
		ti.Cursor.SetMode(cursor.CursorStatic)
	}
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.CharLimit = 256
	if secret {
		ti.EchoMode = textinput.EchoPassword
	}
	return ti
}

func newViewport() viewport.Model { return viewport.New(78, 18) }
