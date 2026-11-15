package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The Inbox screen: every session of the shared projects, one decision at a
// time. Approving here only records the approval; the next sync sends it.

type inboxState struct {
	in      Inbox
	rows    []inboxRow // the flattened list, section headers included
	cursor  int        // index into rows, always on an item
	off     int
	loading bool
	err     string
}

type inboxRow struct {
	header string // a section title; item is unused then
	item   InboxItem
}

type (
	inboxMsg struct {
		in  Inbox
		err error
	}
	inboxDoneMsg struct {
		what string
		err  error
	}
)

func (m *Model) openInbox() tea.Cmd {
	m.scr, m.ib.loading, m.ib.err = scrInbox, true, ""
	return m.loadInbox()
}

func (m *Model) loadInbox() tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		in, err := be.Inbox(ctx)
		return inboxMsg{in, err}
	}
}

func (m *Model) onInbox(msg inboxMsg) {
	ib := &m.ib
	ib.loading = false
	if msg.err != nil {
		ib.err = msg.err.Error()
		return
	}
	ib.in, ib.err, ib.rows = msg.in, "", nil
	section := func(title string, items []InboxItem) {
		if len(items) == 0 {
			return
		}
		ib.rows = append(ib.rows, inboxRow{header: fmt.Sprintf("%s (%d)", title, len(items))})
		for _, it := range items {
			ib.rows = append(ib.rows, inboxRow{item: it})
		}
	}
	section("Waiting for your approval", msg.in.Pending)
	section("Excluded: kept on this machine", msg.in.Excluded)
	section("Sent, or queued for the next sync", msg.in.Sent)
	section("Retracted: deleted from the server", msg.in.Retracted)
	ib.clamp(m, 0)
}

// clamp keeps the cursor on an item (moving in direction dir past headers)
// and in view.
func (ib *inboxState) clamp(m *Model, dir int) {
	if len(ib.rows) == 0 {
		ib.cursor, ib.off = 0, 0
		return
	}
	ib.cursor = max(min(ib.cursor, len(ib.rows)-1), 0)
	if dir == 0 {
		dir = 1
	}
	for i := 0; i < len(ib.rows) && ib.rows[ib.cursor].header != ""; i++ {
		ib.cursor += dir
		if ib.cursor < 0 || ib.cursor >= len(ib.rows) {
			dir = -dir
			ib.cursor = max(min(ib.cursor, len(ib.rows)-1), 0)
		}
	}
	h := max(bodyHeight(m.h)-2, 1)
	if ib.cursor < ib.off+1 { // keep the section header above the first item visible
		ib.off = max(ib.cursor-1, 0)
	}
	if ib.cursor >= ib.off+h {
		ib.off = ib.cursor - h + 1
	}
}

func (ib *inboxState) current() (InboxItem, bool) {
	if ib.cursor < 0 || ib.cursor >= len(ib.rows) || ib.rows[ib.cursor].header != "" {
		return InboxItem{}, false
	}
	return ib.rows[ib.cursor].item, true
}

func (m *Model) inboxDecide(it InboxItem, decision, done string) tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg { return inboxDoneMsg{done, be.InboxDecide(ctx, []InboxItem{it}, decision)} }
}

func (m *Model) onInboxDone(msg inboxDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
	} else {
		m.setStatus(msg.what, false)
	}
	return tea.Batch(m.loadInbox(), m.loadSnapshot())
}

func (m *Model) onKeyInbox(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	ib := &m.ib
	it, ok := ib.current()
	title := clip(firstNonEmptyStr(it.Title, it.ID), 40)
	switch k.String() {
	case "esc", "q", "backspace":
		m.scr = scrProjects
		return m, m.loadSnapshot()
	case "r":
		return m, m.openInbox()
	case "up", "k":
		ib.cursor--
		ib.clamp(m, -1)
	case "down", "j":
		ib.cursor++
		ib.clamp(m, 1)
	case "a", " ":
		switch {
		case !ok:
		case it.State == "sent" || it.State == "queued":
			m.setStatus("Nothing to approve: this session is already "+it.State+".", false)
		default:
			return m, m.inboxDecide(it, InboxApprove, "approved \""+title+"\": the next sync sends it")
		}
	case "x":
		switch {
		case !ok:
		case it.State == "excluded" || it.State == "retracted":
			m.setStatus("This session is already kept on this machine.", false)
		default:
			return m, m.inboxDecide(it, InboxExclude, "excluded \""+title+"\": it stays on this machine")
		}
	case "u":
		if ok && (it.State == "excluded" || it.State == "retracted") {
			return m, m.inboxDecide(it, InboxReset, "decision removed: \""+title+"\" is judged afresh")
		}
		if ok {
			m.setStatus("Nothing to undo for this session.", false)
		}
	case "R":
		if !ok {
			break
		}
		if it.State != "sent" && it.State != "changed" {
			m.setStatus("Only a session that was sent can be retracted. To keep this one local, press x.", false)
			break
		}
		m.modal = &modal{title: "Retract from the server", lines: []string{"Delete \"" + title + "\"", "from the server and never send it again?", "Assistants lose it at once. Local files are not touched."},
			yes: func(m *Model) tea.Cmd {
				return m.inboxDecide(it, InboxRetract, "retracted \""+title+"\": deleted from the server")
			}}
	}
	return m, nil
}

func (m *Model) viewInbox() string {
	ib := &m.ib
	w, bh := m.w-2, bodyHeight(m.h)
	var out []string
	switch {
	case ib.loading && len(ib.rows) == 0:
		out = append(out, " "+m.th.Dim.Render("reading your sessions…"))
	case ib.err != "":
		out = append(out, " "+m.th.Bad.Render(clip(ib.err, w-4)))
	case len(ib.rows) == 0:
		out = append(out, " "+m.th.Title.Render("No sessions yet"), "", " Share a project first; its sessions then wait here for your approval.")
	default:
		out = append(out, " "+m.th.Dim.Render(clip("Approving records your decision; the next sync sends it. Or review and send everything from Sync (2).", w-2)), "")
		h := max(bh-2, 1)
		for i := ib.off; i < min(ib.off+h, len(ib.rows)); i++ {
			r := ib.rows[i]
			y := yBody + len(out)
			if r.header != "" {
				out = append(out, " "+m.th.Title.Render(r.header))
				continue
			}
			it := r.item
			note := it.State
			if it.State == "changed" {
				note = "changed since sent"
			}
			pw := max(min(w/5, 22), 10)
			line := fmt.Sprintf("  %s %s  %s  %s  %s", m.tag(it.Source), fit(clip(it.Project, pw), pw), fit(relTime(m.now(), it.Updated), 8),
				fit(note, 18), m.highlight(it.Title))
			i := i
			m.addZone(1, 1+w, y, func(m *Model) tea.Cmd { m.ib.cursor = i; return nil })
			if i == ib.cursor {
				line = m.th.Cursor.Render(fit(m.th.Accent.Render("▌")+line[1:], w-1))
			}
			out = append(out, line)
		}
	}
	f := frame{th: m.th, w: m.w, h: m.h, title: m.header("inbox"), right: m.serverBadge(), top: " " + strings.TrimSpace(m.sourcesLine())}
	f.left = lines(strings.Join(out, "\n"), w, bh)
	f.hint = m.statusLine(m.hints(w, "a", "approve", "x", "exclude", "u", "undo", "R", "retract", "r", "refresh", "esc", "back"))
	return f.render()
}
