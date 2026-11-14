package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// The Agents screen: a grid of projects by agents that decides which
// assistant sees which project. Changes are collected, reviewed and only then
// applied. It needs the admin secret, which is asked for once and kept in
// memory until the UI exits.

type agentsPhase int

const (
	agAsk agentsPhase = iota // waiting for the admin secret
	agLoading
	agGrid
	agView   // view as agent
	agDetail // agent details
)

// allRow is the grid row for the "every project" switch; project rows follow.
const allRow = 0

type grantKey struct{ agent, project string } // project "" is the "every project" switch

type agentsState struct {
	phase   agentsPhase
	input   textinput.Model
	secret  string // admin secret, in memory only
	info    AgentsInfo
	err     string
	row     int // 0 is the "every project" row, then projects
	col     int
	rowOff  int
	pending map[grantKey]bool // wanted state for cells that differ from the server
	view    AgentView
	viewErr string
	scroll  int
	tok     int // cursor in the details token list
}

func newAgentsState(blink bool) agentsState {
	in := newInput("", false, blink)
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	return agentsState{input: in, pending: map[grantKey]bool{}}
}

type (
	agentsMsg struct {
		info AgentsInfo
		err  error
	}
	agentsAppliedMsg struct {
		n   int
		err error
	}
	agentViewMsg struct {
		view AgentView
		err  error
	}
	agentTokenMsg struct {
		label string
		err   error
	}
)

func (m *Model) openAgents() tea.Cmd {
	m.scr = scrAgents
	ag := &m.ag
	ag.err = ""
	if m.snap.Server == "" {
		ag.phase = agAsk
		return nil
	}
	if ag.secret == "" {
		ag.phase = agAsk
		ag.input.SetValue("")
		return ag.input.Focus()
	}
	return m.loadAgents()
}

func (m *Model) loadAgents() tea.Cmd {
	m.ag.phase = agLoading
	be, ctx, secret := m.be, m.ctx, m.ag.secret
	return func() tea.Msg {
		i, err := be.Agents(ctx, secret)
		return agentsMsg{i, err}
	}
}

func (m *Model) onAgents(msg agentsMsg) tea.Cmd {
	ag := &m.ag
	if msg.err != nil {
		// most likely a wrong secret: forget it and ask again
		ag.secret, ag.phase, ag.err = "", agAsk, msg.err.Error()
		ag.input.SetValue("")
		return ag.input.Focus()
	}
	ag.info, ag.err = msg.info, ""
	if ag.phase == agLoading {
		ag.phase = agGrid
	}
	// drop pending changes that no longer apply
	for k, want := range ag.pending {
		cur, ok := ag.cellNow(k)
		if !ok || cur == want {
			delete(ag.pending, k)
		}
	}
	ag.clamp(m)
	return nil
}

func (ag *agentsState) agent() *AgentInfo {
	if ag.col < 0 || ag.col >= len(ag.info.Agents) {
		return nil
	}
	return &ag.info.Agents[ag.col]
}

// keyAt is the cell under (row, col).
func (ag *agentsState) keyAt(row, col int) (grantKey, bool) {
	if col < 0 || col >= len(ag.info.Agents) || row < 0 || row > len(ag.info.Projects) {
		return grantKey{}, false
	}
	k := grantKey{agent: ag.info.Agents[col].ID}
	if row != allRow {
		k.project = ag.info.Projects[row-1].ID
	}
	return k, true
}

// cellNow is what the server has for a cell.
func (ag *agentsState) cellNow(k grantKey) (bool, bool) {
	for _, a := range ag.info.Agents {
		if a.ID != k.agent {
			continue
		}
		if k.project == "" {
			return a.AllProjects, true
		}
		for _, p := range ag.info.Projects {
			if p.ID == k.project {
				return a.Grants[k.project], true
			}
		}
	}
	return false, false
}

// cell is the wanted state of a cell and whether that is a pending change.
func (ag *agentsState) cell(k grantKey) (on, changed bool) {
	cur, _ := ag.cellNow(k)
	if want, ok := ag.pending[k]; ok {
		return want, want != cur
	}
	return cur, false
}

