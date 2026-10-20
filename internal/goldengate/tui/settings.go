package tui

import (
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/llm"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type settingKind int

const (
	setLevel settingKind = iota
	setSince
	setSource
	setPattern
	setAddPattern
	setGit
	setNote
	setTheme
	setSummarizer
	setSumModel
	setSumURL
	setSumKey
	setSumTest
)

type settingRow struct {
	kind  settingKind
	label string
	value string
	idx   int
}

type settingsState struct {
	s       Settings
	loaded  bool
	cursor  int
	editing bool
	kind    settingKind
	input   textinput.Model
	err     string

	dd       menuKind // which dropdown is open
	ddCursor int      // highlighted item in it
	ddOrig   string   // the value before it opened (esc returns to it)

	test     string // the summarizer test's last result
	focusKey bool   // after choosing a provider, go to its API key

	ollama        string   // Ollama's state for the menu: "", "checking…", "running · 3 models", …
	models        []string // models the current provider offers
	modelsErr     string
	modelsLoading bool
}

func newSettingsState(blink bool) settingsState {
	return settingsState{input: newInput("", false, blink)}
}

func (m *Model) openSettings() tea.Cmd {
	m.scr = scrSettings
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		s, err := be.Settings(ctx)
		return settingsMsg{s, err}
	}
}

func (m *Model) onSettings(msg settingsMsg) tea.Cmd {
	if msg.err != nil {
		m.st.err = msg.err.Error()
		return nil
	}
	m.st.s, m.st.loaded, m.st.err = msg.s, true, ""
	if m.st.focusKey { // a provider was just chosen: its key, or for a local one its model, comes next
		m.st.focusKey = false
		next := setSumKey
		if p, ok := llm.Lookup(m.st.s.Summarize.Provider); ok && p.KeyOptional {
			next = setSumModel
		}
		for i, r := range m.settingRows() {
			if r.kind == next {
				m.st.cursor = i
			}
		}
		if next == setSumModel {
			return m.openModelMenu() // pick from what is installed
		}
	}
	m.st.cursor = max(min(m.st.cursor, len(m.settingRows())-1), 0)
	return nil
}

func (m *Model) onSaved(msg savedMsg) tea.Cmd {
	if msg.err != nil {
		m.st.err = msg.err.Error()
		m.setStatus("not saved: "+msg.err.Error(), true)
	} else {
		m.st.err = ""
		m.setStatus("saved", false)
	}
	return tea.Batch(m.openSettingsKeepScreen(), m.loadSnapshot())
}

func (m *Model) openSettingsKeepScreen() tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		s, err := be.Settings(ctx)
		return settingsMsg{s, err}
	}
}

func (m *Model) settingRows() []settingRow {
	s := m.st.s
	rows := []settingRow{
		{kind: setTheme, label: "Theme", value: themeName(s.Theme)},
		{kind: setLevel, label: "Share level", value: s.Level},
		{kind: setSince, label: "Look back", value: orNone(s.Since)},
		{kind: setSummarizer, label: "Summarizer", value: s.Summarize.Provider},
	}
	if p, ok := llm.Lookup(s.Summarize.Provider); ok {
		sm := s.Summarize
		def := "(choose one)"
		if p.Model != "" {
			def = p.Model + " (default)"
		}
		rows = append(rows, settingRow{kind: setSumModel, label: "  Model", value: orDefault(sm.Model, def) + " ▾"})
		if p.BaseURL != "" {
			rows = append(rows, settingRow{kind: setSumURL, label: "  Endpoint", value: orDefault(sm.BaseURL, p.BaseURL+" (default)")})
		}
		rows = append(rows,
			settingRow{kind: setSumKey, label: "  API key", value: keyNote(sm, p)},
			settingRow{kind: setSumTest, label: "  Test", value: m.st.test})
	}
	for i, src := range s.Sources {
		v := "off"
		if src.Enabled {
			v = "on"
		}
		if !src.Installed {
			v += " (not found)"
		}
		rows = append(rows, settingRow{kind: setSource, label: "Source " + metaOf(src.Name).tag + " " + pretty(src.Name), value: v, idx: i})
	}
	for i, p := range s.ExtraPatterns {
		rows = append(rows, settingRow{kind: setPattern, label: "Redact pattern", value: p, idx: i})
	}
	rows = append(rows, settingRow{kind: setAddPattern, label: "+ add a redaction pattern (regex)"})
	git := "off"
	if s.EnrichGit {
		git = "on"
	}
	rows = append(rows,
		settingRow{kind: setGit, label: "Verify commits with local git", value: git},
		settingRow{kind: setNote, label: "Session hook / schedule", value: "not available yet (coming with deployment support)"},
	)
	return rows
}

func themeName(s string) string {
	n, _ := ResolveTheme(s)
	return n
}

// swatch shows a palette's main colors.
func (m *Model) swatch(p Palette) string {
	sw := ""
	for _, c := range []string{p.Brand, p.Accent, p.Good, p.Warn, p.Bad, p.Chat} {
		sw += m.rendStyle().Foreground(lipgloss.Color(c)).Render("■")
	}
	return sw
}

