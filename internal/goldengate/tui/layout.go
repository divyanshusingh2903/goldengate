package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Minimum usable size; below it the UI says so instead of garbling the screen.
const (
	minW = 60
	minH = 16
)

// fit truncates s to w cells (with an ellipsis) and pads it to exactly w.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// truncLeft keeps the tail of s: for paths the end is the part that matters.
func truncLeft(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	if w <= 1 {
		return ansi.Truncate(s, w, "")
	}
	return "…" + ansi.TruncateLeft(s, lipgloss.Width(s)-(w-1), "")
}

// lines returns exactly h lines of width w taken from the text, padding with blanks.
func lines(text string, w, h int) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		out = append(out, fit(l, w))
		if len(out) == h {
			return out
		}
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out
}

// frame draws the screen: a titled box holding a top line, the body (one or two
// panes) and a hint bar, in the style of the mockup in DESIGN.md.
type frame struct {
	th         Theme
	w, h       int
	title      string
	right      string // right side of the title bar (server status)
	top        string // line under the title
	left, rght []string
	split      bool
	hint       string
}

// bodyHeight is the number of body lines for a total height h.
func bodyHeight(h int) int { return max(h-6, 1) }

func (f frame) render() string {
	b := f.th.Border.Render
	inner := f.w - 2
	var sb strings.Builder

	// title bar: ┌ title ──────── right ┐
	left := " " + f.title + " "
	right := ""
	if f.right != "" {
		right = " " + f.right + " "
	}
	fill := inner - lipgloss.Width(left) - lipgloss.Width(right)
	if fill < 1 {
		right, fill = "", max(inner-lipgloss.Width(left), 0)
		left = fit(left, inner)
	}
	sb.WriteString(b("┌") + f.th.Title.Render(left) + b(strings.Repeat("─", fill)) + right + b("┐") + "\n")
	sb.WriteString(b("│") + fit(f.top, inner) + b("│") + "\n")

	lw := inner
	if f.split {
		lw = (inner - 1) / 2
	}
	rw := inner - lw - 1
	if f.split {
		sb.WriteString(b("├"+strings.Repeat("─", lw)+"┬"+strings.Repeat("─", rw)+"┤") + "\n")
	} else {
		sb.WriteString(b("├"+strings.Repeat("─", inner)+"┤") + "\n")
	}
	bh := bodyHeight(f.h)
	for i := 0; i < bh; i++ {
		l := strings.Repeat(" ", lw)
		if i < len(f.left) {
			l = fit(f.left[i], lw)
		}
		if f.split {
			r := strings.Repeat(" ", rw)
			if i < len(f.rght) {
				r = fit(f.rght[i], rw)
			}
			sb.WriteString(b("│") + l + b("│") + r + b("│") + "\n")
		} else {
			sb.WriteString(b("│") + l + b("│") + "\n")
		}
	}
	if f.split {
		sb.WriteString(b("├"+strings.Repeat("─", lw)+"┴"+strings.Repeat("─", rw)+"┤") + "\n")
	} else {
		sb.WriteString(b("├"+strings.Repeat("─", inner)+"┤") + "\n")
	}
	sb.WriteString(b("│") + fit(f.hint, inner) + b("│") + "\n")
	sb.WriteString(b("└" + strings.Repeat("─", inner) + "┘"))
	return sb.String()
}

// hints formats "key label" pairs for the bottom bar. The last pair is always
// kept (it is the way out or the way to help); earlier ones are dropped from
// the end when the line is too narrow.
func hints(th Theme, w int, pairs ...string) string {
	if len(pairs) < 2 {
		return ""
	}
	fmtPair := func(i int) string { return th.Key.Render(pairs[i]) + " " + pairs[i+1] }
	last := fmtPair(len(pairs) - 2)
	budget := w - 1 - lipgloss.Width(last) - 2
	var parts []string
	used := 0
	for i := 0; i+3 < len(pairs); i += 2 {
		p := fmtPair(i)
		if used+lipgloss.Width(p)+2 > budget {
			break
		}
		parts = append(parts, p)
		used += lipgloss.Width(p) + 2
	}
	return " " + strings.Join(append(parts, last), "  ")
}

// center places text in the middle of a w x h area (for "too small" and modals).
func center(text string, w, h int) string {
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, text)
}

// overlay draws a box of lines over the middle of a rendered screen.
func overlay(screen string, box []string, w, h int) string {
	rows := strings.Split(screen, "\n")
	bw := 0
	for _, l := range box {
		bw = max(bw, lipgloss.Width(l))
	}
	top := max((len(rows)-len(box))/2, 0)
	left := max((w-bw)/2, 0)
	for i, l := range box {
		if top+i >= len(rows) {
			break
		}
		row := rows[top+i]
		rows[top+i] = ansi.Truncate(row, left, "") + fit(l, bw) + ansi.TruncateLeft(row, left+bw, "")
	}
	return strings.Join(rows, "\n")
}

// overlayAt draws a box of lines with its top-left corner at (x, y).
func overlayAt(screen string, box []string, x, y int) string {
	rows := strings.Split(screen, "\n")
	bw := 0
	for _, l := range box {
		bw = max(bw, lipgloss.Width(l))
	}
	y = max(min(y, len(rows)-len(box)), 0) // keep it on screen
	for i, l := range box {
		if y+i >= len(rows) {
			break
		}
		row := rows[y+i]
		rows[y+i] = ansi.Truncate(row, x, "") + fit(l, bw) + ansi.TruncateLeft(row, x+bw, "")
	}
	return strings.Join(rows, "\n")
}

// box wraps content lines in a small border for overlays.
func box(th Theme, title string, content []string) []string {
	inner := lipgloss.Width(title) + 2
	for _, l := range content {
		inner = max(inner, lipgloss.Width(l)+2)
	}
	b := th.Border.Render
	out := []string{b("┌") + th.Title.Render(" "+title+" ") + b(strings.Repeat("─", inner-lipgloss.Width(title)-2)+"┐")}
	for _, l := range content {
		out = append(out, b("│")+" "+fit(l, inner-2)+" "+b("│"))
	}
	return append(out, b("└"+strings.Repeat("─", inner)+"┘"))
}

// clipLines truncates lines so a box built from them fits in a window w wide.
func clipLines(ls []string, w int) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = ansi.Truncate(l, max(w-4, 1), "…")
	}
	return out
}
