package workitems_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/adapters/claudecode"
	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/workitems"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtures = "../../testdata/claude-code"

func loadSession(t *testing.T, name string) *model.RawSession {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "projects", "-p")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "33333333-3333-4333-8333-333333333333.jsonl"), data, 0o600)
	a := claudecode.New(claudecode.Options{ConfigDir: cfg})
	for ref, err := range a.ListSessions(context.Background(), adapters.ListOpts{}) {
		if err != nil {
			t.Fatal(err)
		}
		s, err := a.Load(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	t.Fatal("no session")
	return nil
}

type goldItem struct {
	Kind, Provider, Key, Title, URL, Status, RawState string
	Evidence                                          []string
}

func gold(items []model.WorkItem) []goldItem {
	var out []goldItem
	for _, it := range items {
		g := goldItem{it.Kind, it.Provider, it.Key, it.Title, it.URL, it.Status, it.RawState, nil}
		for _, e := range it.Evidence {
			g.Evidence = append(g.Evidence, e.Via)
		}
		out = append(out, g)
	}
	// stable order independent of timestamps
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Kind+out[j].Key < out[j-1].Kind+out[j-1].Key; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func find(items []model.WorkItem, kind, key string) *model.WorkItem {
	for i := range items {
		if items[i].Kind == kind && items[i].Key == key {
			return &items[i]
		}
	}
	return nil
}

func TestGoldenAndNegativeCases(t *testing.T) {
	items := workitems.Extract(loadSession(t, "2.1/workitems.jsonl"), workitems.Options{})

	b, _ := json.MarshalIndent(gold(items), "", "  ")
	b = append(b, '\n')
	gp := filepath.Join(fixtures, "2.1/workitems.golden.json")
	if *update {
		if err := os.WriteFile(gp, b, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if want, err := os.ReadFile(gp); err != nil || string(want) != string(b) {
		t.Errorf("golden mismatch (run -update and review):\n%s", b)
	}

	status := func(kind, key string) string {
		if it := find(items, kind, key); it != nil {
			return it.Status
		}
		return "<absent>"
	}
	expect := map[[2]string]string{
		// commits
		{"commit", "p@abc1234"}:  "created",   // `git commit` succeeded; later text mention must not change it
		{"commit", "p@deadbee1"}: "mentioned", // only in text
		// pull requests
		{"pull_request", "example/demo#7"}: "merged",    // created then merged; later mention must not downgrade
		{"pull_request", "example/demo#8"}: "mentioned", // pr-link alone is association, not creation
		{"pull_request", "p#12"}:           "mentioned",
		{"pull_request", "other/repo#99"}:  "mentioned", // URL pasted from elsewhere
		// files
		{"file", "src/new.go"}:  "created",
		{"file", "src/a.go"}:    "updated",
		{"file", "src/fail.go"}: "<absent>", // failed write
		{"file", ".env"}:        "<absent>", // secret file never listed
		// issues
		{"issue", "github:example/demo#3"}: "updated",   // closed then reopened: moved back from completed
		{"issue", "linear:ENG-5"}:          "updated",   // created, completed, read, then moved back to In Progress
		{"issue", "linear:ENG-6"}:          "mentioned", // prefix proven by tool result in the same session
		{"issue", "linear:ABC-77"}:         "<absent>",  // unknown prefix
		{"issue", "linear:UTF-8"}:          "<absent>",
		{"issue", "linear:SHA-256"}:        "<absent>",
		{"issue", "linear:ISO-8601"}:       "<absent>",
		{"issue", "linear:CVE-2024"}:       "<absent>",
	}
	for k, want := range expect {
		// repo name for text-derived keys is inferred from PR URLs in the session
		key := strings.Replace(k[1], "p@", "example/demo@", 1)
		key = strings.Replace(key, "p#", "example/demo#", 1)
		if got := status(k[0], key); got != want {
			t.Errorf("%s %s: got %q want %q", k[0], key, got, want)
		}
	}
	if it := find(items, "issue", "linear:ENG-5"); it == nil || it.RawState != "In Progress" {
		t.Errorf("tracker's own state must be kept: %+v", it)
	}
	if find(items, "commit", "example/demo@oops") != nil {
		t.Error("failed commit became an item")
	}
	// a failed `gh pr create` must not have created anything beyond #7
	n := 0
	for _, it := range items {
		if it.Kind == "pull_request" && it.Status == "created" {
			n++
		}
	}
	if n != 0 { // #7 progressed to merged
		t.Errorf("unexpected created PRs: %d", n)
	}
	// nothing from issue descriptions/comments may reach an item
	all, _ := json.Marshal(items)
	if strings.Contains(string(all), "SECRET-") {
		t.Errorf("issue description/comment leaked into work items: %s", all)
	}
	// every non-mention status has non-text evidence
	for _, it := range items {
		if it.Status == model.StatusMentioned {
			continue
		}
		ok := false
		for _, e := range it.Evidence {
			if e.Via == model.ViaToolResult {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s %s has status %s without tool-result evidence", it.Kind, it.Key, it.Status)
		}
	}
}

func TestProviderDisabled(t *testing.T) {
	items := workitems.Extract(loadSession(t, "2.1/workitems.jsonl"), workitems.Options{Providers: []string{}})
	for _, it := range items {
		if it.Kind == "issue" {
			t.Errorf("issues must be off when providers is empty: %+v", it)
		}
	}
	items = workitems.Extract(loadSession(t, "2.1/workitems.jsonl"), workitems.Options{Providers: []string{"github"}})
	if find(items, "issue", "linear:ENG-5") != nil {
		t.Error("linear disabled but extracted")
	}
	if find(items, "issue", "github:example/demo#3") == nil {
		t.Error("github enabled but missing")
	}
}

func TestMergeRules(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(kind, st string, d time.Duration, via string) model.WorkItem {
		return model.WorkItem{Kind: kind, Key: "k", Status: st, ObservedAt: t0.Add(d), Evidence: []model.Evidence{{Via: via}}}
	}
	merged := mk("pull_request", "merged", 0, "tool_result")
	if got := workitems.Merge(merged, mk("pull_request", "mentioned", time.Hour, "text")); got.Status != "merged" {
		t.Errorf("mention downgraded merged: %s", got.Status)
	}
	if got := workitems.Merge(mk("issue", "completed", 0, "tool_result"), mk("issue", "updated", time.Hour, "tool_result")); got.Status != "updated" {
		t.Errorf("reopen must win by later evidence: %s", got.Status)
	}
	if got := workitems.Merge(mk("issue", "updated", time.Hour, "tool_result"), mk("issue", "completed", 0, "tool_result")); got.Status != "updated" {
		t.Errorf("older evidence must not win: %s", got.Status)
	}
	if got := workitems.Merge(mk("commit", "created", 0, "tool_result"), mk("commit", "updated", time.Hour, "tool_result")); got.Status != "created" {
		t.Errorf("commits are immutable: %s", got.Status)
	}
	if got := workitems.Merge(mk("pull_request", "mentioned", 0, "text"), mk("pull_request", "created", time.Hour, "tool_result")); got.Status != "created" {
		t.Errorf("evidence must upgrade a mention: %s", got.Status)
	}
	// text evidence is dropped first when trimming
	acc := mk("pull_request", "created", 0, "tool_result")
	for i := 0; i < 30; i++ {
		acc = workitems.Merge(acc, mk("pull_request", "mentioned", time.Duration(i)*time.Minute, "text"))
	}
	if len(acc.Evidence) > 10 || acc.Evidence[0].Via != "tool_result" {
		t.Errorf("status-backing evidence lost: %+v", acc.Evidence)
	}
}

func TestMalformedLinearResultIgnored(t *testing.T) {
	s := &model.RawSession{Ref: model.SessionRef{ID: "s", ProjectKey: "/p"}, Turns: []model.Turn{{Role: "assistant", Tools: []model.ToolCall{
		{Name: "mcp__x__save_issue", Kind: model.ToolIssue, Input: []byte(`{}`), Result: model.ToolResult{Known: true, OK: true, Text: `{"id":"nonsense"}`}},
		{Name: "mcp__x__save_issue", Kind: model.ToolIssue, Input: []byte(`{}`), Result: model.ToolResult{Known: true, OK: true, Text: `not json`}},
	}}}}
	if items := workitems.Extract(s, workitems.Options{}); len(items) != 0 {
		t.Errorf("malformed results must yield nothing: %+v", items)
	}
}

// Regression (found by dogfooding on real sessions): shell text that merely
// contains "gh pr merge" or "git commit" (heredoc bodies, quoted strings,
// substitutions) must never count as a command, even when the output happens
// to look like a real result.
func TestCommandTextInsideHeredocOrQuotesIsNotACommand(t *testing.T) {
	sh := func(cmd, out string) model.ToolCall {
		return model.ToolCall{Name: "Bash", Kind: model.ToolShell, Command: cmd, Result: model.ToolResult{Known: true, OK: true, Text: out}}
	}
	const prOut = "https://github.com/example/demo/pull/7\n"
	const commitOut = "[main abc1234] feat: x\n"
	s := &model.RawSession{Ref: model.SessionRef{ID: "s", ProjectKey: "/p"}, Turns: []model.Turn{{Role: "assistant", Tools: []model.ToolCall{
		sh("cat > t.go <<'EOF'\ngh pr create --title x\ngh pr merge 7\ngit commit -m y\nEOF\ngo test ./...", prOut+commitOut),
		sh(`echo "gh pr create && git commit -m x"`, prOut+commitOut),
		sh("grep -r 'gh issue create' .", "https://github.com/example/demo/issues/3\n"),
		sh("git commit --dry-run -m x", commitOut),
	}}}}
	if items := workitems.Extract(s, workitems.Options{}); len(items) != 0 {
		t.Fatalf("non-commands produced work items: %+v", items)
	}
	// ... while the same commands for real do count
	real := &model.RawSession{Ref: s.Ref, Turns: []model.Turn{{Role: "assistant", Tools: []model.ToolCall{
		sh(`FOO=1 git -C /p commit -m "x" && gh pr create --title "T" --body "b"`, commitOut+prOut),
	}}}}
	items := workitems.Extract(real, workitems.Options{})
	if find(items, "pull_request", "example/demo#7") == nil || len(items) != 2 {
		t.Fatalf("real commands must count: %+v", items)
	}
}
