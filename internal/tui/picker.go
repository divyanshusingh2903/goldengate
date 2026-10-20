package tui

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
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
		src := it.row.Sources[m.pk.srcCursor].Name
		return m, m.actKeys(Action{Kind: ActToggleSource, Keys: []string{it.row.Key}, Source: src}, "source")
	}
	return m, nil
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

func (m *Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.scr != scrProjects || m.modal != nil || m.help {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return m, m.moveCursor(-3)
	case tea.MouseButtonWheelDown:
		return m, m.moveCursor(3)
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionRelease {
			return m, nil
		}
		inner := m.w - 2
		leftW := inner
		if m.w >= splitMin {
			leftW = (inner - 1) / 2
		}
		if msg.X < 1 || msg.X > leftW {
			return m, nil
		}
		idx := m.pk.top + (msg.Y - 4) // rows start below the border, sources line, separator and filter line
		if msg.Y >= 4 && idx < len(m.items()) {
			return m, m.moveCursor(idx - m.pk.cursor)
		}
	}
	return m, nil
}

// --- views ---

// splitMin is the width from which the project card sits beside the list.
const splitMin = 78

func (m *Model) viewProjects() string {
	inner := m.w - 2
	split := m.w >= splitMin
	lw, rw := inner, 0
	if split {
		lw = (inner - 1) / 2
		rw = inner - lw - 1
	}
	bh := bodyHeight(m.h)
	f := frame{th: m.th, w: m.w, h: m.h, title: "goldengate", right: m.serverBadge(), top: m.sourcesLine(), split: split}
	f.left = m.listLines(lw, bh)
	if split {
		f.rght = m.cardLines(rw, bh)
	}
	f.hint = m.projectHints()
	return f.render()
}

func (m *Model) projectHints() string {
	if m.status != "" {
		return m.statusLine("")
	}
	p := m.pk
	switch {
	case p.filtering:
		return hints(m.th, m.w-2, "enter", "accept", "esc", "clear")
	case p.srcOpen:
		return hints(m.th, m.w-2, "space", "toggle source", "esc", "close")
	}
	pairs := []string{"space", "toggle", "a", "all", "d", "deny", "p", "preview", "s", "sync", "/", "filter", "t", "tree", "u", "undo", "?", "help"}
	if p.rangeMode {
		pairs = append([]string{"v", "end range"}, pairs...)
	}
	return hints(m.th, m.w-2, pairs...)
}

func stateGlyph(s ShareState) string {
	return [...]string{"[ ]", "[x]", "[~]", "[-]"}[s]
}

func (m *Model) stateStyled(s ShareState) string {
	g := stateGlyph(s)
	switch s {
	case Shared:
		return m.th.Good.Render(g)
	case Denied:
		return m.th.Bad.Render(g)
	case Partial:
		return m.th.Warn.Render(g)
	}
	return g
}

func (m *Model) listLines(w, h int) []string {
	p := &m.pk
	out := make([]string, 0, h)
	filter := "Filter: "
	switch {
	case p.filtering:
		filter += "/" + p.filter.Value() + "_"
	case p.query != "":
		filter += "/" + p.query
	default:
		filter += m.th.Dim.Render("/ to search")
	}
	meta := fmt.Sprintf("sort:%s", p.sort)
	if p.tree {
		meta += " tree"
	}
	if n := len(p.marks); n > 0 {
		meta = fmt.Sprintf("%d marked  %s", n, meta)
	}
	gap := w - lipglossWidth(filter) - lipglossWidth(meta) - 2
	if gap < 1 {
		out = append(out, " "+filter)
	} else {
		out = append(out, " "+filter+strings.Repeat(" ", gap)+m.th.Dim.Render(meta))
	}

	its := m.items()
	if !m.loaded {
		out = append(out, " "+m.th.Dim.Render("reading your session history…"))
	} else if len(its) == 0 {
		if p.query != "" {
			out = append(out, " "+m.th.Dim.Render("Nothing matches the filter."))
		} else {
			out = append(out, " "+m.th.Dim.Render("No projects found."), " "+m.th.Dim.Render("Is Claude Code, Codex or OpenCode installed?"))
		}
	}
	for i := p.top; i < len(its) && len(out) < h; i++ {
		line := m.rowLine(its[i], i == p.cursor, w)
		out = append(out, line)
	}
	return out
}

func lipglossWidth(s string) int { return len([]rune(stripANSI(s))) }

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

