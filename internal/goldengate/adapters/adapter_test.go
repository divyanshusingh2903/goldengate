package adapters

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/model"
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

func TestIsIssueTool(t *testing.T) {
	for name, want := range map[string]bool{
		"mcp__abc-123__save_issue": true, "mcp__x__get_issue": true, "linear_save_issue": true, "github_issue_write": true,
		"github_issue_read": true, "issue_write": true, "mcp__gh__create_issue": true,
		"bash": false, "mcp__x__list_issues": false, "github_search_issues": false, "reissue_write_all": false, "": false,
	} {
		if got := IsIssueTool(name); got != want {
			t.Errorf("%q: got %v want %v", name, got, want)
		}
	}
}

func TestTrimIssueInputDropsBodies(t *testing.T) {
	got := string(TrimIssueInput([]byte(`{"method":"create","owner":"o","repo":"r","title":"T","body":"SECRET","description":"SECRET2","labels":["x"],"issue_number":3}`)))
	for _, bad := range []string{"SECRET", "labels", "description", "body"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q kept: %s", bad, got)
		}
	}
	for _, keep := range []string{`"method":"create"`, `"owner":"o"`, `"issue_number":3`, `"title":"T"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s dropped: %s", keep, got)
		}
	}
	if TrimIssueInput([]byte("not json")) != nil {
		t.Error("garbage yields nil")
	}
}
