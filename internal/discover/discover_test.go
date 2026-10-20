package discover_test

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/discover"
	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/project"
	"github.com/OWNER/goldengate/internal/selection"
)

type fake struct {
	name string
	refs []model.SessionRef
}

func (f fake) Name() string { return f.name }
func (fake) Detect(context.Context) (adapters.Detection, error) {
	return adapters.Detection{Installed: true}, nil
}
func (f fake) ListSessions(context.Context, adapters.ListOpts) iter.Seq2[model.SessionRef, error] {
	return func(y func(model.SessionRef, error) bool) {
		for _, r := range f.refs {
			if !y(r, nil) {
				return
			}
		}
		y(model.SessionRef{}, &adapters.SkipWarning{Path: "x", Reason: "odd"})
	}
}
func (fake) Load(context.Context, model.SessionRef) (*model.RawSession, error) { return nil, nil }

func TestGroupsAcrossToolsAndDecides(t *testing.T) {
	now := time.Now()
	ref := func(src, id, proj string, age time.Duration) model.SessionRef {
		return model.SessionRef{Source: src, ID: id, ProjectKey: proj, UpdatedAt: now.Add(-age)}
	}
	var reg adapters.Registry
	_ = reg.Register(fake{"claude-code", []model.SessionRef{ref("claude-code", "1", "/h/a", time.Hour), ref("claude-code", "2", "/h/b", 2*time.Hour)}})
	_ = reg.Register(fake{"codex", []model.SessionRef{ref("codex", "3", "/h/a", time.Minute)}})
	m, _ := selection.New(selection.Policy{Allow: []string{"/h/a"}}, selection.Options{})
	res, err := discover.Projects(context.Background(), &reg, m, time.Time{}, project.Resolver{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Projects) != 2 || len(res.Warnings) != 2 {
		t.Fatalf("%+v", res)
	}
	a := res.Projects[0]
	if a.Key != "/h/a" || a.Sessions != 2 || a.Sources["claude-code"] != 1 || a.Sources["codex"] != 1 || !a.Decision.Shared {
		t.Fatalf("grouping wrong: %+v", a)
	}
	if res.Projects[1].Decision.Shared {
		t.Fatal("b must not be shared")
	}
}

func TestWorktreesGroupWithTheirRepository(t *testing.T) {
	now := time.Now()
	var reg adapters.Registry
	_ = reg.Register(fake{"claude-code", []model.SessionRef{
		{Source: "claude-code", ID: "1", ProjectKey: "/h/app", UpdatedAt: now},
		{Source: "claude-code", ID: "2", ProjectKey: "/h/app/.claude/worktrees/fox", UpdatedAt: now},
	}})
	_ = reg.Register(fake{"opencode", []model.SessionRef{
		{Source: "opencode", ID: "3", ProjectKey: "/h/app-wt", RepoRoot: "/h/app", UpdatedAt: now},
	}})
	m, _ := selection.New(selection.Policy{Default: "allow"}, selection.Options{})
	res, err := discover.Projects(context.Background(), &reg, m, time.Time{}, project.Resolver{})
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string][]string{}
	for _, p := range res.Projects {
		groups[p.Group()] = append(groups[p.Group()], p.Key)
	}
	if len(groups) != 1 || len(groups["/h/app"]) != 3 {
		t.Fatalf("worktrees from different tools must group under the repository: %v", groups)
	}
}
