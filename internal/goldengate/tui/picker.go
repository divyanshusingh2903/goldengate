package tui

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type sortMode int

const (
	sortRecent sortMode = iota
	sortName
	sortSize
)

func (s sortMode) String() string { return [...]string{"recent", "name", "size"}[s] }

type picker struct {
	cursor, top int
	marks       map[string]bool
	rangeMode   bool
	anchor      int
	rangeBase   map[string]bool
	filter      textinput.Model
	filtering   bool
	query       string
	sort        sortMode
	tree        bool
	expanded    map[string]bool
	srcOpen     bool
	srcCursor   int
	paranoid    bool
	view        listView
}

func newPicker(blink bool) picker {
	f := newInput("", false, blink)
	f.Prompt = ""
	return picker{marks: map[string]bool{}, expanded: map[string]bool{}, filter: f}
}

// item is one visible line: a project (list mode) or a node (tree mode).
type item struct {
	row  *Row
	node *treeNode
}

func (it item) key() string {
	if it.node != nil {
		return it.node.Path
	}
	return it.row.Key
}

func (it item) state() ShareState {
	if it.node != nil {
		return it.node.state()
	}
	return it.row.State
}

// items returns the visible lines after filtering, sorting and tree building.
func (m *Model) items() []item {
	type scored struct {
		r Row
		s int
	}
	var rs []scored
	for _, r := range m.snap.Rows {
		if !inView(m.pk.view, r) {
			continue
		}
		sc, ok := fuzzyScore(m.pk.query, r.Label+" "+r.Key)
		if !ok {
			continue
		}
		rs = append(rs, scored{r, sc})
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if m.pk.query != "" && rs[i].s != rs[j].s {
			return rs[i].s > rs[j].s
		}
		a, b := rs[i].r, rs[j].r
		switch m.pk.sort {
		case sortName:
			return a.Label < b.Label
		case sortSize:
			if a.Sessions != b.Sessions {
				return a.Sessions > b.Sessions
			}
		default:
			if !a.Last.Equal(b.Last) {
				return a.Last.After(b.Last)
			}
		}
		return a.Label < b.Label
	})
	rows := make([]Row, len(rs))
	for i := range rs {
		rows[i] = rs[i].r
	}
	if !m.pk.tree {
		out := make([]item, len(rows))
		for i := range rows {
			out[i] = item{row: &rows[i]}
		}
		return out
	}
	roots := buildTree(rows)
	exp := m.pk.expanded
	if m.pk.query != "" { // a search shows every match, unfolded
		exp = map[string]bool{}
		var all func([]*treeNode)
		all = func(ns []*treeNode) {
			for _, n := range ns {
				exp[n.Path] = true
				all(n.Children)
			}
		}
		all(roots)
	}
	var out []item
	for _, n := range flatten(roots, exp) {
		out = append(out, item{node: n, row: n.Row})
	}
	return out
}

func (p *picker) clamp(m *Model) {
	n := len(m.items())
	p.cursor = max(min(p.cursor, n-1), 0)
	vis := max(bodyHeight(m.h)-1, 1)
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+vis {
		p.top = p.cursor - vis + 1
	}
	p.top = max(min(p.top, max(n-vis, 0)), 0)
}

func (m *Model) current() (item, bool) {
	its := m.items()
	if m.pk.cursor < 0 || m.pk.cursor >= len(its) {
		return item{}, false
	}
	return its[m.pk.cursor], true
}

