package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
)

// The Rules screen goes through tui.Backend: it lists a project's trust level
// and rules, and adds, removes and changes them with the same checks as
// `goldengate rules` and `projects trust` (#208).
func TestRulesThroughTheBackend(t *testing.T) {
	m := newMulti(t)
	if err := m.run("share", "add", "~/projects/demo"); err != nil {
		t.Fatalf("%v\n%s", err, m.errOut)
	}
	a := &app{Deps: m.deps}
	b := a.tuiBackend()
	ctx := context.Background()

	list, err := b.Rules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	demo := func() tui.ProjectRules {
		t.Helper()
		list, err := b.Rules(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			if p.Name == "demo" {
				return p
			}
		}
		t.Fatalf("no demo in %+v", list)
		return tui.ProjectRules{}
	}
	if len(list) == 0 || demo().Trust != "ask" || len(demo().Rules) != 0 || len(demo().Members) != 1 {
		t.Fatalf("rules: %+v", list)
	}

	msg, err := b.AddRule(ctx, "Demo", tui.RuleInfo{Action: "approve", Source: "codex", Branch: " fix/* "})
	if err != nil || !strings.Contains(msg, "added rule") || !strings.Contains(msg, "to let the rule act") {
		t.Fatalf("add: %q %v", msg, err)
	}
	r := demo().Rules
	if len(r) != 1 || r[0].ID == "" || r[0].Branch != "fix/*" || !strings.Contains(r[0].Words, "on branch fix/*") {
		t.Fatalf("added: %+v", r)
	}
	if _, err := b.AddRule(ctx, "Demo", tui.RuleInfo{Action: "approve", Source: "codex", Branch: "fix/*"}); err == nil {
		t.Error("the same rule twice was accepted")
	}
	if _, err := b.AddRule(ctx, "Demo", tui.RuleInfo{Action: "exclude"}); err == nil {
		t.Error("an exclude rule with no condition was accepted")
	}
	if _, err := b.AddRule(ctx, "Nope", tui.RuleInfo{Action: "approve"}); err == nil {
		t.Error("a rule for a missing project was accepted")
	}

	if _, err := b.SetTrust(ctx, "Demo", "sometimes"); err == nil {
		t.Error("a made-up trust level was accepted")
	}
	if msg, err := b.SetTrust(ctx, "Demo", "AUTO"); err != nil || !strings.Contains(msg, "auto") || demo().Trust != "auto" {
		t.Fatalf("trust: %q %v %+v", msg, err, demo())
	}

	if _, err := b.RemoveRule(ctx, "Demo", "r_missing"); err == nil {
		t.Error("removing a missing rule succeeded")
	}
	if msg, err := b.RemoveRule(ctx, "Demo", r[0].ID); err != nil || !strings.Contains(msg, "removed rule") || len(demo().Rules) != 0 {
		t.Fatalf("remove: %q %v", msg, err)
	}
}
