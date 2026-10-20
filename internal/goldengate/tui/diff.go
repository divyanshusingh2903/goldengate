package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// DiffPayload lists, in words, what differs between the payload that was last
// sent and the one that would be sent now. It compares only fields that travel.
func DiffPayload(prev, cur *model.SessionPayload) []string {
	if prev == nil || cur == nil {
		return nil
	}
	var out []string
	chg := func(name, a, b string) {
		if a != b {
			out = append(out, fmt.Sprintf("%s: %q -> %q", name, clip(a, 60), clip(b, 60)))
		}
	}
	chg("title", titleOf(prev), titleOf(cur))
	ps, cs := prev.Summary, cur.Summary
	if ps != nil && cs != nil {
		chg("goal", ps.Goal, cs.Goal)
		chg("outcome", ps.Outcome, cs.Outcome)
		chg("last message", ps.LastAssistant, cs.LastAssistant)
		out = append(out, setDiff("file", ps.FilesTouched, cs.FilesTouched)...)
		out = append(out, setDiff("command", ps.Commands, cs.Commands)...)
		if ps.TurnCount != cs.TurnCount {
			out = append(out, fmt.Sprintf("turns: %d -> %d", ps.TurnCount, cs.TurnCount))
		}
	}
	out = append(out, workDiff(prev.WorkItems, cur.WorkItems)...)
	if prev.Level != cur.Level {
		out = append(out, fmt.Sprintf("level: %s -> %s", prev.Level, cur.Level))
	}
	return out
}

func titleOf(p *model.SessionPayload) string {
	if p.Summary != nil {
		return p.Summary.Title
	}
	return p.TitleOnly
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func setDiff(kind string, a, b []string) []string {
	in := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	var out []string
	for _, v := range b {
		if !in(a, v) {
			out = append(out, fmt.Sprintf("+ %s %s", kind, v))
		}
	}
	for _, v := range a {
		if !in(b, v) {
			out = append(out, fmt.Sprintf("- %s %s", kind, v))
		}
	}
	return out
}

func workDiff(a, b []model.WorkItem) []string {
	key := func(w model.WorkItem) string { return w.Kind + "|" + w.Provider + "|" + w.Key }
	old := map[string]model.WorkItem{}
	for _, w := range a {
		old[key(w)] = w
	}
	cur := map[string]bool{}
	var out []string
	for _, w := range b {
		cur[key(w)] = true
		o, ok := old[key(w)]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("+ %s %s [%s]", w.Kind, w.Key, w.Status))
		case o.Status != w.Status:
			out = append(out, fmt.Sprintf("~ %s %s: %s -> %s", w.Kind, w.Key, o.Status, w.Status))
		}
	}
	for k, w := range old {
		if !cur[k] {
			out = append(out, fmt.Sprintf("- %s %s", w.Kind, w.Key))
		}
	}
	sort.Strings(out)
	return out
}

// ParseDiffSummary is used by the preview header: "3 new, 1 changed".
func summarizeStatuses(items []PreviewItem) string {
	n := map[string]int{}
	for _, it := range items {
		n[it.Status]++
	}
	var parts []string
	for _, s := range []string{"new", "changed", "unchanged"} {
		if n[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[s], s))
		}
	}
	return strings.Join(parts, ", ")
}
