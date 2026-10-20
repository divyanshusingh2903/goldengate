package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the UI and blocks until it exits.
func Run(be Backend, o Options) error {
	painted := false
	o.SetColors = func(fg, bg string) {
		if bg == "" {
			if painted {
				fmt.Fprint(os.Stdout, "\x1b]111\x07\x1b]110\x07") // back to the terminal's own colors
				painted = false
			}
			return
		}
		fmt.Fprintf(os.Stdout, "\x1b]11;%s\x07\x1b]10;%s\x07", bg, fg)
		painted = true
	}
	defer o.SetColors("", "")
	p := tea.NewProgram(New(be, o), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(o.Context))
	_, err := p.Run()
	return err
}

// typing reports whether keystrokes belong to a text field, so that letters
// like q and s are text rather than commands.
func (m *Model) typing() bool {
	switch m.scr {
	case scrProjects:
		return m.pk.filtering
	case scrSettings:
		return m.st.editing || m.st.dd != menuNone
	case scrWizard:
		return m.wz.step == wizServer && !m.wz.done && m.wz.mode != 2
	}
	return false
}

func (m *Model) quit() (tea.Model, tea.Cmd) {
	if m.sy.phase == syncRunning {
		m.modal = &modal{title: "Sync running", lines: []string{"A sync is in progress.", "Cancel it and quit?"},
			yes: func(m *Model) tea.Cmd {
				if m.sy.cancel != nil {
					m.sy.cancel()
				}
				return tea.Quit
			}}
		return m, nil
	}
	return m, tea.Quit
}

func (m *Model) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if m.modal != nil {
		md := m.modal
		m.modal = nil
		if md.yes != nil && (s == "y" || s == "Y") {
			return m, md.yes(m)
		}
		return m, nil
	}
	if m.help {
		m.help = false
		return m, nil
	}
	if s == "ctrl+c" {
		return m.quit()
	}
	m.status, m.bad = "", false // a message lasts until the next key
	if !m.typing() {
		switch s {
		case "?":
			m.help = true
			return m, nil
		case "q":
			if m.scr == scrProjects {
				return m.quit()
			}
		case "1", "2", "3", "4":
			if m.scr != scrWizard {
				return m, m.goTo(int(s[0] - '0'))
			}
		case "tab":
			if m.scr != scrWizard && m.scr != scrPreview {
				next := map[screen]int{scrProjects: 2, scrSync: 3, scrServer: 4, scrSettings: 1}[m.scr]
				return m, m.goTo(next)
			}
		case "s":
			if m.scr == scrProjects {
				return m, m.openSync()
			}
		}
	}
	switch m.scr {
	case scrPreview:
		return m.onKeyPreview(k)
	case scrSync:
		return m.onKeySync(k)
	case scrServer:
		return m.onKeyServer(k)
	case scrSettings:
		return m.onKeySettings(k)
	case scrWizard:
		return m.onKeyWizard(k)
	}
	return m.onKeyProjects(k)
}

func (m *Model) goTo(n int) tea.Cmd {
	switch n {
	case 1:
		m.scr = scrProjects
		return m.loadSnapshot()
	case 2:
		return m.openSync()
	case 3:
		return m.openServer()
	case 4:
		return m.openSettings()
	}
	return nil
}
