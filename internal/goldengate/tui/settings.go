package tui

import (
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

	dd       bool   // the theme dropdown is open
	ddCursor int    // highlighted theme in it
	ddOrig   string // the theme before the dropdown opened (esc returns to it)
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

func (m *Model) onSettings(msg settingsMsg) {
	if msg.err != nil {
		m.st.err = msg.err.Error()
		return
	}
	m.st.s, m.st.loaded, m.st.err = msg.s, true, ""
	m.st.cursor = max(min(m.st.cursor, len(m.settingRows())-1), 0)
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

// openThemeMenu opens the theme dropdown on the current theme.
func (m *Model) openThemeMenu() {
	st := &m.st
	st.dd, st.ddOrig = true, themeName(st.s.Theme)
	for i, n := range ThemeNames() {
		if n == st.ddOrig {
			st.ddCursor = i
		}
	}
}

// moveThemeMenu moves the dropdown cursor and previews that theme.
func (m *Model) moveThemeMenu(i int) {
	names := ThemeNames()
	m.st.ddCursor = min(max(i, 0), len(names)-1)
	m.applyTheme(names[m.st.ddCursor])
}

// closeThemeMenu keeps the highlighted theme (and saves it) or reverts.
func (m *Model) closeThemeMenu(keep bool) tea.Cmd {
	st := &m.st
	st.dd = false
	if !keep {
		m.applyTheme(st.ddOrig)
		return nil
	}
	name := ThemeNames()[st.ddCursor]
	m.applyTheme(name)
	if name == themeName(st.s.Theme) && st.s.Theme != "" {
		return nil
	}
	s := st.s
	s.Theme = name
	st.s = s
	return m.save(s)
}

func (m *Model) onKeyThemeMenu(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.st.ddCursor
	switch k.String() {
	case "up", "k":
		m.moveThemeMenu(c - 1)
	case "down", "j":
		m.moveThemeMenu(c + 1)
	case "home", "g":
		m.moveThemeMenu(0)
	case "end", "G":
		m.moveThemeMenu(len(ThemeNames()) - 1)
	case "enter", " ":
		return m, m.closeThemeMenu(true)
	case "esc", "q", "backspace":
		return m, m.closeThemeMenu(false)
	}
	return m, nil
}

// swatch shows a palette's main colors.
func (m *Model) swatch(p Palette) string {
	sw := ""
	for _, c := range []string{p.Brand, p.Accent, p.Good, p.Warn, p.Bad, p.Chat} {
		sw += m.rendStyle().Foreground(lipgloss.Color(c)).Render("■")
	}
	return sw
}

// themeMenu is the dropdown box, drawn under the Theme row at (x, y).
func (m *Model) themeMenu(x, y int) []string {
	names := ThemeNames()
	var ls []string
	for i, n := range names {
		p := Presets[n]
		mode := "light"
		if p.Dark {
			mode = "dark "
		}
		mark := "  "
		if n == m.st.ddOrig {
			mark = m.th.Good.Render("✓ ")
		}
		line := " " + mark + fit(ThemeTitle(n), 18) + " " + m.swatch(p) + " " + m.th.Dim.Render(mode)
		if i == m.st.ddCursor {
			// every segment carries the highlight, since an inner reset would end it
			hl := func(st lipgloss.Style, t string) string { return st.Background(m.th.CurBg).Render(t) }
			mk := hl(m.th.Plain, "  ")
			if n == m.st.ddOrig {
				mk = hl(m.th.Good, "✓ ")
			}
			sw := ""
			for _, c := range []string{p.Brand, p.Accent, p.Good, p.Warn, p.Bad, p.Chat} {
				sw += hl(m.rendStyle().Foreground(lipgloss.Color(c)), "■")
			}
			line = hl(m.th.Accent, "▌") + mk + hl(m.th.Title, fit(ThemeTitle(n), 18)+" ") + sw + hl(m.th.Plain, " ") + hl(m.th.Dim, mode)
		}
		ls = append(ls, line)
		if i == 1 && len(names) > 2 { // Golden Gate's pair, then the rest
			ls = append(ls, m.th.Border.Render(strings.Repeat("─", 34)))
		}
	}
	b := box(m.th, "Theme", ls)
	// each item is a click target; the separator is not
	row := 0
	for i := range names {
		idx := i
		m.addZone(x+1, x+lipgloss.Width(b[0])-1, y+1+row, func(m *Model) tea.Cmd {
			m.moveThemeMenu(idx)
			return m.closeThemeMenu(true)
		})
		row++
		if i == 1 && len(names) > 2 {
			row++
		}
	}
	return b
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
	if st.dd {
		return m.onKeyThemeMenu(k)
	}
	if st.editing {
		switch k.String() {
		case "esc":
			st.editing = false
			return m, nil
		case "enter":
			v := strings.TrimSpace(st.input.Value())
			st.editing = false
			s := st.s
			switch st.kind {
			case setSince:
				s.Since = v
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
			m.openThemeMenu()
			return m, nil
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
	case setTheme:
		name := r.value
		if m.st.dd {
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
		if !st.editing && !st.dd && r.kind != setNote {
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
		if st.kind == setSince {
			label = "Look back (e.g. 30d, 12h, 2w; empty = no limit)"
		} else {
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
	case st.dd:
		f.hint = m.hints(w, "↑/↓", "preview", "enter", "choose", "esc", "cancel")
	case st.editing:
		f.hint = m.hints(w, "enter", "save", "esc", "cancel")
	default:
		f.hint = m.statusLine(m.hints(w, "↑/↓", "select", "enter/space", "change", "x", "delete pattern", "esc", "back"))
	}
	out2 := f.render()
	if st.dd {
		y := yBody
		for i, r := range m.settingRows() {
			if r.kind == setTheme {
				y += i
			}
		}
		x := settingValueX - 1
		top := max(min(y+1, m.h-1-(len(ThemeNames())+3)), 1) // below the row, but on screen
		out2 = overlayAt(out2, m.themeMenu(x, top), x, top)
	}
	return out2
}
