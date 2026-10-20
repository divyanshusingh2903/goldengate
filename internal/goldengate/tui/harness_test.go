package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestMain(m *testing.M) {
	previewDebounce = time.Millisecond
	os.Exit(m.Run())
}

type harness struct {
	t  *testing.T
	m  *Model
	be *fakeBackend
}

func newHarness(t *testing.T, w, h int) *harness {
	t.Helper()
	be := newFake()
	return newHarnessWith(t, be, w, h)
}

func newHarnessWith(t *testing.T, be *fakeBackend, w, h int) *harness {
	t.Helper()
	th := NewTheme(PlainRenderer())
	m := New(be, Options{Now: func() time.Time { return clock }, Theme: &th, StaticCursor: true})
	hs := &harness{t: t, m: m, be: be}
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	hs.run(m.Init())
	return hs
}

// run executes a command and feeds its messages back, depth first. Commands
// that wait (cursor blink timers) are abandoned after a short time.
func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(150 * time.Millisecond):
		return
	}
	switch v := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range v {
			h.run(c)
		}
	default:
		_, next := h.m.Update(msg)
		h.run(next)
	}
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

var keyNames = map[string]tea.KeyType{
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "enter": tea.KeyEnter, "esc": tea.KeyEsc,
	"tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab, "pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "home": tea.KeyHome, "end": tea.KeyEnd,
	"backspace": tea.KeyBackspace, "ctrl+c": tea.KeyCtrlC, "space": tea.KeySpace, "ctrl+n": tea.KeyCtrlN, "ctrl+p": tea.KeyCtrlP,
}

func (h *harness) key(names ...string) {
	h.t.Helper()
	for _, n := range names {
		if kt, ok := keyNames[n]; ok {
			k := tea.KeyMsg{Type: kt}
			if kt == tea.KeySpace {
				k.Runes = []rune{' '}
			}
			h.send(k)
			continue
		}
		h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(n)})
	}
}

func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func (h *harness) view() string { return h.m.View() }

func (h *harness) contains(want ...string) {
	h.t.Helper()
	v := h.view()
	for _, w := range want {
		if !strings.Contains(v, w) {
			h.t.Errorf("view does not contain %q:\n%s", w, v)
		}
	}
}

func (h *harness) lacks(bad ...string) {
	h.t.Helper()
	v := h.view()
	for _, b := range bad {
		if strings.Contains(v, b) {
			h.t.Errorf("view must not contain %q:\n%s", b, v)
		}
	}
}

// golden compares the current view with testdata/<name>.golden.
func (h *harness) golden(name string) {
	h.t.Helper()
	got := h.view() + "\n"
	path := filepath.Join("testdata", name+".golden")
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			h.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatalf("missing golden %s (run with -update): %v", path, err)
	}
	if string(want) != got {
		h.t.Errorf("snapshot %s changed (run with -update and review the diff)\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

// checkGeometry asserts the view is exactly w x h cells: no line overflows and
// nothing is missing, which is what "usable at 80x24 and resizes cleanly" means.
func (h *harness) checkGeometry(w, hh int) {
	h.t.Helper()
	v := h.view()
	rows := strings.Split(v, "\n")
	if len(rows) != hh {
		h.t.Errorf("view has %d lines, want %d", len(rows), hh)
	}
	for i, r := range rows {
		if got := lipgloss.Width(r); got != w {
			h.t.Errorf("line %d is %d cells wide, want %d: %q", i, got, w, r)
		}
	}
	if strings.Contains(v, "\x1b") {
		h.t.Error("plain renderer must not emit escape sequences")
	}
	_ = runewidth.StringWidth
}
