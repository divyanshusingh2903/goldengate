package rules

import (
	"math/rand"
	"strings"
	"testing"
)

const home = "/home/u"

func TestMatchConditions(t *testing.T) {
	f := Facts{Source: "codex", Folder: "/home/u/code/app", Remote: "git@github.com:acme/app.git", Branch: "fix/lin-412-auth",
		WorkItems: []string{"linear:LIN-412", "acme/app#7"}}
	for _, tc := range []struct {
		r    Rule
		want bool
	}{
		{Rule{}, true},
		{Rule{Source: "codex"}, true},
		{Rule{Source: "claude-code"}, false},
		{Rule{Folder: "~/code/app"}, true},
		{Rule{Folder: "~/code"}, true}, // a folder covers what is under it
		{Rule{Folder: "~/code/ap"}, false},
		{Rule{Folder: "~/code/*"}, true},
		{Rule{Folder: "~/work/*"}, false},
		{Rule{Folder: "chatgpt:*"}, false}, // a chat pattern never matches a folder
		{Rule{Remote: "github.com/acme/*"}, true},
		{Rule{Remote: "https://github.com/acme/app"}, true},
		{Rule{Remote: "GitHub.com/ACME/app"}, true},
		{Rule{Remote: "github.com/other/*"}, false},
		{Rule{Remote: "github.com/acme"}, false}, // no wildcard: the whole remote must match
		{Rule{Branch: "fix/*"}, true},
		{Rule{Branch: "fix"}, false},
		{Rule{Branch: "*412*"}, true},
		{Rule{Branch: "Fix/*"}, false}, // branches are case-sensitive
		{Rule{WorkItem: "linear:LIN"}, true},
		{Rule{WorkItem: "LIN"}, true},
		{Rule{WorkItem: "LI"}, false}, // a prefix ends at -, # or !
		{Rule{WorkItem: "acme/app"}, true},
		{Rule{WorkItem: "acme/ap"}, false},
		{Rule{WorkItem: "linear:ENG"}, false},
		{Rule{Source: "codex", Branch: "fix/*", WorkItem: "LIN"}, true},
		{Rule{Source: "codex", Branch: "main"}, false}, // every condition must hold
	} {
		if got := tc.r.Match(f, home); got != tc.want {
			t.Errorf("%+v: got %v want %v", tc.r, got, tc.want)
		}
	}
	chat := Facts{Source: "chatgpt", Folder: "chatgpt:Work"}
	for r, want := range map[Rule]bool{{Folder: "chatgpt:*"}: true, {Folder: "chatgpt:Work"}: true, {Folder: "chatgpt:Wor"}: false, {Folder: "claude-ai:*"}: false,
		{Branch: "*"}: false, {Remote: "*"}: false, {WorkItem: "LIN"}: false} { // a fact the session lacks never matches
		if got := r.Match(chat, home); got != want {
			t.Errorf("%+v on a chat: got %v want %v", r, got, want)
		}
	}
}