func (m *Model) rowLine(it item, cur bool, w int) string {
	cursor, mark := " ", " "
	if cur {
		cursor = ">"
	}
	if m.pk.marks[it.key()] {
		mark = m.th.Marked.Render("*")
	}
	label := ""
	suffix := ""
	switch {
	case it.node != nil:
		arrow := "  "
		if len(it.node.Children) > 0 {
			arrow = "▸ "
			if m.pk.expanded[it.node.Path] || m.pk.query != "" {
				arrow = "▾ "
			}
		}
		label = strings.Repeat("  ", it.node.Depth) + arrow + it.node.Name
		if it.row != nil && it.row.Pending > 0 {
			suffix = fmt.Sprintf("▲ %d", it.row.Pending)
		}
	default:
		label = it.row.Label
		if it.row.Pending > 0 {
			suffix = fmt.Sprintf("▲ %d", it.row.Pending)
		}
	}
	st := it.state()
	if it.row != nil {
		switch it.row.State {
		case Denied:
			suffix = "denied"
		case Partial:
			suffix = "(partial)"
		}
	}
	box := cursor + mark + m.stateStyled(st) + " "
	avail := w - 6 - lipglossWidth(suffix) - 1
	if suffix == "" {
		avail = w - 6
	}
	label = truncLeft(label, max(avail, 4))
	line := box + fit(label, max(avail, 4))
	if suffix != "" {
		line += " " + suffix
	}
	line = fit(line, w)
	if cur {
		return m.th.Cursor.Render(line)
	}
	return line
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

func (m *Model) cardLines(w, h int) []string {
	it, ok := m.current()
	if !ok {
		return []string{" " + m.th.Dim.Render("Select a project to see what would be shared.")}
	}
	var out []string
	add := func(s string) { out = append(out, " "+s) }
	if m.pk.srcOpen && it.row != nil {
		add(m.th.Title.Render("Sources of " + path.Base(it.row.Label)))
		add("")
		for i, s := range it.row.Sources {
			mk := "[ ]"
			if s.Shared {
				mk = "[x]"
			}
			line := fmt.Sprintf("%s %s (%d)", mk, pretty(s.Name), s.Sessions)
			if i == m.pk.srcCursor {
				line = m.th.Cursor.Render(fit(line, w-2))
			}
			add(line)
		}
		add("")
		add(m.th.Dim.Render("A source switched off here is never sent"))
		add(m.th.Dim.Render("for this project; the rest still is."))
		return out
	}
	if it.node != nil && it.row == nil {
		rs := it.node.descendants()
		shared := 0
		for _, r := range rs {
			if r.State == Shared {
				shared++
			}
		}
		add(m.th.Title.Render(it.node.Name))
		add(m.th.Dim.Render(it.node.Path))
		add("")
		add(fmt.Sprintf("%d project(s), %d shared", len(rs), shared))
		add("")
		add(m.th.Dim.Render("space shares this whole folder"))
		add(m.th.Dim.Render("(one rule covers everything below)"))
		return out
	}
	r := it.row
	add(m.th.Title.Render(path.Base(strings.TrimSuffix(r.Label, "/"))))
	add(r.Label)
	if r.Root != "" {
		add(m.th.Dim.Render("worktree of " + r.Root))
	}
	var srcs []string
	for _, s := range r.Sources {
		srcs = append(srcs, fmt.Sprintf("%s %d", s.Name, s.Sessions))
	}
	add("Sources: " + strings.Join(srcs, ", "))
	add("Last activity: " + relTime(m.now(), r.Last))
	add("Level: " + m.snap.Level)
	add("Status: " + m.describe(r))
	pv, have := m.pv.cache[r.Key]
	switch {
	case have && pv.Total > 0:
		add("")
		add(m.th.Title.Render("Recent sessions"))
		for _, it := range pv.Items {
			if len(out) >= h-3 {
				break
			}
			add(" • " + clip(titleOf(&it.Payload), w-6))
		}
		if n := totalRedactions(pv.Redactions); n > 0 {
			add(m.th.Redact.Render(fmt.Sprintf("Redactions in preview: %d", n)))
		} else {
			add(m.th.Dim.Render("Redactions in preview: 0"))
		}
		if s := summarizeStatuses(pv.Items); s != "" {
			add(m.th.Dim.Render("Since last sync: " + s))
		}
	case m.pv.loading[r.Key]:
		add("")
		add(m.th.Dim.Render("reading sessions…"))
	}
	return out
}

func (m *Model) describe(r *Row) string {
	switch r.State {
	case Shared:
		return "shared (" + r.Reason + ")"
	case Partial:
		var off []string
		for _, s := range r.Sources {
			if !s.Shared {
				off = append(off, s.Name)
			}
		}
		return "partly shared; off: " + strings.Join(off, ", ")
	case Denied:
		return "denied (" + r.Reason + ")"
	}
	return "not shared"
}

func totalRedactions(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