func orNone(s string) string {
	if s == "" {
		return "no limit"
	}
	return s
}

func (m *Model) save(s Settings) tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg { return savedMsg{be.SaveSettings(ctx, s)} }
}

func (m *Model) onKeySettings(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &m.st
	rows := m.settingRows()
	if st.dd != menuNone {
		return m.onKeyMenu(k)
	}
	if st.editing {
		switch k.String() {
		case "esc":
			st.editing = false
			st.input.EchoMode = textinput.EchoNormal
			return m, nil
		case "enter":
			v := strings.TrimSpace(st.input.Value())
			st.editing = false
			if st.kind == setSumKey { // goes to the keyring, never into the config
				st.input.EchoMode = textinput.EchoNormal
				st.input.SetValue("")
				return m, m.saveKey(st.s.Summarize.Provider, v)
			}
			s := st.s
			switch st.kind {
			case setSince:
				s.Since = v
			case setSumModel:
				s.Summarize.Model = v
			case setSumURL:
				s.Summarize.BaseURL = v
			case setAddPattern:
				if v == "" {
					return m, nil
				}
				s.ExtraPatterns = append(slices.Clone(s.ExtraPatterns), v)
			}
			return m, m.save(s)
		}
		var cmd tea.Cmd
		st.input, cmd = st.input.Update(k)
		return m, cmd
	}
	row := rows[st.cursor]
	switch k.String() {
	case "esc", "q", "backspace":
		m.scr = scrProjects
	case "up", "k":
		st.cursor = max(st.cursor-1, 0)
	case "down", "j":
		st.cursor = min(st.cursor+1, len(rows)-1)
	case "enter", " ", "x":
		s := st.s
		switch row.kind {
		case setTheme:
			m.openMenu(menuTheme)
			return m, nil
		case setSummarizer:
			m.openMenu(menuSummarizer)
			return m, m.probeOllama()
		case setSumKey:
			return m, m.startKeyEdit()
		case setSumTest:
			if st.test != "testing…" {
				return m, m.runSummarizerTest()
			}
			return m, nil
		case setSumModel:
			return m, m.openModelMenu()
		case setSumURL:
			st.editing, st.kind = true, row.kind
			v := s.Summarize.Model
			if row.kind == setSumURL {
				v = s.Summarize.BaseURL
			}
			st.input.SetValue(v)
			st.input.CursorEnd()
			return m, st.input.Focus()
		case setLevel:
			if s.Level == "summary" {
				s.Level = "titles-only"
			} else {
				s.Level = "summary"
			}
			return m, m.save(s)
		case setSource:
			s.Sources = slices.Clone(s.Sources)
			s.Sources[row.idx].Enabled = !s.Sources[row.idx].Enabled
			return m, m.save(s)
		case setGit:
			s.EnrichGit = !s.EnrichGit
			return m, m.save(s)
		case setPattern:
			if k.String() == "x" || k.String() == "enter" {
				s.ExtraPatterns = slices.Delete(slices.Clone(s.ExtraPatterns), row.idx, row.idx+1)
				return m, m.save(s)
			}
		case setSince, setAddPattern:
			st.editing, st.kind = true, row.kind
			st.input.SetValue("")
			if row.kind == setSince {
				st.input.SetValue(s.Since)
			}
			return m, st.input.Focus()
		}
	}
	return m, nil
}

// settingValueX is the screen column where setting values start.
const settingValueX = 37

