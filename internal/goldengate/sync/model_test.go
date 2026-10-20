package sync_test

import (
	"context"
	"errors"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
)

// recorder is a fake model that remembers everything sent to it.
type recorder struct {
	mu     stdsync.Mutex
	inputs []string
	fail   bool
}

func (r *recorder) Name() string { return "fake:model" }
func (r *recorder) JSON(_ context.Context, _, user string, _ map[string]any) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, user)
	if r.fail {
		return nil, errors.New("model down")
	}
	return []byte(`{"title":"Built the widget","goals":["Build the widget"],"description":["Added widget.go"],"outcomes":["Done"]}`), nil
}

func TestModelSummarizerSeesOnlySharedSessions(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}},
		sess("a", "/home/u/code/app", "build the widget"),
		sess("b", "/home/u/code/app", "and the gadget"),
		sess("c", "/home/u/work/secret", "TOPSECRET plans"),
	)
	rec := &recorder{}
	e.s.Summarizer = rec
	sink := &capture{}
	rep, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.inputs) != 2 || len(sink.got) != 2 || rep.ModelFailures != 0 {
		t.Fatalf("one call per shared session: %d calls, %d sent, %+v", len(rec.inputs), len(sink.got), rep)
	}
	for _, in := range rec.inputs {
		if strings.Contains(in, "TOPSECRET") {
			t.Fatal("an unshared session reached the model")
		}
	}
	for _, p := range sink.got {
		if p.Summary.SummarizedBy != "fake:model" || p.Summary.Title != "Built the widget" {
			t.Errorf("the model's summary is sent: %+v", p.Summary)
		}
	}
}

func TestPreviewNeverCallsTheModel(t *testing.T) {
	e := setup(t, selection.Policy{}, sess("a", "/home/u/code/app", "build the widget")) // nothing shared
	rec := &recorder{}
	e.s.Summarizer = rec
	pv, err := e.s.Preview(context.Background(), "/home/u/code/app", 10)
	if err != nil || len(pv.Items) != 1 {
		t.Fatalf("%v %+v", err, pv)
	}
	if len(rec.inputs) != 0 || pv.Items[0].Payload.Summary.SummarizedBy != "heuristic" {
		t.Fatal("a preview uses only cached model answers")
	}
}

func TestModelFailureFallsBackAndIsCounted(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "build the widget"))
	e.s.Summarizer = &recorder{fail: true}
	sink := &capture{}
	rep, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false)
	if err != nil || len(sink.got) != 1 || rep.ModelFailures != 1 || sink.got[0].Summary.SummarizedBy != "heuristic" {
		t.Fatalf("the session is still sent with its heuristic summary: %v %+v", err, rep)
	}
}

func TestResummarizeUpgradesHeuristicSessions(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}},
		sess("a", "/home/u/code/app", "build the widget"),
		sess("b", "/home/u/code/app", "and the gadget"),
		sess("c", "/home/u/work/secret", "TOPSECRET plans"),
	)
	ctx := context.Background()
	// first synced with the heuristic
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, &capture{}, false); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	e.s.Summarizer = rec

	// turning the model on alone changes nothing that is already pushed
	sink := &capture{}
	rep, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 0 || len(rec.inputs) != 0 || rep.Stale != 2 || rep.Unchanged != 2 {
		t.Fatalf("without the flag: %d sent, %d calls, %+v", len(sink.got), len(rec.inputs), rep)
	}

	// a dry run with the flag asks the model once per shared session
	dry := &capture{}
	rep, err = e.s.Run(ctx, gsync.RunOpts{Resummarize: true}, dry, true)
	if err != nil || len(dry.got) != 2 || len(rec.inputs) != 2 || rep.Resummarized != 2 || rep.Stale != 0 {
		t.Fatalf("dry run with the flag: %v, %d planned, %d calls, %+v", err, len(dry.got), len(rec.inputs), rep)
	}
	for _, in := range rec.inputs {
		if strings.Contains(in, "TOPSECRET") {
			t.Fatal("an unshared session reached the model")
		}
	}

	// the real run reuses the cached answers and pushes the upgraded summaries
	sink = &capture{}
	if _, err := e.s.Run(ctx, gsync.RunOpts{Resummarize: true}, sink, false); err != nil {
		t.Fatal(err)
	}
	if len(rec.inputs) != 2 {
		t.Fatalf("cached answers are not asked for again: %d calls", len(rec.inputs))
	}
	if len(sink.got) != 2 {
		t.Fatalf("both sessions are pushed again: %d", len(sink.got))
	}
	for _, p := range sink.got {
		if p.Summary.SummarizedBy != "fake:model" {
			t.Errorf("the model's summary is pushed: %+v", p.Summary)
		}
	}

	// once upgraded there is nothing stale left, and the flag sends nothing
	sink = &capture{}
	rep, err = e.s.Run(ctx, gsync.RunOpts{Resummarize: true}, sink, false)
	if err != nil || len(sink.got) != 0 || rep.Stale != 0 || rep.Resummarized != 0 || len(rec.inputs) != 2 {
		t.Fatalf("after the upgrade: %v, %d sent, %d calls, %+v", err, len(sink.got), len(rec.inputs), rep)
	}
}

func TestResummarizeKeepsSessionsTheModelCannotSummarize(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "build the widget"))
	ctx := context.Background()
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, &capture{}, false); err != nil {
		t.Fatal(err)
	}
	e.s.Summarizer = &recorder{fail: true}
	sink := &capture{}
	rep, err := e.s.Run(ctx, gsync.RunOpts{Resummarize: true}, sink, false)
	if err != nil || len(sink.got) != 0 || rep.ModelFailures != 1 {
		t.Fatalf("an unchanged heuristic summary is not pushed again: %v, %d sent, %+v", err, len(sink.got), rep)
	}
}
