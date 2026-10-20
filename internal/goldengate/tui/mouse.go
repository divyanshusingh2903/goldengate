package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Mouse support works through click zones that views register while they
// render. A zone is either a known rectangle (list rows) or a piece of text on
// a given screen line (tabs, chips, buttons, hint-bar keys), which is located
// after the frame is drawn. Text zones that were overwritten (a status message
// replacing the hints, a modal on top) simply fail to resolve, so a click can
// only ever hit what is actually on screen.

type zone struct {
	x0, x1, y int // x1 exclusive
	do        func(m *Model) tea.Cmd
}

type textZone struct {
	y    int // screen line; -1 searches every line (overlays)
	text string
	do   func(m *Model) tea.Cmd
}

type clickState struct {
	zones []zone
	text  []textZone
	// last left click, for double clicks on list rows
	lastIdx int
	lastAt  time.Time
	msg     tea.MouseMsg // the click being handled
}

// doubleClick is the longest gap between two clicks of a double click.
const doubleClick = 400 * time.Millisecond

func (m *Model) addZone(x0, x1, y int, do func(m *Model) tea.Cmd) {
	m.ck.zones = append(m.ck.zones, zone{x0, x1, y, do})
}

// clickText makes the first occurrence of text on line y clickable.
func (m *Model) clickText(y int, text string, do func(m *Model) tea.Cmd) {
	m.ck.text = append(m.ck.text, textZone{y, stripANSI(text), do})
}

// clickKey makes text on line y behave like pressing key.
func (m *Model) clickKey(y int, text, key string) {
	m.clickText(y, text, func(m *Model) tea.Cmd { return m.press(key) })
}

// resolveZones turns text zones into rectangles against the final screen.
func (m *Model) resolveZones(screen string) {
	rows := strings.Split(screen, "\n")
	plain := make([]string, len(rows))
	for i, r := range rows {
		plain[i] = stripANSI(r)
	}
	find := func(y int, text string) (int, int, bool) {
		if y < 0 || y >= len(plain) || text == "" {
			return 0, 0, false
		}
		i := strings.Index(plain[y], text)
		if i < 0 {
			return 0, 0, false
		}
		x := lipgloss.Width(plain[y][:i])
		return x, x + lipgloss.Width(text), true
	}
	for _, t := range m.ck.text {
		if t.y >= 0 {
			if x0, x1, ok := find(t.y, t.text); ok {
				m.addZone(x0, x1, t.y, t.do)
			}
			continue
		}
		for y := range plain {
			if x0, x1, ok := find(y, t.text); ok {
				m.addZone(x0, x1, y, t.do)
				break
			}
		}
	}
	m.ck.text = nil
}

// keyMsg builds the key event a hint label stands for.
func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

// clickable reports whether a hint label maps to a single key press.
func clickable(key string) bool {
	if strings.Contains(key, "/") && key != "/" {
		return false // "↑/↓", "enter/space": a choice of keys, not one action
	}
	return true
}

// press runs a key through the normal key handling.
func (m *Model) press(key string) tea.Cmd {
	_, cmd := m.onKey(keyMsg(key))
	return cmd
}

func (m *Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		if m.modal != nil || m.help || msg.Action != tea.MouseActionPress {
			return m, nil
		}
		up := msg.Button == tea.MouseButtonWheelUp
		switch {
		case m.scr == scrProjects || (m.scr == scrWizard && m.wz.step == wizProjects):
			if m.pk.srcOpen {
				return m, nil
			}
			d := 3
			if up {
				d = -3
			}
			return m, m.moveCursor(d)
		case m.scr == scrPreview:
			if up {
				m.pv.vp.ScrollUp(3)
			} else {
				m.pv.vp.ScrollDown(3)
			}
			return m, nil
		}
		k := "down"
		if up {
			k = "up"
		}
		return m, m.press(k)
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionRelease {
			return m, nil
		}
		m.ck.msg = msg
		// later zones are drawn on top, so they win
		for i := len(m.ck.zones) - 1; i >= 0; i-- {
			z := m.ck.zones[i]
			if msg.Y == z.y && msg.X >= z.x0 && msg.X < z.x1 {
				if m.modal == nil && !m.help {
					m.status, m.bad = "", false
				}
				return m, z.do(m)
			}
		}
		// a click outside an overlay dismisses it, like "no"
		if m.modal != nil {
			m.modal = nil
		}
		if m.st.dd {
			return m, m.closeThemeMenu(false)
		}
		m.help = false
	}
	return m, nil
}

// clickRow handles a click on list row idx: on the box it shares or un-shares
// that row, ctrl+click marks it, a double click opens the preview.
func (m *Model) clickRow(idx int, onBox, ctrl bool) tea.Cmd {
	its := m.items()
	if idx < 0 || idx >= len(its) {
		return nil
	}
	now := time.Now()
	double := m.ck.lastIdx == idx && now.Sub(m.ck.lastAt) < doubleClick
	m.ck.lastIdx, m.ck.lastAt = idx, now
	cmd := m.moveCursor(idx - m.pk.cursor)
	switch {
	case ctrl:
		k := its[idx].key()
		if m.pk.marks[k] {
			delete(m.pk.marks, k)
		} else {
			m.pk.marks[k] = true
		}
		return cmd
	case onBox:
		m.ck.lastAt = time.Time{}
		it := its[idx]
		if it.state() == Shared {
			return m.actKeys(Action{Kind: ActRemove, Keys: []string{it.key()}}, "unshare")
		}
		return m.actKeys(Action{Kind: ActAllow, Keys: []string{it.key()}}, "share")
	case double:
		m.ck.lastAt = time.Time{}
		if it := its[idx]; it.node != nil && len(it.node.Children) > 0 {
			m.pk.expanded[it.node.Path] = !m.pk.expanded[it.node.Path]
			m.pk.clamp(m)
			return nil
		}
		return m.openPreview()
	}
	if it := its[idx]; it.node != nil && len(it.node.Children) > 0 && it.row == nil {
		m.pk.expanded[it.node.Path] = !m.pk.expanded[it.node.Path]
		m.pk.clamp(m)
	}
	return cmd
}

// btn is a clickable button that presses key.
type btn struct {
	st         lipgloss.Style
	key, label string
}

func (b btn) text() string { return "[" + b.key + " " + b.label + "]" }

// button renders b and registers it on line y (-1: wherever it ends up). The
// brackets keep buttons visible without color.
func (m *Model) button(st lipgloss.Style, y int, key, label string) string {
	b := btn{st, key, label}
	m.clickKey(y, b.text(), key)
	return st.Render(b.text())
}

// buttonRows lays buttons out in lines of at most w cells, starting at screen
// line y, and returns the lines.
func (m *Model) buttonRows(y, w int, bs ...btn) []string {
	var out []string
	cur, used := "", 0
	for _, b := range bs {
		bw := lipgloss.Width(b.text())
		if used > 0 && used+1+bw > w {
			out = append(out, cur)
			cur, used = "", 0
		}
		if used > 0 {
			cur += " "
			used++
		}
		cur += m.button(b.st, y+len(out), b.key, b.label)
		used += bw
	}
	if used > 0 {
		out = append(out, cur)
	}
	return out
}
