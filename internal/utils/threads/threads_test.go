package threads_test

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/threads"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func at(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }

func sh(cmd, out string) model.ToolCall {
	return model.ToolCall{Name: "Bash", Kind: model.ToolShell, Command: cmd, Result: model.ToolResult{Known: true, OK: true, Text: out}}
}

type raw struct {
	source, id, project, branch string
	hour                        int
	turns                       []model.Turn
}

// session runs the real extractor, so the test covers branch parsing and
// grouping together.
func session(r raw) threads.Session {
	s := &model.RawSession{
		Ref:       model.SessionRef{Source: r.source, ID: r.id, ProjectKey: r.project, StartedAt: at(r.hour), UpdatedAt: at(r.hour + 1)},
		GitBranch: r.branch, Turns: r.turns,
	}
	for i := range s.Turns {
		s.Turns[i].Time = at(r.hour)
	}
	items := workitems.Extract(s, workitems.Options{KnownPrefixes: []string{"LIN"}})
	return threads.Session{Source: r.source, ID: r.id, ProjectID: "prj_app", ProjectName: "App", Title: r.id, Branch: r.branch, UpdatedAt: at(r.hour + 1), Items: items}
}

func text(role, s string) model.Turn { return model.Turn{Role: role, Text: s} }

func story() []threads.Session {
	return []threads.Session{
		// a chat that plans the work and mentions the issue
		session(raw{source: "claude-ai", id: "chat-plan", project: "claude-ai:App", hour: 0, turns: []model.Turn{
			text("user", "How should we fix https://linear.app/acme/issue/LIN-412/token-refresh ? The header is UTF-8 and the digest SHA-256."),
		}}),
		// a coding session on the issue's branch that never names the issue
		session(raw{source: "claude-code", id: "code-impl", project: "/home/u/app", branch: "fix/lin-412-token-refresh", hour: 2, turns: []model.Turn{
			{Role: "assistant", Tools: []model.ToolCall{sh(`git commit -m "refresh tokens"`, "[fix/lin-412-token-refresh abc1234] refresh tokens\n")}},
		}}),
		// another tool opens the pull request from the same branch
		session(raw{source: "codex", id: "codex-pr", project: "/home/u/app", branch: "fix/lin-412-token-refresh", hour: 4, turns: []model.Turn{
			{Role: "assistant", Tools: []model.ToolCall{sh(`gh pr create --title "Refresh tokens before expiry"`, "https://github.com/example/app/pull/7\n")}},
		}}),
		// later, on main, the pull request is merged by number only
		session(raw{source: "claude-code", id: "code-merge", project: "/home/u/app", branch: "main", hour: 6, turns: []model.Turn{
			{Role: "assistant", Tools: []model.ToolCall{sh(`gh pr merge 7 --merge`, "✓ Merged pull request #7 (Refresh tokens before expiry)\n")}},
		}}),
		// a different piece of work, on a branch full of strings that are not issues
		session(raw{source: "opencode", id: "other-work", project: "/home/u/app", branch: "feature/LIN-500-utf-8-sha-256-export", hour: 8, turns: []model.Turn{
			text("user", "Export must be UTF-8, hashed with SHA-256, dated per ISO-8601."),
		}}),
		// a triage chat that mentions both issues: it is in both threads and joins neither
		session(raw{source: "chatgpt", id: "chat-triage", project: "chatgpt:App", hour: 10, turns: []model.Turn{
			text("user", "Status of LIN-412 and LIN-500? Also ABC-9 from the other team."),
		}}),
		// a session with no issue or pull request is in no thread
		session(raw{source: "claude-code", id: "code-misc", project: "/home/u/app", branch: "main", hour: 12, turns: []model.Turn{
			text("user", "rename a variable"),
		}}),
	}
}