func (ag *agentsState) toggle(k grantKey) {
	cur, ok := ag.cellNow(k)
	if !ok {
		return
	}
	on, _ := ag.cell(k)
	if !on == cur {
		delete(ag.pending, k)
	} else {
		ag.pending[k] = !on
	}
}

// changes lists the pending changes in grid order.
func (ag *agentsState) changes() []GrantChange {
	var out []GrantChange
	for _, a := range ag.info.Agents {
		if want, ok := ag.pending[grantKey{agent: a.ID}]; ok {
			out = append(out, GrantChange{AgentID: a.ID, AgentName: a.Name, All: true, Grant: want})
		}
		for _, p := range ag.info.Projects {
			if want, ok := ag.pending[grantKey{a.ID, p.ID}]; ok {
				out = append(out, GrantChange{AgentID: a.ID, AgentName: a.Name, ProjectID: p.ID, ProjectName: p.Name, Grant: want})
			}
		}
	}
	return out
}

func (c GrantChange) String() string {
	switch {
	case c.All && c.Grant:
		return fmt.Sprintf("+ %s: every project, including future ones", c.AgentName)
	case c.All:
		return fmt.Sprintf("- %s: no longer every project", c.AgentName)
	case c.Grant:
		return fmt.Sprintf("+ %s can see %s", c.AgentName, c.ProjectName)
	}
	return fmt.Sprintf("- %s can no longer see %s", c.AgentName, c.ProjectName)
}

func (ag *agentsState) gridRows(m *Model) int { return max(bodyHeight(m.h)-5, 1) }

func (ag *agentsState) clamp(m *Model) {
	ag.col = max(min(ag.col, len(ag.info.Agents)-1), 0)
	ag.row = max(min(ag.row, len(ag.info.Projects)), 0)
	rows := ag.gridRows(m)
	if ag.row < ag.rowOff {
		ag.rowOff = ag.row
	}
	if ag.row >= ag.rowOff+rows {
		ag.rowOff = ag.row - rows + 1
	}
	ag.rowOff = max(min(ag.rowOff, len(ag.info.Projects)+1-rows), 0)
}

func (m *Model) review() {
	ch := m.ag.changes()
	if len(ch) == 0 {
		m.setStatus("Nothing to apply: toggle a box with space first.", false)
		return
	}
	ls := make([]string, 0, len(ch)+2)
	for i, c := range ch {
		if i == 8 && len(ch) > 9 {
			ls = append(ls, fmt.Sprintf("… and %d more", len(ch)-8))
			break
		}
		ls = append(ls, c.String())
	}
	ls = append(ls, "", "Apply these changes? They take effect at once.")
	m.modal = &modal{title: fmt.Sprintf("Review %s", plural(len(ch), "change")), lines: ls,
		yes: func(m *Model) tea.Cmd {
			be, ctx, secret := m.be, m.ctx, m.ag.secret
			return func() tea.Msg { return agentsAppliedMsg{len(ch), be.ApplyGrants(ctx, secret, ch)} }
		}}
}

func (m *Model) onAgentsApplied(msg agentsAppliedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("could not apply every change: "+msg.err.Error(), true)
	} else {
		m.setStatus("applied "+plural(msg.n, "change"), false)
		m.ag.pending = map[grantKey]bool{}
	}
	be, ctx, secret := m.be, m.ctx, m.ag.secret
	return func() tea.Msg {
		i, err := be.Agents(ctx, secret)
		return agentsMsg{i, err}
	}
}

func (m *Model) openAgentView() tea.Cmd {
	a := m.ag.agent()
	if a == nil {
		return nil
	}
	m.ag.phase, m.ag.scroll, m.ag.viewErr, m.ag.view = agView, 0, "", AgentView{Agent: *a}
	be, ctx, secret, id := m.be, m.ctx, m.ag.secret, a.ID
	return func() tea.Msg {
		v, err := be.AgentView(ctx, secret, id)
		return agentViewMsg{v, err}
	}
}

