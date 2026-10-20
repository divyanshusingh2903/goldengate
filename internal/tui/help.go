package tui

func (m *Model) helpBox() []string {
	k := func(key, what string) string { return m.th.Key.Render(padRight(key, 9)) + what }
	lines := []string{
		m.th.Title.Render("Projects"),
		k("↑ ↓ j k", "move   (pgup/pgdn, g/G jump)"),
		k("space", "share / un-share (marked rows, or this row)"),
		k("v", "start/end range selection"),
		k("a  A", "mark all visible / clear marks"),
		k("d", "deny (beats any allow rule)"),
		k("u", "undo the last change"),
		k("e", "choose which sources are shared"),
		k("/", "fuzzy filter   (esc clears)"),
		k("t", "folder tree: space on a folder shares all of it"),
		k("o", "sort: recent / name / size"),
		k("p enter", "preview exactly what would be sent"),
		k("P", "paranoid: raw payloads, every string"),
		m.th.Title.Render("Everywhere"),
		k("1-4 tab", "projects / sync / server / settings"),
		k("s", "sync   (D toggles dry run)"),
		k("r", "refresh"),
		k("?", "this help"),
		k("q ctrl+c", "quit"),
		"",
		m.th.Dim.Render("[x] shared  [ ] not  [~] some sources  [-] denied  ▲ n new"),
		m.th.Dim.Render("Nothing is sent until you sync, and previews are exact."),
	}
	return box(m.th, "Keys", clipLines(lines, m.w))
}

func padRight(s string, n int) string {
	for len([]rune(s)) < n {
		s += " "
	}
	return s
}
