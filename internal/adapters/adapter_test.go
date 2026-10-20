package adapters

import (
	"context"
	"iter"
	"testing"

	"github.com/OWNER/goldengate/internal/model"
)

type stub string

func (s stub) Name() string                            { return string(s) }
func (stub) Detect(context.Context) (Detection, error) { return Detection{}, nil }
func (stub) ListSessions(context.Context, ListOpts) iter.Seq2[model.SessionRef, error] {
	return func(func(model.SessionRef, error) bool) {}
}
func (stub) Load(context.Context, model.SessionRef) (*model.RawSession, error) { return nil, nil }

func TestRegistry(t *testing.T) {
	var r Registry
	for _, n := range []string{"b", "a"} {
		if err := r.Register(stub(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Register(stub("a")); err == nil {
		t.Fatal("duplicate must fail")
	}
	all := r.All()
	if len(all) != 2 || all[0].Name() != "a" {
		t.Fatalf("not sorted: %v", all)
	}
	if _, ok := r.Get("b"); !ok {
		t.Fatal("missing b")
	}
	if _, ok := r.Get("zz"); ok {
		t.Fatal("unexpected zz")
	}
}

func TestIsSkip(t *testing.T) {
	if !IsSkip(&SkipWarning{Path: "p", Reason: "r"}) || IsSkip(context.Canceled) {
		t.Fatal("IsSkip")
	}
}