// targets are the keys an action applies to: the marked set, or the cursor row.
func (m *Model) targets() []string {
	if len(m.pk.marks) > 0 {
		keys := make([]string, 0, len(m.pk.marks))
		for k := range m.pk.marks {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	if it, ok := m.current(); ok {
		return []string{it.key()}
	}
	return nil
}

// targetState reports whether every target is already shared.
func (m *Model) allShared(keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	byKey := map[string]ShareState{}
	for _, it := range m.items() {
		byKey[it.key()] = it.state()
	}
	for _, k := range keys {
		if byKey[k] != Shared {
			return false
		}
	}
	return true
}

func (m *Model) moveCursor(delta int) tea.Cmd {
	m.pk.cursor += delta
	m.pk.clamp(m)
	if m.pk.rangeMode {
		its := m.items()
		lo, hi := min(m.pk.anchor, m.pk.cursor), max(m.pk.anchor, m.pk.cursor)
		marks := map[string]bool{}
		for k := range m.pk.rangeBase {
			marks[k] = true
		}
		for i := lo; i <= hi && i < len(its); i++ {
			marks[its[i].key()] = true
		}
		m.pk.marks = marks
	}
	return m.requestPreview()
}

func (m *Model) onKeyProjects(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := &m.pk
	if p.filtering {
		return m.onKeyFilter(k)
	}
	if p.srcOpen {
		return m.onKeySources(k)
	}
	s := k.String()
	switch s {
	case "up", "k":
		return m, m.moveCursor(-1)
	case "down", "j":
		return m, m.moveCursor(1)
	case "pgup", "ctrl+u":
		return m, m.moveCursor(-max(bodyHeight(m.h)-2, 1))
	case "pgdown", "ctrl+d":
		return m, m.moveCursor(max(bodyHeight(m.h)-2, 1))
	case "home", "g":
		return m, m.moveCursor(-len(m.items()))
	case "end", "G":
		return m, m.moveCursor(len(m.items()))
	case " ", "x":
		return m, m.shareToggle()
	case "d":
		return m, m.act(ActDeny, "deny")
	case "u":
		return m, m.undo()
	case "v":
		if p.rangeMode {
			p.rangeMode = false
			return m, nil
		}
		p.rangeMode, p.anchor, p.rangeBase = true, p.cursor, copyMarks(p.marks)
		if it, ok := m.current(); ok {
			p.marks[it.key()] = true
		}
		return m, nil
	case "a":
		for _, it := range m.items() {
			p.marks[it.key()] = true
		}
		return m, nil
	case "A":
		p.marks, p.rangeMode = map[string]bool{}, false
		return m, nil
	case "esc":
		switch {
		case p.rangeMode:
			p.rangeMode = false
		case len(p.marks) > 0:
			p.marks = map[string]bool{}
		case p.query != "":
			p.query = ""
			p.filter.SetValue("")
			p.clamp(m)
			return m, m.requestPreview()
		}
		return m, nil
	case "/":
		p.filtering = true
		p.filter.SetValue(p.query)
		p.filter.CursorEnd()
		return m, p.filter.Focus()
	case "t":
		p.tree = !p.tree
		if p.tree && len(p.expanded) == 0 {
			m.expandTop(buildTree(m.snap.Rows), 1)
		}
		p.cursor, p.top = 0, 0
		p.clamp(m)
		return m, m.requestPreview()
	case "f":
		return m, m.setView((p.view + 1) % 4)
	case "o":
		p.sort = (p.sort + 1) % 3
		p.clamp(m)
		return m, m.requestPreview()
	case "e":
		if it, ok := m.current(); ok && it.row != nil && len(it.row.Sources) > 0 {
			p.srcOpen, p.srcCursor = true, 0
		}
		return m, nil
	case "p":
		return m, m.openPreview()
	case "enter":
		if it, ok := m.current(); ok && it.node != nil && len(it.node.Children) > 0 {
			p.expanded[it.node.Path] = !p.expanded[it.node.Path]
			p.clamp(m)
			return m, nil
		}
		return m, m.openPreview()
	case "right", "l":
		if it, ok := m.current(); ok && it.node != nil && len(it.node.Children) > 0 {
			p.expanded[it.node.Path] = true
		}
		return m, nil
	case "left", "h":
		if it, ok := m.current(); ok && it.node != nil {
			p.expanded[it.node.Path] = false
			p.clamp(m)
		}
		return m, nil
	case "P":
		p.paranoid = !p.paranoid
		m.pv.refresh(m)
		return m, nil
	case "r":
		m.pv.cache = map[string]Preview{}
		m.setStatus("refreshing…", false)
		return m, m.loadSnapshot()
	}
	return m, nil
}

// expandTop unfolds the first levels of the tree the first time it is shown.
func (m *Model) expandTop(ns []*treeNode, depth int) {
	for _, n := range ns {
		if n.Depth <= depth {
			m.pk.expanded[n.Path] = true
		}
		m.expandTop(n.Children, depth)
	}
}

func copyMarks(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (m *Model) onKeyFilter(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := &m.pk
	switch k.String() {
	case "enter":
		p.filtering = false
		p.filter.Blur()
		return m, m.requestPreview()
	case "esc":
		p.filtering = false
		p.query = ""
		p.filter.SetValue("")
		p.filter.Blur()
		p.clamp(m)
		return m, m.requestPreview()
	}
	var cmd tea.Cmd
	p.filter, cmd = p.filter.Update(k)
	p.query = p.filter.Value()
	p.cursor, p.top = 0, 0
	p.clamp(m)
	return m, cmd
}

func (m *Model) onKeySources(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	it, ok := m.current()
	if !ok || it.row == nil {
		m.pk.srcOpen = false
		return m, nil
	}
	n := len(it.row.Sources)
	switch k.String() {
	case "esc", "e", "q":
		m.pk.srcOpen = false
	case "up", "k":
		m.pk.srcCursor = max(m.pk.srcCursor-1, 0)
	case "down", "j":
		m.pk.srcCursor = min(m.pk.srcCursor+1, n-1)
	case " ", "enter", "x":
		return m, m.toggleSource(it.row, m.pk.srcCursor)
	}
	return m, nil
}

// toggleSource switches one source of a project. On a project that is not
// shared at all, it shares the project with that source only.
func (m *Model) toggleSource(r *Row, i int) tea.Cmd {
	if r.State == Denied {
		m.setStatus("this project is denied; share it first (space on the project)", true)
		return nil
	}
	var others []string
	for j, s := range r.Sources {
		if j != i {
			others = append(others, s.Name)
		}
	}
	return m.actKeys(Action{Kind: ActToggleSource, Keys: []string{r.Key}, Source: r.Sources[i].Name, Others: others}, "source")
}

// shareToggle shares everything targeted, or un-shares it when it is all shared already.
func (m *Model) shareToggle() tea.Cmd {
	keys := m.targets()
	if len(keys) == 0 {
		return nil
	}
	if m.allShared(keys) {
		return m.actKeys(Action{Kind: ActRemove, Keys: keys}, "unshare")
	}
	return m.actKeys(Action{Kind: ActAllow, Keys: keys}, "share")
}

func (m *Model) act(kind ActionKind, verb string) tea.Cmd {
	keys := m.targets()
	if len(keys) == 0 {
		return nil
	}
	return m.actKeys(Action{Kind: kind, Keys: keys}, verb)
}

func (m *Model) actKeys(a Action, verb string) tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		res, err := be.Apply(ctx, a)
		return actionMsg{res: res, err: err, verb: verb}
	}
}

func (m *Model) undo() tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		msg, err := be.Undo(ctx)
		return actionMsg{res: ActionResult{Message: msg}, err: err, verb: "undo"}
	}
}

