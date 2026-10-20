package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		Description:  []string{"Changed auth.go to keep the redirect URL; the leaked key " + fakeSecret() + " was rotated", "Added auth_test.go"},
		OpenProblems: []string{"The staging redirect is still untested"},
	})}
	p, err := Build(session(), Options{Home: "/home/u", Model: &ModelOptions{Client: fm}})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Summary
	if s.Title != "Fix the login redirect and add tests" || len(s.Goals) != 2 || len(s.Description) != 2 || len(s.OpenProblems) != 1 || s.SummarizedBy != "fake:small" {
		t.Fatalf("the model's answer is used: %+v", s)
	}
	if got := strings.Join(s.Outcomes, ", "); got != "tests passed, commit made" {
		t.Errorf("outcomes come from the command results, never the model: %q", got)
	}
	if len(s.FilesTouched) == 0 || len(s.Commands) == 0 || len(p.WorkItems) == 0 && len(session().Turns) == 0 {
		t.Error("files and commands still come from the rules")
	}
	if strings.Contains(strings.Join(s.Description, " "), fakeSecret()) {
		t.Error("the model's answer is redacted again")
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
	if _, err := Build(session(), Options{Home: "/home/u", Model: &ModelOptions{Client: fm}}); err != nil {
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
	s := session()
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
	p, _ := Build(session(), Options{Model: off})
	if fm.calls != 0 || p.Summary.SummarizedBy != Heuristic {
		t.Fatal("offline with nothing cached keeps the heuristic and calls nothing")
	}
	on := &ModelOptions{Client: fm, Cache: c}
	a, _ := Build(session(), Options{Model: on})
	b, _ := Build(session(), Options{Model: on})
	if fm.calls != 1 || a.ContentHash != b.ContentHash {
		t.Fatalf("one call per content; the same payload after: %d calls", fm.calls)
	}
	p, _ = Build(session(), Options{Model: off})
	if fm.calls != 1 || p.Summary.Title != "Cached title" {
		t.Fatal("offline uses the cached answer")
	}
}

func TestModelFailureKeepsTheHeuristic(t *testing.T) {
	var got error
	fm := &fakeModel{err: errors.New("503")}
	p, err := Build(session(), Options{Model: &ModelOptions{Client: fm, OnError: func(e error) { got = e }}})
	if err != nil || got == nil || p.Summary.SummarizedBy != Heuristic || len(p.Summary.Goals) != 1 {
		t.Fatalf("a failed call is reported and the heuristic summary stays: %v %+v", got, p.Summary)
	}
	fm = &fakeModel{answer: "not json"}
	p, _ = Build(session(), Options{Model: &ModelOptions{Client: fm}})
	if p.Summary.SummarizedBy != Heuristic {
		t.Error("an unreadable answer keeps the heuristic")
	}
}

func TestModelListsAreCapped(t *testing.T) {
	many := make([]string, 20)
	for i := range many {
		many[i] = strings.Repeat("x", 500)
	}
	fm := &fakeModel{answer: answer(modelAnswer{Title: "t", Goals: many, Description: many, OpenProblems: many})}
	p, _ := Build(session(), Options{Model: &ModelOptions{Client: fm}})
	if err := p.Validate(p.UpdatedAt); err != nil {
		t.Fatalf("capped to the limits: %v", err)
	}
	if b, _ := json.Marshal(p.Summary); len(b) > MaxSummaryBytes {
		t.Errorf("summary is %d bytes", len(b))
	}
}

func TestModelTextShrinksBeforeExtractedFacts(t *testing.T) {
	s := session()
	for i := 0; i < 20; i++ {
		s.FilesTouched = append(s.FilesTouched, fmt.Sprintf("pkg/file%02d.go", i))
	}
	long := make([]string, 8)
	for i := range long {
		long[i] = strings.Repeat("a long explanation ", 15)
	}
	fm := &fakeModel{answer: answer(modelAnswer{Title: "t", Goals: long, Description: long, OpenProblems: long})}
	p, _ := Build(s, Options{Model: &ModelOptions{Client: fm}})
	if len(p.Summary.FilesTouched) < 15 {
		t.Errorf("a wordy answer must not push out file names: %d left", len(p.Summary.FilesTouched))
	}
	if len(p.Summary.OpenProblems) > maxOpenProblems {
		t.Errorf("open problems are capped: %d", len(p.Summary.OpenProblems))
	}
}