func (m *Model) onAgentView(msg agentViewMsg) {
	if msg.err != nil {
		m.ag.viewErr = msg.err.Error()
		return
	}
	m.ag.view = msg.view
}

func (m *Model) onAgentToken(msg agentTokenMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("could not revoke "+msg.label+": "+msg.err.Error(), true)
		return nil
	}
	m.setStatus("revoked "+msg.label+": it stops working at once", false)
	be, ctx, secret := m.be, m.ctx, m.ag.secret
	return func() tea.Msg {
		i, err := be.Agents(ctx, secret)
		return agentsMsg{i, err}
	}
}

func (m *Model) onKeyAgents(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	ag := &m.ag
	s := k.String()
	switch ag.phase {
	case agAsk:
		if m.snap.Server == "" { // nothing to type into: any of the usual keys goes back
			if s == "esc" || s == "q" || s == "backspace" {
				m.scr = scrProjects
			}
			return m, nil
		}
		switch s {
		case "esc":
			m.scr = scrProjects
			return m, nil
		case "enter":
			if m.snap.Server == "" {
				return m, nil
			}
			v := strings.TrimSpace(ag.input.Value())
			ag.input.SetValue("")
			if v == "" {
				ag.err = "Type the admin secret, or press esc to go back."
				return m, nil
			}
			ag.secret = v
			return m, m.loadAgents()
		}
		var cmd tea.Cmd
		ag.input, cmd = ag.input.Update(k)
		return m, cmd
	case agLoading:
		if s == "esc" {
			m.scr = scrProjects
		}
		return m, nil
	case agView:
		switch s {
		case "esc", "q", "backspace", "v":
			ag.phase = agGrid
		case "up", "k":
			ag.scroll = max(ag.scroll-1, 0)
		case "down", "j":
			ag.scroll++
		case "pgup":
			ag.scroll = max(ag.scroll-bodyHeight(m.h), 0)
		case "pgdown":
			ag.scroll += bodyHeight(m.h)
		}
		return m, nil
	case agDetail:
		a := ag.agent()
		switch s {
		case "esc", "q", "backspace", "d":
			ag.phase = agGrid
		case "up", "k":
			ag.tok = max(ag.tok-1, 0)
		case "down", "j":
			if a != nil {
				ag.tok = min(ag.tok+1, max(len(a.Tokens)-1, 0))
			}
		case "R", "x":
			if a != nil && ag.tok < len(a.Tokens) {
				t := a.Tokens[ag.tok]
				label := firstNonEmptyStr(t.Label, t.ID)
				m.modal = &modal{title: "Revoke token", lines: []string{"Revoke the token \"" + label + "\" of " + a.Name + "?", "The assistant using it loses access at once."},
					yes: func(m *Model) tea.Cmd {
						be, ctx, secret := m.be, m.ctx, m.ag.secret
						return func() tea.Msg { return agentTokenMsg{label, be.RevokeAgentToken(ctx, secret, t.ID)} }
					}}
			}
		case "v":
			return m, m.openAgentView()
		}
		return m, nil
	}
	switch s {
	case "esc", "q", "backspace":
		if n := len(ag.changes()); n > 0 {
			m.modal = &modal{title: "Discard changes", lines: []string{fmt.Sprintf("Leave without applying %s?", plural(n, "change"))},
				yes: func(m *Model) tea.Cmd {
					m.ag.pending = map[grantKey]bool{}
					m.scr = scrProjects
					return nil
				}}
			return m, nil
		}
		m.scr = scrProjects
	case "up", "k":
		ag.row--
	case "down", "j":
		ag.row++
	case "left", "h":
		ag.col--
	case "right", "l":
		ag.col++
	case " ", "x":
		if key, ok := ag.keyAt(ag.row, ag.col); ok {
			ag.toggle(key)
		}
	case "enter":
		m.review()
	case "u":
		if len(ag.pending) > 0 {
			ag.pending = map[grantKey]bool{}
			m.setStatus("changes cleared; nothing was applied", false)
		}
	case "v":
		return m, m.openAgentView()
	case "d":
		if ag.agent() != nil {
			ag.phase, ag.tok = agDetail, 0
		}
	case "r":
		return m, m.loadAgents()
	case "L":
		ag.secret, ag.pending = "", map[grantKey]bool{}
		m.setStatus("the admin secret was forgotten", false)
		return m, m.openAgents()
	}
	ag.clamp(m)
	return m, nil
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (m *Model) viewAgents() string {
	ag := &m.ag
	w, bh := m.w-2, bodyHeight(m.h)
	var out []string
	add := func(l string) { out = append(out, " "+l) }
	hint := ""
	switch ag.phase {
	case agAsk:
		if m.snap.Server == "" {
			add(m.th.Title.Render("No server configured"))
			add("")
			add("Agents live on your server. Connect one first: press " + m.th.Key.Render("3") + " then " + m.th.Key.Render("i") + ",")
			add("or run  goldengate init --server https://your-host")
			hint = m.hints(w, "esc", "back")
			break
		}
		add(m.th.Title.Render("Which assistant sees which project"))
		add("")
		add("Each assistant you connect is an agent with its own list of projects.")
		add("Changing that list needs your server's admin secret. It is kept in")
		add("memory until you quit and is never written anywhere.")
		add("")
		add(m.th.Title.Render("Admin secret"))
		add("> " + ag.input.View())
		if ag.err != "" {
			add("")
			add(m.th.Bad.Render(clip(ag.err, w-4)))
		}
		hint = m.hints(w, "enter", "continue", "esc", "back")
	case agLoading:
		add(m.th.Dim.Render("reading agents from the server…"))
		hint = m.hints(w, "esc", "back")
	case agView:
		out, hint = m.viewAsAgent(w, bh)
	case agDetail:
		out, hint = m.viewAgentDetail(w, bh)
	default:
		out, hint = m.viewAgentGrid(w, bh)
	}
	f := frame{th: m.th, w: m.w, h: m.h, title: m.header("agents"), right: m.serverBadge(), top: " " + strings.TrimSpace(m.sourcesLine())}
	f.left = lines(strings.Join(out, "\n"), w, bh)
	f.hint = m.statusLine(hint)
	return f.render()
}

// Grid geometry: a project column, then one fixed-width column per agent.
const agentColW = 16

func (m *Model) viewAgentGrid(w, bh int) ([]string, string) {
	ag := &m.ag
	var out []string
	if len(ag.info.Agents) == 0 {
		out = append(out, " "+m.th.Title.Render("No agents yet"), "",
			" An agent appears when you connect an assistant: `goldengate agents add <name>`,",
			" or approve one that connects through OAuth.")
		return out, m.hints(w, "r", "refresh", "esc", "back")
	}
	// how many agent columns fit, keeping the cursor's column visible
	nameW := max(min(w-2-agentColW*len(ag.info.Agents), 40), 18)
	fitCols := max((w-2-nameW)/agentColW, 1)
	colOff := 0
	if ag.col >= fitCols {
		colOff = ag.col - fitCols + 1
	}
	last := min(colOff+fitCols, len(ag.info.Agents))

	head := " " + fit("", nameW)
	for c := colOff; c < last; c++ {
		a := ag.info.Agents[c]
		label := fit(clip(a.Name, agentColW-2), agentColW-1)
		st := m.th.Label
		if c == ag.col {
			st = m.th.Accent
		}
		x0 := 2 + nameW + (c-colOff)*agentColW
		c := c
		m.addZone(x0, x0+agentColW-1, yBody, func(m *Model) tea.Cmd { m.ag.col = c; return nil })
		head += " " + st.Render(label)
	}
	if last < len(ag.info.Agents) || colOff > 0 {
		head += m.th.Dim.Render(fmt.Sprintf(" %d/%d", ag.col+1, len(ag.info.Agents)))
	}
	out = append(out, head)

	rows := ag.gridRows(m)
	total := len(ag.info.Projects) + 1
	for r := ag.rowOff; r < min(ag.rowOff+rows, total); r++ {
		y := yBody + len(out)
		name := "Every project, including future ones"
		if r != allRow {
			p := ag.info.Projects[r-1]
			name = p.Name
		}
		line := " " + fit(clip(name, nameW), nameW)
		for c := colOff; c < last; c++ {
			key, _ := ag.keyAt(r, c)
			on, changed := ag.cell(key)
			box := "[ ]"
			if on {
				box = "[x]"
			}
			// a project the agent sees only through "every project"
			if allOn, _ := ag.cell(grantKey{agent: key.agent}); r != allRow && !on && allOn {
				box = "[*]"
			}
			mark := " "
			st := m.th.Plain
			switch {
			case changed && on:
				mark, st = "+", m.th.Added
			case changed:
				mark, st = "-", m.th.Removed
			}
			cellText := box + mark
			if r == ag.row && c == ag.col {
				cellText = m.th.Cursor.Render(cellText)
			} else {
				cellText = st.Render(cellText)
			}
			x0 := 2 + nameW + (c-colOff)*agentColW
			r, c := r, c
			m.addZone(x0, x0+agentColW-1, y, func(m *Model) tea.Cmd {
				m.ag.row, m.ag.col = r, c
				if k, ok := m.ag.keyAt(r, c); ok {
					m.ag.toggle(k)
				}
				return nil
			})
			// the cursor's cell is marked without relying on color
			sep := " "
			if r == ag.row && c == ag.col {
				sep = m.th.Accent.Render("›")
			}
			line += sep + cellText + strings.Repeat(" ", agentColW-1-4)
		}
		if r == ag.row {
			line = m.th.Accent.Render("▌") + line[1:]
		}
		out = append(out, line)
	}
	if total > rows {
		out = append(out, m.th.Dim.Render(fmt.Sprintf("  rows %d–%d of %d", ag.rowOff+1, min(ag.rowOff+rows, total), total)))
	}
	out = append(out, "")
	if a := ag.agent(); a != nil {
		out = append(out, " "+m.th.Title.Render(a.Name)+m.th.Dim.Render("  "+agentSummary(*a, m)))
	}
	n := len(ag.changes())
	if n > 0 {
		y := yBody + len(out)
		b := btn{m.th.Button, "enter", "Review " + plural(n, "change")}
		m.clickKey(y, b.text(), "enter")
		out = append(out, " "+m.th.Changed.Render(plural(n, "change")+" not applied yet")+"  "+b.st.Render(b.text()))
	} else {
		out = append(out, " "+m.th.Dim.Render("[x] granted   [*] seen through \"every project\"   space toggles, enter reviews"))
	}
	return out, m.hints(w, "space", "toggle", "enter", "review", "v", "view as agent", "d", "details", "u", "clear", "esc", "back")
}

func agentSummary(a AgentInfo, m *Model) string {
	parts := []string{plural(len(a.Tokens), "token")}
	if len(a.Clients) > 0 {
		parts = append(parts, "OAuth: "+strings.Join(a.Clients, ", "))
	}
	if a.MaxDays > 0 {
		parts = append(parts, fmt.Sprintf("last %d days at most", a.MaxDays))
	}
	if a.LastUsed.IsZero() {
		parts = append(parts, "never used")
	} else {
		parts = append(parts, "used "+relTime(m.now(), a.LastUsed))
	}
	return strings.Join(parts, " · ")
}

func (m *Model) viewAgentDetail(w, bh int) ([]string, string) {
	ag := &m.ag
	a := ag.agent()
	var out []string
	add := func(l string) { out = append(out, " "+l) }
	if a == nil {
		return out, m.hints(w, "esc", "back")
	}
	add(m.th.Title.Render(a.Name) + m.th.Dim.Render("  "+a.ID))
	var sees []string
	for _, p := range ag.info.Projects {
		if a.Grants[p.ID] {
			sees = append(sees, p.Name)
		}
	}
	switch {
	case a.AllProjects:
		add("Sees: every project, including ones you share later")
	case len(sees) == 0:
		add("Sees: nothing (no projects granted)")
	default:
		add(clip("Sees: "+strings.Join(sees, ", "), w-2))
	}
	if a.MaxDays > 0 {
		add(fmt.Sprintf("History: the last %d days at most", a.MaxDays))
	} else {
		add("History: no limit of its own")
	}
	if a.LastUsed.IsZero() {
		add("Last used: never")
	} else {
		add("Last used: " + relTime(m.now(), a.LastUsed))
	}
	if len(a.Clients) > 0 {
		add(clip("OAuth applications: "+strings.Join(a.Clients, ", "), w-2))
	}
	add("")
	add(m.th.Title.Render(fmt.Sprintf("Read tokens (%d)", len(a.Tokens))))
	if len(a.Tokens) == 0 {
		add(m.th.Dim.Render("none: create one with `goldengate agents token " + a.Name + "`"))
	}
	rev := btn{m.th.ButtonBad, "R", "Revoke"}
	for i, t := range a.Tokens {
		if len(out) >= bh-1 {
			break
		}
		y := yBody + len(out)
		used := "never used"
		if !t.LastUsed.IsZero() {
			used = "used " + relTime(m.now(), t.LastUsed)
		}
		line := fmt.Sprintf("  %s  %s  created %s  %s", fit(clip(firstNonEmptyStr(t.Label, "(no label)"), 24), 24), fit(t.ID, 12), relTime(m.now(), t.Created), used)
		i := i
		m.addZone(1, 1+w, y, func(m *Model) tea.Cmd { m.ag.tok = i; return nil })
		if i == ag.tok {
			m.clickText(y, rev.text(), func(m *Model) tea.Cmd { m.ag.tok = i; return m.press("R") })
			line = m.th.Cursor.Render(fit(m.th.Accent.Render("▌")+line[1:]+" "+rev.st.Render(rev.text()), w-1))
		}
		out = append(out, line)
	}
	return out, m.hints(w, "↑/↓", "select", "R", "revoke token", "v", "view as agent", "esc", "back")
}

func (m *Model) viewAsAgent(w, bh int) ([]string, string) {
	ag := &m.ag
	v := ag.view
	var all []string
	add := func(l string) { all = append(all, " "+l) }
	add(m.th.Title.Render("What " + v.Agent.Name + " can read right now"))
	switch {
	case ag.viewErr != "":
		add(m.th.Bad.Render(clip(ag.viewErr, w-4)))
	case v.Note == "" && len(v.Projects) == 0 && len(v.Sessions) == 0:
		add(m.th.Dim.Render("reading…"))
	default:
		if v.Note != "" {
			add(m.th.Dim.Render(clip(v.Note, w-4)))
		}
		add("")
		add(m.th.Title.Render(fmt.Sprintf("Projects (%d)", len(v.Projects))))
		if len(v.Projects) == 0 {
			add(m.th.Dim.Render("none: this assistant sees nothing"))
		}
		for _, p := range v.Projects {
			add(fmt.Sprintf("  %s  %s", fit(clip(p.Name, 40), 40), plural(p.Sessions, "session")))
		}
		add("")
		add(m.th.Title.Render(fmt.Sprintf("Summaries from the last %d days (%d)", AgentViewDays, len(v.Sessions))))
		if len(v.Sessions) == 0 {
			add(m.th.Dim.Render("none"))
		}
		for _, s := range v.Sessions {
			add(clip(fmt.Sprintf("  %s %s  %s  %s", m.tag(s.Source), fit(clip(s.Project, 18), 18), relTime(m.now(), s.Updated), m.highlight(s.Title)), w-2))
			if s.Abstract != "" {
				add(m.th.Dim.Render(clip("      "+s.Abstract, w-4)))
			}
		}
		add("")
		add(m.th.Title.Render(fmt.Sprintf("Work items from the last %d days (%d)", AgentViewDays, len(v.Work))))
		if len(v.Work) == 0 {
			add(m.th.Dim.Render("none"))
		}
		for _, wi := range v.Work {
			add(clip(fmt.Sprintf("  %s %s [%s]  %s", wi.Kind, m.highlight(wi.Key), wi.Status, wi.Project), w-2))
		}
	}
	ag.scroll = max(min(ag.scroll, len(all)-bh), 0)
	return all[ag.scroll:], m.hints(w, "↑/↓", "scroll", "esc", "back")
}
