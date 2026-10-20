package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
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
		rows = append(rows, settingRow{kind: setSource, label: "Source " + pretty(src.Name), value: v, idx: i})
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
		line := fmt.Sprintf(" %-34s %s", clip(r.label, 34), r.value)
		switch r.kind {
		case setNote:
			line = " " + m.th.Dim.Render(fmt.Sprintf("%-34s %s", r.label, r.value))
		case setAddPattern:
			line = " " + m.th.Accent.Render(r.label)
		}
		if i == st.cursor && !st.editing {
			line = m.th.Cursor.Render(fit(line, w))
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
	f := frame{th: m.th, w: m.w, h: m.h, title: "settings", right: m.serverBadge(), top: " Changes are saved immediately"}
	f.left = lines(strings.Join(out, "\n"), w, bh)
	if st.editing {
		f.hint = hints(m.th, w, "enter", "save", "esc", "cancel")
	} else {
		f.hint = m.statusLine(hints(m.th, w, "↑/↓", "select", "enter/space", "change", "x", "delete pattern", "esc", "back"))
	}
	return f.render()
}
