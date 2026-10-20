package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProjectsScreen80x24(t *testing.T) {
	h := newHarness(t, 80, 24)
	h.checkGeometry(80, 24)
	h.golden("projects_80x24")
}

func TestProjectsScreen120x40(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.checkGeometry(120, 40)
	h.golden("projects_120x40")
}

func TestNarrowTerminalUsesOnePane(t *testing.T) {
	h := newHarness(t, 64, 20)
	h.checkGeometry(64, 20)
	if strings.Contains(h.view(), "┬") {
		t.Error("below the split width there is a single pane")
	}
	h.golden("projects_64x20")
}

func TestTooSmallTerminal(t *testing.T) {
	h := newHarness(t, 50, 12)
	h.contains("needs at least 60x16", "50x12")
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30}) // growing back recovers
	h.contains("Filter:")
	h.checkGeometry(100, 30)
}

func TestResizeKeepsGeometryAtEverySize(t *testing.T) {
	h := newHarness(t, 120, 40)
	for _, sz := range [][2]int{{80, 24}, {100, 30}, {60, 16}, {200, 60}, {79, 24}, {78, 24}, {77, 24}, {120, 40}} {
		h.send(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		h.checkGeometry(sz[0], sz[1])
	}
}

func TestNavigationClampsAndScrolls(t *testing.T) {
	be := newFake()
	for i := 0; i < 60; i++ {
		be.rows = append(be.rows, Row{Key: fmt.Sprintf("/home/u/many/p%02d", i), Label: fmt.Sprintf("~/many/p%02d", i), Sessions: 1, Last: ago(time.Duration(300+i) * time.Hour), State: NotShared,
			Sources: []SourceInfo{{"claude-code", 1, false}}})
	}
	h := newHarnessWith(t, be, 80, 24)
	h.key("up", "k")
	if h.m.pk.cursor != 0 {
		t.Fatalf("cannot go above the first row: %d", h.m.pk.cursor)
	}
	h.key("G")
	if h.m.pk.cursor != 67 {
		t.Fatalf("G jumps to the end: %d", h.m.pk.cursor)
	}
	h.contains("p59")
	h.checkGeometry(80, 24)
	h.key("down")
	if h.m.pk.cursor != 67 {
		t.Fatal("cannot go below the last row")
	}
	h.key("g")
	h.contains("pharmfindr")
	h.key("pgdown", "pgdown")
	if h.m.pk.cursor < 20 || h.m.pk.top == 0 {
		t.Errorf("page down scrolls: cursor=%d top=%d", h.m.pk.cursor, h.m.pk.top)
	}
	h.checkGeometry(80, 24)
}

func TestFuzzyFilter(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("/")
	h.typeText("pharm")
	h.contains("Filter: /pharm_")
	h.lacks("~/code/forge", "client-x")
	h.contains("~/code/pharmfindr")
	h.key("enter")
	if h.m.pk.filtering || h.m.pk.query != "pharm" {
		t.Fatalf("enter accepts the filter: filtering=%v query=%q", h.m.pk.filtering, h.m.pk.query)
	}
	h.key("esc")
	if h.m.pk.query != "" {
		t.Fatal("esc clears an accepted filter")
	}
	h.contains("~/code/forge")
	// subsequence matching: "cfg" matches ~/code/forge
	h.key("/")
	h.typeText("cfg")
	h.contains("~/code/forge")
	h.lacks("website")
	h.key("esc")
	h.contains("website")
	h.key("/")
	h.typeText("zzzz")
	h.contains("Nothing matches the filter.")
}

func TestFuzzyScore(t *testing.T) {
	if _, ok := fuzzyScore("xyz", "~/code/app"); ok {
		t.Error("not a subsequence")
	}
	a, _ := fuzzyScore("app", "~/code/app")
	b, _ := fuzzyScore("app", "~/code/apple-pie-thing")
	if a <= b {
		t.Errorf("a tighter match must outrank a loose one: %d <= %d", a, b)
	}
	if s, ok := fuzzyScore("", "anything"); !ok || s != 0 {
		t.Error("empty pattern matches everything")
	}
	if _, ok := fuzzyScore("PHARM", "~/code/pharmfindr"); !ok {
		t.Error("case-insensitive")
	}
	// word-start bonus: "cf" prefers ~/code/forge over a mid-word match
	x, _ := fuzzyScore("cf", "~/code/forge")
	y, _ := fuzzyScore("cf", "~/abcfd/zzzz")
	if x <= y {
		t.Errorf("word starts score higher: %d <= %d", x, y)
	}
}

func TestSpaceTogglesShareState(t *testing.T) {
	h := newHarness(t, 100, 30)
	// the first row (pharmfindr) is shared, so space un-shares it
	h.key("space")
	if len(h.be.actions) != 1 || h.be.actions[0].Kind != ActRemove || h.be.actions[0].Keys[0] != "/home/u/code/pharmfindr" {
		t.Fatalf("actions: %+v", h.be.actions)
	}
	h.contains("[ ] ~/code/pharmfindr", "1 changed")
	h.key("space") // and again: shares it
	if h.be.actions[1].Kind != ActAllow {
		t.Fatalf("second space shares: %+v", h.be.actions[1])
	}
	h.contains("[x] ~/code/pharmfindr")
	// a not-shared row is shared
	h.key("down", "down", "down") // vihayas
	h.key("space")
	if a := h.be.actions[len(h.be.actions)-1]; a.Kind != ActAllow || a.Keys[0] != "/home/u/code/vihayas" {
		t.Fatalf("%+v", a)
	}
}

func TestMarksRangeAndBulkActions(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("v", "down", "down")
	if len(h.m.pk.marks) != 3 {
		t.Fatalf("range v + 2 moves marks 3 rows: %v", h.m.pk.marks)
	}
	h.key("up") // shrinking the range unmarks the row left behind
	if len(h.m.pk.marks) != 2 {
		t.Fatalf("range shrinks: %v", h.m.pk.marks)
	}
	h.key("down", "v") // end range, keep marks
	if h.m.pk.rangeMode || len(h.m.pk.marks) != 3 {
		t.Fatalf("v ends range mode: %v", h.m.pk.marks)
	}
	h.contains("3 marked", "*")
	h.key("space")
	a := h.be.actions[0]
	// pharmfindr, worktree, forge are all shared: bulk space un-shares them all
	if a.Kind != ActRemove || len(a.Keys) != 3 {
		t.Fatalf("bulk: %+v", a)
	}
	if len(h.m.pk.marks) != 0 {
		t.Error("marks clear after an action")
	}
	// a marks everything visible, A clears
	h.key("a")
	if len(h.m.pk.marks) != 8 {
		t.Fatalf("a marks all visible: %d", len(h.m.pk.marks))
	}
	h.key("A")
	if len(h.m.pk.marks) != 0 {
		t.Fatal("A clears marks")
	}
	// esc clears marks first, then leaves the filter alone
	h.key("a", "esc")
	if len(h.m.pk.marks) != 0 {
		t.Fatal("esc clears marks")
	}
	// marking under a filter marks only what is visible
	h.key("/")
	h.typeText("forge")
	h.key("enter", "a")
	if len(h.m.pk.marks) != 1 {
		t.Fatalf("filtered mark all: %v", h.m.pk.marks)
	}
}

func TestDenyAndUndo(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("down", "down", "down", "d")
	if a := h.be.actions[0]; a.Kind != ActDeny || a.Keys[0] != "/home/u/code/vihayas" {
		t.Fatalf("%+v", a)
	}
	h.contains("[-] ~/code/vihayas")
	h.key("u")
	if h.be.undos != 1 {
		t.Fatal("u calls Undo")
	}
	h.contains("undid the last change")
}

func TestActionErrorsAreShown(t *testing.T) {
	be := newFake()
	be.applyErr = fmtErr("denied by ~/work/*; edit the config")
	h := newHarnessWith(t, be, 100, 30)
	h.key("space")
	h.contains("denied by ~/work/*")
	h.key("j") // the message clears on the next key
	h.lacks("denied by ~/work/*")
}

type strErr string

func (e strErr) Error() string { return string(e) }
func fmtErr(s string) error    { return strErr(s) }

func TestSortCycle(t *testing.T) {
	h := newHarness(t, 100, 30)
	first := func() string { it, _ := h.m.current(); return it.row.Label }
	if first() != "~/code/pharmfindr" {
		t.Fatalf("recent first: %s", first())
	}
	h.key("o")
	if h.m.pk.sort != sortName || first() != "claude-ai:Chats" {
		t.Fatalf("name order: %s", first())
	}
	h.contains("sort:name")
	h.key("o")
	if h.m.pk.sort != sortSize || first() != "~/code/pharmfindr" { // 47 sessions
		t.Fatalf("size order: %s", first())
	}
	h.key("o")
	if h.m.pk.sort != sortRecent {
		t.Fatal("cycles back")
	}
}

func TestSourceEditor(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("e")
	h.contains("Sources of pharmfindr", "[x] Claude Code (41)", "[x] Codex (6)")
	h.golden("source_editor")
	h.key("down", "space")
	if a := h.be.actions[0]; a.Kind != ActToggleSource || a.Source != "codex" || a.Keys[0] != "/home/u/code/pharmfindr" {
		t.Fatalf("%+v", a)
	}
	h.key("esc")
	if h.m.pk.srcOpen {
		t.Fatal("esc closes the editor")
	}
}

func TestTreeMode(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("t")
	h.contains("tree")
	h.golden("tree_top")
	// folders aggregate state: ~/code has shared and unshared children
	items := h.m.items()
	var code *item
	for i := range items {
		if items[i].node != nil && strings.HasSuffix(items[i].node.Path, "/code") {
			code = &items[i]
		}
	}
	if code == nil {
		t.Fatalf("no ~/code folder in the tree: %v", labels(items))
	}
	if code.state() != Partial {
		t.Errorf("mixed folder is partial, got %v", code.state())
	}
	// move to the folder and share the whole thing with one action
	for i, it := range items {
		if it.node == code.node {
			h.m.pk.cursor = i
		}
	}
	h.key("space")
	a := h.be.actions[0]
	if a.Kind != ActAllow || len(a.Keys) != 1 || !strings.HasSuffix(a.Keys[0], "/code") {
		t.Fatalf("selecting a folder writes one rule for the folder: %+v", a)
	}
	// collapse and expand
	h.key("h")
	if h.m.pk.expanded[code.node.Path] {
		t.Fatal("h collapses")
	}
	h.golden("tree_collapsed")
	h.key("l")
	if !h.m.pk.expanded[code.node.Path] {
		t.Fatal("l expands")
	}
	// a filter unfolds everything it matches
	h.key("/")
	h.typeText("forge")
	h.key("enter")
	h.contains("forge")
	h.lacks("vihayas")
	h.key("t")
	if h.m.pk.tree {
		t.Fatal("t leaves tree mode")
	}
}

func labels(items []item) []string {
	var out []string
	for _, it := range items {
		if it.node != nil {
			out = append(out, it.node.Path)
		} else {
			out = append(out, it.row.Label)
		}
	}
	return out
}

func TestTreeBuildsCompactFoldersAndChatGroups(t *testing.T) {
	rows := []Row{
		{Key: "/home/u/a/b/c/x", Label: "x"}, {Key: "/home/u/a/b/c/y", Label: "y"},
		{Key: "/home/u/repo", Label: "repo"}, {Key: "/home/u/repo/.claude/worktrees/fox", Label: "wt"},
		{Key: "claude-ai:Chats", Label: "claude-ai:Chats", Virtual: true}, {Key: "chatgpt:Chats", Label: "chatgpt:Chats", Virtual: true},
	}
	roots := buildTree(rows)
	var names []string
	for _, n := range roots {
		names = append(names, n.Name)
	}
	if strings.Join(names, ",") != "home/u,claude-ai:,chatgpt:" && strings.Join(names, ",") != "home/u,chatgpt:,claude-ai:" {
		t.Fatalf("roots: %v", names)
	}
	home := roots[0]
	var sub []string
	for _, c := range home.Children {
		sub = append(sub, c.Name)
	}
	if strings.Join(sub, ",") != "a/b/c,repo" {
		t.Errorf("single-child chains collapse into one row: %v", sub)
	}
	repo := home.Children[1]
	if repo.Row == nil || len(repo.Children) != 1 || repo.Children[0].Name != ".claude/worktrees/fox" {
		t.Errorf("a project can also be a folder: %+v", repo)
	}
	for _, n := range roots[1:] {
		if n.Path != strings.TrimSuffix(n.Name, ":")+":*" {
			t.Errorf("chat group rule is a virtual glob: %s", n.Path)
		}
	}
}

func TestPendingAndStateGlyphs(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.contains("[x]", "[ ]", "[~]", "[-]", "▲ 3", "(partial)", "denied")
}

func TestMouse(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.send(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if h.m.pk.cursor != 3 {
		t.Fatalf("wheel scrolls: %d", h.m.pk.cursor)
	}
	h.send(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if h.m.pk.cursor != 0 {
		t.Fatalf("wheel up: %d", h.m.pk.cursor)
	}
	h.send(tea.MouseMsg{X: 5, Y: 6, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}) // third row
	if h.m.pk.cursor != 2 {
		t.Fatalf("click selects the row under the pointer: %d", h.m.pk.cursor)
	}
	h.send(tea.MouseMsg{X: 90, Y: 6, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}) // right pane: ignored
	if h.m.pk.cursor != 2 {
		t.Fatal("clicks outside the list do nothing")
	}
}
