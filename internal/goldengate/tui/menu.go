package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/llm"
)

// Dropdowns on the Settings screen. One opens under its row, lists its
// choices with a check on the current one, and is driven by ↑/↓ + enter, the
// wheel, or a click; esc or a click outside closes it without a change.

type menuKind int

const (
	menuNone menuKind = iota
	menuTheme
	menuSummarizer
	menuModel
)

// Special ids in the model menu.
const (
	itemTypeModel = "\x00type"
	itemNone      = "\x00none" // a line that is information, not a choice
)

type menuItem struct {
	id, title, detail string
	swatch            *Palette // themes only
	sepAfter          bool     // a divider follows this item
}

// heuristicTitle is the summarizer menu's first choice.
const heuristicTitle = "Local heuristic"

func (m *Model) menuItems(k menuKind) []menuItem {
	var out []menuItem
	switch k {
	case menuTheme:
		for i, n := range ThemeNames() {
			p := Presets[n]
			mode := "light"
			if p.Dark {
				mode = "dark"
			}
			out = append(out, menuItem{id: n, title: ThemeTitle(n), detail: mode, swatch: &p, sepAfter: i == 1})
		}
	case menuSummarizer:
		out = append(out, menuItem{id: "", title: heuristicTitle, detail: "local rules · nothing is sent", sepAfter: true})
		for _, p := range llm.Providers {
			d := p.Detail
			if p.ID == llm.ProviderOllama && m.st.ollama != "" {
				d = m.st.ollama
			}
			out = append(out, menuItem{id: p.ID, title: p.Title, detail: d})
		}
	case menuModel:
		switch {
		case m.st.modelsLoading:
			out = append(out, menuItem{id: itemNone, title: "loading models…"})
		case m.st.modelsErr != "":
			out = append(out, menuItem{id: itemNone, title: "couldn't list models", detail: clip(m.st.modelsErr, 60)})
		case len(m.st.models) == 0:
			hint := "none offered"
			if m.st.s.Summarize.Provider == llm.ProviderOllama {
				hint = "none installed · run: ollama pull llama3.2"
			}
			out = append(out, menuItem{id: itemNone, title: "no models", detail: hint})
		}
		for _, id := range m.st.models {
			out = append(out, menuItem{id: id, title: id})
		}
		if len(out) > 0 {
			out[len(out)-1].sepAfter = true
		}
		out = append(out, menuItem{id: itemTypeModel, title: "✎ Type a model name…"})
	}
	return out
}

type modelsMsg struct {
	ollama bool // a probe for the summarizer menu, not the model list
	list   []string
	err    error
}

// probeOllama checks whether Ollama runs here, for the summarizer menu.
func (m *Model) probeOllama() tea.Cmd {
	m.st.ollama = "checking whether Ollama is running…"
	be, ctx := m.be, m.ctx
	url := ""
	if m.st.s.Summarize.Provider == llm.ProviderOllama {
		url = m.st.s.Summarize.BaseURL
	}
	return func() tea.Msg {
		l, err := be.SummarizerModels(ctx, llm.ProviderOllama, url)
		return modelsMsg{ollama: true, list: l, err: err}
	}
}

// openModelMenu opens the model picker and fetches what the provider offers.
func (m *Model) openModelMenu() tea.Cmd {
	m.st.models, m.st.modelsErr, m.st.modelsLoading = nil, "", true
	m.openMenu(menuModel)
	be, ctx := m.be, m.ctx
	p, url := m.st.s.Summarize.Provider, m.st.s.Summarize.BaseURL
	return func() tea.Msg {
		l, err := be.SummarizerModels(ctx, p, url)
		return modelsMsg{list: l, err: err}
	}
}

func (m *Model) onModels(msg modelsMsg) {
	if msg.ollama {
		switch {
		case msg.err != nil:
			m.st.ollama = "not running here · install from ollama.com"
		case len(msg.list) == 0:
			m.st.ollama = "running · no models yet (ollama pull llama3.2)"
		default:
			m.st.ollama = fmt.Sprintf("running here · %s installed", plural(len(msg.list), "model"))
		}
		return
	}
	m.st.modelsLoading = false
	if msg.err != nil {
		m.st.modelsErr = msg.err.Error()
	}
	m.st.models = msg.list
	if m.st.dd == menuModel { // land on the current model, else the first one
		m.st.ddCursor = 0
		for i, it := range m.menuItems(menuModel) {
			if it.id == m.st.ddOrig {
				m.st.ddCursor = i
			}
		}
	}
}

// menuRowKind is the settings row a menu belongs to.
func menuRowKind(k menuKind) settingKind {
	switch k {
	case menuSummarizer:
		return setSummarizer
	case menuModel:
		return setSumModel
	}
	return setTheme
}

