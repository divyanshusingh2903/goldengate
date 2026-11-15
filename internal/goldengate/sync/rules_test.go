package sync_test

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/rules"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

func onBranch(s model.RawSession, branch, remote string) model.RawSession {
	s.GitBranch, s.GitRemote = branch, remote
	return s
}

// rulesEnv is one project, "App", with a named folder and sessions on
// different branches.
func rulesEnv(t *testing.T, trust string, rs ...rules.Rule) *env {
	t.Helper()
	pol := selection.Policy{Allow: []string{"~/code/app"}, Projects: map[string]string{"~/code/app": "App"}}
	e := setup(t, pol,
		onBranch(sess("fix1", "/home/u/code/app", "FIXWORK one"), "fix/login", "git@github.com:acme/app.git"),
		onBranch(sess("fix2", "/home/u/code/app", "FIXWORK two"), "fix/logout", "git@github.com:acme/app.git"),
		onBranch(sess("wip1", "/home/u/code/app", "WIPWORK scratch"), "wip/idea", "git@github.com:acme/app.git"),
		onBranch(sess("main1", "/home/u/code/app", "MAINWORK plain"), "main", "git@github.com:acme/app.git"),
	)
	e.s.Config.Projects = []config.Project{{ID: "p_app001", Name: "App", Members: []config.Member{{Path: "~/code/app"}}, Trust: trust, Rules: rs}}
	return e
}

var (
	approveFix = rules.Rule{ID: "r_fix", Action: rules.ActionApprove, Branch: "fix/*"}
	excludeWip = rules.Rule{ID: "r_wip", Action: rules.ActionExclude, Branch: "wip/*"}
)

func sorted(c *capture) string {
	var out []string
	for _, p := range c.got {
		out = append(out, p.SessionID)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestAutoSendsWhatARuleApprovesAndTheRestWaits(t *testing.T) {
	e := rulesEnv(t, rules.TrustAuto, approveFix, excludeWip)
	ctx := context.Background()
	sink := &capture{}
	rep, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if err != nil {
		t.Fatal(err)
	}
	if sorted(sink) != "fix1,fix2" || rep.Pending != 1 || rep.RuleExcluded != 1 || rep.Excluded != 1 {
		t.Fatalf("the rule sends its matches, main waits, wip is excluded: %s %+v", sorted(sink), rep)
	}
	// every automatic send is listed with the rule that sent it
	if len(rep.Auto) != 2 || rep.Auto[0].RuleID != "r_fix" || rep.Auto[0].ProjectID != "p_app001" || rep.Auto[0].Project != "/home/u/code/app" {
		t.Fatalf("auto items: %+v", rep.Auto)
	}
	if it := rep.PendingItems[0]; it.SessionID != "main1" || it.Suggested || it.NewGrouping || it.RuleID != "" {
		t.Fatalf("the unmatched session simply waits: %+v", it)
	}
	// --approve sends what waits, never what a rule excludes
	sink = &capture{}
	if rep, err := e.s.Run(ctx, gsync.RunOpts{Approve: true}, sink, false); err != nil || sorted(sink) != "main1" || rep.RuleExcluded != 1 {
		t.Fatalf("approve sends the waiting one only: %s %+v %v", sorted(sink), rep, err)
	}
	for _, p := range sink.got {
		if strings.Contains(p.Summary.FirstMessage, "WIPWORK") {
			t.Fatal("an excluded session was sent")
		}
	}
}

func TestSuggestMarksAndSendsNothing(t *testing.T) {
	e := rulesEnv(t, rules.TrustSuggest, approveFix)
	sink := &capture{}
	rep, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false)
	if err != nil || len(sink.got) != 0 || rep.Pending != 4 || rep.Suggested != 2 || len(rep.Auto) != 0 {
		t.Fatalf("suggest sends nothing: %s %+v %v", sorted(sink), rep, err)
	}
	for _, it := range rep.PendingItems {
		want := strings.HasPrefix(it.SessionID, "fix")
		if it.Suggested != want || (want && it.RuleID != "r_fix") {
			t.Errorf("%s: suggested=%v rule=%q", it.SessionID, it.Suggested, it.RuleID)
		}
	}
}

func TestAskIgnoresApproveRulesButKeepsExcludeRules(t *testing.T) {
	e := rulesEnv(t, rules.TrustAsk, approveFix, excludeWip)
	sink := &capture{}
	rep, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false)
	if err != nil || len(sink.got) != 0 || rep.Pending != 3 || rep.Suggested != 0 || rep.RuleExcluded != 1 {
		t.Fatalf("ask: everything waits, exclude still holds: %+v %v", rep, err)
	}
}

