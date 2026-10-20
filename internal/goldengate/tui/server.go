package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

type serverState struct {
	info    ServerInfo
	loading bool
	cursor  int
}

func (m *Model) openServer() tea.Cmd {
	m.scr, m.sv.loading = scrServer, true
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		i, err := be.Server(ctx)
		return serverMsg{i, err}
	}
}

func (m *Model) onServer(msg serverMsg) {
	m.sv.loading = false
	if msg.err != nil {
		m.sv.info = ServerInfo{Configured: m.snap.Server != "", URL: m.snap.Server, Err: msg.err.Error()}
		return
	}
	m.sv.info = msg.info
	m.sv.cursor = max(min(m.sv.cursor, len(msg.info.Projects)-1), 0)
}

func (m *Model) onServerDelete(msg serverDelMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("could not delete "+msg.label+": "+msg.err.Error(), true)
		return nil
	}
	m.setStatus("deleted "+msg.label+" from the server", false)
	return tea.Batch(m.openServer(), m.loadSnapshot())
}

func (m *Model) onKeyServer(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q", "backspace":
		m.scr = scrProjects
	case "r":
		return m, m.openServer()
	case "up", "k":
		m.sv.cursor = max(m.sv.cursor-1, 0)
	case "down", "j":
		m.sv.cursor = min(m.sv.cursor+1, max(len(m.sv.info.Projects)-1, 0))
	case "D", "x":
		if m.sv.cursor < len(m.sv.info.Projects) {
			label := m.sv.info.Projects[m.sv.cursor].Label
			m.modal = &modal{title: "Delete from the server", lines: []string{"Delete " + label + " and all its sessions", "from the server? Local data is not touched."},
				yes: func(m *Model) tea.Cmd {
					be, ctx := m.be, m.ctx
					return func() tea.Msg { return serverDelMsg{label, be.DeleteServerProject(ctx, label)} }
				}}
		}
	case "i":
		return m, m.startWizard()
	}
	return m, nil
}

func (m *Model) viewServer() string {
	i := m.sv.info
	w, bh := m.w-2, bodyHeight(m.h)
	var out []string
	add := func(l string) { out = append(out, " "+l) }
	switch {
	case m.sv.loading:
		add(m.th.Dim.Render("checking the server…"))
	case !i.Configured && m.snap.Server == "":
		add(m.th.Title.Render("No server configured"))
		add("")
		add("Without a server nothing leaves this machine. To connect one,")
		add("press " + m.th.Key.Render("i") + " (setup wizard) or run:")
		add("    goldengate init --server https://your-host")
	default:
		add(m.th.Title.Render(i.URL))
		health := m.th.Good.Render("● reachable")
		if !i.Healthy {
			health = m.th.Bad.Render("✗ unreachable")
		}
		tok := m.th.Good.Render("● accepted")
		if !i.TokenOK {
			tok = m.th.Bad.Render("✗ not accepted")
		}
		add(fmt.Sprintf("Server %s   Ingest token %s (%s)", health, tok, i.TokenStore))
		if i.Err != "" {
			add(m.th.Bad.Render(clip(i.Err, w-4)))
		}
		add("")
		add(m.th.Title.Render(fmt.Sprintf("What the server holds (%d project(s))", len(i.Projects))))
		del := btn{m.th.ButtonBad, "D", "Delete"}
		lw := max(w-50, 10)
		for idx, p := range i.Projects {
			if len(out) >= bh {
				break
			}
			y := yBody + len(out)
			idx := idx
			m.addZone(1, 1+w, y, func(m *Model) tea.Cmd {
				m.sv.cursor = idx
				return nil
			})
			kind := m.th.CodeTag.Render("code")
			if pathutil.IsVirtualKey(p.Label) {
				kind = m.th.ChatTag.Render("chat")
			}
			cur := idx == m.sv.cursor
			mark := " "
			if cur {
				mark = m.th.Accent.Render("▌")
			}
			line := fmt.Sprintf("%s %s  %s  %4d sessions  %-9s", mark, kind, fit(truncLeft(p.Label, lw), lw), p.Sessions, relTime(m.now(), p.Last))
			if cur {
				m.clickText(y, del.text(), func(m *Model) tea.Cmd { m.sv.cursor = idx; return m.press("D") })
				line += " " + del.st.Render(del.text())
				line = m.th.Cursor.Render(fit(line, w-1))
			}
			out = append(out, line)
		}
	}
	f := frame{th: m.th, w: m.w, h: m.h, title: m.header("server"), right: m.serverBadge(), top: " " + strings.TrimSpace(m.sourcesLine())}
	f.left = lines(strings.Join(out, "\n"), w, bh)
	f.hint = m.statusLine(m.hints(w, "↑/↓", "select", "D", "delete from server", "r", "refresh", "i", "setup", "esc", "back"))
	return f.render()
}