func (m *Model) openMenu(k menuKind) {
	st := &m.st
	st.dd, st.ddCursor = k, 0
	st.ddOrig = themeName(st.s.Theme)
	switch k {
	case menuSummarizer:
		st.ddOrig = st.s.Summarize.Provider
	case menuModel:
		st.ddOrig = st.s.Summarize.Model
		if st.ddOrig == "" {
			if p, ok := llm.Lookup(st.s.Summarize.Provider); ok {
				st.ddOrig = p.Model
			}
		}
	}
	for i, it := range m.menuItems(k) {
		if it.id == st.ddOrig {
			st.ddCursor = i
		}
	}
}

// moveMenu moves the highlight; the theme menu previews as it moves.
func (m *Model) moveMenu(i int) {
	items := m.menuItems(m.st.dd)
	m.st.ddCursor = min(max(i, 0), len(items)-1)
	if m.st.dd == menuTheme {
		m.applyTheme(items[m.st.ddCursor].id)
	}
}

// closeMenu keeps the highlighted choice or reverts.
func (m *Model) closeMenu(keep bool) tea.Cmd {
	st := &m.st
	k := st.dd
	items := m.menuItems(k)
	st.dd = menuNone
	if k == menuTheme {
		if !keep {
			m.applyTheme(st.ddOrig)
			return nil
		}
		name := items[st.ddCursor].id
		m.applyTheme(name)
		if name == themeName(st.s.Theme) && st.s.Theme != "" {
			return nil
		}
		s := st.s
		s.Theme = name
		st.s = s
		return m.save(s)
	}
	if !keep {
		return nil
	}
	id := items[st.ddCursor].id
	if k == menuModel {
		switch id {
		case itemNone:
			return nil
		case itemTypeModel:
			st.editing, st.kind = true, setSumModel
			st.input.SetValue(st.s.Summarize.Model)
			st.input.CursorEnd()
			return st.input.Focus()
		}
		s := st.s
		s.Summarize.Model = id
		st.s, st.test = s, ""
		return m.save(s)
	}
	return m.chooseSummarizer(id)
}

func (m *Model) onKeyMenu(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.st.ddCursor
	switch k.String() {
	case "up", "k":
		m.moveMenu(c - 1)
	case "down", "j":
		m.moveMenu(c + 1)
	case "home", "g":
		m.moveMenu(0)
	case "end", "G":
		m.moveMenu(len(m.menuItems(m.st.dd)) - 1)
	case "enter", " ":
		return m, m.closeMenu(true)
	case "esc", "q", "backspace":
		return m, m.closeMenu(false)
	}
	return m, nil
}

// menuBox renders the open dropdown with its top-left corner at (x, y) and
// registers a click target per item.
func (m *Model) menuBox(x, y int) []string {
	k := m.st.dd
	items := m.menuItems(k)
	tw, dw := 0, 0
	for _, it := range items {
		tw = max(tw, lipgloss.Width(it.title))
		dw = max(dw, lipgloss.Width(it.detail))
	}
	dw = min(dw, max(m.w-x-tw-14, 10))
	width := 4 + tw + 1 + dw
	if k == menuTheme {
		width += 7
	}
	// the menu's lines: items and dividers (item index -1)
	type entry struct{ item int }
	var entries []entry
	for i, it := range items {
		entries = append(entries, entry{i})
		if it.sepAfter && i < len(items)-1 {
			entries = append(entries, entry{-1})
		}
	}
	// a long menu shows a window of it that follows the highlight
	rows := m.menuRows()
	curEntry := 0
	for e, en := range entries {
		if en.item == m.st.ddCursor {
			curEntry = e
		}
	}
	top := 0
	if len(entries) > rows+2 { // scrolling for one or two lines is not worth it
		top = min(max(curEntry-rows/2, 0), len(entries)-rows)
	} else {
		rows = len(entries)
	}
	var ls []string
	more := func(n int, arrow string) string {
		return m.th.Dim.Render(fit("    "+arrow+" "+fmt.Sprintf("%d more", n), width))
	}
	if top > 0 {
		ls = append(ls, more(top, "▲"))
	}
	for e := top; e < top+rows; e++ {
		i := entries[e].item
		if i < 0 {
			ls = append(ls, m.th.Border.Render(strings.Repeat("─", width)))
			continue
		}
		it := items[i]
		cur := i == m.st.ddCursor
		paint := func(st lipgloss.Style, t string) string {
			if cur {
				st = st.Background(m.th.CurBg)
			}
			return st.Render(t)
		}
		bar, mark := paint(m.th.Plain, " "), paint(m.th.Plain, "  ")
		if cur {
			bar = paint(m.th.Accent, "▌")
		}
		if it.id == m.st.ddOrig {
			mark = paint(m.th.Good, "✓ ")
		}
		titleSt := m.th.Plain
		if cur {
			titleSt = m.th.Title
		}
		line := bar + mark + paint(titleSt, fit(it.title, tw)+" ")
		if it.swatch != nil {
			for _, c := range []string{it.swatch.Brand, it.swatch.Accent, it.swatch.Good, it.swatch.Warn, it.swatch.Bad, it.swatch.Chat} {
				line += paint(m.rendStyle().Foreground(lipgloss.Color(c)), "■")
			}
			line += paint(m.th.Plain, " ")
		}
		line += paint(m.th.Dim, fit(it.detail, dw))
		ls = append(ls, line)
	}
	if rest := len(entries) - top - rows; rest > 0 {
		ls = append(ls, more(rest, "▼"))
	}
	title := map[menuKind]string{menuTheme: "Theme", menuSummarizer: "Summarizer", menuModel: "Model"}[k]
	b := box(m.th, title, ls)
	// click targets for the visible items; dividers and the "more" lines are not
	row := 1 // below the top border
	if top > 0 {
		row++
	}
	for e := top; e < top+rows; e++ {
		if i := entries[e].item; i >= 0 {
			idx := i
			m.addZone(x+1, x+lipgloss.Width(b[0])-1, y+row, func(m *Model) tea.Cmd {
				m.moveMenu(idx)
				return m.closeMenu(true)
			})
		}
		row++
	}
	return b
}