func (m *Model) onAction(msg actionMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
		return nil
	}
	m.pk.marks, m.pk.rangeMode = map[string]bool{}, false
	m.pv.cache = map[string]Preview{}
	m.setStatus(msg.res.Message, false)
	if r := msg.res.Remote; r != nil && r.Count > 0 && m.snap.Server != "" {
		h := r.Handle
		m.modal = &modal{
			title: "Server",
			lines: []string{fmt.Sprintf("%d project(s) are no longer shared.", r.Count), "Delete their data from the server too?"},
			yes: func(m *Model) tea.Cmd {
				be, ctx := m.be, m.ctx
				return func() tea.Msg {
					s, err := be.DeleteRemote(ctx, h)
					return remoteMsg{msg: s, err: err}
				}
			},
		}
	}
	return m.loadSnapshot()
}

// --- views ---

// listView narrows the list to one kind of project.
type listView int

const (
	viewAll listView = iota
	viewShared
	viewToSync
	viewLocal
)

var viewNames = [...]string{"All", "Shared", "To sync", "Not shared"}

// inView reports whether a row belongs to a list view.
func inView(v listView, r Row) bool {
	shared := r.State == Shared || r.State == Partial
	switch v {
	case viewShared:
		return shared
	case viewToSync:
		return shared && r.Pending > 0
	case viewLocal:
		return !shared
	}
	return true
}

// splitMin is the width from which the project card sits beside the list.
const splitMin = 78

// Screen lines of the projects screen.
const (
	yTop  = 1 // view chips
	yBody = 3 // first body line: search bar on the left, card on the right
	yRows = 4 // first list row
)

