package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type syncPhase int

const (
	syncIdle syncPhase = iota
	syncPlanning
	syncReady
	syncRunning
	syncDone
)

type projProgress struct {
	label    string
	sessions int
	status   string // waiting | sending | ok | failed
	err      string
}

type syncState struct {
	phase  syncPhase
	plan   PlanInfo
	dry    bool
	total  int
	done   int
	order  []string
	proj   map[string]*projProgress
	errs   int
	err    string
	cancel context.CancelFunc
	ch     chan tea.Msg
}

func newSyncState() syncState { return syncState{dry: true, proj: map[string]*projProgress{}} }

func (m *Model) openSync() tea.Cmd {
	m.scr = scrSync
	if m.sy.phase == syncRunning {
		return nil
	}
	m.sy.phase, m.sy.err = syncPlanning, ""
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		p, err := be.Plan(ctx)
		return planMsg{p, err}
	}
}

func (m *Model) onPlan(msg planMsg) {
	if msg.err != nil {
		m.sy.phase, m.sy.err = syncIdle, msg.err.Error()
		return
	}
	m.sy.plan, m.sy.phase = msg.plan, syncReady
	m.sy.dry = m.sy.dry || msg.plan.Server == "" // without a server there is nothing but a dry run
}

func (m *Model) startSync() tea.Cmd {
	s := &m.sy
	s.phase, s.err, s.errs, s.done, s.total = syncRunning, "", 0, 0, 0
	s.order, s.proj = nil, map[string]*projProgress{}
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	ch := make(chan tea.Msg, 64)
	s.ch = ch
	be, dry := m.be, s.dry
	go func() {
		defer close(ch)
		err := be.Sync(ctx, dry, func(ev SyncEvent) {
			select {
			case ch <- syncEventMsg{ev}:
			case <-ctx.Done():
			}
		})
		ch <- syncDoneMsg{err}
	}()
	return listen(ch)
}

// listen waits for the next message from a running sync.
func listen(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *Model) onSyncEvent(msg syncEventMsg) tea.Cmd {
	s, ev := &m.sy, msg.ev
	switch ev.Kind {
	case SyncPlanned:
		s.total = ev.Sessions
	case SyncProjectStart:
		p := s.get(ev.Project)
		p.sessions, p.status = ev.Sessions, "sending"
	case SyncProjectDone:
		p := s.get(ev.Project)
		p.sessions, p.status = ev.Sessions, "ok"
		s.done += ev.Sessions
	case SyncProjectFailed:
		p := s.get(ev.Project)
		p.status, p.err = "failed", ev.Err
		s.errs++
	}
	return listen(s.ch)
}

func (s *syncState) get(label string) *projProgress {
	p, ok := s.proj[label]
	if !ok {
		p = &projProgress{label: label, status: "waiting"}
		s.proj[label] = p
		s.order = append(s.order, label)
	}
	return p
}

func (m *Model) onSyncDone(msg syncDoneMsg) {
	s := &m.sy
	s.phase = syncDone
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if msg.err != nil {
		s.err = msg.err.Error()
	}
	if s.errs == 0 && s.err == "" {
		verb := "synced"
		if s.dry {
			verb = "dry run finished"
		}
		m.setStatus(fmt.Sprintf("%s: %d session(s)", verb, s.done), false)
	}
	m.pv.cache = map[string]Preview{}
}

func (m *Model) onKeySync(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.sy
	switch k.String() {
	case "esc", "q", "backspace":
		if s.phase == syncRunning {
			return m, nil // leave it running; esc does not abandon a push half way
		}
		m.scr = scrProjects
		return m, m.loadSnapshot()
	case "D":
		if s.phase == syncReady || s.phase == syncDone {
			if s.plan.Server == "" && s.dry {
				m.setStatus("no server is configured, so only a dry run is possible (see the Server screen)", true)
				return m, nil
			}
			s.dry = !s.dry
		}
	case "enter":
		if s.phase == syncReady {
			if s.plan.Sessions == 0 {
				m.setStatus("nothing to send", false)
				return m, nil
			}
			return m, m.startSync()
		}
	case "c":
		if s.phase == syncRunning && s.cancel != nil {
			s.cancel()
			m.setStatus("cancelling…", false)
		}
	case "r":
		if s.phase == syncDone || s.phase == syncReady || s.phase == syncIdle {
			return m, m.openSync()
		}
	}
	return m, nil
}

