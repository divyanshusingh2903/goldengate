package tui

import "testing"

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
	if !h.m.st.dd {
		t.Fatal("enter opens the dropdown")
	}
	h.contains("┌ Theme", "✓ Golden Gate Light", "Golden Gate Dark", "Gruvbox Dark", "↑/↓ preview")
	h.golden("theme_menu")
	h.key("down") // previews at once, saves nothing yet
	if h.m.th.Name != "goldengate-dark" || len(h.be.saved) != 0 {
		t.Fatalf("preview: %s, saved %d", h.m.th.Name, len(h.be.saved))
	}
	h.key("esc")
	if h.m.st.dd || h.m.th.Name != "goldengate-light" || len(h.be.saved) != 0 {
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
	if !h.m.st.dd {
		t.Fatal("click opens the dropdown")
	}
	h.click("Golden Gate Dark")
	if h.m.st.dd || len(h.be.saved) != 1 || h.be.saved[0].Theme != "goldengate-dark" {
		t.Fatalf("clicking an item chooses it: %+v", h.be.saved)
	}
	h.key("enter")
	h.clickAt(5, 25, false) // outside the menu
	if h.m.st.dd || len(h.be.saved) != 1 {
		t.Fatal("a click outside closes it without saving")
	}
}
