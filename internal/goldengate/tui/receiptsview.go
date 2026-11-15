package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The Receipts screen: what was sent automatically, receipt by receipt, with
// the rule that sent each session. Retracting from here is the same retract
// as in the inbox.

type receiptsState struct {
	rows    []receiptRow // receipt headers and their sessions
	cursor  int          // always on a session
	off     int
	loading bool
	err     string
}

type receiptRow struct {
	header string
	item   ReceiptItem
}

type receiptsMsg struct {
	rs  []Receipt
	err error
}

func (m *Model) openReceipts() tea.Cmd {
	m.scr, m.rc.loading, m.rc.err = scrReceipts, true, ""
	return m.loadReceipts()
}

func (m *Model) loadReceipts() tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		rs, err := be.Receipts(ctx)
		return receiptsMsg{rs, err}
	}
}

func (m *Model) onReceipts(msg receiptsMsg) {
	rc := &m.rc
	rc.loading = false
	if msg.err != nil {
		rc.err = msg.err.Error()
		return
	}
	rc.err, rc.rows = "", nil
	for _, r := range msg.rs {
		rc.rows = append(rc.rows, receiptRow{header: fmt.Sprintf("%s · %s · %d sent automatically", r.At.Local().Format("2006-01-02 15:04"), r.ID, len(r.Items))})
		for _, it := range r.Items {
			rc.rows = append(rc.rows, receiptRow{item: it})
		}
	}
	rc.clamp(m, 0)
}

func (rc *receiptsState) clamp(m *Model, dir int) {
	if len(rc.rows) == 0 {
		rc.cursor, rc.off = 0, 0
		return
	}
	rc.cursor = max(min(rc.cursor, len(rc.rows)-1), 0)
	if dir == 0 {
		dir = 1
	}
	for i := 0; i < len(rc.rows) && rc.rows[rc.cursor].header != ""; i++ {
		rc.cursor += dir
		if rc.cursor < 0 || rc.cursor >= len(rc.rows) {
			dir = -dir
			rc.cursor = max(min(rc.cursor, len(rc.rows)-1), 0)
		}
	}
	h := max(bodyHeight(m.h)-2, 1)
	if rc.cursor < rc.off+1 {
		rc.off = max(rc.cursor-1, 0)
	}
	if rc.cursor >= rc.off+h {
		rc.off = rc.cursor - h + 1
	}
}

func (rc *receiptsState) current() (ReceiptItem, bool) {
	if rc.cursor < 0 || rc.cursor >= len(rc.rows) || rc.rows[rc.cursor].header != "" {
		return ReceiptItem{}, false
	}
	return rc.rows[rc.cursor].item, true
}

func (m *Model) onKeyReceipts(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	rc := &m.rc
	it, ok := rc.current()
	switch k.String() {
	case "esc", "q", "backspace":
		return m, m.openInbox()
	case "r":
		return m, m.openReceipts()
	case "up", "k":
		rc.cursor--
		rc.clamp(m, -1)
	case "down", "j":
		rc.cursor++
		rc.clamp(m, 1)
	case "R":
		switch {
		case !ok:
		case it.Retracted:
			m.setStatus("This session was already retracted.", false)
		default:
			title := clip(firstNonEmptyStr(it.Title, it.ID), 40)
			be, ctx := m.be, m.ctx
			m.modal = &modal{title: "Retract from the server", lines: []string{"Delete \"" + title + "\"", "from the server and never send it again?", "Assistants lose it at once. Local files are not touched."},
				yes: func(m *Model) tea.Cmd {
					return func() tea.Msg {
						err := be.InboxDecide(ctx, []InboxItem{{Source: it.Source, ID: it.ID, Project: it.Project, Title: it.Title}}, InboxRetract)
						if err != nil {
							return receiptsMsg{err: err}
						}
						rs, err := be.Receipts(ctx)
						return receiptsMsg{rs, err}
					}
				}}
		}
	}
	return m, nil
}

func (m *Model) viewReceipts() string {
	rc := &m.rc
	w, bh := m.w-2, bodyHeight(m.h)
	var out []string
	switch {
	case rc.loading && len(rc.rows) == 0:
		out = append(out, " "+m.th.Dim.Render("reading receipts…"))
	case rc.err != "" && len(rc.rows) == 0:
		out = append(out, " "+m.th.Bad.Render(clip(rc.err, w-4)))
	case len(rc.rows) == 0:
		out = append(out, " "+m.th.Title.Render("No receipts"), "",
			" Nothing was sent automatically. A project sends on its own only at trust",
			" level auto: goldengate projects trust <project> auto")
	default:
		note := "Sessions your projects' trust level and rules sent without asking. R deletes one from the server."
		if rc.err != "" {
			note = rc.err
		} else if it, ok := rc.current(); ok && it.Why != "" {
			note = "Sent because: " + it.Why
		}
		out = append(out, " "+m.th.Dim.Render(clip(note, w-2)), "")
		h := max(bh-2, 1)
		for i := rc.off; i < min(rc.off+h, len(rc.rows)); i++ {
			r := rc.rows[i]
			y := yBody + len(out)
			if r.header != "" {
				out = append(out, " "+m.th.Title.Render(clip(r.header, w-2)))
				continue
			}
			it := r.item
			state := "on the server"
			if it.Retracted {
				state = "retracted"
			}
			pw := max(min(w/5, 22), 10)
			line := fmt.Sprintf("  %s %s  %s  %s", m.tag(it.Source), fit(clip(it.Project, pw), pw), fit(state, 13), m.highlight(it.Title))
			i := i
			m.addZone(1, 1+w, y, func(m *Model) tea.Cmd { m.rc.cursor = i; return nil })
			if i == rc.cursor {
				line = m.th.Cursor.Render(fit(m.th.Accent.Render("▌")+line[1:], w-1))
			}
			out = append(out, line)
		}
	}
	f := frame{th: m.th, w: m.w, h: m.h, title: m.header("receipts"), right: m.serverBadge(), top: " " + strings.TrimSpace(m.sourcesLine())}
	f.left = lines(strings.Join(out, "\n"), w, bh)
	f.hint = m.statusLine(m.hints(w, "R", "retract", "r", "reload", "esc", "back to inbox"))
	return f.render()
}
