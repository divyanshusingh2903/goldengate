package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"strings"
	"testing"
)

type fakeModel struct {
	answer string
	err    error
	calls  int
	input  string
}

func (f *fakeModel) Name() string { return "fake:small" }
func (f *fakeModel) JSON(_ context.Context, _, user string, _ map[string]any) ([]byte, error) {
	f.calls++
	f.input = user
	return []byte(f.answer), f.err
}

type mapCache map[string][]byte

func (c mapCache) Get(k string) ([]byte, bool) { v, ok := c[k]; return v, ok }
func (c mapCache) Put(k string, v []byte)      { c[k] = v }

func answer(a modelAnswer) string { b, _ := json.Marshal(a); return string(b) }

func TestModelSummarizerWritesTheWorkDone(t *testing.T) {
	fm := &fakeModel{answer: answer(modelAnswer{
		Title:        "Fix the login redirect and add tests",
		Goals:        []string{"Fix the login bug", "Add a regression test"},
		Abstract:     "Fixed the login redirect and added a regression test; staging is untested.",
		Narrative:    "The redirect was rebuilt without the original path, so auth.go now keeps the URL. The leaked key " + fakeSecret() + " was rotated.\n\nA regression test went into auth_test.go. Rewriting the callback was considered and rejected as too wide a change.",
		OpenProblems: []string{"The staging redirect is still untested"},
	})}
	p, err := Build(chatty(), Options{Home: "/home/u", Model: &ModelOptions{Client: fm}})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Summary
	if s.Title != "Fix the login redirect and add tests" || len(s.Goals) != 2 || len(s.Description) != 0 || len(s.OpenProblems) != 1 || s.SummarizedBy != "fake:small" {
		t.Fatalf("the model's answer is used: %+v", s)
	}
	if got := strings.Join(s.Outcomes, ", "); got != "tests passed, commit made" {
		t.Errorf("outcomes come from the command results, never the model: %q", got)
	}
	if len(s.FilesTouched) == 0 || len(s.Commands) == 0 || len(p.WorkItems) == 0 && len(chatty().Turns) == 0 {
		t.Error("files and commands still come from the rules")
	}
	if strings.Contains(s.Narrative, fakeSecret()) || !strings.Contains(s.Narrative, "[REDACTED") {
		t.Errorf("the model's answer is redacted again: %q", s.Narrative)
	}
	if !strings.Contains(s.Narrative, "\n\nA regression test") || s.NarrativeBy != "fake:small" || !strings.HasPrefix(s.Abstract, "Fixed the login redirect") {
		t.Errorf("the narrative keeps its paragraphs and names its writer: %+v", s)
	}
	if s.FirstMessage == "" || s.LastMessage == "" {
		t.Error("first and last message still come from the session")
	}
	if err := p.Validate(p.UpdatedAt); err != nil {
		t.Errorf("payload is valid: %v", err)
	}
}

