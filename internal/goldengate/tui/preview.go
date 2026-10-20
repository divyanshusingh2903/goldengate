package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const previewLimit = 20

// previewDebounce waits for the cursor to settle before reading sessions.
var previewDebounce = 150 * time.Millisecond

type previewState struct {
	cache   map[string]Preview
	loading map[string]bool
	seq     int
	key     string
	vp      viewport.Model
}

func newPreviewState() previewState {
	return previewState{cache: map[string]Preview{}, loading: map[string]bool{}, vp: newViewport()}
}

// rowByKey finds a project in the current snapshot.
func (m *Model) rowByKey(key string) (*Row, bool) {
	for i := range m.snap.Rows {
		if m.snap.Rows[i].Key == key {
			return &m.snap.Rows[i], true
		}
	}
	return nil, false
}

func (m *Model) currentProject() (*Row, bool) {
	it, ok := m.current()
	if !ok || it.row == nil {
		return nil, false
	}
	return it.row, true
}

// requestPreview schedules a (debounced) preview load for the highlighted project.
func (m *Model) requestPreview() tea.Cmd {
	r, ok := m.currentProject()
	if !ok {
		return nil
	}
	if _, have := m.pv.cache[r.Key]; have || m.pv.loading[r.Key] {
		return nil
	}
	m.pv.seq++
	seq, key := m.pv.seq, r.Key
	return tea.Tick(previewDebounce, func(time.Time) tea.Msg { return debounceMsg{key, seq} })
}

func (m *Model) onDebounce(msg debounceMsg) tea.Cmd {
	if msg.seq != m.pv.seq {
		return nil // the cursor moved on
	}
	return m.fetchPreview(msg.key)
}

func (m *Model) fetchPreview(key string) tea.Cmd {
	if _, have := m.pv.cache[key]; have || m.pv.loading[key] {
		return nil
	}
	m.pv.loading[key] = true
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		pv, err := be.Preview(ctx, key, previewLimit)
		return previewMsg{key: key, pv: pv, err: err}
	}
}

func (m *Model) onPreview(msg previewMsg) {
	delete(m.pv.loading, msg.key)
	if msg.err != nil {
		m.setStatus("preview failed: "+msg.err.Error(), true)
		return
	}
	m.pv.cache[msg.key] = msg.pv
	if m.scr == scrPreview && m.pv.key == msg.key {
		m.pv.refresh(m)
	}
}

func (m *Model) openPreview() tea.Cmd {
	r, ok := m.currentProject()
	if !ok {
		return nil
	}
	m.scr, m.pv.key = scrPreview, r.Key
	m.pv.vp.GotoTop()
	m.pv.refresh(m)
	return m.fetchPreview(r.Key)
}

func (p *previewState) refresh(m *Model) {
	if m.scr != scrPreview {
		return
	}
	pv, ok := p.cache[p.key]
	if !ok {
		p.vp.SetContent(m.th.Dim.Render(" reading sessions…"))
		return
	}
	p.vp.SetContent(m.renderPreview(pv, max(m.w-4, 20)))
}

var reRedacted = regexp.MustCompile(`\[REDACTED:[a-z0-9-]+\]`)

// highlight makes redaction markers stand out wherever they appear.
func (m *Model) highlight(s string) string {
	return reRedacted.ReplaceAllStringFunc(s, func(x string) string { return m.th.Redact.Render(x) })
}

