package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme holds every style the UI uses, derived from one Palette. With NO_COLOR
// (or a dumb terminal) lipgloss drops the colors and the UI relies on glyphs
// and words alone, so nothing is conveyed by color only.
type Theme struct {
	Name string
	Pal  Palette

	Title, Dim, Accent, Good, Warn, Bad lipgloss.Style
	Cursor, Marked, Redact, Key, Border lipgloss.Style
	Added, Removed, Changed             lipgloss.Style

	Plain                       lipgloss.Style
	Brand                       lipgloss.Style // the wordmark
	Tab, TabOn                  lipgloss.Style
	Chip, ChipOn                lipgloss.Style
	Button, ButtonGo, ButtonBad lipgloss.Style
	Label                       lipgloss.Style // field names in the card
	CodeTag, ChatTag            lipgloss.Style // source tags by kind

	// CurBg is the cursor row's background. Segments of the cursor row are
	// painted with it one by one, since an inner reset would end an outer one.
	CurBg lipgloss.TerminalColor
}

// Palette is a color scheme. Bg is painted as the terminal background while
// the UI runs (and restored on exit), so a preset looks the same everywhere.
type Palette struct {
	Dark bool
	Bg   string
	Fg   string

	Surface string // chips, keys, buttons
	Sel     string // the cursor row
	Line    string // borders
	Dim     string // secondary text

	Accent, Good, Warn, Bad, Brand, Chat string
}

// DefaultTheme is used when none is configured.
const DefaultTheme = "goldengate-light"

// Presets are the selectable color schemes, after the editor themes of the
// same names.
var Presets = map[string]Palette{
	// Golden Gate's own colors (docs/brand/BRAND.md): International Orange
	// on night bay or fog.
	"goldengate-dark": {Dark: true, Bg: "#09131B", Fg: "#E5EBEE", Surface: "#132330", Sel: "#1B3446", Line: "#2A4256", Dim: "#8D9DA7",
		Accent: "#7FBADB", Good: "#72C59A", Warn: "#E8B652", Bad: "#F27EB2", Brand: "#F0563F", Chat: "#B9A0E6"},
	"goldengate-light": {Bg: "#F8F9F8", Fg: "#13202A", Surface: "#DCE8F0", Sel: "#C6DDEE", Line: "#93A3AB", Dim: "#4A5960",
		Accent: "#0B5F94", Good: "#137A44", Warn: "#B05A00", Bad: "#B8165F", Brand: "#D03A2B", Chat: "#6A35C2"},
	"github-dark": {Dark: true, Bg: "#0d1117", Fg: "#e6edf3", Surface: "#21262d", Sel: "#1f3049", Line: "#30363d", Dim: "#8b949e",
		Accent: "#58a6ff", Good: "#3fb950", Warn: "#d29922", Bad: "#f85149", Brand: "#f0883e", Chat: "#bc8cff"},
	"github-light": {Bg: "#ffffff", Fg: "#1f2328", Surface: "#eaeef2", Sel: "#ddf4ff", Line: "#d0d7de", Dim: "#59636e",
		Accent: "#0969da", Good: "#1a7f37", Warn: "#9a6700", Bad: "#cf222e", Brand: "#bc4c00", Chat: "#8250df"},
	"gruvbox-dark": {Dark: true, Bg: "#282828", Fg: "#ebdbb2", Surface: "#3c3836", Sel: "#504945", Line: "#665c54", Dim: "#a89984",
		Accent: "#83a598", Good: "#b8bb26", Warn: "#fabd2f", Bad: "#fb4934", Brand: "#fe8019", Chat: "#d3869b"},
	"gruvbox-light": {Bg: "#fbf1c7", Fg: "#3c3836", Surface: "#ebdbb2", Sel: "#d5c4a1", Line: "#bdae93", Dim: "#7c6f64",
		Accent: "#076678", Good: "#79740e", Warn: "#b57614", Bad: "#9d0006", Brand: "#af3a03", Chat: "#8f3f71"},
	"monokai": {Dark: true, Bg: "#272822", Fg: "#f8f8f2", Surface: "#3e3d32", Sel: "#49483e", Line: "#75715e", Dim: "#a59f85",
		Accent: "#66d9ef", Good: "#a6e22e", Warn: "#e6db74", Bad: "#f92672", Brand: "#fd971f", Chat: "#ae81ff"},
	"dracula": {Dark: true, Bg: "#282a36", Fg: "#f8f8f2", Surface: "#343746", Sel: "#44475a", Line: "#6272a4", Dim: "#9ea8d0",
		Accent: "#8be9fd", Good: "#50fa7b", Warn: "#f1fa8c", Bad: "#ff5555", Brand: "#ffb86c", Chat: "#bd93f9"},
	"nord": {Dark: true, Bg: "#2e3440", Fg: "#eceff4", Surface: "#3b4252", Sel: "#434c5e", Line: "#4c566a", Dim: "#9aa5b8",
		Accent: "#88c0d0", Good: "#a3be8c", Warn: "#ebcb8b", Bad: "#bf616a", Brand: "#d08770", Chat: "#b48ead"},
	"catppuccin-mocha": {Dark: true, Bg: "#1e1e2e", Fg: "#cdd6f4", Surface: "#313244", Sel: "#45475a", Line: "#585b70", Dim: "#9399b2",
		Accent: "#89b4fa", Good: "#a6e3a1", Warn: "#f9e2af", Bad: "#f38ba8", Brand: "#fab387", Chat: "#cba6f7"},
	"catppuccin-latte": {Bg: "#eff1f5", Fg: "#4c4f69", Surface: "#dce0e8", Sel: "#ccd0da", Line: "#bcc0cc", Dim: "#6c6f85",
		Accent: "#1e66f5", Good: "#40a02b", Warn: "#df8e1d", Bad: "#d20f39", Brand: "#fe640b", Chat: "#8839ef"},
	"tokyo-night": {Dark: true, Bg: "#1a1b26", Fg: "#c0caf5", Surface: "#24283b", Sel: "#283457", Line: "#3b4261", Dim: "#7a83b0",
		Accent: "#7aa2f7", Good: "#9ece6a", Warn: "#e0af68", Bad: "#f7768e", Brand: "#ff9e64", Chat: "#bb9af7"},
	"solarized-dark": {Dark: true, Bg: "#002b36", Fg: "#93a1a1", Surface: "#073642", Sel: "#0b4a5a", Line: "#586e75", Dim: "#768a90",
		Accent: "#268bd2", Good: "#859900", Warn: "#b58900", Bad: "#dc322f", Brand: "#cb4b16", Chat: "#6c71c4"},
	"solarized-light": {Bg: "#fdf6e3", Fg: "#586e75", Surface: "#eee8d5", Sel: "#e3dcc5", Line: "#c9c2ad", Dim: "#839496",
		Accent: "#268bd2", Good: "#859900", Warn: "#b58900", Bad: "#dc322f", Brand: "#cb4b16", Chat: "#6c71c4"},
}