func TestNormalizeRemote(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:acme/app.git":              "github.com/acme/app",
		"https://github.com/acme/app":              "github.com/acme/app",
		"https://user:tok@github.com/acme/app.git": "github.com/acme/app",
		"ssh://git@gitlab.example.com:2222/g/p":    "gitlab.example.com/g/p",
		"github.com/acme/*":                        "github.com/acme/*",
	} {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

// holds re-checks each condition of a rule with simple, separate code.
func holds(r Rule, f Facts) bool {
	if r.Source != "" && r.Source != f.Source {
		return false
	}
	if r.Folder != "" && r.Folder != f.Folder && !strings.HasPrefix(f.Folder, r.Folder+"/") {
		return false
	}
	if r.Branch != "" && r.Branch != f.Branch {
		return false
	}
	if r.Remote != "" && r.Remote != f.Remote {
		return false
	}
	if r.WorkItem != "" {
		ok := false
		for _, k := range f.WorkItems {
			if k == r.WorkItem || strings.HasPrefix(k, r.WorkItem+"-") {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Property: a rule never matches outside its pattern. With literal patterns
// (no wildcards), Match agrees with a condition-by-condition check on
// thousands of random rules and sessions, and a match never appears when one
// condition fails.
func TestPropertyARuleNeverMatchesOutsideItsPattern(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	pick := func(vs ...string) string { return vs[rng.Intn(len(vs))] }
	sources := []string{"", "codex", "claude-code", "chatgpt"}
	folders := []string{"", "/home/u/code/app", "/home/u/code/lib", "/home/u/code", "/home/u/work/x", "/home/u/code/app2"}
	branches := []string{"", "main", "fix/a", "fix/b", "release"}
	remotes := []string{"", "github.com/acme/app", "github.com/acme/lib", "gitlab.com/g/p"}
	items := []string{"", "linear:ENG", "linear:OPS", "linear:EN"}
	keys := []string{"linear:ENG-1", "linear:OPS-2", "linear:ENGX-3", "linear:EN-4"}
	matched := 0
	for i := 0; i < 20000; i++ {
		r := Rule{ID: "r", Action: ActionApprove, Source: pick(sources...), Folder: pick(folders...), Branch: pick(branches...), Remote: pick(remotes...), WorkItem: pick(items...)}
		f := Facts{Source: pick(sources[1:]...), Folder: pick(folders[1:]...), Branch: pick(branches...), Remote: pick(remotes...)}
		for _, k := range keys {
			if rng.Intn(3) == 0 {
				f.WorkItems = append(f.WorkItems, k)
			}
		}
		got, want := r.Match(f, home), holds(r, f)
		if got != want {
			t.Fatalf("rule %+v on %+v: Match says %v, the conditions say %v", r, f, got, want)
		}
		if got {
			matched++
		}
	}
	if matched < 100 || matched > 19000 {
		t.Fatalf("the generator is lopsided: %d matches", matched)
	}
}

// Property: an exclude rule that matches always wins, whatever approve rules
// stand next to it and in whatever order.
func TestPropertyExcludeWinsOverApprove(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	f := Facts{Source: "codex", Folder: "/home/u/code/app", Branch: "fix/a"}
	pool := []Rule{
		{ID: "a1", Action: ActionApprove}, {ID: "a2", Action: ActionApprove, Source: "codex"}, {ID: "a3", Action: ActionApprove, Branch: "main"},
		{ID: "x1", Action: ActionExclude, Branch: "fix/*"}, {ID: "x2", Action: ActionExclude, Source: "chatgpt"},
	}
	for i := 0; i < 2000; i++ {
		var rs []Rule
		for _, r := range pool {
			if rng.Intn(2) == 0 {
				rs = append(rs, r)
			}
		}
		rng.Shuffle(len(rs), func(a, b int) { rs[a], rs[b] = rs[b], rs[a] })
		v := Evaluate(rs, f, home)
		hasX1, approvers, approving := false, 0, false
		for _, r := range rs {
			switch r.ID {
			case "x1":
				hasX1 = true
			case "a1", "a2":
				approvers, approving = approvers+1, true
			case "a3":
				approvers++
			}
		}
		want := !hasX1 && (approving || approvers == 0)
		if v.Approved() != want {
			t.Fatalf("rules %+v: approved=%v want %v", rs, v.Approved(), want)
		}
		if hasX1 && (v.Exclude == nil || v.RuleID() != "x1") {
			t.Fatalf("the matching exclude rule is named: %+v", v)
		}
	}
}

func TestEvaluateWithoutApproveRulesCoversTheProject(t *testing.T) {
	f := Facts{Source: "codex", Folder: "/p"}
	if v := Evaluate(nil, f, home); !v.Approved() || !v.All || v.RuleID() != "" {
		t.Errorf("no rules: %+v", v)
	}
	if v := Evaluate([]Rule{{ID: "x", Action: ActionExclude, Source: "chatgpt"}}, f, home); !v.Approved() || !v.All {
		t.Errorf("only an exclude rule that does not match: %+v", v)
	}
	// once there is an approve rule, only what it matches is approved
	if v := Evaluate([]Rule{{ID: "a", Action: ActionApprove, Source: "chatgpt"}}, f, home); v.Approved() {
		t.Errorf("an approve rule narrows the project: %+v", v)
	}
}

func TestValidateAndWords(t *testing.T) {
	if err := (Rule{ID: "r", Action: "send"}).Validate(); err == nil {
		t.Error("unknown action")
	}
	if err := (Rule{Action: ActionApprove}).Validate(); err == nil {
		t.Error("no id")
	}
	if err := (Rule{ID: "r", Action: ActionApprove, Branch: " fix "}).Validate(); err == nil {
		t.Error("stray whitespace")
	}
	if err := (Rule{ID: "r", Action: ActionApprove, Folder: "~/code/[", Branch: "x"}).Validate(); err == nil {
		t.Error("bad folder pattern")
	}
	if got := (Rule{Action: ActionExclude, Source: "codex", Branch: "wip/*"}).String(); got != "exclude sessions from codex, on branch wip/*" {
		t.Error(got)
	}
	if got := (Rule{Action: ActionApprove}).String(); got != "approve every session" {
		t.Error(got)
	}
	if !ValidTrust("") || !ValidTrust("auto") || ValidTrust("always") || !strings.HasPrefix(NewID(), "r_") {
		t.Error("trust levels or ids")
	}
}
