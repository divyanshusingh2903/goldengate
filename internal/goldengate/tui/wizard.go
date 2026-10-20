package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type wizStep int

const (
	wizWelcome wizStep = iota
	wizServer
	wizLevel
	wizProjects
	wizReview
	wizSync
)

var wizTitles = [...]string{"Welcome", "Server", "Share level", "Choose projects", "Review", "First sync"}

type wizardState struct {
	started bool
	step    wizStep
	tools   []ToolStatus
	mode    int // 0 admin token, 1 pairing code, 2 stay local
	field   int
	url     textinput.Model
	admin   textinput.Model
	pair    textinput.Model
	busy    bool
	err     string
	done    bool // connected
	res     InitResult
	level   int
}

func newWizardState(blink bool) wizardState {
	return wizardState{url: newInput("https://bridge.example.com", false, blink), admin: newInput("admin token (input hidden)", true, blink), pair: newInput("ABCD-EFGH-JKLM", false, blink)}
}

func (m *Model) startWizard() tea.Cmd {
	m.wz = newWizardState(!m.opts.StaticCursor)
	m.wz.started = true
	m.wz.url.SetValue(m.snap.Server)
	m.scr = scrWizard
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		t, err := be.Detect(ctx)
		return detectMsg{t, err}
	}
}

func (m *Model) onDetect(msg detectMsg) {
	if msg.err == nil {
		m.wz.tools = msg.tools
	}
}

func (m *Model) onInit(msg initMsg) {
	w := &m.wz
	w.busy = false
	w.admin.SetValue("") // the admin token never outlives the request
	if msg.err != nil {
		w.err = msg.err.Error()
		return
	}
	w.err, w.done, w.res = "", true, msg.res
	m.snap.Server = w.url.Value()
}

func (m *Model) wizNext() tea.Cmd {
	w := &m.wz
	w.step++
	w.err = ""
	switch w.step {
	case wizServer:
		w.field = 0
		return m.focusField() // typing must work the moment the step opens
	case wizProjects:
		m.pk.query = ""
		return m.loadSnapshot()
	case wizReview:
		return m.openSyncKeep()
	case wizSync:
		if m.sy.phase != syncRunning && !m.sy.checked {
			return m.planSync()
		}
		return nil
	}
	return nil
}

// openSyncKeep plans a sync without leaving the wizard.
func (m *Model) openSyncKeep() tea.Cmd {
	m.sy.phase = syncPlanning
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		p, err := be.Plan(ctx)
		return planMsg{p, err}
	}
}

func (m *Model) onKeyWizard(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	w := &m.wz
	s := k.String()
	if s == "esc" && w.step != wizServer || s == "ctrl+x" {
		return m, m.leaveWizard()
	}
	if w.busy {
		return m, nil
	}
	switch w.step {
	case wizWelcome:
		if s == "enter" {
			return m, m.wizNext()
		}
	case wizServer:
		return m.wizServerKey(k)
	case wizLevel:
		switch s {
		case "left", "right", "up", "down", "h", "l", "j", "k", " ":
			w.level = 1 - w.level
		case "enter":
			lv := []string{"summary", "titles-only"}[w.level]
			st := Settings{Level: lv, Since: m.st.s.Since, Sources: m.st.s.Sources, ExtraPatterns: m.st.s.ExtraPatterns}
			be, ctx := m.be, m.ctx
			return m, func() tea.Msg {
				if cur, err := be.Settings(ctx); err == nil {
					cur.Level = lv
					st = cur
				}
				return wizLevelSaved{be.SaveSettings(ctx, st)}
			}
		}
	case wizProjects:
		switch s {
		case "tab":
			return m, m.wizNext()
		case "shift+tab":
			w.step--
			return m, nil
		case "enter":
			return m, m.openPreview()
		case "esc":
			return m, nil
		}
		return m.onKeyProjects(k)
	case wizReview:
		switch s {
		case "enter":
			if m.sy.phase == syncReady {
				return m, m.wizNext()
			}
		case "shift+tab", "backspace":
			w.step = wizProjects
		}
	case wizSync:
		return m.onKeySync(k) // its way out finishes the setup
	}
	return m, nil
}

// leaveWizard returns to the picker. On a first run it also writes the default
// (nothing shared) configuration, so skipping setup does not bring it back
// every time the program starts.
func (m *Model) leaveWizard() tea.Cmd {
	m.scr = scrProjects
	if !m.snap.FirstRun {
		return m.loadSnapshot()
	}
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		if cur, err := be.Settings(ctx); err == nil {
			_ = be.SaveSettings(ctx, cur)
		}
		return m.loadSnapshot()()
	}
}

