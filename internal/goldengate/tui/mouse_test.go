package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// clickAt sends a left click (press and release) at a cell.
func (h *harness) clickAt(x, y int, ctrl bool) {
	h.t.Helper()
	h.send(tea.MouseMsg{X: x, Y: y, Ctrl: ctrl, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	h.send(tea.MouseMsg{X: x, Y: y, Ctrl: ctrl, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
}

// click clicks the middle of the first occurrence of text on screen.
func (h *harness) click(text string) {
	h.t.Helper()
	for y, l := range strings.Split(h.view(), "\n") {
		if i := strings.Index(l, text); i >= 0 {
			h.clickAt(lipgloss.Width(l[:i])+lipgloss.Width(text)/2, y, false)
			return
		}
	}
	h.t.Fatalf("%q is not on screen:\n%s", text, h.view())
}

func TestMouseClickBoxShares(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.clickAt(4, yRows+3, false) // [ ] of ~/code/vihayas
	if n := len(h.be.actions); n != 1 || h.be.actions[0].Kind != ActAllow || h.be.actions[0].Keys[0] != "/home/u/code/vihayas" {
		t.Fatalf("clicking the box shares that row: %+v", h.be.actions)
	}
}

func TestMouseCtrlClickMarks(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.clickAt(20, yRows+2, true)
	h.clickAt(20, yRows+3, true)
	if len(h.m.pk.marks) != 2 {
		t.Fatalf("ctrl+click marks rows: %v", h.m.pk.marks)
	}
}

func TestMouseDoubleClickOpensPreview(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.clickAt(20, yRows, false)
	h.clickAt(20, yRows, false)
	if h.m.scr != scrPreview {
		t.Fatalf("double click opens the preview, screen %d", h.m.scr)
	}
}

func TestMouseChipsTabsHintsAndButtons(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.click("Not shared 4")
	if h.m.pk.view != viewLocal || len(h.m.items()) != 4 {
		t.Fatalf("chip selects the view: %d, %d items", h.m.pk.view, len(h.m.items()))
	}
	h.click("All 8")
	h.click("? help")
	if !h.m.help {
		t.Fatal("hint bar keys are clickable")
	}
	h.clickAt(1, 1, false)
	if h.m.help {
		t.Fatal("a click closes help")
	}
	h.click("[p Preview]")
	if h.m.scr != scrPreview {
		t.Fatal("card buttons are clickable")
	}
	h.click("4 Settings")
	if h.m.scr != scrSettings {
		t.Fatalf("tabs switch screens: %d", h.m.scr)
	}
}

func TestMouseModalButtons(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.be.remote = &RemotePrompt{Count: 1}
	h.key("space") // un-share pharmfindr: asks about the server
	if h.m.modal == nil {
		t.Fatal("expected the server prompt")
	}
	h.click("[n No]")
	if h.m.modal != nil || len(h.be.remoteDels) != 0 {
		t.Fatal("No closes the prompt without deleting")
	}
}

func TestMouseWheelScrollsPreview(t *testing.T) {
	h := newHarness(t, 80, 16)
	h.key("p")
	h.send(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if h.m.pv.vp.YOffset == 0 {
		t.Fatal("the wheel scrolls the preview")
	}
}

func TestCardExplainsSyncState(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.key("down") // the worktree: shared, never synced
	h.contains("never synced", "Not on the server yet. The next sync sends 2 sessions.")
	h.key("down", "down") // vihayas: not shared
	h.contains("○ Not shared", "Nothing from this project leaves your machine.", "[space Share]")
	if strings.Contains(h.view(), "since the last sync") {
		t.Fatal("a project that is not shared has no last sync")
	}
	h.key("down") // website: partly shared, up to date
	h.contains("◐ Partly shared", "up to date")
}

func TestMouseSettings(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("4")
	h.clickAt(10, yBody+4, false) // label of "Source CC Claude Code": selects only
	if h.m.st.cursor != 4 || len(h.be.saved) != 0 {
		t.Fatalf("a click on the label selects: cursor %d, saved %d", h.m.st.cursor, len(h.be.saved))
	}
	h.clickAt(10, yBody+4, false) // second click changes it
	if len(h.be.saved) != 1 || h.be.saved[0].Sources[0].Enabled {
		t.Fatalf("a second click toggles the source: %+v", h.be.saved)
	}
	h.click("[summary]") // the value column changes at once
	if h.be.saved[len(h.be.saved)-1].Level != "titles-only" {
		t.Fatal("clicking the level value switches it")
	}
}

func TestMouseSettingsDeletePattern(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.be.patterns = []string{`acme-[0-9]+`}
	h.key("4")
	h.click("acme-[0-9]+") // clicking the pattern itself never deletes it
	if len(h.be.saved) != 0 {
		t.Fatal("pattern clicks only select")
	}
	h.click("[x Delete]")
	if len(h.be.saved) != 1 || len(h.be.saved[0].ExtraPatterns) != 0 {
		t.Fatalf("the Delete button removes the pattern: %+v", h.be.saved)
	}
}

func TestMouseServer(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.key("3")
	h.click("~/code/forge")
	if h.m.sv.cursor != 1 {
		t.Fatalf("a click selects the server project: %d", h.m.sv.cursor)
	}
	h.click("[D Delete]")
	if h.m.modal == nil {
		t.Fatal("Delete asks first")
	}
	h.click("[y Yes]")
	if len(h.be.srvDels) != 1 || h.be.srvDels[0] != "~/code/forge" {
		t.Fatalf("deleted %v", h.be.srvDels)
	}
}

func TestSourceTags(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.contains("CC CX", "AI", "code · coding-tool sessions", "CC Fix OAuth redirect mismatch")
	h.key("end") // claude-ai:Chats
	h.contains("chat · chat-app export")
}