func (m *Model) viewProjects() string {
	inner := m.w - 2
	split := m.w >= splitMin
	lw, rw := inner, 0
	if split {
		lw = (inner - 1) / 2
		rw = inner - lw - 1
	}
	bh := bodyHeight(m.h)
	f := frame{th: m.th, w: m.w, h: m.h, title: m.header("goldengate"), right: m.serverBadge(), top: m.chipsLine(inner), split: split}
	f.left = m.listLines(lw, bh)
	if split {
		f.rght = m.cardLines(rw, bh, lw+2)
	}
	f.hint = m.projectHints()
	return f.render()
}

// header is the title bar: the wordmark and, when there is room, the screens
// as clickable tabs.
func (m *Model) header(screenName string) string {
	brand := m.th.Brand.Render("goldengate")
	if m.scr == scrWizard {
		return brand + " setup"
	}
	if m.w < 84 {
		if screenName != "goldengate" {
			return brand + m.th.Dim.Render(" › ") + screenName
		}
		return brand
	}
	cur := map[screen]int{scrProjects: 1, scrPreview: 1, scrSync: 2, scrServer: 3, scrSettings: 4}[m.scr]
	parts := []string{brand, " "}
	for i, name := range []string{"Projects", "Sync", "Server", "Settings"} {
		n := i + 1
		label := fmt.Sprintf("%d %s", n, name)
		m.clickText(0, label, func(m *Model) tea.Cmd { return m.goTo(n) })
		if n == cur {
			parts = append(parts, m.th.TabOn.Render("["+label+"]"))
		} else {
			parts = append(parts, m.th.Tab.Render(" "+label+" "))
		}
	}
	if m.scr == scrPreview {
		parts = append(parts, m.th.Dim.Render(" › preview"))
	}
	return strings.Join(parts, "")
}

// counts tallies the rows per list view.
func (m *Model) counts() [4]int {
	var n [4]int
	for _, r := range m.snap.Rows {
		for v := viewAll; v <= viewLocal; v++ {
			if inView(v, r) {
				n[v]++
			}
		}
	}
	return n
}

// chipsLine shows the list views with their counts; clicking one selects it.
func (m *Model) chipsLine(w int) string {
	n := m.counts()
	var b strings.Builder
	b.WriteString(" ")
	for v := viewAll; v <= viewLocal; v++ {
		label := fmt.Sprintf("%s %d", viewNames[v], n[v])
		vv := v
		m.clickText(yTop, label, func(m *Model) tea.Cmd { return m.setView(vv) })
		st := m.th.Chip
		switch {
		case v == m.pk.view:
			b.WriteString(m.th.ChipOn.Render("[" + label + "]"))
		case v == viewToSync && n[v] > 0:
			b.WriteString(st.Foreground(lipgloss.Color(m.th.Pal.Warn)).Render(" " + label + " "))
		default:
			b.WriteString(st.Render(" " + label + " "))
		}
		b.WriteString(" ")
	}
	if src := m.sourcesLine(); lipgloss.Width(b.String())+lipgloss.Width(src)+2 <= w {
		b.WriteString(strings.Repeat(" ", w-lipgloss.Width(b.String())-lipgloss.Width(src)) + src)
	}
	return b.String()
}

func (m *Model) setView(v listView) tea.Cmd {
	m.pk.view = v
	m.pk.cursor, m.pk.top = 0, 0
	m.pk.clamp(m)
	return m.requestPreview()
}

func (m *Model) projectHints() string {
	if m.status != "" {
		return m.statusLine("")
	}
	p := m.pk
	switch {
	case p.filtering:
		return m.hints(m.w-2, "enter", "accept", "esc", "clear")
	case p.srcOpen:
		return m.hints(m.w-2, "↑/↓", "move", "space", "send / don't send", "esc", "done")
	}
	share := "share"
	if m.allShared(m.targets()) {
		share = "unshare"
	}
	pairs := []string{"space", share, "p", "preview", "s", "sync", "f", "view", "/", "search", "d", "deny", "u", "undo", "t", "tree", "?", "help"}
	if p.rangeMode {
		pairs = append([]string{"v", "end range"}, pairs...)
	}
	return m.hints(m.w-2, pairs...)
}