// ThemeNames lists Golden Gate's own pair first, then every other preset,
// dark ones first.
func ThemeNames() []string {
	var dark, light []string
	for n, p := range Presets {
		switch {
		case strings.HasPrefix(n, "goldengate-"):
		case p.Dark:
			dark = append(dark, n)
		default:
			light = append(light, n)
		}
	}
	sort.Strings(dark)
	sort.Strings(light)
	return append(append([]string{"goldengate-light", "goldengate-dark"}, light...), dark...)
}

// ThemeTitle is a preset's display name: "gruvbox-dark" -> "Gruvbox Dark".
func ThemeTitle(name string) string {
	special := map[string]string{"goldengate": "Golden Gate", "github": "GitHub"}
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if t, ok := special[p]; ok {
			parts[i] = t
		} else if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// ResolveTheme maps a configured name to a palette. "" and unknown names get
// the default; "light"/"dark" are Golden Gate's pair.
func ResolveTheme(name string) (string, Palette) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "light":
		n = "goldengate-light"
	case "dark":
		n = "goldengate-dark"
	}
	if p, ok := Presets[n]; ok {
		return n, p
	}
	return DefaultTheme, Presets[DefaultTheme]
}

// NewTheme builds the default theme on a renderer; tests pass one pinned to
// no color.
func NewTheme(r *lipgloss.Renderer) Theme {
	name, p := ResolveTheme(DefaultTheme)
	return NewThemeFrom(r, name, p)
}

// NewThemeFrom derives every style from a palette.
func NewThemeFrom(r *lipgloss.Renderer, name string, p Palette) Theme {
	s := r.NewStyle
	c := func(hex string) lipgloss.Color { return lipgloss.Color(hex) }
	bg := c(p.Bg)
	if p.Bg == "" { // text on colored fills: the scheme's own background color
		if p.Dark {
			bg = c("#0d1117")
		} else {
			bg = c("#ffffff")
		}
	}
	fg := func(hex string) lipgloss.Style { return s().Foreground(c(hex)) }
	text := fg(p.Fg)
	if p.Bg == "" { // on the terminal's own background, keep its own text color
		text = s()
	}
	fill := func(fgc lipgloss.TerminalColor, hex string) lipgloss.Style {
		return s().Foreground(fgc).Background(c(hex)).Bold(true)
	}
	return Theme{
		Name:    name,
		Pal:     p,
		Title:   text.Bold(true),
		Dim:     fg(p.Dim),
		Accent:  fg(p.Accent).Bold(true),
		Good:    fg(p.Good),
		Warn:    fg(p.Warn),
		Bad:     fg(p.Bad),
		Cursor:  s().Background(c(p.Sel)),
		Marked:  fg(p.Accent).Bold(true),
		Redact:  s().Foreground(bg).Background(c(p.Fg)),
		Key:     fg(p.Accent).Background(c(p.Surface)).Bold(true),
		Border:  fg(p.Line),
		Added:   fg(p.Good),
		Removed: fg(p.Bad),
		Changed: fg(p.Warn),

		Plain:     text,
		Brand:     fg(p.Brand).Bold(true),
		Tab:       fg(p.Dim),
		TabOn:     fill(bg, p.Brand),
		Chip:      text.Background(c(p.Surface)),
		ChipOn:    fill(bg, p.Accent),
		Button:    fg(p.Accent).Background(c(p.Surface)).Bold(true),
		ButtonGo:  fill(bg, p.Good),
		ButtonBad: fill(bg, p.Bad),
		Label:     fg(p.Dim),
		CodeTag:   fg(p.Accent).Bold(true),
		ChatTag:   fg(p.Chat).Bold(true),
		CurBg:     c(p.Sel),
	}
}