func bar(done, total, width int) string {
	if total <= 0 {
		return "[" + strings.Repeat("-", max(width-2, 0)) + "]"
	}
	inner := max(width-2, 1)
	n := done * inner / total
	n = min(max(n, 0), inner)
	return "[" + strings.Repeat("#", n) + strings.Repeat("-", inner-n) + "]"
}

func (m *Model) viewSync() string {
	s := &m.sy
	bh := bodyHeight(m.h)
	w := m.w - 2
	var out []string
	add := func(l string) { out = append(out, " "+l) }
	mode := "REAL SYNC"
	if s.dry {
		mode = "dry run (nothing is sent or recorded)"
	}
	top := " Sync · " + mode
	switch s.phase {
	case syncIdle, syncPlanning:
		add(m.th.Dim.Render("planning…"))
		if s.err != "" {
			add(m.th.Bad.Render(s.err))
		}
	case syncReady:
		add(m.th.Title.Render(fmt.Sprintf("%d session(s) in %d project(s) would be sent", s.plan.Sessions, len(s.plan.Projects))))
		add(fmt.Sprintf("%d session(s) not selected, %d already up to date, %d warning(s)", s.plan.NotSelected, s.plan.Unchanged, s.plan.Warnings))
		if n := totalRedactions(s.plan.Redactions); n > 0 {
			add(m.th.Redact.Render(fmt.Sprintf("%d redaction(s) applied", n)))
		}
		add("")
		for _, p := range s.plan.Projects {
			if len(out) >= bh-2 {
				add(m.th.Dim.Render("…"))
				break
			}
			add(fmt.Sprintf("  %-*s %d", max(w-14, 10), truncLeft(p.Label, max(w-14, 10)), p.Sessions))
		}
		if s.plan.Server == "" {
			add("")
			add(m.th.Warn.Render("No server configured: only a dry run is available."))
		}
	case syncRunning, syncDone:
		add(bar(s.done, s.total, min(w-20, 50)) + fmt.Sprintf("  %d/%d sessions", s.done, s.total))
		add("")
		for _, label := range s.order {
			if len(out) >= bh-1 {
				add(m.th.Dim.Render("…"))
				break
			}
			p := s.proj[label]
			icon := map[string]string{"waiting": "[  ]", "sending": "[..]", "ok": m.th.Good.Render("[ok]"), "failed": m.th.Bad.Render("[!!]")}[p.status]
			line := fmt.Sprintf("%s %s", icon, truncLeft(p.label, max(w-24, 10)))
			if p.sessions > 0 {
				line += fmt.Sprintf("  %d", p.sessions)
			}
			add(line)
			if p.err != "" && len(out) < bh-1 {
				add("     " + m.th.Bad.Render(clip(p.err, w-8)))
			}
		}
		if s.phase == syncDone {
			if s.err != "" {
				add(m.th.Bad.Render("finished with an error: " + clip(s.err, w-4)))
			} else if s.errs > 0 {
				add(m.th.Warn.Render(fmt.Sprintf("%d project(s) failed. Press r to retry; finished projects are skipped.", s.errs)))
			} else {
				add(m.th.Good.Render("done"))
			}
		}
	}
	f := frame{th: m.th, w: m.w, h: m.h, title: "sync", right: m.serverBadge(), top: top}
	f.left = lines(strings.Join(out, "\n"), w, bh)
	switch s.phase {
	case syncReady:
		f.hint = m.statusLine(hints(m.th, w, "enter", "start", "D", "dry/real", "r", "replan", "esc", "back"))
	case syncRunning:
		f.hint = m.statusLine(hints(m.th, w, "c", "cancel"))
	default:
		f.hint = m.statusLine(hints(m.th, w, "r", "retry/replan", "D", "dry/real", "esc", "back"))
	}
	return f.render()
}