func stateGlyph(s ShareState) string {
	return [...]string{"[ ]", "[x]", "[~]", "[-]"}[s]
}

func (m *Model) stateStyle(s ShareState) lipgloss.Style {
	switch s {
	case Shared:
		return m.th.Good.Bold(true)
	case Denied:
		return m.th.Bad.Bold(true)
	case Partial:
		return m.th.Warn.Bold(true)
	}
	return m.th.Dim
}

func (m *Model) listLines(w, h int) []string {
	p := &m.pk
	out := make([]string, 0, h)
	var search string
	switch {
	case p.filtering:
		search = m.th.Accent.Render("/ ") + p.filter.Value() + m.th.Accent.Render("▏")
	case p.query != "":
		search = m.th.Accent.Render("/ ") + p.query + m.th.Dim.Render("  (esc clears)")
	default:
		search = m.th.Dim.Render("/ search projects…")
	}
	m.clickKey(yBody, stripANSI(search), "/")
	layout := "list"
	if p.tree {
		layout = "tree"
	}
	sortL, layoutL := "sort "+p.sort.String(), "▤ "+layout
	m.clickKey(yBody, sortL, "o")
	m.clickKey(yBody, layoutL, "t")
	meta := m.th.Dim.Render(sortL + " · " + layoutL)
	if n := len(p.marks); n > 0 {
		marked := m.th.Marked.Render(fmt.Sprintf("%d marked", n))
		meta = marked + m.th.Dim.Render(" · ") + meta
		if w-lipgloss.Width(search)-lipgloss.Width(meta)-2 < 1 {
			meta = marked
		}
	}
	gap := w - lipgloss.Width(search) - lipgloss.Width(meta) - 2
	if gap < 1 {
		out = append(out, " "+search)
	} else {
		out = append(out, " "+search+strings.Repeat(" ", gap)+meta)
	}

	its := m.items()
	if !m.loaded {
		out = append(out, " "+m.th.Dim.Render("reading your session history…"))
	} else if len(its) == 0 {
		switch {
		case p.query != "":
			out = append(out, " "+m.th.Dim.Render("Nothing matches the search."))
		case p.view != viewAll:
			out = append(out, " "+m.th.Dim.Render("No projects in this view."), " "+m.th.Dim.Render("Press f (or click All) to see every project."))
		default:
			out = append(out, " "+m.th.Dim.Render("No projects found."), " "+m.th.Dim.Render("Is Claude Code, Codex or OpenCode installed?"))
		}
	}
	// the source tags get a column of their own when the list is wide enough
	tw := 0
	for _, it := range its {
		if it.row != nil {
			tw = max(tw, lipgloss.Width(stripANSI(m.tags(it.row))))
		}
	}
	if w-6-min(statusCol, max(w/3, 8))-1-tw-1 < 22 {
		tw = 0
	}
	for i := p.top; i < len(its) && len(out) < h; i++ {
		y := yRows + i - p.top
		idx := i
		m.addZone(1, 1+w, y, func(m *Model) tea.Cmd {
			onBox := m.ck.msg.X >= 3 && m.ck.msg.X < 6
			return m.clickRow(idx, onBox, m.ck.msg.Ctrl)
		})
		out = append(out, m.rowLine(its[i], i == p.cursor, w, tw))
	}
	return out
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// statusCol is the width of the sync column on the right of each row.
const statusCol = 15

// rowStatus is the short sync state shown at the end of a row.
func (m *Model) rowStatus(it item) (string, lipgloss.Style) {
	r := it.row
	if r == nil {
		rs := it.node.descendants()
		pending := 0
		for _, d := range rs {
			pending += d.Pending
		}
		if pending > 0 {
			return fmt.Sprintf("▲ %d to sync", pending), m.th.Warn
		}
		return fmt.Sprintf("%d projects", len(rs)), m.th.Dim
	}
	switch r.State {
	case Denied:
		return "✕ denied", m.th.Bad
	case NotShared:
		return "not shared", m.th.Dim
	}
	switch {
	case r.Pending > 0:
		return fmt.Sprintf("▲ %d to sync", r.Pending), m.th.Warn
	case r.Synced > 0:
		return "✓ synced " + shortAge(m.now(), r.LastSync), m.th.Good
	}
	return "nothing to send", m.th.Dim
}

// shortAge is a compact age for the row column: 5m, 3h, 2d, 6w.
func shortAge(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return ""
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 1))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dw", int(d.Hours()/24/7))
}