func TestWithoutApproveRulesTheTrustLevelCoversTheProject(t *testing.T) {
	e := rulesEnv(t, rules.TrustAuto, excludeWip)
	sink := &capture{}
	rep, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false)
	if err != nil || sorted(sink) != "fix1,fix2,main1" || len(rep.Auto) != 3 || rep.Auto[0].RuleID != "" {
		t.Fatalf("auto with no approve rule sends all but the excluded: %s %+v %v", sorted(sink), rep, err)
	}
}

func TestYourOwnApprovalBeatsAnExcludeRule(t *testing.T) {
	e := rulesEnv(t, rules.TrustAsk, excludeWip)
	ctx := context.Background()
	_, rep, _ := e.s.Plan(ctx, gsync.RunOpts{Approve: true})
	if rep.RuleExcluded != 1 {
		t.Fatalf("%+v", rep)
	}
	// find the excluded session's content hash by lifting the rule for a moment
	rs := e.s.Config.Projects[0].Rules
	e.s.Config.Projects[0].Rules = nil
	_, all, _ := e.s.Plan(ctx, gsync.RunOpts{})
	e.s.Config.Projects[0].Rules = rs
	for _, it := range all.PendingItems {
		if it.SessionID == "wip1" {
			if err := e.st.Decide(ctx, gsync.Decision{Source: it.Source, SessionID: it.SessionID, State: gsync.DecisionApproved, ContentHash: it.ContentHash, At: t0}); err != nil {
				t.Fatal(err)
			}
		}
	}
	sink := &capture{}
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false); err != nil || sorted(sink) != "wip1" {
		t.Fatalf("an explicit approval of exactly this content is sent: %s %v", sorted(sink), err)
	}
}

// New-match confirmation: a project that reaches folders through a broad
// member never starts sending from a folder nobody named. The first session
// from a new folder waits, marked; once the user has sent one, the rule
// takes over there.
func TestABroadPatternAsksBeforeItFirstSendsFromANewFolder(t *testing.T) {
	pol := selection.Policy{Allow: []string{"~/work/*", "~/code/named"}, Projects: map[string]string{"~/work/*": "Work", "~/code/named": "Work"}}
	e := setup(t, pol,
		sess("n1", "/home/u/code/named", "named folder"),
		sess("a1", "/home/u/work/alpha", "alpha one"),
		sess("b1", "/home/u/work/beta", "beta one"),
	)
	e.s.Config.Projects = []config.Project{{ID: "p_work01", Name: "Work", Trust: rules.TrustAuto,
		Members: []config.Member{{Path: "~/work/*"}, {Path: "~/code/named"}}}}
	ctx := context.Background()

	sink := &capture{}
	rep, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if err != nil || sorted(sink) != "n1" || rep.NewGroupings != 2 || rep.Pending != 2 {
		t.Fatalf("only the folder the user named is sent without asking: %s %+v %v", sorted(sink), rep, err)
	}
	for _, it := range rep.PendingItems {
		if !it.NewGrouping {
			t.Errorf("%s waits because its folder is new: %+v", it.SessionID, it)
		}
	}
	// the user approves alpha's session: that is the confirmation for alpha
	for _, it := range rep.PendingItems {
		if it.SessionID == "a1" {
			if err := e.st.Decide(ctx, gsync.Decision{Source: it.Source, SessionID: it.SessionID, State: gsync.DecisionApproved, ContentHash: it.ContentHash, At: t0}); err != nil {
				t.Fatal(err)
			}
		}
	}
	sink = &capture{}
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false); err != nil || sorted(sink) != "a1" {
		t.Fatalf("the approved session goes: %s %v", sorted(sink), err)
	}
	// from now on alpha is automatic; beta, and a folder that appears later, still ask
	e.f.sessions = append(e.f.sessions, sess("a2", "/home/u/work/alpha", "alpha two"), sess("g1", "/home/u/work/gamma", "gamma one"))
	sink = &capture{}
	rep, err = e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if err != nil || sorted(sink) != "a2" || rep.NewGroupings != 2 || len(rep.Auto) != 1 {
		t.Fatalf("alpha is automatic now, beta and gamma ask: %s %+v %v", sorted(sink), rep, err)
	}
}

