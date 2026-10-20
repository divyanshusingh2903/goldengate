package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestThemeNames(t *testing.T) {
	names := ThemeNames()
	if len(names) != len(Presets) || names[0] != "goldengate-light" || names[1] != "goldengate-dark" {
		t.Fatalf("Golden Gate's pair first, then the rest once each: %v", names)
	}
	for _, n := range names {
		if got, _ := ResolveTheme(n); got != n {
			t.Errorf("%s resolves to %s", n, got)
		}
	}
	for _, n := range []string{"", "auto", "nope"} {
		if got, _ := ResolveTheme(n); got != DefaultTheme {
			t.Errorf("%q gets the default, not %s", n, got)
		}
	}
	if ThemeTitle("goldengate-light") != "Golden Gate Light" || ThemeTitle("github-dark") != "GitHub Dark" || ThemeTitle("catppuccin-mocha") != "Catppuccin Mocha" {
		t.Error("display names")
	}
}

func TestThemeDropdown(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4", "enter") // the Theme row is first
	if h.m.st.dd != menuTheme {
		t.Fatal("enter opens the dropdown")
	}
	h.contains("┌ Theme", "✓ Golden Gate Light", "Golden Gate Dark", "Gruvbox Dark", "↑/↓ preview")
	h.golden("theme_menu")
	h.key("down") // previews at once, saves nothing yet
	if h.m.th.Name != "goldengate-dark" || len(h.be.saved) != 0 {
		t.Fatalf("preview: %s, saved %d", h.m.th.Name, len(h.be.saved))
	}
	h.key("esc")
	if h.m.st.dd != menuNone || h.m.th.Name != "goldengate-light" || len(h.be.saved) != 0 {
		t.Fatalf("esc reverts: %s, saved %d", h.m.th.Name, len(h.be.saved))
	}
	h.key("enter", "down", "down", "enter")
	if len(h.be.saved) != 1 || h.be.saved[0].Theme != ThemeNames()[2] {
		t.Fatalf("enter keeps and saves: %+v", h.be.saved)
	}
	h.contains(ThemeTitle(ThemeNames()[2]))
}

func TestThemeDropdownMouse(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4")
	h.click("Golden Gate Light  ▾") // a click on the value opens it
	if h.m.st.dd != menuTheme {
		t.Fatal("click opens the dropdown")
	}
	h.click("Golden Gate Dark")
	if h.m.st.dd != menuNone || len(h.be.saved) != 1 || h.be.saved[0].Theme != "goldengate-dark" {
		t.Fatalf("clicking an item chooses it: %+v", h.be.saved)
	}
	h.key("enter")
	h.clickAt(5, 25, false) // outside the menu
	if h.m.st.dd != menuNone || len(h.be.saved) != 1 {
		t.Fatal("a click outside closes it without saving")
	}
}

func TestLongDropdownsScroll(t *testing.T) {
	h := newHarness(t, 120, 40)
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, fmt.Sprintf("model-%02d", i))
	}
	h.be.models = map[string][]string{"gemini": many}
	h.be.summ = SummarizeSettings{Provider: "gemini"}
	h.key("4")
	for i, r := range h.m.settingRows() {
		if r.kind == setSumModel {
			h.m.st.cursor = i
		}
	}
	h.key("enter")
	v := h.view()
	if strings.Count(v, "model-") > h.m.menuRows() || !strings.Contains(v, "▼ ") || strings.Contains(v, "model-59") {
		t.Fatalf("a long list shows a window of %d rows:\n%s", h.m.menuRows(), v)
	}
	h.key("end")
	h.contains("model-59", "▲ ")
	if lines := strings.Split(h.view(), "\n"); len(lines) != 40 {
		t.Fatalf("the screen keeps its height: %d", len(lines))
	}
	for i := 0; i < 3; i++ { // the wheel scrolls it
		h.send(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	}
	if h.m.st.ddCursor != len(h.m.menuItems(menuModel))-1-3 {
		t.Errorf("the wheel moves the highlight: %d", h.m.st.ddCursor)
	}
	h.click("model-55")
	if s := h.be.saved[len(h.be.saved)-1].Summarize; s.Model != "model-55" {
		t.Fatalf("a visible item is clickable: %+v", s)
	}
}