func TestGoldenThread(t *testing.T) {
	got := threads.Build(story())
	b, _ := json.MarshalIndent(got, "", "  ")
	b = append(b, '\n')
	const gp = "../../../testdata/threads/story.golden.json"
	if *update {
		if err := os.WriteFile(gp, b, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if want, err := os.ReadFile(gp); err != nil || string(want) != string(b) {
		t.Errorf("golden mismatch (run -update and review):\n%s", b)
	}

	if len(got) != 2 {
		t.Fatalf("want 2 threads, got %d", len(got))
	}
	byID := map[string]threads.Thread{}
	for _, th := range got {
		byID[th.ID] = th
	}
	th, ok := byID["issue:linear:LIN-412"]
	if !ok {
		t.Fatalf("no LIN-412 thread: %v", ids(got))
	}
	// the chat, the branch session, the session that opened the PR, the one
	// that merged it, and the triage chat
	if s := members(th); s != "chat-triage code-merge codex-pr code-impl chat-plan" {
		t.Errorf("LIN-412 sessions: %s", s)
	}
	if strings.Join(th.Sources, " ") != "chatgpt claude-ai claude-code codex" {
		t.Errorf("sources: %v", th.Sources)
	}
	status := map[string]string{}
	for _, it := range th.Items {
		status[it.Key] = it.Status
	}
	// a mention never upgrades: the issue is only ever mentioned; the PR is
	// merged because a command result said so
	if status["linear:LIN-412"] != model.StatusMentioned || status["example/app#7"] != model.StatusMerged || len(status) != 2 {
		t.Errorf("items: %v", status)
	}
	if o := byID["issue:linear:LIN-500"]; members(o) != "chat-triage other-work" {
		t.Errorf("LIN-500 sessions: %s", members(o))
	}
	for _, bad := range []string{"UTF-8", "SHA-256", "ISO-8601", "ABC-9"} {
		if strings.Contains(string(b), bad+`"`) {
			t.Errorf("%s became a work item", bad)
		}
	}
}

func ids(ts []threads.Thread) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func members(t threads.Thread) string {
	var out []string
	for _, m := range t.Sessions {
		out = append(out, m.ID)
	}
	return strings.Join(out, " ")
}

func item(kind, provider, key, status, via, title string, h int) model.WorkItem {
	return model.WorkItem{Kind: kind, Provider: provider, Key: key, Status: status, Title: title, ObservedAt: at(h),
		Evidence: []model.Evidence{{Via: via, At: at(h)}}}
}

func TestMentionsAloneNeverJoinAnIssueAndAPullRequest(t *testing.T) {
	got := threads.Build([]threads.Session{{Source: "claude-code", ID: "a", ProjectID: "p", UpdatedAt: at(1), Items: []model.WorkItem{
		item(model.KindIssue, "linear", "linear:LIN-1", model.StatusMentioned, model.ViaText, "", 0),
		item(model.KindPR, "github", "o/r#2", model.StatusCreated, model.ViaToolResult, "Tidy up", 0),
	}}})
	if len(got) != 2 {
		t.Fatalf("a text mention joined an issue to a pull request: %v", ids(got))
	}
	// on the issue's branch, a pull request that is only mentioned stays apart too
	got = threads.Build([]threads.Session{{Source: "claude-code", ID: "a", ProjectID: "p", UpdatedAt: at(1), Items: []model.WorkItem{
		item(model.KindIssue, "linear", "linear:LIN-1", model.StatusMentioned, model.ViaBranch, "", 0),
		item(model.KindPR, "github", "o/r#2", model.StatusMentioned, model.ViaText, "", 0),
	}}})
	if len(got) != 2 {
		t.Fatalf("a mentioned pull request joined the branch's issue: %v", ids(got))
	}
}

func TestPullRequestTitleNamesTheIssue(t *testing.T) {
	build := func(title string) int {
		return len(threads.Build([]threads.Session{
			{Source: "chatgpt", ID: "a", ProjectID: "p", UpdatedAt: at(1), Items: []model.WorkItem{
				item(model.KindIssue, "jira", "jira:OPS-41", model.StatusMentioned, model.ViaText, "", 0)}},
			{Source: "codex", ID: "b", ProjectID: "p", UpdatedAt: at(2), Items: []model.WorkItem{
				item(model.KindPR, "github", "o/r#2", model.StatusCreated, model.ViaToolResult, title, 1)}},
		}))
	}
	for title, want := range map[string]int{"OPS-41: rotate keys": 1, "[ops-41] rotate keys": 1, "ops-41-rotate-keys": 1, "OPS-410 rotate keys": 2, "rotate keys": 2} {
		if got := build(title); got != want {
			t.Errorf("%q: %d threads, want %d", title, got, want)
		}
	}
}

func TestShortGitHubKeys(t *testing.T) {
	pr := func(key, status, via string) []model.WorkItem {
		return []model.WorkItem{item(model.KindPR, "github", key, status, via, "", 0)}
	}
	// two repositories in one project have a #7: the short key matches neither
	got := threads.Build([]threads.Session{
		{Source: "a", ID: "1", ProjectID: "p", UpdatedAt: at(1), Items: pr("o/app#7", model.StatusCreated, model.ViaToolResult)},
		{Source: "a", ID: "2", ProjectID: "p", UpdatedAt: at(2), Items: pr("o/lib#7", model.StatusCreated, model.ViaToolResult)},
		{Source: "a", ID: "3", ProjectID: "p", UpdatedAt: at(3), Items: pr("app#7", model.StatusMentioned, model.ViaText)},
	})
	if len(got) != 3 {
		t.Errorf("ambiguous short key was matched: %v", ids(got))
	}
	// the same short key in two projects is two threads
	got = threads.Build([]threads.Session{
		{Source: "a", ID: "1", ProjectID: "p", UpdatedAt: at(1), Items: pr("app#7", model.StatusMentioned, model.ViaText)},
		{Source: "a", ID: "2", ProjectID: "q", UpdatedAt: at(2), Items: pr("app#7", model.StatusMentioned, model.ViaText)},
	})
	if len(got) != 2 {
		t.Errorf("short keys joined across projects: %v", ids(got))
	}
	// a full key is the same thread in any project
	got = threads.Build([]threads.Session{
		{Source: "a", ID: "1", ProjectID: "p", UpdatedAt: at(1), Items: pr("o/app#7", model.StatusMerged, model.ViaToolResult)},
		{Source: "b", ID: "2", ProjectID: "q", UpdatedAt: at(2), Items: pr("o/app#7", model.StatusMentioned, model.ViaText)},
	})
	if len(got) != 1 || got[0].Primary.Status != model.StatusMerged {
		t.Errorf("full key across projects: %+v", got)
	}
}

func TestFromPayload(t *testing.T) {
	p := &model.SessionPayload{Source: "codex", SessionID: "s", Project: "~/app", ProjectID: "prj_1", ProjectName: "App", UpdatedAt: at(1),
		Summary: &model.Summary{Title: "T", GitBranch: "fix/lin-1"}, WorkItems: []model.WorkItem{{Kind: model.KindIssue, Key: "linear:LIN-1"}}}
	s := threads.FromPayload(p)
	if s.ProjectID != "prj_1" || s.ProjectName != "App" || s.Title != "T" || s.Branch != "fix/lin-1" || len(s.Items) != 1 {
		t.Errorf("%+v", s)
	}
	if s := threads.FromPayload(&model.SessionPayload{Project: "~/app", TitleOnly: "only"}); s.ProjectID != "~/app" || s.Title != "only" {
		t.Errorf("%+v", s)
	}
}

func TestGitLabKeys(t *testing.T) {
	// a session that merged "api!7" by number joins the one that created the full key
	got := threads.Build([]threads.Session{
		{Source: "a", ID: "1", ProjectID: "p", UpdatedAt: at(1), Items: []model.WorkItem{
			item(model.KindIssue, "linear", "linear:LIN-1", model.StatusMentioned, model.ViaBranch, "", 0),
			item(model.KindPR, "gitlab", "acme/api!7", model.StatusCreated, model.ViaToolResult, "", 0)}},
		{Source: "b", ID: "2", ProjectID: "p", UpdatedAt: at(2), Items: []model.WorkItem{
			item(model.KindPR, "gitlab", "api!7", model.StatusMerged, model.ViaToolResult, "", 1)}},
		// GitHub's #7 in the same project is something else
		{Source: "c", ID: "3", ProjectID: "p", UpdatedAt: at(3), Items: []model.WorkItem{
			item(model.KindPR, "github", "acme/api#7", model.StatusMentioned, model.ViaText, "", 2)}},
	})
	if len(got) != 2 {
		t.Fatalf("threads: %v", ids(got))
	}
	for _, th := range got {
		if th.ID == "issue:linear:LIN-1" && (members(th) != "2 1" || th.Items[1].Status != model.StatusMerged) {
			t.Errorf("gitlab thread: %s %+v", members(th), th.Items)
		}
	}
}