// menuRows is how many lines a dropdown shows before it scrolls.
func (m *Model) menuRows() int { return max(min(14, m.h-10), 3) }

// menuHeight is the dropdown's height on screen, borders included.
func (m *Model) menuHeight() int {
	n := 0
	items := m.menuItems(m.st.dd)
	for i, it := range items {
		n++
		if it.sepAfter && i < len(items)-1 {
			n++
		}
	}
	if n > m.menuRows()+2 {
		return m.menuRows() + 2 + 2 // the window, two "more" lines, the borders
	}
	return n + 2
}

// --- summarizer ---

func summarizerName(id string) string {
	if p, ok := llm.Lookup(id); ok {
		return p.Title
	}
	return heuristicTitle
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func keyNote(sm SummarizeSettings, p llm.Provider) string {
	switch {
	case sm.KeySource != "" && sm.KeySource != "none":
		return "✓ " + sm.KeySource + " · enter to replace"
	case p.KeyOptional:
		return "none (fine for a local server) · enter to set"
	}
	return "missing · enter to paste your key"
}

// chooseSummarizer switches to id. Choosing a model asks first, since it
// changes what leaves the machine; the heuristic needs no question.
func (m *Model) chooseSummarizer(id string) tea.Cmd {
	if id == m.st.s.Summarize.Provider {
		return nil
	}
	s := m.st.s
	if id == "" {
		s.Summarize = SummarizeSettings{}
		m.st.test = ""
		return m.save(s)
	}
	p, _ := llm.Lookup(id)
	s.Summarize = SummarizeSettings{Provider: id} // that provider's default model and endpoint
	where := "It runs on that provider's servers under its terms."
	switch id {
	case llm.ProviderOllama:
		where = "Ollama runs on this machine: nothing leaves it."
	case llm.ProviderCustom:
		where = "With a server on this machine (LM Studio) nothing leaves it."
	}
	m.modal = &modal{title: "Summarize with " + p.Title + "?", lines: []string{
		"Sessions of the projects you share will be summarized by a model.",
		"It receives each session's redacted conversation: your messages and",
		"the assistant's replies, after secret and path redaction.",
		"Tool output and file contents are never sent, and projects you",
		"don't share never reach it. Previews show only summaries already made.",
		where,
	}, yes: func(m *Model) tea.Cmd {
		m.st.test, m.st.focusKey = "", true
		return m.save(s)
	}}
	return nil
}

// startKeyEdit opens the hidden input for the provider's API key.
func (m *Model) startKeyEdit() tea.Cmd {
	st := &m.st
	st.editing, st.kind = true, setSumKey
	st.input.SetValue("")
	st.input.EchoMode = textinput.EchoPassword
	st.input.EchoCharacter = '•'
	return st.input.Focus()
}

type (
	keySavedMsg struct{ err error }
	testDoneMsg struct {
		res string
		err error
	}
)

func (m *Model) saveKey(provider, key string) tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg { return keySavedMsg{be.SetSummarizerKey(ctx, provider, key)} }
}

func (m *Model) onKeySaved(msg keySavedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("the key was not saved: "+msg.err.Error(), true)
		return nil
	}
	m.setStatus("key saved in the OS keyring", false)
	return m.openSettingsKeepScreen()
}

func (m *Model) runSummarizerTest() tea.Cmd {
	m.st.test = "testing…"
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		res, err := be.TestSummarizer(ctx)
		return testDoneMsg{res, err}
	}
}

func (m *Model) onTestDone(msg testDoneMsg) {
	if msg.err != nil {
		m.st.test = "✗ " + msg.err.Error()
		return
	}
	m.st.test = "✓ " + msg.res
}