func (m *Model) renderPreview(pv Preview, width int) string {
	var b strings.Builder
	wrap := func(indent, s string) {
		for _, l := range strings.Split(ansi.Wrap(s, max(width-len(indent), 10), " /,;"), "\n") {
			b.WriteString(indent + l + "\n")
		}
	}
	if len(pv.Items) == 0 {
		return " " + m.th.Dim.Render("No sessions in the window for this project.")
	}
	if m.pk.paranoid {
		b.WriteString(m.th.Warn.Render(" PARANOID: the exact bytes of every payload that would leave this machine") + "\n\n")
		for _, it := range pv.Items {
			b.WriteString(m.th.Title.Render(fmt.Sprintf(" ── %s %s", it.Source, it.ID)) + "\n")
			for _, l := range strings.Split(it.JSON, "\n") {
				wrap("  ", m.highlight(l))
			}
			b.WriteString("\n")
		}
		return b.String()
	}
	r, _ := m.rowByKey(pv.Key)
	tracked := r == nil || ((r.State == Shared || r.State == Partial) && r.Synced > 0)
	for _, it := range pv.Items {
		tag := map[string]string{"new": m.th.Added.Render("new"), "changed": m.th.Changed.Render("changed"), "unchanged": m.th.Dim.Render("unchanged")}[it.Status]
		if !tracked {
			tag = m.th.Dim.Render("not sent")
			if r.State == Shared || r.State == Partial {
				tag = m.th.Added.Render("new")
			}
		}
		b.WriteString(m.th.Title.Render(" ── ") + fmt.Sprintf("[%s] %s %s · %s · %s\n", tag, m.tag(it.Source), pretty(it.Source), relTime(m.now(), it.Updated), clip(it.ID, 12)))
		p := it.Payload
		wrap("  ", "Title: "+m.highlight(titleOf(&p)))
		if s := p.Summary; s != nil {
			if s.Goal != "" {
				wrap("  ", "Goal: "+m.highlight(s.Goal))
			}
			if s.Outcome != "" {
				wrap("  ", "Outcome: "+m.highlight(s.Outcome))
			}
			if len(s.FilesTouched) > 0 {
				wrap("  ", "Files: "+m.highlight(strings.Join(s.FilesTouched, ", ")))
			}
			if len(s.Commands) > 0 {
				wrap("  ", "Commands: "+strings.Join(s.Commands, ", "))
			}
			if s.LastAssistant != "" {
				wrap("  ", "Last message: "+m.highlight(s.LastAssistant))
			}
		}
		if len(p.WorkItems) > 0 {
			b.WriteString("  Work items:\n")
			for _, w := range p.WorkItems {
				ev := ""
				if len(w.Evidence) > 0 {
					ev = " (" + w.Evidence[0].Via + ")"
				}
				wrap("    ", fmt.Sprintf("%s %s [%s]%s", w.Kind, m.highlight(w.Key), w.Status, ev))
			}
		}
		if len(it.Diff) > 0 && tracked {
			b.WriteString("  " + m.th.Changed.Render("Since the last sync:") + "\n")
			for _, d := range it.Diff {
				st := m.th.Changed
				switch {
				case strings.HasPrefix(d, "+"):
					st = m.th.Added
				case strings.HasPrefix(d, "-"):
					st = m.th.Removed
				}
				wrap("    ", st.Render(d))
			}
		}
		b.WriteString("\n")
	}
	if pv.Total > len(pv.Items) {
		b.WriteString(m.th.Dim.Render(fmt.Sprintf(" showing the newest %d of %d sessions", len(pv.Items), pv.Total)) + "\n")
	}
	return b.String()
}

func (m *Model) onKeyPreview(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q", "p", "backspace":
		m.scr = scrProjects
		return m, nil
	case "P":
		m.pk.paranoid = !m.pk.paranoid
		m.pv.vp.GotoTop()
		m.pv.refresh(m)
		return m, nil
	case "r":
		delete(m.pv.cache, m.pv.key)
		m.pv.refresh(m)
		return m, m.fetchPreview(m.pv.key)
	case "s":
		return m, m.openSync()
	}
	var cmd tea.Cmd
	m.pv.vp, cmd = m.pv.vp.Update(k)
	return m, cmd
}

func (m *Model) viewPreview() string {
	pv := m.pv.cache[m.pv.key]
	label := m.pv.key
	for _, r := range m.snap.Rows {
		if r.Key == m.pv.key {
			label = r.Label
		}
	}
	mode := "summary view"
	if m.pk.paranoid {
		mode = "PARANOID: raw payloads"
	}
	top := fmt.Sprintf(" %s · level %s · %s", label, m.snap.Level, mode)
	switch r, _ := m.rowByKey(m.pv.key); {
	case r != nil && (r.State == NotShared || r.State == Denied):
		top += " · " + m.th.Dim.Render("not shared: this is what sharing it would send")
	case r != nil && r.Synced == 0:
		top += " · " + m.th.Warn.Render("never synced: everything here is new")
	default:
		if s := summarizeStatuses(pv.Items); s != "" {
			top += " · " + s
		}
	}
	if n := totalRedactions(pv.Redactions); n > 0 {
		top += fmt.Sprintf(" · %d redactions", n)
	}
	f := frame{th: m.th, w: m.w, h: m.h, title: m.header("preview"), right: m.serverBadge(), top: top}
	f.left = lines(m.pv.vp.View(), m.w-2, bodyHeight(m.h))
	f.hint = m.statusLine(m.hints(m.w-2, "↑/↓", "scroll", "P", "paranoid", "r", "reload", "s", "sync", "esc", "back"))
	return f.render()
}