func (m *Model) settingLine(r settingRow, cur bool, w int) string {
	label := " " + fit(clip(r.label, 34), 34) + " "
	switch r.kind {
	case setNote:
		return " " + m.th.Dim.Render(strings.TrimPrefix(label, " ")+r.value)
	case setAddPattern:
		return " " + m.th.Accent.Render(r.label)
	case setSource:
		src := m.st.s.Sources[r.idx]
		md := metaOf(src.Name)
		label = " " + fit("Source "+m.kindStyle(md.kind).Render(md.tag)+" "+pretty(src.Name), 34) + " "
		v := m.toggle(src.Enabled) + m.th.Dim.Render("  "+md.kind.String())
		if !src.Installed {
			v += m.th.Dim.Render(" · not found on this computer")
		}
		return label + v
	case setGit:
		return label + m.toggle(m.st.s.EnrichGit)
	case setSummarizer:
		name := r.value
		if m.st.dd == menuSummarizer {
			name = m.st.ddOrig
		}
		v := m.th.Chip.Render(" " + fit(summarizerName(name), 26) + " ▾ ")
		if name == "" {
			return label + v + m.th.Dim.Render("  nothing is sent to a model")
		}
		return label + v + m.th.Warn.Render("  redacted chats are sent")
	case setSumKey:
		st := m.th.Good
		if !strings.HasPrefix(r.value, "✓") {
			st = m.th.Warn
		}
		return label + st.Render(r.value)
	case setSumTest:
		switch {
		case strings.HasPrefix(r.value, "✓"):
			return label + m.th.Good.Render(r.value)
		case strings.HasPrefix(r.value, "✗"):
			return label + m.th.Bad.Render(r.value)
		case r.value == "testing…":
			return label + m.th.Dim.Render(r.value)
		}
		return label + m.th.Button.Render("[enter Test connection]") + m.th.Dim.Render("  one tiny sample, not your data")
	case setTheme:
		name := r.value
		if m.st.dd == menuTheme {
			name = m.st.ddOrig
		}
		return label + m.th.Chip.Render(" "+fit(ThemeTitle(name), 18)+" ▾ ") + " " + m.swatch(Presets[name])
	case setLevel:
		var parts []string
		for _, l := range []string{"summary", "titles-only"} {
			if l == r.value {
				parts = append(parts, m.th.ChipOn.Render("["+l+"]"))
			} else {
				parts = append(parts, m.th.Chip.Render(" "+l+" "))
			}
		}
		return label + strings.Join(parts, " ")
	case setPattern:
		y := yBody
		for i, rr := range m.settingRows() {
			if rr.kind == r.kind && rr.idx == r.idx {
				y += i
			}
		}
		del := btn{m.th.ButtonBad, "x", "Delete"}
		m.clickText(y, del.text(), func(m *Model) tea.Cmd {
			for i, rr := range m.settingRows() {
				if rr.kind == setPattern && rr.idx == r.idx {
					m.st.cursor = i
				}
			}
			return m.press("x")
		})
		v := clip(r.value, max(w-lipgloss.Width(label)-len(del.text())-3, 8))
		return label + m.th.Accent.Render(v) + "  " + del.st.Render(del.text())
	}
	return label + r.value
}

// toggle renders an on/off value.
func (m *Model) toggle(on bool) string {
	if on {
		return m.th.Good.Bold(true).Render("● on ")
	}
	return m.th.Dim.Render("○ off")
}

func (m *Model) viewSettings() string {
	st := &m.st
	w, bh := m.w-2, bodyHeight(m.h)
	var out []string
	if !st.loaded {
		out = append(out, " "+m.th.Dim.Render("loading…"))
	}
	for i, r := range m.settingRows() {
		if !st.loaded {
			break
		}
		cur := i == st.cursor && !st.editing
		y := yBody + len(out)
		if !st.editing && st.dd == menuNone && r.kind != setNote {
			i, r := i, r
			m.addZone(1, 1+w, y, func(m *Model) tea.Cmd {
				again := m.st.cursor == i
				m.st.cursor = i
				// the value column (or a second click) changes the setting;
				// patterns are only deleted through their button
				if r.kind != setPattern && (again || m.ck.msg.X >= settingValueX) {
					return m.press("enter")
				}
				return nil
			})
		}
		line := m.settingLine(r, cur, w)
		if cur {
			line = m.th.Cursor.Render(fit(m.th.Accent.Render("▌")+strings.TrimPrefix(line, " "), w))
		}
		out = append(out, line)
	}
	if st.editing {
		var label string
		switch st.kind {
		case setSince:
			label = "Look back (e.g. 30d, 12h, 2w; empty = no limit)"
		case setSumModel:
			label = "Model id (empty = the provider's default)"
		case setSumURL:
			label = "Endpoint, OpenAI-compatible (empty = the provider's default)"
		case setSumKey:
			label = "API key for " + summarizerName(st.s.Summarize.Provider) + " (saved in the OS keyring; empty removes the stored key)"
		default:
			label = "Redaction regex (matched text is replaced before anything leaves)"
		}
		out = append(out, "", " "+m.th.Title.Render(label), " > "+st.input.View())
	}
	if st.err != "" {
		out = append(out, "", " "+m.th.Bad.Render(clip(st.err, w-4)))
	}
	f := frame{th: m.th, w: m.w, h: m.h, title: m.header("settings"), right: m.serverBadge(), top: " Changes are saved immediately"}
	f.left = lines(strings.Join(out, "\n"), w, bh)
	switch {
	case st.dd == menuTheme:
		f.hint = m.hints(w, "↑/↓", "preview", "enter", "choose", "esc", "cancel")
	case st.dd != menuNone:
		f.hint = m.hints(w, "↑/↓", "move", "enter", "choose", "esc", "cancel")
	case st.editing:
		f.hint = m.hints(w, "enter", "save", "esc", "cancel")
	default:
		f.hint = m.statusLine(m.hints(w, "↑/↓", "select", "enter/space", "change", "x", "delete pattern", "esc", "back"))
	}
	out2 := f.render()
	if st.dd != menuNone {
		y := yBody
		for i, r := range m.settingRows() {
			if r.kind == menuRowKind(st.dd) {
				y += i
			}
		}
		x := settingValueX - 1
		top := max(min(y+1, m.h-1-m.menuHeight()), 1) // below the row, but on screen
		out2 = overlayAt(out2, m.menuBox(x, top), x, top)
	}
	return out2
}