func TestModelInputIsTheRedactedConversationOnly(t *testing.T) {
	fm := &fakeModel{answer: answer(modelAnswer{Title: "t", Goals: []string{"g"}})}
	if _, err := Build(chatty(), Options{Home: "/home/u", Model: &ModelOptions{Client: fm}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{fakeSecret(), "/home/u/code/other", "[main abc1234]", "super secret msg"} {
		if strings.Contains(fm.input, bad) {
			t.Errorf("the model input contains %q:\n%s", bad, fm.input)
		}
	}
	for _, want := range []string{"[developer]", "Fix the login bug", "[assistant]", "Commands run: git commit, go test", "Observed results:"} {
		if !strings.Contains(fm.input, want) {
			t.Errorf("the model input lacks %q:\n%s", want, fm.input)
		}
	}
}

func TestModelInputCutsTheMiddleOfLongSessions(t *testing.T) {
	s := chatty()
	for i := 0; i < 200; i++ {
		s.Turns = append(s.Turns, s.Turns[0], s.Turns[1])
	}
	fm := &fakeModel{answer: answer(modelAnswer{Title: "t"})}
	if _, err := Build(s, Options{Model: &ModelOptions{Client: fm, MaxInputChars: 3000}}); err != nil {
		t.Fatal(err)
	}
	if len(fm.input) > 4000 || !strings.Contains(fm.input, "middle of the conversation is omitted") || strings.Contains(fm.input, fakeSecret()) {
		t.Errorf("long input is cut and still redacted (%d bytes)", len(fm.input))
	}
}

func TestModelAnswersAreCachedAndOfflineNeverCalls(t *testing.T) {
	fm := &fakeModel{answer: answer(modelAnswer{Title: "Cached title"})}
	c := mapCache{}
	off := &ModelOptions{Client: fm, Cache: c, Offline: true}
	p, _ := Build(chatty(), Options{Model: off})
	if fm.calls != 0 || p.Summary.SummarizedBy != Heuristic {
		t.Fatal("offline with nothing cached keeps the heuristic and calls nothing")
	}
	on := &ModelOptions{Client: fm, Cache: c}
	a, _ := Build(chatty(), Options{Model: on})
	b, _ := Build(chatty(), Options{Model: on})
	if fm.calls != 1 || a.ContentHash != b.ContentHash {
		t.Fatalf("one call per content; the same payload after: %d calls", fm.calls)
	}
	p, _ = Build(chatty(), Options{Model: off})
	if fm.calls != 1 || p.Summary.Title != "Cached title" {
		t.Fatal("offline uses the cached answer")
	}
}

func TestFingerprintIgnoresTheSummary(t *testing.T) {
	plain, _ := Build(chatty(), Options{})
	one, _ := Build(chatty(), Options{Model: &ModelOptions{Client: &fakeModel{answer: answer(modelAnswer{Title: "One"})}}})
	two, _ := Build(chatty(), Options{Model: &ModelOptions{Client: &fakeModel{answer: answer(modelAnswer{Title: "Two"})}}})
	if one.Summary.Title == two.Summary.Title {
		t.Fatal("the answers differ")
	}
	if plain.ContentHash != one.ContentHash || one.ContentHash != two.ContentHash {
		t.Fatal("a different summary of the same conversation is not a change")
	}
}

func TestModelFailureKeepsTheHeuristic(t *testing.T) {
	var got error
	fm := &fakeModel{err: errors.New("503")}
	p, err := Build(chatty(), Options{Model: &ModelOptions{Client: fm, OnError: func(e error) { got = e }}})
	if err != nil || got == nil || p.Summary.SummarizedBy != Heuristic || len(p.Summary.Goals) != 1 {
		t.Fatalf("a failed call is reported and the heuristic summary stays: %v %+v", got, p.Summary)
	}
	fm = &fakeModel{answer: "not json"}
	p, _ = Build(chatty(), Options{Model: &ModelOptions{Client: fm}})
	if p.Summary.SummarizedBy != Heuristic {
		t.Error("an unreadable answer keeps the heuristic")
	}
}

func TestModelListsAreCapped(t *testing.T) {
	many := make([]string, 20)
	for i := range many {
		many[i] = strings.Repeat("x", 500)
	}
	fm := &fakeModel{answer: answer(modelAnswer{Title: "t", Goals: many, Abstract: many[0], OpenProblems: many})}
	p, _ := Build(chatty(), Options{Model: &ModelOptions{Client: fm}})
	if err := p.Validate(p.UpdatedAt); err != nil {
		t.Fatalf("capped to the limits: %v", err)
	}
	if b, _ := json.Marshal(p.Summary); len(b) > MaxSummaryBytes {
		t.Errorf("summary is %d bytes", len(b))
	}
}

func TestModelTextShrinksBeforeExtractedFacts(t *testing.T) {
	s := chatty()
	for i := 0; i < 20; i++ {
		s.FilesTouched = append(s.FilesTouched, fmt.Sprintf("pkg/file%02d.go", i))
	}
	long := make([]string, 8)
	for i := range long {
		long[i] = strings.Repeat("a long explanation ", 15)
	}
	fm := &fakeModel{answer: answer(modelAnswer{Title: "t", Goals: long, Narrative: strings.Repeat("A long explanation of the work. ", 200), OpenProblems: long})}
	p, _ := Build(s, Options{Model: &ModelOptions{Client: fm}})
	if len(p.Summary.FilesTouched) < 15 {
		t.Errorf("a wordy answer must not push out file names: %d left", len(p.Summary.FilesTouched))
	}
	if err := p.Validate(p.UpdatedAt); err != nil {
		t.Errorf("a long narrative and full facts still fit their separate caps: %v", err)
	}
	if len(p.Summary.OpenProblems) > maxOpenProblems {
		t.Errorf("open problems are capped: %d", len(p.Summary.OpenProblems))
	}
}

// multiTask is a model answer for a session that covered several tasks, with
// the things a narrative must never carry: code, a secret, an absolute path.
func multiTask(paragraphs int) string {
	var b strings.Builder
	for i := 0; i < paragraphs; i++ {
		fmt.Fprintf(&b, "Task %d changed the handler in pkg/file%02d.go and kept the old behaviour behind a flag, because removing it would break older clients. A rewrite was considered and rejected as too wide. ", i+1, i)
		b.WriteString(strings.Repeat("More detail on the decision follows here. ", 6))
		b.WriteString("\n\n")
	}
	b.WriteString("The token " + fakeSecret() + " was pasted by mistake, and /home/u/code/other/notes.txt was read.\n\n```go\nfunc leak() string { return \"body\" }\n```\n")
	return b.String()
}

func TestDetailedNarrativeCoversEachTaskWithinTheCapAndCarriesNoCodeOrSecrets(t *testing.T) {
	fm := &fakeModel{answer: answer(modelAnswer{Title: "Three tasks", Narrative: multiTask(3), Goals: []string{"a", "b", "c"}})}
	p, err := Build(chatty(), Options{Home: "/home/u", Model: &ModelOptions{Client: fm, Length: LengthDetailed}})
	if err != nil {
		t.Fatal(err)
	}
	n := p.Summary.Narrative
	for _, want := range []string{"Task 1 ", "Task 2 ", "Task 3 ", "[code omitted]"} {
		if !strings.Contains(n, want) {
			t.Errorf("narrative lacks %q:\n%s", want, n)
		}
	}
	for _, bad := range []string{fakeSecret(), "func leak", "```", "/home/u"} {
		if strings.Contains(n, bad) {
			t.Errorf("narrative contains %q:\n%s", bad, n)
		}
	}
	if strings.Count(n, "\n\n") < 3 {
		t.Errorf("paragraphs are kept: %q", n)
	}
	if err := p.Validate(p.UpdatedAt); err != nil {
		t.Fatalf("valid payload: %v", err)
	}
	if p.Summary.AbstractText() == "" || strings.Contains(p.Summary.AbstractText(), "\n") || len(p.Summary.AbstractText()) > model.MaxAbstractLen {
		t.Errorf("the abstract is derived from the first sentence: %q", p.Summary.AbstractText())
	}
}

func TestNarrativeLengthSettingBoundsTheNarrative(t *testing.T) {
	caps := map[string]int{LengthShort: 800, "": 2000, LengthStandard: 2000, LengthDetailed: model.MaxNarrativeLen}
	for length, max := range caps {
		fm := &fakeModel{answer: answer(modelAnswer{Title: "t", Narrative: multiTask(40)})}
		p, err := Build(chatty(), Options{Model: &ModelOptions{Client: fm, Length: length}})
		if err != nil {
			t.Fatal(err)
		}
		n := []rune(p.Summary.Narrative)
		if len(n) > max || len(n) < max/2 {
			t.Errorf("%q: narrative is %d characters, cap %d", length, len(n), max)
		}
		if strings.HasSuffix(p.Summary.Narrative, "…") {
			t.Errorf("%q: cut at a paragraph or sentence end, not mid-word: …%q", length, string(n[len(n)-40:]))
		}
		if err := p.Validate(p.UpdatedAt); err != nil {
			t.Errorf("%q: %v", length, err)
		}
	}
	// The length is part of the cache key: changing it asks the model again.
	fm := &fakeModel{answer: answer(modelAnswer{Title: "t", Narrative: "One paragraph."})}
	c := mapCache{}
	for _, l := range []string{LengthShort, LengthShort, LengthDetailed, "", LengthStandard} {
		if _, err := Build(chatty(), Options{Model: &ModelOptions{Client: fm, Cache: c, Length: l}}); err != nil {
			t.Fatal(err)
		}
	}
	if fm.calls != 3 {
		t.Errorf("one call per length (empty means standard): %d", fm.calls)
	}
}

func TestHeuristicWritesNoNarrative(t *testing.T) {
	p, _ := Build(chatty(), Options{})
	b, _ := json.Marshal(p.Summary)
	for _, k := range []string{"narrative", "abstract", "narrative_by"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("the heuristic payload must not change: it carries %q", k)
		}
	}
}

func TestAUserNarrativeIsNeverOverwrittenByAModel(t *testing.T) {
	user := func(source, id string) (string, bool) {
		return "I wrote this myself.\n\nIt mentions " + fakeSecret() + " by mistake.", true
	}
	fm := &fakeModel{answer: answer(modelAnswer{Title: "Model title", Abstract: "Model abstract.", Narrative: "The model's own story.", Goals: []string{"g"}})}
	p, err := Build(chatty(), Options{UserNarrative: user, Model: &ModelOptions{Client: fm}})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Summary
	if !strings.HasPrefix(s.Narrative, "I wrote this myself.\n\n") || s.NarrativeBy != model.NarrativeByUser || s.Abstract != "" {
		t.Fatalf("the user's narrative wins: %+v", s)
	}
	if strings.Contains(s.Narrative, fakeSecret()) {
		t.Error("a user narrative is redacted like everything else")
	}
	if fm.calls != 1 || s.Title != "Model title" || s.SummarizedBy != "fake:small" {
		t.Errorf("the model still writes the other fields: %+v", s)
	}
	if s.AbstractText() != "I wrote this myself." {
		t.Errorf("abstract comes from the user's text: %q", s.AbstractText())
	}
	// Without a model the user's narrative is used too.
	p, _ = Build(chatty(), Options{UserNarrative: user})
	if p.Summary.NarrativeBy != model.NarrativeByUser || p.Summary.SummarizedBy != Heuristic {
		t.Errorf("heuristic facts with a user narrative: %+v", p.Summary)
	}
	if err := p.Validate(p.UpdatedAt); err != nil {
		t.Error(err)
	}
}

func TestTrivialSessionsKeepTheRules(t *testing.T) {
	s := session()
	s.Turns = []model.Turn{{Role: "user", Text: "nothing"}, {Role: "assistant", Text: "Hi! How can I help?", LastText: "Hi! How can I help?"}}
	fm := &fakeModel{answer: answer(modelAnswer{Title: "Invented", Goals: []string{"invented"}})}
	p, _ := Build(s, Options{Model: &ModelOptions{Client: fm}})
	if fm.calls != 0 || p.Summary.SummarizedBy != Heuristic {
		t.Fatal("a near-empty session is not sent to the model, which would invent work")
	}
}

func TestNoProblemFillerIsDropped(t *testing.T) {
	got := realProblems([]string{
		"The conversation does not mention any specific issues or problems that need to be addressed.",
		"None", "No open problems.", "N/A", "No issues were identified.",
		"The staging redirect test still fails",
	})
	if len(got) != 1 || got[0] != "The staging redirect test still fails" {
		t.Errorf("only real problems stay: %q", got)
	}
}

// chatty is the test session with a real amount of conversation, so the
// model is asked (a near-empty session never is).
func chatty() *model.RawSession {
	s := session()
	s.Turns[1].Text = "I traced the redirect to auth.go, where the callback URL was rebuilt without the original path. " +
		"I kept the original URL in the session, added a regression test, ran go test and committed the fix."
	return s
}
