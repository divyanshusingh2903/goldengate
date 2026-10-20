package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
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
		m.th = NewTheme(defaultRenderer())
	}
	m.pk = newPicker(!o.StaticCursor)
	m.pv = newPreviewState()
	m.sy = newSyncState()
	m.st = newSettingsState(!o.StaticCursor)
	m.wz = newWizardState(!o.StaticCursor)
	return m
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
		m.onPlan(msg)
		return m, nil
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
		m.onSettings(msg)
		return m, nil
	case savedMsg:
		return m, m.onSaved(msg)
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
	if m.help {
		out = overlay(out, m.helpBox(), m.w, m.h)
	}
	if m.modal != nil {
		out = overlay(out, m.modalBox(), m.w, m.h)
	}
	return out
}

func (m *Model) modalBox() []string {
	lines := append([]string{}, m.modal.lines...)
	if m.modal.yes != nil {
		lines = append(lines, "", m.th.Key.Render("y")+" yes   "+m.th.Key.Render("n")+" no")
	} else {
		lines = append(lines, "", m.th.Dim.Render("press any key"))
	}
	return box(m.th, m.modal.title, clipLines(lines, m.w))
}

// --- common frame pieces ---

func (m *Model) serverBadge() string {
	switch {
	case m.snap.Server == "":
		return m.th.Dim.Render("server: ○ not configured")
	case m.sv.info.Configured && m.sv.info.Healthy:
		return m.th.Good.Render("server: ● connected")
	case m.sv.info.Configured && m.sv.info.Err != "":
		return m.th.Bad.Render("server: ✗ unreachable")
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
		dot := "○"
		if s.Installed && s.Enabled {
			dot = "●"
		}
		p := dot + " " + pretty(s.Name)
		if s.Installed {
			p += fmt.Sprintf(" %d", s.Sessions)
		}
		if !s.Installed || !s.Enabled {
			p = m.th.Dim.Render(p)
		}
		parts = append(parts, p)
	}
	return " Sources  " + strings.Join(parts, "  ")
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