// rowLine draws one list row; tw is the width of the source-tag column (0: none).
func (m *Model) rowLine(it item, cur bool, w, tw int) string {
	paint := func(st lipgloss.Style, s string) string {
		if cur {
			st = st.Background(m.th.CurBg)
		}
		return st.Render(s)
	}
	base := m.th.Plain
	if cur {
		base = m.th.Title
	}
	cursor, mark := paint(base, " "), paint(base, " ")
	if cur {
		cursor = paint(m.th.Accent, "▌")
	}
	if m.pk.marks[it.key()] {
		mark = paint(m.th.Marked, "•")
	}
	var label string
	if it.node != nil {
		arrow := "  "
		if len(it.node.Children) > 0 {
			arrow = "▸ "
			if m.pk.expanded[it.node.Path] || m.pk.query != "" {
				arrow = "▾ "
			}
		}
		label = strings.Repeat("  ", it.node.Depth) + arrow + it.node.Name
	} else {
		label = it.row.Label
	}
	st := it.state()
	status, sst := m.rowStatus(it)
	sw := min(statusCol, max(w/3, 8))
	avail := max(w-6-sw-1, 4)
	tags := ""
	if tw > 0 {
		avail -= tw + 1
		var ts []string
		if it.row != nil {
			for _, s := range it.row.Sources {
				md := metaOf(s.Name)
				ts = append(ts, paint(m.kindStyle(md.kind), md.tag))
			}
		}
		tags = strings.Join(ts, paint(base, " "))
		if pad := tw - lipgloss.Width(tags); pad > 0 {
			tags = paint(base, strings.Repeat(" ", pad)) + tags
		}
		tags += paint(base, " ")
	}
	labelSt := base
	if st == NotShared || st == Denied {
		if !cur {
			labelSt = m.th.Dim
		}
	}
	line := cursor + mark + paint(m.stateStyle(st), stateGlyph(st)) + paint(base, " ") +
		paint(labelSt, fit(truncLeft(label, avail), avail)) + paint(base, " ") + tags +
		paint(sst, fitRight(status, sw))
	if pad := w - lipgloss.Width(line); pad > 0 {
		line += paint(base, strings.Repeat(" ", pad))
	}
	return line
}

// fitRight right-aligns s in w cells.
func fitRight(s string, w int) string {
	if lipgloss.Width(s) >= w {
		return fit(s, w)
	}
	return strings.Repeat(" ", w-lipgloss.Width(s)) + s
}

// relTime is a short human time since t.
func relTime(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}

