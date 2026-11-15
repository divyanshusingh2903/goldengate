package summarize

import (
	"fmt"
	"strings"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// Edit is the user's own version of a session's summary: the text fields as
// they want them sent, and the work items they unlinked. It belongs to one
// content of the session (ContentHash); when the session changes, the edit
// no longer applies by itself.
type Edit struct {
	ContentHash  string   `json:"content_hash"`
	Title        string   `json:"title"`
	Goals        []string `json:"goals,omitempty"`
	Description  []string `json:"description,omitempty"`
	Outcomes     []string `json:"outcomes,omitempty"`
	OpenProblems []string `json:"open_problems,omitempty"`
	Narrative    string   `json:"narrative,omitempty"`
	// DropWorkItems are the work items the user unlinked, as "kind key".
	DropWorkItems []string `json:"drop_work_items,omitempty"`
}

// apply writes the user's text into sum. Every string goes through the same
// redaction and caps as generated text.
func (e *Edit) apply(sum *model.Summary, r *scoped) {
	if t := r.str(e.Title, maxTitle); t != "" {
		sum.Title = t
	}
	list := func(in []string) []string {
		var out []string
		for _, v := range in {
			if v = r.str(v, model.MaxListItemLen); v != "" && len(out) < model.MaxListItems {
				out = append(out, v)
			}
		}
		return out
	}
	sum.Goals, sum.Description, sum.Outcomes, sum.OpenProblems = list(e.Goals), list(e.Description), list(e.Outcomes), list(e.OpenProblems)
	sum.Narrative, sum.NarrativeBy, sum.NarrativeLength, sum.Abstract = "", "", "", ""
	if n := r.prose(e.Narrative, model.MaxNarrativeLen); n != "" {
		sum.Narrative, sum.NarrativeBy = n, model.NarrativeByUser
	}
	sum.Edited = true
}

// keep drops the work items the user unlinked.
func (e *Edit) keep(items []model.WorkItem) []model.WorkItem {
	if len(e.DropWorkItems) == 0 {
		return items
	}
	drop := map[string]bool{}
	for _, k := range e.DropWorkItems {
		drop[k] = true
	}
	out := items[:0:0]
	for _, it := range items {
		if !drop[workKey(it)] {
			out = append(out, it)
		}
	}
	return out
}

func workKey(it model.WorkItem) string { return it.Kind + " " + it.Key }

// The edit form: a plain-text view of the editable parts of a summary. The
// user changes text, deletes lines to strike them, and deletes a work-item
// line to unlink it.

const formHelp = `# Edit this session's summary. Lines that start with # are ignored.
#  - Change any text. Delete a line to strike it.
#  - Under [work items], delete a line to unlink that item; lines cannot be added.
#  - Save and close to keep your version. It is redacted like everything else,
#    and no summarizer will replace it. Close without changes to keep the
#    generated summary.
`

// FormatEdit renders p's editable parts as the edit form. note, when not
// empty, is shown as comment lines at the top (for example a diff against the
// user's earlier version).
func FormatEdit(p *model.SessionPayload, note string) string {
	var b strings.Builder
	b.WriteString(formHelp)
	for _, l := range strings.Split(strings.TrimRight(note, "\n"), "\n") {
		if l != "" {
			b.WriteString("# " + l + "\n")
		}
	}
	title := p.TitleOnly
	sum := p.Summary
	if sum != nil {
		title = sum.Title
	}
	b.WriteString("\ntitle: " + title + "\n")
	if sum != nil {
		list := func(name string, vs []string) {
			b.WriteString("\n[" + name + "]\n")
			for _, v := range vs {
				b.WriteString("- " + v + "\n")
			}
		}
		list("goals", sum.GoalList())
		list("done", sum.Description)
		list("outcomes", sum.OutcomeList())
		list("open problems", sum.OpenProblems)
		b.WriteString("\n[narrative]\n")
		if sum.Narrative != "" {
			b.WriteString(sum.Narrative + "\n")
		}
		b.WriteString("\n[work items]\n")
		for _, it := range p.WorkItems {
			b.WriteString("- " + workKey(it) + "\n")
		}
	}
	return b.String()
}

// ParseEdit reads an edit form back. p is the payload the form was made
// from: it supplies the content hash and the work items that exist, so an
// unknown work-item line is an error rather than a silent addition.
func ParseEdit(text string, p *model.SessionPayload) (Edit, error) {
	e := Edit{ContentHash: p.ContentHash}
	have := map[string]bool{}
	for _, it := range p.WorkItems {
		have[workKey(it)] = true
	}
	kept := map[string]bool{}
	section, sawTitle := "", false
	var narrative []string
	for n, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			switch section {
			case "goals", "done", "outcomes", "open problems", "narrative", "work items":
			default:
				return Edit{}, fmt.Errorf("line %d: unknown section %s", n+1, trimmed)
			}
			continue
		}
		if section == "" {
			if v, ok := strings.CutPrefix(trimmed, "title:"); ok {
				e.Title, sawTitle = strings.TrimSpace(v), true
			} else if trimmed != "" {
				return Edit{}, fmt.Errorf("line %d: expected \"title: ...\" or a [section]", n+1)
			}
			continue
		}
		if section == "narrative" {
			narrative = append(narrative, line)
			continue
		}
		if trimmed == "" {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		if item == "" {
			continue
		}
		switch section {
		case "goals":
			e.Goals = append(e.Goals, item)
		case "done":
			e.Description = append(e.Description, item)
		case "outcomes":
			e.Outcomes = append(e.Outcomes, item)
		case "open problems":
			e.OpenProblems = append(e.OpenProblems, item)
		case "work items":
			if !have[item] {
				return Edit{}, fmt.Errorf("line %d: %q is not a work item of this session; lines can only be deleted here", n+1, item)
			}
			kept[item] = true
		}
	}
	if !sawTitle || e.Title == "" {
		return Edit{}, fmt.Errorf("the summary needs a title: keep the \"title: ...\" line")
	}
	e.Narrative = strings.TrimSpace(strings.Join(narrative, "\n"))
	for _, it := range p.WorkItems {
		if k := workKey(it); !kept[k] {
			e.DropWorkItems = append(e.DropWorkItems, k)
		}
	}
	return e, nil
}

// SameText reports whether an edit says exactly what p already says, so
// saving it would change nothing.
func (e Edit) SameText(p *model.SessionPayload) bool {
	again, err := ParseEdit(FormatEdit(p, ""), p)
	if err != nil {
		return false
	}
	eq := func(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }
	return e.Title == again.Title && e.Narrative == again.Narrative && eq(e.Goals, again.Goals) && eq(e.Description, again.Description) &&
		eq(e.Outcomes, again.Outcomes) && eq(e.OpenProblems, again.OpenProblems) && eq(e.DropWorkItems, again.DropWorkItems)
}