func TestSessionsARuleHoldsBackNeverReachTheModel(t *testing.T) {
	e := rulesEnv(t, rules.TrustAuto, approveFix, excludeWip)
	rec := &recorder{}
	e.s.Summarizer = rec
	sink := &capture{}
	if _, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false); err != nil || sorted(sink) != "fix1,fix2" {
		t.Fatalf("%s %v", sorted(sink), err)
	}
	if len(rec.inputs) != 2 {
		t.Fatalf("the model sees the two sessions that are sent: %d calls", len(rec.inputs))
	}
	for _, in := range rec.inputs {
		if strings.Contains(in, "WIPWORK") || strings.Contains(in, "MAINWORK") {
			t.Fatal("a waiting or excluded session was sent to the model")
		}
	}
}

// Property: deny beats rules. Whatever rules and trust levels the projects
// have, a session in a denied folder, or of a denied source, is never sent,
// never listed as waiting and never loaded.
func TestPropertyDenyBeatsRules(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	trusts := []string{rules.TrustAsk, rules.TrustSuggest, rules.TrustAuto}
	conds := []rules.Rule{{}, {Source: "claude-code"}, {Folder: "~/code"}, {Folder: "~/code/secret"}, {Folder: "~/code/*"}, {Branch: "*"}, {Branch: "main"}}
	for i := 0; i < 60; i++ {
		pol := selection.Policy{Allow: []string{"~/code/*"}, Deny: []string{"~/code/secret"}, Projects: map[string]string{"~/code/*": "All"}}
		e := setup(t, pol,
			onBranch(sess("ok1", "/home/u/code/open", "open work"), "main", ""),
			onBranch(sess("deny1", "/home/u/code/secret", "DENIEDTEXT one"), "main", ""),
			onBranch(sess("deny2", "/home/u/code/secret/sub", "DENIEDTEXT two"), "main", ""),
		)
		var rs []rules.Rule
		for n := rng.Intn(4); n > 0; n-- {
			r := conds[rng.Intn(len(conds))]
			r.ID, r.Action = fmt.Sprintf("r_%d", len(rs)), rules.ActionApprove
			rs = append(rs, r)
		}
		e.s.Config.Projects = []config.Project{{ID: "p_all001", Name: "All", Trust: trusts[rng.Intn(3)], Members: []config.Member{{Path: "~/code/*"}}, Rules: rs}}
		for _, o := range []gsync.RunOpts{{}, {Approve: true}} {
			sink := &capture{}
			rep, err := e.s.Run(context.Background(), o, sink, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range sink.got {
				if strings.HasPrefix(p.SessionID, "deny") {
					t.Fatalf("rules %+v sent a denied session", rs)
				}
			}
			for _, it := range rep.PendingItems {
				if strings.HasPrefix(it.SessionID, "deny") {
					t.Fatalf("rules %+v listed a denied session as waiting", rs)
				}
			}
			if rep.NotSelected != 2 {
				t.Fatalf("both denied sessions are dropped before anything else: %+v", rep)
			}
		}
		if e.f.loads["deny1"]+e.f.loads["deny2"] != 0 || strings.Contains(e.log.String(), "DENIEDTEXT") {
			t.Fatalf("a denied session was read: %v", e.f.loads)
		}
	}
}

func TestFactsNeverComeFromTheSummary(t *testing.T) {
	raw := &model.RawSession{GitBranch: "fix/a", GitRemote: "git@github.com:acme/app.git"}
	p := &model.SessionPayload{
		Summary: &model.Summary{Title: "on branch release/1 touching LIN-9", Narrative: "LIN-9 release/1", GitBranch: "release/1"},
		WorkItems: []model.WorkItem{{Kind: model.KindIssue, Key: "linear:LIN-1"}, {Kind: model.KindPR, Key: "acme/app#2"},
			{Kind: model.KindCommit, Key: "app@abc1234"}, {Kind: model.KindFile, Key: "release/notes.md"}},
	}
	f := gsync.Facts(model.SessionRef{Source: "codex", ProjectKey: "/p"}, raw, p)
	if f.Branch != "fix/a" || f.Remote != raw.GitRemote || f.Source != "codex" || f.Folder != "/p" || strings.Join(f.WorkItems, " ") != "linear:LIN-1 acme/app#2" {
		t.Fatalf("facts come from the tool's record and the extracted issues and pull requests only: %+v", f)
	}
}