// cardLines is the right pane: what the highlighted project is, whether it
// reaches the server, and what the next sync would do with it. x0 is the
// pane's first screen column, for click zones.
func (m *Model) cardLines(w, h, x0 int) []string {
	it, ok := m.current()
	if !ok {
		return []string{" " + m.th.Dim.Render("Select a project to see what would be shared.")}
	}
	var out []string
	add := func(s string) { out = append(out, " "+s) }
	y := func() int { return yBody + len(out) }
	para := func(st lipgloss.Style, s string) {
		for _, l := range strings.Split(ansi.Wrap(s, max(w-3, 10), " "), "\n") {
			add(st.Render(l))
		}
	}
	field := func(name, val string) { add(m.th.Label.Render(padRight(name, 10)) + val) }

	if m.pk.srcOpen && it.row != nil {
		r := it.row
		add(m.th.Title.Render("Which tools to send from " + path.Base(r.Label)))
		add("")
		switch r.State {
		case NotShared:
			para(m.th.Dim, "○ Not shared yet. Tick a tool to share only its sessions from this project.")
		case Denied:
			para(m.th.Bad, "✕ Denied. Share the project first (space on it); then choose its tools here.")
		default:
			para(m.th.Dim, "● Shared. Untick a tool to stop sending its sessions; the others are still sent.")
		}
		add("")
		for i, s := range r.Sources {
			i := i
			cur := i == m.pk.srcCursor
			paint := func(st lipgloss.Style, t string) string {
				if cur {
					st = st.Background(m.th.CurBg)
				}
				return st.Render(t)
			}
			bar, box, state, sst := " ", "[ ]", "not sent", m.th.Dim
			if cur {
				bar = "▌"
			}
			switch {
			case s.Shared:
				box, state, sst = "[x]", "✓ sent", m.th.Good
			case r.State == Shared || r.State == Partial:
				state, sst = "✗ off", m.th.Warn
			}
			bst := m.th.Dim
			if s.Shared {
				bst = m.th.Good.Bold(true)
			}
			md := metaOf(s.Name)
			line := paint(m.th.Accent, bar) + paint(bst, box) + paint(m.th.Plain, " ") + paint(m.kindStyle(md.kind), fit(md.tag, 3)) +
				paint(m.th.Plain, fit(pretty(s.Name), 14)) + paint(m.th.Dim, fitRight(plural(s.Sessions, "session"), 13)+"  ") + paint(sst, state)
			if pad := w - 2 - lipgloss.Width(line); pad > 0 {
				line += paint(m.th.Plain, strings.Repeat(" ", pad))
			}
			m.addZone(x0, x0+w, y(), func(m *Model) tea.Cmd {
				m.pk.srcCursor = i
				if it, ok := m.current(); ok && it.row != nil {
					return m.toggleSource(it.row, i)
				}
				return nil
			})
			out = append(out, line)
		}
		add("")
		add(m.button(m.th.Button, y(), "esc", "Done"))
		return out
	}
	if it.node != nil && it.row == nil {
		rs := it.node.descendants()
		shared, pending := 0, 0
		for _, r := range rs {
			if r.State == Shared || r.State == Partial {
				shared++
			}
			pending += r.Pending
		}
		add(m.th.Title.Render(it.node.Name))
		add(m.th.Dim.Render(it.node.Path))
		add("")
		field("Projects", fmt.Sprintf("%d, %d shared", len(rs), shared))
		if pending > 0 {
			field("To sync", m.th.Warn.Render(fmt.Sprintf("▲ %d sessions", pending)))
		}
		add("")
		label := "Share folder"
		if it.state() == Shared {
			label = "Unshare folder"
		}
		for _, l := range m.buttonRows(y(), w-2, btn{m.th.ButtonGo, "space", label}, btn{m.th.Button, "enter", "Fold"}) {
			add(l)
		}
		add("")
		para(m.th.Dim, "One rule covers everything below this folder, including projects you start later.")
		return out
	}

	r := it.row
	add(m.th.Title.Render(path.Base(strings.TrimSuffix(r.Label, "/"))))
	add(m.th.Dim.Render(r.Label))
	if r.Root != "" {
		add(m.th.Dim.Render("worktree of " + r.Root))
	}
	add("")
	pv, have := m.pv.cache[r.Key]
	head, detail := m.syncState(r, pv, have)
	add(head)
	para(m.th.Dim, detail)
	add("")

	field("Kind", m.kindLabel(rowKind(r)))
	var srcs []string
	for _, s := range r.Sources {
		t := m.tag(s.Name) + " " + fmt.Sprintf("%s %d", pretty(s.Name), s.Sessions)
		if !s.Shared && r.State == Partial {
			t += m.th.Dim.Render(" (off)")
		}
		srcs = append(srcs, t)
	}
	if inline := fmt.Sprintf("%d · %s", r.Sessions, strings.Join(srcs, ", ")); lipgloss.Width(inline) <= w-12 {
		field("Sessions", inline)
	} else {
		field("Sessions", fmt.Sprintf("%d", r.Sessions))
		for _, s := range srcs {
			add(strings.Repeat(" ", 10) + s)
		}
	}
	field("Activity", relTime(m.now(), r.Last))
	if r.State == Shared || r.State == Partial {
		last := "never"
		if r.Synced > 0 {
			last = fmt.Sprintf("%s · %d on the server", relTime(m.now(), r.LastSync), r.Synced)
		}
		field("Synced", last)
	}
	field("Level", m.snap.Level)
	if r.Reason != "" && r.State != NotShared {
		field("Rule", m.th.Dim.Render(r.Reason))
	}
	add("")

	var bs []btn
	switch r.State {
	case Shared, Partial:
		if r.Pending > 0 {
			bs = append(bs, btn{m.th.ButtonGo, "s", "Sync now"})
		}
		bs = append(bs, btn{m.th.Button, "space", "Unshare"})
	case Denied:
		bs = append(bs, btn{m.th.Button, "space", "Share anyway"})
	default:
		bs = append(bs, btn{m.th.ButtonGo, "space", "Share"})
	}
	bs = append(bs, btn{m.th.Button, "p", "Preview"})
	if len(r.Sources) > 1 {
		bs = append(bs, btn{m.th.Button, "e", "Sources"})
	}
	for _, l := range m.buttonRows(y(), w-2, bs...) {
		add(l)
	}

	switch {
	case have && pv.Total > 0 && len(out) < h-4:
		add("")
		title := "Recent sessions"
		if r.State == NotShared || r.State == Denied {
			title = "Recent sessions (not sent)"
		}
		add(m.th.Title.Render(title))
		for _, it := range pv.Items {
			if len(out) >= h-2 {
				break
			}
			tag := ""
			if (r.State == Shared || r.State == Partial) && r.Synced > 0 {
				tag = map[string]string{"new": m.th.Added.Render(" · new"), "changed": m.th.Changed.Render(" · changed")}[it.Status]
			}
			if it.Payload.Origin != "" {
				tag = m.th.Dim.Render(" · "+it.Payload.Origin) + tag // started by a command, not a chat
			}
			tg := m.tag(it.Source)
			add(" " + tg + " " + clip(titleOf(&it.Payload), w-5-lipgloss.Width(tg)-lipgloss.Width(tag)) + tag)
		}
		if n := totalRedactions(pv.Redactions); n > 0 {
			add(m.th.Accent.Render(fmt.Sprintf("%d secret(s) redacted", n)) + m.th.Dim.Render(" before sending"))
		}
	case m.pv.loading[r.Key]:
		add("")
		add(m.th.Dim.Render("reading sessions…"))
	}
	return out
}