// wizLevelSaved reports that the level chosen in the wizard was written.
type wizLevelSaved struct{ err error }

func (m *Model) wizServerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	w := &m.wz
	s := k.String()
	if w.done {
		if s == "enter" {
			return m, m.wizNext()
		}
		return m, nil
	}
	fields := m.wizFields()
	switch s {
	case "esc":
		w.step = wizWelcome
		return m, nil
	case "ctrl+n", "down":
		if w.field >= len(fields)-1 && w.mode != 2 {
			w.mode = (w.mode + 1) % 3
			w.field = 0
		} else if len(fields) > 0 {
			w.field++
		} else {
			w.mode = (w.mode + 1) % 3
		}
		return m, m.focusField()
	case "ctrl+p", "up":
		if w.field > 0 {
			w.field--
		} else {
			w.mode = (w.mode + 2) % 3
			w.field = 0
		}
		return m, m.focusField()
	case "tab":
		w.mode = (w.mode + 1) % 3
		w.field = 0
		return m, m.focusField()
	case "enter":
		if w.mode == 2 {
			return m, m.wizNext() // stay local: nothing is sent anywhere
		}
		if strings.TrimSpace(w.url.Value()) == "" {
			w.err = "enter the server URL"
			return m, nil
		}
		p := InitParams{ServerURL: strings.TrimSpace(w.url.Value())}
		if w.mode == 0 {
			p.AdminToken = w.admin.Value()
			if p.AdminToken == "" {
				w.err = "enter the admin token (it is never stored)"
				return m, nil
			}
		} else {
			p.PairCode = strings.TrimSpace(w.pair.Value())
			if p.PairCode == "" {
				w.err = "enter the pairing code"
				return m, nil
			}
		}
		w.busy, w.err = true, ""
		be, ctx := m.be, m.ctx
		return m, func() tea.Msg {
			r, err := be.Init(ctx, p)
			return initMsg{r, err}
		}
	}
	// typing goes to the focused input
	var cmd tea.Cmd
	switch {
	case len(fields) > 0 && fields[w.field] == "url":
		w.url, cmd = w.url.Update(k)
	case len(fields) > 0 && fields[w.field] == "admin":
		w.admin, cmd = w.admin.Update(k)
	case len(fields) > 0 && fields[w.field] == "pair":
		w.pair, cmd = w.pair.Update(k)
	}
	return m, cmd
}

func (m *Model) wizFields() []string {
	switch m.wz.mode {
	case 0:
		return []string{"url", "admin"}
	case 1:
		return []string{"url", "pair"}
	}
	return nil
}

func (m *Model) focusField() tea.Cmd {
	w := &m.wz
	w.url.Blur()
	w.admin.Blur()
	w.pair.Blur()
	f := m.wizFields()
	if len(f) == 0 {
		return nil
	}
	switch f[w.field] {
	case "url":
		return w.url.Focus()
	case "admin":
		return w.admin.Focus()
	}
	return w.pair.Focus()
}

