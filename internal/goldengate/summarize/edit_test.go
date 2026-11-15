package summarize

import (
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

func TestEditFormRoundTripsAndOnlyAllowsDeletingWorkItems(t *testing.T) {
	p, err := Build(chatty(), Options{Home: "/home/u"})
	if err != nil {
		t.Fatal(err)
	}
	form := FormatEdit(p, "a note\nover two lines")
	for _, want := range []string{"# a note", "# over two lines", "title: " + p.Summary.Title, "[goals]", "[outcomes]", "- tests passed", "[narrative]", "[work items]", "- commit made"} {
		if !strings.Contains(form, want) {
			t.Errorf("form lacks %q:\n%s", want, form)
		}
	}
	e, err := ParseEdit(form, p)
	if err != nil {
		t.Fatal(err)
	}
	if !e.SameText(p) || e.ContentHash != p.ContentHash || len(e.DropWorkItems) != 0 {
		t.Fatalf("an untouched form is no edit: %+v", e)
	}
	// change the title, strike an outcome, add a narrative, unlink the first work item
	lines := strings.Split(form, "\n")
	var out []string
	dropped, inWork := "", false
	for _, l := range lines {
		if l == "[work items]" {
			inWork = true
		}
		switch {
		case strings.HasPrefix(l, "title:"):
			out = append(out, "title: My own title")
		case l == "- tests passed":
		case l == "[narrative]":
			out = append(out, l, "First paragraph.", "", "Second paragraph.")
		case inWork && strings.HasPrefix(l, "- ") && dropped == "":
			dropped = strings.TrimPrefix(l, "- ")
		default:
			out = append(out, l)
		}
	}
	e, err = ParseEdit(strings.Join(out, "\r\n"), p)
	if err != nil {
		t.Fatal(err)
	}
	if e.SameText(p) || e.Title != "My own title" || strings.Contains(strings.Join(e.Outcomes, ","), "tests passed") || e.Narrative != "First paragraph.\n\nSecond paragraph." ||
		len(e.DropWorkItems) != 1 || e.DropWorkItems[0] != dropped {
		t.Fatalf("parsed edit: %+v", e)
	}
	for name, bad := range map[string]string{
		"no title":        strings.Replace(form, "title: "+p.Summary.Title, "", 1),
		"empty title":     strings.Replace(form, "title: "+p.Summary.Title, "title:   ", 1),
		"unknown section": form + "\n[secrets]\n- x\n",
		"added work item": form + "- pull_request evil/repo#1\n",
		"stray text":      "hello\n" + form,
	} {
		if _, err := ParseEdit(bad, p); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestAnEditIsUsedInsteadOfAnySummarizerAndIsRedacted(t *testing.T) {
	base, _ := Build(chatty(), Options{Home: "/home/u"})
	edit := &Edit{ContentHash: base.ContentHash, Title: "My title with " + fakeSecret(),
		Goals: []string{"What I really wanted, see /home/u/code/other/notes.txt"}, Outcomes: []string{"shipped"},
		Narrative:     "I wrote this.\n\nThe key " + fakeSecret() + " leaked.\n\n```go\nfunc x() {}\n```",
		DropWorkItems: []string{workKey(base.WorkItems[0])}}
	fm := &fakeModel{answer: answer(modelAnswer{Title: "Model title", Narrative: "The model's story.", Goals: []string{"model goal"}})}
	p, err := Build(chatty(), Options{Home: "/home/u", Edit: edit, Model: &ModelOptions{Client: fm}})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Summary
	if fm.calls != 0 {
		t.Fatal("no model is asked when the user wrote the summary")
	}
	if !s.Edited || !strings.HasPrefix(s.Title, "My title with ") || len(s.Goals) != 1 || strings.Join(s.Outcomes, ",") != "shipped" || s.NarrativeBy != model.NarrativeByUser || s.SummarizedBy != Heuristic {
		t.Fatalf("the user's text is used: %+v", s)
	}
	blob := s.Title + s.Narrative + strings.Join(s.Goals, " ")
	for _, leak := range []string{fakeSecret(), "/home/u", "func x()"} {
		if strings.Contains(blob, leak) {
			t.Errorf("user text is redacted like everything else; found %q in %q", leak, blob)
		}
	}
	if !strings.Contains(blob, "[REDACTED") || p.Redactions == nil || !strings.Contains(s.Narrative, "I wrote this.\n\n") {
		t.Errorf("redaction markers and paragraphs: %q %v", blob, p.Redactions)
	}
	if len(p.WorkItems) != len(base.WorkItems)-1 {
		t.Errorf("the unlinked work item is gone: %d of %d", len(p.WorkItems), len(base.WorkItems))
	}
	if len(s.FilesTouched) == 0 || s.FirstMessage == "" || p.ContentHash != base.ContentHash || p.EditConflict {
		t.Errorf("facts still come from the rules, and the content hash is the session's: %+v", s)
	}
	if err := p.Validate(p.UpdatedAt); err != nil {
		t.Fatal(err)
	}

	// The session continues: the edit is for other content. Nothing of it is
	// applied, the payload is the generated one, and it is flagged.
	changed := chatty()
	changed.Turns = append(changed.Turns, model.Turn{Role: "user", Text: "one more thing", Time: changed.Turns[0].Time.Add(2 * 60e9)})
	p, err = Build(changed, Options{Home: "/home/u", Edit: edit, Model: &ModelOptions{Client: fm}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.EditConflict || p.Summary.Edited || p.Summary.Title != "Model title" || fm.calls != 1 {
		t.Fatalf("a stale edit is not applied and the conflict is flagged: %+v conflict=%v", p.Summary, p.EditConflict)
	}
	// titles-only level: the edited title is the title
	p, _ = Build(chatty(), Options{Home: "/home/u", Level: model.LevelTitlesOnly, Edit: &Edit{ContentHash: baseHash(t, model.LevelTitlesOnly), Title: "Only a title"}})
	if p.TitleOnly != "Only a title" {
		t.Errorf("titles-only: %q", p.TitleOnly)
	}
}

func baseHash(t *testing.T, level string) string {
	t.Helper()
	p, err := Build(chatty(), Options{Home: "/home/u", Level: level})
	if err != nil {
		t.Fatal(err)
	}
	return p.ContentHash
}
