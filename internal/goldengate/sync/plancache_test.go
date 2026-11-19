package sync_test

import (
	"context"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

func loadsOf(e *env) int {
	n := 0
	for _, v := range e.f.loads {
		n += v
	}
	return n
}

func offlinePlan(t *testing.T, e *env) gsync.Report {
	t.Helper()
	_, rep, err := e.s.Plan(context.Background(), gsync.RunOpts{Offline: true, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func offlineBatch(t *testing.T, e *env) []*model.SessionPayload {
	t.Helper()
	b, _, err := e.s.Plan(context.Background(), gsync.RunOpts{Offline: true, Approve: true})
	if err != nil {
		t.Fatal(err)
	}
	return b.Payloads()
}

func cacheEnv(t *testing.T) *env {
	t.Helper()
	return setup(t, selection.Policy{Allow: []string{"~/code/a"}},
		sess("s1", "/home/u/code/a", "first prompt"), sess("s2", "/home/u/code/a", "second prompt"))
}

func TestOfflinePlanReusesWhatItBuilt(t *testing.T) {
	e := cacheEnv(t)
	first := offlineBatch(t, e)
	if loadsOf(e) != 2 || len(first) != 2 {
		t.Fatalf("first plan loaded %d sessions and planned %d, want 2 and 2", loadsOf(e), len(first))
	}
	second := offlineBatch(t, e)
	if loadsOf(e) != 2 {
		t.Fatalf("second plan read the transcripts again: %d loads", loadsOf(e))
	}
	if len(second) != 2 {
		t.Fatalf("second plan has %d sessions, want 2", len(second))
	}
	for i := range second {
		if second[i].SessionID != first[i].SessionID || second[i].ContentHash != first[i].ContentHash || payloadTitleOf(second[i]) != payloadTitleOf(first[i]) {
			t.Fatalf("session %d changed between plans: %+v vs %+v", i, second[i], first[i])
		}
	}
}

func payloadTitleOf(p *model.SessionPayload) string {
	if p.Summary != nil {
		return p.Summary.Title
	}
	return p.TitleOnly
}

func TestPlanCacheMissesWhenTheFileChanges(t *testing.T) {
	e := cacheEnv(t)
	offlinePlan(t, e)
	e.f.sessions[0].Ref.SizeBytes = 99 // the transcript grew
	offlinePlan(t, e)
	if got := loadsOf(e); got != 3 {
		t.Fatalf("loads = %d, want 3 (only the changed session is read again)", got)
	}
}

func TestPlanCacheMissesWhenSettingsChange(t *testing.T) {
	cases := map[string]func(e *env){
		"share level":     func(e *env) { e.s.Config.Share.Level = model.LevelTitlesOnly },
		"redaction":       func(e *env) { e.s.Config.Redact.ExtraPatterns = []string{`secret-\d+`} },
		"summary length":  func(e *env) { e.s.Config.Summarize.Length = "detailed" },
		"issue providers": func(e *env) { e.s.Config.Issues.Providers = []string{"github"} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e := cacheEnv(t)
			offlinePlan(t, e)
			change(e)
			offlinePlan(t, e)
			if got := loadsOf(e); got != 4 {
				t.Fatalf("loads = %d, want 4: a changed setting must rebuild every session", got)
			}
		})
	}
}

func TestPlanThatSendsNeverUsesTheCache(t *testing.T) {
	e := cacheEnv(t)
	offlinePlan(t, e)
	offlinePlan(t, e)
	if _, _, err := e.s.Plan(context.Background(), gsync.RunOpts{Approve: true}); err != nil {
		t.Fatal(err)
	}
	if got := loadsOf(e); got != 4 {
		t.Fatalf("loads = %d, want 4: a plan that is not offline reads each session itself", got)
	}
}

func TestPlanCacheForgetsSessionsThatLeave(t *testing.T) {
	e := cacheEnv(t)
	offlinePlan(t, e)
	kept := e.f.sessions
	e.f.sessions = kept[:1] // s2 is gone, or its project is no longer shared
	offlinePlan(t, e)
	e.f.sessions = kept
	offlinePlan(t, e)
	if got := loadsOf(e); got != 3 {
		t.Fatalf("loads = %d, want 3: s2 must be rebuilt after it left the plan", got)
	}
}

func TestPlanCacheKeepsRuleDecisions(t *testing.T) {
	e := inboxEnv(t)
	cold := offlinePlan(t, e)
	warm := offlinePlan(t, e)
	if loadsOf(e) != 3 {
		t.Fatalf("loads = %d, want 3", loadsOf(e))
	}
	if cold.Approving != warm.Approving || cold.Pending != warm.Pending || len(cold.ApprovingItems) != len(warm.ApprovingItems) {
		t.Fatalf("a cached plan decides differently: %+v vs %+v", cold, warm)
	}
	for i := range cold.ApprovingItems {
		c, w := cold.ApprovingItems[i], warm.ApprovingItems[i]
		if c.NewGrouping != w.NewGrouping || c.Suggested != w.Suggested || c.RuleID != w.RuleID || c.ContentHash != w.ContentHash {
			t.Fatalf("item %d: %+v vs %+v", i, c, w)
		}
	}
}

func TestReviewPlanReusesBuildsOnlyWithoutAModel(t *testing.T) {
	e := cacheEnv(t)
	review := func() {
		if _, _, err := e.s.Plan(context.Background(), gsync.RunOpts{Approve: true, Review: true}); err != nil {
			t.Fatal(err)
		}
	}
	review()
	review()
	if got := loadsOf(e); got != 2 {
		t.Fatalf("loads = %d, want 2: a review without a model reuses what it built", got)
	}
	// a plan that is not marked as a review keeps reading each session
	if _, _, err := e.s.Plan(context.Background(), gsync.RunOpts{Approve: true}); err != nil {
		t.Fatal(err)
	}
	if got := loadsOf(e); got != 4 {
		t.Fatalf("loads = %d, want 4", got)
	}
}