func (m *Model) viewWizard() string {
	w := &m.wz
	switch w.step {
	case wizProjects:
		out := m.viewProjects()
		return m.replaceHint(out, m.hints(m.w-2, "space", "share", "t", "tree", "p", "preview", "tab", "next step", "shift+tab", "back", "?", "help"), " Setup 4/6 · Choose what to share (nothing is shared by default)")
	case wizSync:
		out := m.viewSync()
		return m.replaceHint(out, "", "")
	}
	inner, bh := m.w-2, bodyHeight(m.h)
	var out []string
	add := func(l string) { out = append(out, " "+l) }
	top := fmt.Sprintf(" Setup %d/6 · %s", int(w.step)+1, wizTitles[w.step])
	hint := ""
	switch w.step {
	case wizWelcome:
		add(m.th.Title.Render("Welcome to Golden Gate"))
		add("")
		add("It lets an AI assistant know what you have been working on,")
		add("for only the projects you choose. Nothing is shared until you say so.")
		add("")
		add(m.th.Title.Render("Found on this computer"))
		if len(w.tools) == 0 {
			add(m.th.Dim.Render("looking…"))
		}
		for _, t := range w.tools {
			mark, st := "○", m.th.Dim
			note := "not found"
			if t.Installed {
				mark, st, note = "●", m.th.Good, fmt.Sprintf("%d session(s)", t.Sessions)
			}
			add(st.Render(mark) + " " + fmt.Sprintf("%-12s %s", pretty(t.Name), note))
		}
		hint = m.hints(inner, "enter", "continue", "esc", "skip setup")
	case wizServer:
		hint = m.wizServerView(&out)
	case wizLevel:
		add(m.th.Title.Render("What should be sent for each session?"))
		add("")
		for i, o := range []struct{ n, d string }{
			{"summary", "title, goal, files, commands, outcome, work items (recommended)"},
			{"titles-only", "just the project, title and time: the most private"},
		} {
			mk := "( )"
			if w.level == i {
				mk = "(•)"
			}
			add(fmt.Sprintf("%s %-12s %s", mk, o.n, o.d))
		}
		add("")
		add(m.th.Dim.Render("Redaction runs either way. Raw transcripts are never sent."))
		hint = m.hints(inner, "←/→", "choose", "enter", "continue", "esc", "skip setup")
	case wizReview:
		add(m.th.Title.Render("Dry run: this is exactly what the first sync would send"))
		add("")
		if m.sy.phase == syncPlanning || m.sy.phase == syncRunning {
			add(m.th.Dim.Render("planning…"))
		} else {
			p := m.sy.plan
			add(fmt.Sprintf("%d session(s) in %d project(s)", p.Sessions, len(p.Projects)))
			if n := totalRedactions(p.Redactions); n > 0 {
				add(m.th.Redact.Render(fmt.Sprintf("%d redaction(s) applied", n)))
			}
			for _, pr := range p.Projects {
				if len(out) >= bh-2 {
					add("…")
					break
				}
				add(fmt.Sprintf("  %-*s %d", max(inner-14, 10), truncLeft(pr.Label, max(inner-14, 10)), pr.Sessions))
			}
			if p.Sessions == 0 {
				add(m.th.Dim.Render("Nothing selected yet: go back and share a project, or finish and do it later."))
			}
			add("")
			add(m.th.Dim.Render("Open any project with p on the previous step to read its payloads."))
		}
		hint = m.hints(inner, "enter", "continue", "shift+tab", "back", "esc", "finish later")
	}
	f := frame{th: m.th, w: m.w, h: m.h, title: m.header("setup"), right: m.serverBadge(), top: top}
	f.left = lines(strings.Join(out, "\n"), inner, bh)
	f.hint = m.statusLine(hint)
	return f.render()
}

// replaceHint swaps the hint bar and top line of an already rendered frame.
func (m *Model) replaceHint(frame, hint, top string) string {
	rows := strings.Split(frame, "\n")
	inner := m.w - 2
	b := m.th.Border.Render
	if hint != "" && len(rows) >= 3 {
		rows[len(rows)-2] = b("│") + fit(hint, inner) + b("│")
	}
	if top != "" && len(rows) > 1 {
		rows[1] = b("│") + fit(top, inner) + b("│")
	}
	return strings.Join(rows, "\n")
}

func (m *Model) wizServerView(out *[]string) string {
	w := &m.wz
	inner := m.w - 2
	add := func(l string) { *out = append(*out, " "+l) }
	if w.done {
		add(m.th.Good.Render("Connected."))
		add("")
		add("Ingest token stored in the " + w.res.TokenStore + ". The admin token was not saved.")
		if w.res.ReadToken != "" {
			add("")
			add(m.th.Title.Render("Read token for your assistant (shown only now):"))
			add("   " + w.res.ReadToken)
		}
		add("Connector URL: " + w.res.MCPURL)
		if w.res.OAuth {
			add("OAuth is enabled: assistants that need it can connect without a token.")
		}
		return m.hints(inner, "enter", "continue")
	}
	add(m.th.Title.Render("Where do the summaries go?"))
	add("")
	for i, n := range []string{"Connect with the admin token", "Connect with a pairing code", "Stay local for now (nothing is sent)"} {
		mk := "( )"
		if w.mode == i {
			mk = "(•)"
		}
		add(mk + " " + n)
	}
	add("")
	switch w.mode {
	case 0, 1:
		add("Server URL   " + w.url.View())
		if w.mode == 0 {
			add("Admin token  " + w.admin.View())
			add(m.th.Dim.Render("The admin token is used once to create scoped tokens and is never written down."))
		} else {
			add("Pairing code " + w.pair.View())
			add(m.th.Dim.Render("Get one on the server with: goldengatebridge pair"))
		}
	default:
		add(m.th.Dim.Render("You can connect later from the Server screen."))
	}
	switch {
	case w.busy:
		add("")
		add(m.th.Dim.Render("connecting…"))
	case w.err != "":
		add("")
		add(m.th.Bad.Render(clip(w.err, inner-4)))
	}
	return m.hints(inner, "tab", "mode", "↑/↓", "field", "enter", "connect/continue", "esc", "back")
}