// syncState is the card's headline and one-sentence explanation: whether the
// project reaches the server and what the next sync would do with it.
func (m *Model) syncState(r *Row, pv Preview, have bool) (string, string) {
	sessions := func(n int) string {
		if n == 1 {
			return "1 session"
		}
		return fmt.Sprintf("%d sessions", n)
	}
	switch r.State {
	case Denied:
		return m.th.Bad.Bold(true).Render("✕ Denied"),
			"Never sent, even when a parent folder is shared. Share it to lift the denial."
	case NotShared:
		d := "Nothing from this project leaves your machine."
		if have && pv.Total > 0 {
			d += " If you share it, the next sync sends " + sessions(pv.Total) + "."
		}
		return m.th.Dim.Bold(true).Render("○ Not shared"), d
	}
	head := m.th.Good.Bold(true).Render("● Shared")
	if r.State == Partial {
		var off []string
		for _, s := range r.Sources {
			if !s.Shared {
				off = append(off, pretty(s.Name))
			}
		}
		head = m.th.Warn.Bold(true).Render("◐ Partly shared") + m.th.Dim.Render(" · "+strings.Join(off, ", ")+" off")
	}
	switch {
	case r.Synced == 0 && r.Pending > 0:
		return head + m.th.Warn.Render(" · never synced"),
			"Not on the server yet. The next sync sends " + sessions(r.Pending) + "."
	case r.Pending > 0:
		d := sessions(r.Pending) + " waiting: "
		if have {
			d += changeSummary(pv.Items) + " since the last sync."
		} else {
			d += "new or changed since the last sync."
		}
		return head + m.th.Warn.Render(fmt.Sprintf(" · ▲ %d to sync", r.Pending)), d
	case r.Synced > 0:
		return head + m.th.Good.Render(" · up to date"), "The server has everything from this project."
	}
	return head, "No sessions in the sync window yet; new ones will go out on the next sync."
}

// changeSummary says what kind of changes are waiting, e.g. "2 new, 1 changed".
func changeSummary(items []PreviewItem) string {
	n := map[string]int{}
	for _, it := range items {
		n[it.Status]++
	}
	var parts []string
	for _, s := range []string{"new", "changed"} {
		if n[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[s], s))
		}
	}
	if len(parts) == 0 {
		return "new or changed"
	}
	return strings.Join(parts, ", ")
}

func totalRedactions(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
