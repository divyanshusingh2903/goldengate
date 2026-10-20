package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Theme holds every style the UI uses. Colors adapt to light and dark
// backgrounds; with NO_COLOR (or a dumb terminal) lipgloss drops them and the
// UI relies on glyphs and layout alone, so nothing is conveyed by color only.
type Theme struct {
	Title, Dim, Accent, Good, Warn, Bad lipgloss.Style
	Cursor, Marked, Redact, Key, Border lipgloss.Style
	Added, Removed, Changed             lipgloss.Style
}

func ac(light, dark string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: light, Dark: dark}
}

// NewTheme builds the theme on a renderer (tests pass one pinned to no color).
func NewTheme(r *lipgloss.Renderer) Theme {
	s := r.NewStyle
	return Theme{
		Title:   s().Bold(true),
		Dim:     s().Foreground(ac("#6b6b76", "#8b8b96")),
		Accent:  s().Foreground(ac("#1a5fb4", "#78aeed")).Bold(true),
		Good:    s().Foreground(ac("#1c7a3c", "#57e389")),
		Warn:    s().Foreground(ac("#9a6700", "#f5c211")),
		Bad:     s().Foreground(ac("#b00020", "#ff7b72")),
		Cursor:  s().Reverse(true),
		Marked:  s().Foreground(ac("#8a2be2", "#c061cb")).Bold(true),
		Redact:  s().Foreground(ac("#b00020", "#ff7b72")).Bold(true).Underline(true),
		Key:     s().Foreground(ac("#1a5fb4", "#78aeed")).Bold(true),
		Border:  s().Foreground(ac("#8b8b96", "#5e5e6b")),
		Added:   s().Foreground(ac("#1c7a3c", "#57e389")),
		Removed: s().Foreground(ac("#b00020", "#ff7b72")),
		Changed: s().Foreground(ac("#9a6700", "#f5c211")),
	}
}
