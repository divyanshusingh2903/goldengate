package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Syncing is two steps, the way a "review, then push" button works in a git
// client:
//
//  1. Review: opening the screen plans the sync and runs it dry at once. Every
//     session is read, summarized and redacted exactly as it would be sent,
//     and nothing leaves the machine. The result is a list of what would go.
//  2. Send: one button sends exactly that. Nothing is sent without it.
//
// With a model summarizer configured, the review also counts synced sessions
// whose summaries the model did not write, and offers to include them: that
// re-runs the review with SyncOpts.Resummarize.

type syncPhase int

const (
	syncIdle     syncPhase = iota // planning failed
	syncPlanning                  // working out what would be sent
	syncReady                     // reviewed (or nothing to send): waiting for the user
	syncRunning                   // a dry check (dry) or the real send (!dry) is running
	syncDone                      // the real send finished
)

type projProgress struct {
	label    string
	sessions int
	sent     int    // sessions of it that reached the server (or were checked) so far
	status   string // waiting | sending | ok | failed
	err      string
}

type syncState struct {
	phase    syncPhase
	plan     PlanInfo
	dry      bool // the running (or last) pass is a dry check
	checked  bool // the review pass finished
	total    int
	done     int
	prepared int // sessions read and redacted while the plan builds
	order    []string
	proj     map[string]*projProgress
	errs     int
	err      string
	cancel   context.CancelFunc
	ch       chan tea.Msg
	opts     SyncOpts // kept for "review again"; reset when the screen opens

	// while the review prepares sessions
	expect    int           // about how many it will prepare (from the projects' pending counts)
	recent    []preparedOne // the last few prepared, newest last
	fallbacks int           // sessions the model could not summarize (rules were used)
	started   time.Time
}

type preparedOne struct{ project, title, by string }

func newSyncState() syncState { return syncState{dry: true, proj: map[string]*projProgress{}} }

func (m *Model) openSync() tea.Cmd {
	m.scr = scrSync
	if m.sy.phase == syncRunning {
		return nil
	}
	m.sy.opts = SyncOpts{}
	return m.planSync()
}

// planSync starts step 1 over: the review pass plans as it goes and reports
// each session as it is read, redacted and summarized, so a slow model shows
// its progress instead of a blank wait.
func (m *Model) planSync() tea.Cmd {
	s := &m.sy
	stale := 0
	if s.opts.Resummarize {
		stale = s.plan.Stale
	}
	s.err, s.checked = "", false
	s.plan = PlanInfo{Server: m.snap.Server, Summarizer: m.snap.Summarizer}
	s.expect = stale
	for _, r := range m.snap.Rows {
		s.expect += r.Pending
	}
	return m.startSync(true)
}

func (m *Model) onPlan(msg planMsg) tea.Cmd {
	if msg.err != nil {
		m.sy.phase, m.sy.err = syncIdle, msg.err.Error()
		return nil
	}
	m.sy.plan, m.sy.phase = msg.plan, syncReady
	if msg.plan.Sessions == 0 {
		m.sy.checked, m.sy.dry = true, true // nothing to review (and nothing being sent)
		return nil
	}
	return m.startSync(true) // step 1 runs by itself: it sends nothing
}

