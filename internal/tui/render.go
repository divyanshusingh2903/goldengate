package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// defaultRenderer honors NO_COLOR and dumb terminals through termenv. The
// light/dark choice is the terminal's own answer (Bubble Tea asks at start-up),
// unless GOLDENGATE_THEME=light|dark overrides it, which also helps terminals
// that cannot answer.
func defaultRenderer() *lipgloss.Renderer {
	r := lipgloss.DefaultRenderer()
	if os.Getenv("NO_COLOR") != "" {
		r.SetColorProfile(termenv.Ascii)
	}
	if dark, ok := themeOverride(os.Getenv); ok {
		r.SetHasDarkBackground(dark)
	}
	return r
}

// themeOverride reads GOLDENGATE_THEME.
func themeOverride(getenv func(string) string) (dark, ok bool) {
	switch strings.ToLower(getenv("GOLDENGATE_THEME")) {
	case "light":
		return false, true
	case "dark":
		return true, true
	}
	return false, false
}

// PlainRenderer returns a renderer that never emits color or styling: used
// by tests so snapshots are readable text.
func PlainRenderer() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(os.Stdout)
	r.SetColorProfile(termenv.Ascii)
	r.SetHasDarkBackground(true)
	return r
}