func (m *Model) startSync(dry bool) tea.Cmd {
	s := &m.sy
	s.phase, s.dry, s.err, s.errs, s.done, s.total, s.prepared = syncRunning, dry, "", 0, 0, 0, 0
	s.recent, s.fallbacks, s.started = nil, 0, time.Now()
	s.order, s.proj = nil, map[string]*projProgress{}
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	ch := make(chan tea.Msg, 64)
	s.ch = ch
	be, o := m.be, s.opts
	go func() {
		defer close(ch)
		err := be.Sync(ctx, dry, o, func(ev SyncEvent) {
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
	case SyncPrepared:
		s.prepared += ev.Sessions
		if ev.Title != "" {
			s.recent = append(s.recent, preparedOne{ev.Project, ev.Title, ev.By})
			if len(s.recent) > 5 {
				s.recent = s.recent[1:]
			}
		}
		if m.snap.Summarizer != "" && ev.By == "heuristic" {
			s.fallbacks++
		}
	case SyncPlanned:
		s.total = ev.Sessions
		if ev.Plan != nil {
			s.plan = *ev.Plan
		}
	case SyncProgress:
		p := s.get(ev.Project)
		p.sent += ev.Sessions
		s.done += ev.Sessions
	case SyncProjectStart:
		p := s.get(ev.Project)
		p.sessions, p.status = ev.Sessions, "sending"
	case SyncProjectDone:
		p := s.get(ev.Project)
		p.sessions, p.status = ev.Sessions, "ok"
		s.done += max(ev.Sessions-p.sent, 0) // whatever progress events did not cover
		p.sent = ev.Sessions
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
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if msg.err != nil {
		s.err = msg.err.Error()
	}
	if s.dry {
		s.phase, s.checked = syncReady, true
	} else {
		s.phase = syncDone
	}
	m.pv.cache = map[string]Preview{}
}

// canSend reports whether the review passed and there is somewhere to send to.
func (s *syncState) canSend() bool {
	return s.phase == syncReady && s.checked && s.dry && s.done > 0 && s.errs == 0 && s.err == "" && s.plan.Server != ""
}

// canResummarize reports whether the review can offer to include synced
// sessions whose summaries the configured model did not write.
func (s *syncState) canResummarize() bool {
	return s.phase == syncReady && s.checked && s.dry && !s.opts.Resummarize && s.plan.Summarizer != "" && s.plan.Stale > 0
}

// syncBack leaves the sync screen (or, during setup, finishes it).
func (m *Model) syncBack() tea.Cmd {
	if m.scr == scrWizard {
		return m.leaveWizard()
	}
	m.scr = scrProjects
	return m.loadSnapshot()
}

func (m *Model) onKeySync(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.sy
	switch k.String() {
	case "esc", "q", "backspace":
		if s.phase == syncRunning && !s.dry {
			return m, nil // leave it running; esc does not abandon a push half way
		}
		if s.phase == syncRunning && s.cancel != nil {
			s.cancel()
		}
		return m, m.syncBack()
	case "enter":
		switch {
		case s.canSend():
			return m, m.startSync(false) // step 2
		case s.phase == syncReady && s.checked && s.plan.Server == "" && s.done > 0:
			if m.scr == scrWizard {
				return m, m.leaveWizard()
			}
			return m, m.openServer()
		case s.phase == syncDone && (s.errs > 0 || s.err != ""):
			return m, m.planSync() // retry: finished projects are skipped
		case s.phase == syncReady && s.checked && (s.errs > 0 || s.err != ""):
			return m, m.planSync()
		case s.phase == syncDone, s.phase == syncReady && s.checked:
			return m, m.syncBack()
		}
	case "c":
		if s.phase == syncRunning && s.cancel != nil {
			s.cancel()
			m.setStatus("cancelling…", false)
		}
	case "r":
		if s.phase != syncRunning {
			return m, m.planSync()
		}
	case "m":
		if s.canResummarize() {
			s.opts.Resummarize = true
			return m, m.planSync()
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

func (m *Model) coloredBar(done, total, width int) string {
	b := bar(done, total, width)
	filled := strings.Count(b, "#")
	return m.th.Border.Render("[") + m.th.Good.Render(strings.Repeat("#", filled)) +
		m.th.Dim.Render(strings.Repeat("-", len(b)-2-filled)) + m.th.Border.Render("]")
}

// stepper is the top line: ① Review ── ② Send, with the current step lit.
func (m *Model) stepper() string {
	s := &m.sy
	sending := !s.dry && (s.phase == syncRunning || s.phase == syncDone)
	one, two := m.th.Accent.Render("① Review"), m.th.Dim.Render("② Send")
	switch {
	case sending && s.phase == syncDone && s.errs == 0 && s.err == "":
		one, two = m.th.Good.Render("✓ Review"), m.th.Good.Render("✓ Send")
	case sending:
		one, two = m.th.Good.Render("✓ Review"), m.th.Accent.Render("② Send")
	case s.canSend():
		one = m.th.Good.Render("✓ Review")
		two = m.th.Plain.Render("② Send")
	}
	dest := "nowhere yet (no server)"
	if srv := orDefault(s.plan.Server, m.snap.Server); srv != "" {
		dest = shortHost(srv)
	}
	return " " + one + m.th.Border.Render(" ──── ") + two + m.th.Dim.Render("   to "+dest)
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// resummarizeOffer is the line and button that include synced sessions with
// rule-based (or another model's) summaries in this sync.
func (m *Model) resummarizeOffer(add func(string), buttons func(...btn)) {
	s := &m.sy
	if !s.canResummarize() {
		return
	}
	add(m.th.Warn.Render(fmt.Sprintf("%s rule-based summaries", plural(s.plan.Stale, "synced session")+" "+haveHas(s.plan.Stale))))
	add(m.th.Dim.Render(clip("Including them sends their redacted conversations to "+s.plan.Summarizer+".", m.w-6)))
	buttons(btn{m.th.Button, "m", "Include them in this sync"})
	add("")
}

func haveHas(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

func (m *Model) viewSync() string {
	s := &m.sy
	bh := bodyHeight(m.h)
	w := m.w - 2
	var out []string
	add := func(l string) { out = append(out, " "+l) }
	y := func() int { return yBody + len(out) }
	buttons := func(bs ...btn) {
		for _, l := range m.buttonRows(y(), w-2, bs...) {
			add(l)
		}
	}
	sendingPass := !s.dry && (s.phase == syncRunning || s.phase == syncDone)

	switch {
	case s.phase == syncIdle:
		add(m.th.Bad.Render("✗ Could not work out what to send"))
		add(m.th.Dim.Render(clip(s.err, w-4)))
		add("")
		buttons(btn{m.th.Button, "r", "Try again"}, btn{m.th.Button, "esc", "Back"})
	case s.phase == syncPlanning:
		add(m.th.Dim.Render("Looking for new and changed sessions in the projects you share…"))
	case s.phase == syncRunning && s.dry && s.total == 0:
		m.preparingView(add, w)
	case s.phase == syncReady && s.plan.Sessions == 0:
		if len(s.plan.Projects) == 0 && s.plan.Unchanged == 0 {
			add(m.th.Title.Render("Nothing is shared yet"))
			add(m.th.Dim.Render("Share a project on the Projects screen (space, or click its box), then sync."))
		} else {
			add(m.th.Good.Render("✓ Everything is up to date"))
			add(m.th.Dim.Render(fmt.Sprintf("The server already has all %s from the projects you share.", plural(s.plan.Unchanged, "session"))))
		}
		add("")
		m.resummarizeOffer(add, buttons)
		buttons(btn{m.th.Button, "enter", "Back to projects"})
	default:
		// title for the pass
		switch {
		case s.phase == syncRunning && s.dry:
			add(m.th.Title.Render("Step 1 · Review") + m.th.Dim.Render(" — reading and redacting, nothing is sent"))
		case s.phase == syncRunning:
			add(m.th.Title.Render("Step 2 · Sending") + m.th.Dim.Render(" to "+shortHost(s.plan.Server)))
		case sendingPass && s.errs == 0 && s.err == "":
			add(m.th.Good.Bold(true).Render(fmt.Sprintf("✓ Sent %s to %s", plural(s.done, "session"), shortHost(s.plan.Server))))
			add(m.th.Dim.Render("The server is up to date with everything you share."))
		case sendingPass:
			add(m.th.Bad.Bold(true).Render(fmt.Sprintf("✗ %s could not be sent", plural(max(s.errs, 1), "project"))))
			add(m.th.Dim.Render(fmt.Sprintf("%s were sent. Retrying skips what already arrived.", plural(s.done, "session"))))
		case s.errs > 0 || s.err != "":
			add(m.th.Bad.Bold(true).Render("✗ The review found problems") + m.th.Dim.Render(" · nothing was sent"))
		default:
			add(m.th.Good.Bold(true).Render("✓ Ready to send") + m.th.Dim.Render(" · nothing has left this machine yet"))
		}
		if s.phase == syncRunning {
			bw := min(w-40, 50)
			switch {
			case s.total == 0: // still reading and redacting
				want := max(s.plan.Sessions, s.prepared)
				add(m.coloredBar(s.prepared, want, bw) + m.th.Dim.Render(fmt.Sprintf("  preparing %d/%d sessions", s.prepared, want)))
			case s.dry:
				add(m.coloredBar(s.done, s.total, bw) + fmt.Sprintf("  checked %d/%d sessions", s.done, s.total))
			default:
				add(m.coloredBar(s.done, s.total, bw) + fmt.Sprintf("  sent %d/%d sessions", s.done, s.total))
			}
		}
		add("")

		// the table: one line per project
		lw := max(min(w-30, 70), 16)
		add(m.th.Label.Render(fit("PROJECT", lw+5) + fitRight("SESSIONS", 9)))
		labels := s.order
		if len(labels) == 0 {
			for _, p := range s.plan.Projects {
				labels = append(labels, p.Label)
			}
		}
		planned := map[string]int{}
		for _, p := range s.plan.Projects {
			planned[p.Label] = p.Sessions
		}
		for _, label := range labels {
			if len(out) >= bh-6 {
				add(m.th.Dim.Render("…"))
				break
			}
			p, ok := s.proj[label]
			n := planned[label]
			icon := m.th.Dim.Render("·  ")
			if ok {
				if p.sessions > 0 {
					n = p.sessions
				}
				icon = map[string]string{"waiting": m.th.Dim.Render("·  "), "sending": m.th.Warn.Render("…  "),
					"ok": m.th.Good.Render("✓  "), "failed": m.th.Bad.Render("✗  ")}[p.status]
			}
			count := fmt.Sprintf("%d", n)
			if ok && p.status == "sending" {
				count = fmt.Sprintf("%d/%d", p.sent, n)
			}
			add(icon + fit(truncLeft(label, lw), lw) + "  " + fitRight(count, 9))
			if ok && p.err != "" && len(out) < bh-6 {
				add("     " + m.th.Bad.Render(clip(p.err, w-8)))
			}
		}
		if s.err != "" {
			add(m.th.Bad.Render(clip(s.err, w-4)))
		}
		add("")

		// totals and what stays behind
		parts := []string{plural(s.plan.Sessions, "session"), plural(len(s.plan.Projects), "project")}
		if n := totalRedactions(s.plan.Redactions); n > 0 {
			parts = append(parts, m.th.Accent.Render(plural(n, "secret")+" redacted"))
		}
		if s.plan.Warnings > 0 {
			parts = append(parts, m.th.Warn.Render(plural(s.plan.Warnings, "warning")))
		}
		add(strings.Join(parts, m.th.Dim.Render(" · ")))
		if s.plan.NotSelected > 0 {
			add(m.th.Dim.Render(fmt.Sprintf("%s in projects you don't share stay on this machine.", plural(s.plan.NotSelected, "session"))))
		}
		if s.opts.Resummarize && s.plan.Resummarized > 0 {
			add(m.th.Dim.Render(fmt.Sprintf("Includes %s re-summarized by %s.", plural(s.plan.Resummarized, "synced session"), s.plan.Summarizer)))
		}
		add("")
		m.resummarizeOffer(add, buttons)

		// the one thing to do next
		switch {
		case s.canSend():
			buttons(btn{m.th.ButtonGo, "enter", fmt.Sprintf("Send %s", plural(s.done, "session"))},
				btn{m.th.Button, "r", "Review again"}, btn{m.th.Button, "esc", "Not now"})
		case s.phase == syncReady && s.checked && s.plan.Server == "":
			add(m.th.Warn.Render("No server is connected, so this can only be a review."))
			buttons(btn{m.th.Button, "enter", "Connect a server"}, btn{m.th.Button, "esc", "Back"})
		case s.phase == syncRunning && s.dry:
			buttons(btn{m.th.Button, "c", "Cancel"})
		case s.phase == syncRunning:
			buttons(btn{m.th.Button, "c", "Cancel"})
		case s.errs > 0 || s.err != "":
			buttons(btn{m.th.Button, "enter", "Retry"}, btn{m.th.Button, "esc", "Back"})
		case s.phase == syncDone:
			buttons(btn{m.th.ButtonGo, "enter", "Done"})
		}
	}

	f := frame{th: m.th, w: m.w, h: m.h, title: m.header("sync"), right: m.serverBadge(), top: m.stepper()}
	f.left = lines(strings.Join(out, "\n"), w, bh)
	switch {
	case s.phase == syncRunning:
		f.hint = m.statusLine(m.hints(w, "c", "cancel", "esc", "back"))
	case s.canSend() && s.canResummarize():
		f.hint = m.statusLine(m.hints(w, "enter", "send", "m", "include re-summaries", "r", "review again", "esc", "not now"))
	case s.canSend():
		f.hint = m.statusLine(m.hints(w, "enter", "send", "r", "review again", "esc", "not now"))
	default:
		f.hint = m.statusLine(m.hints(w, "enter", "continue", "r", "review again", "esc", "back"))
	}
	return f.render()
}

// preparingView explains step 1 while it runs: what happens to each session,
// who summarizes it, how far along it is, and what was just done.
func (m *Model) preparingView(add func(string), w int) {
	s := &m.sy
	by := m.snap.Summarizer
	local := by == "" || strings.HasPrefix(by, "ollama:")
	add(m.th.Title.Render("Step 1 · Review") + m.th.Dim.Render(" — nothing is sent in this step"))
	add("")
	add("Each new or changed session in the projects you share is, on this machine:")
	add(m.th.Accent.Render("  1 ") + "read from its tool's log")
	add(m.th.Accent.Render("  2 ") + "redacted: secrets, keys and paths outside the project are removed")
	switch {
	case by == "":
		add(m.th.Accent.Render("  3 ") + "summarized by local rules " + m.th.Dim.Render("(title, goals, outcomes, files, commands)"))
	case local:
		add(m.th.Accent.Render("  3 ") + "summarized by " + m.th.Accent.Render(by) + m.th.Dim.Render(" on this machine: goals, what was done, outcomes"))
	default:
		add(m.th.Accent.Render("  3 ") + "summarized by " + m.th.Accent.Render(by) + m.th.Dim.Render(": its redacted conversation goes to that provider"))
	}
	add("")

	want := max(s.expect, s.prepared)
	bw := min(w-48, 40)
	switch {
	case s.prepared == 0 && by != "":
		add(m.coloredBar(0, max(want, 1), bw) + m.th.Dim.Render(fmt.Sprintf("  starting · the first answer can be slow while %s loads", by)))
	case want > 0:
		line := fmt.Sprintf("  %d of ~%d sessions", s.prepared, want)
		elapsed := m.now().Sub(s.started)
		if elapsed > 0 && elapsed < 24*time.Hour && s.prepared >= 2 {
			per := elapsed / time.Duration(s.prepared)
			line += m.th.Dim.Render(fmt.Sprintf(" · ~%s each", per.Round(100*time.Millisecond)))
			if left := want - s.prepared; left > 0 {
				line += m.th.Dim.Render(" · about " + (per * time.Duration(left)).Round(time.Second).String() + " left")
			}
		}
		add(m.coloredBar(s.prepared, want, bw) + line)
	default:
		add(m.th.Dim.Render("Looking for new and changed sessions…"))
	}
	if s.fallbacks > 0 {
		add(m.th.Warn.Render(fmt.Sprintf("%s kept the rule-based summary: the model did not answer", plural(s.fallbacks, "session"))))
	}
	if len(s.recent) > 0 {
		add("")
		add(m.th.Label.Render("Just summarized"))
		for i := len(s.recent) - 1; i >= 0; i-- {
			r := s.recent[i]
			mark, tag := m.th.Good.Render("✓ "), m.th.Dim.Render("  "+r.by)
			if m.snap.Summarizer != "" && r.by == "heuristic" {
				mark, tag = m.th.Warn.Render("⚠ "), m.th.Warn.Render("  rules (model failed)")
			}
			add(mark + clip(r.title, max(w-lipgloss.Width(r.project)-lipgloss.Width(tag)-10, 20)) + m.th.Dim.Render("  "+r.project) + tag)
		}
	}
	add("")
	add(m.th.Dim.Render("Model answers are cached: a session is summarized once per content, and the send reuses this review."))
}
