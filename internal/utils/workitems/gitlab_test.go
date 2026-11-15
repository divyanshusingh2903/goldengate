package workitems_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

// gitlabSession is a hand-built session exercising GitLab extraction the way
// testdata/claude-code/2.1/workitems.jsonl does for GitHub. The glab output
// shapes are what the tests assume; only a URL or an explicit number in the
// command ever identifies an item.
func gitlabSession() *model.RawSession {
	t0 := time.Date(2026, 2, 3, 10, 0, 0, 0, time.UTC)
	n := 0
	turn := func(role, text string, tools ...model.ToolCall) model.Turn {
		n++
		return model.Turn{Role: role, Text: text, Time: t0.Add(time.Duration(n) * time.Minute), Tools: tools}
	}
	ok := func(cmd, out string) model.ToolCall {
		return model.ToolCall{Name: "Bash", Kind: model.ToolShell, Command: cmd, Result: model.ToolResult{Known: true, OK: true, Text: out}}
	}
	fail := func(cmd, out string) model.ToolCall {
		c := ok(cmd, out)
		c.Result.OK = false
		return c
	}
	const base = "https://gitlab.com/acme/platform/api/-/"
	return &model.RawSession{
		Ref: model.SessionRef{Source: "claude-code", ID: "gl", ProjectKey: "/home/user/projects/api", StartedAt: t0},
		Turns: []model.Turn{
			turn("user", "Pick up "+base+"issues/3 and look at "+base+"merge_requests/8 for context. MR !12 is unrelated."),
			// merge request: created, then merged without a number
			turn("assistant", "", ok(`glab mr create --title "Add rate limit" --description "x"`, "!7 Add rate limit (feat/limit)\n "+base+"merge_requests/7\n")),
			turn("assistant", "", ok(`glab mr merge`, "✓ Merged\n")),
			turn("assistant", "Merged; see "+base+"merge_requests/7 again."), // a later mention must not downgrade
			// a create that failed, and one that printed no URL, prove nothing
			turn("assistant", "", fail(`glab mr create --title "Broken"`, base+"merge_requests/9\n")),
			turn("assistant", "", ok(`glab mr create --title "No URL"`, "created\n")),
			// closed by number, in the project the session's URLs point at
			turn("assistant", "", ok(`glab mr close 10`, "✓ Closed merge request !10\n")),
			// issues: created, closed, reopened
			turn("assistant", "", ok(`glab issue create --title "Flaky test" --description "SECRET-body"`, base+"issues/4\n")),
			turn("assistant", "", ok(`glab issue close 4`, "✓ Closed issue #4\n")),
			turn("assistant", "", ok(`glab issue reopen 4`, "✓ Reopened issue #4\n")),
			turn("assistant", "", ok(`glab issue close `+base+`issues/5`, "✓ Closed issue #5\n")),
			// viewing is not changing
			turn("assistant", "", ok(`glab issue view 6`, base+"issues/6\nSECRET-comment\n")),
			turn("assistant", "", ok(`glab mr list`, "!11 Something  "+base+"merge_requests/11\n")),
			// text that only looks like a command
			turn("assistant", "", ok(`echo "glab mr merge 20 && glab issue close 21"`, "glab mr merge 20 && glab issue close 21\n")),
			// self-hosted: read only for hosts in the config
			turn("user", "Also https://gitlab.example.com/team/app/-/merge_requests/2 and https://gitlab.example.com/team/app/-/issues/1, "+
				"not https://gitlab.evil.test/team/app/-/issues/1 or https://notgitlab.com/a/b/-/issues/1, "+
				"and not the old form https://gitlab.com/acme/platform/api/issues/30 or a blob https://gitlab.com/acme/platform/api/-/blob/main/issues/31."),
		},
	}
}

func TestGitLabGoldenAndNegativeCases(t *testing.T) {
	items := workitems.Extract(gitlabSession(), workitems.Options{GitLabHosts: []string{"gitlab.example.com"}})

	b, _ := json.MarshalIndent(gold(items), "", "  ")
	b = append(b, '\n')
	const gp = "../../../testdata/gitlab/workitems.golden.json"
	if *update {
		if err := os.WriteFile(gp, b, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if want, err := os.ReadFile(gp); err != nil || string(want) != string(b) {
		t.Errorf("golden mismatch (run -update and review):\n%s", b)
	}

	status := func(kind, key string) string {
		if it := find(items, kind, key); it != nil {
			if it.Provider != "gitlab" {
				return "provider " + it.Provider
			}
			return it.Status
		}
		return "<absent>"
	}
	const repo = "acme/platform/api"
	for k, want := range map[[2]string]string{
		{"pull_request", repo + "!7"}:                     "merged",    // created, merged without a number, later mention keeps it
		{"pull_request", repo + "!8"}:                     "mentioned", // URL in text
		{"pull_request", repo + "!12"}:                    "mentioned", // "MR !12"
		{"pull_request", repo + "!9"}:                     "<absent>",  // failed create
		{"pull_request", repo + "!10"}:                    "closed",
		{"pull_request", repo + "!11"}:                    "<absent>", // listing output is not a mention by the user or the assistant
		{"pull_request", repo + "!20"}:                    "<absent>", // inside a quoted string
		{"issue", "gitlab:" + repo + "#3"}:                "mentioned",
		{"issue", "gitlab:" + repo + "#4"}:                "updated", // closed then reopened
		{"issue", "gitlab:" + repo + "#5"}:                "completed",
		{"issue", "gitlab:" + repo + "#6"}:                "<absent>", // only viewed by a command
		{"issue", "gitlab:" + repo + "#21"}:               "<absent>",
		{"issue", "gitlab:" + repo + "#30"}:               "<absent>", // not the /-/ form
		{"issue", "gitlab:" + repo + "#31"}:               "<absent>",
		{"pull_request", "gitlab.example.com/team/app!2"}: "mentioned",
		{"issue", "gitlab:gitlab.example.com/team/app#1"}: "mentioned",
		{"issue", "gitlab:gitlab.evil.test/team/app#1"}:   "<absent>",
		{"issue", "gitlab:team/app#1"}:                    "<absent>",
		{"issue", "gitlab:a/b#1"}:                         "<absent>",
	} {
		if got := status(k[0], k[1]); got != want {
			t.Errorf("%s %s: got %q want %q", k[0], k[1], got, want)
		}
	}
	if it := find(items, "pull_request", repo+"!7"); it == nil || it.Title != "Add rate limit" || it.URL != "https://gitlab.com/acme/platform/api/-/merge_requests/7" {
		t.Errorf("created merge request: %+v", it)
	}
	created := 0
	for _, it := range items {
		if it.Status == model.StatusCreated {
			created++
		}
		// every status above a mention has tool-result evidence
		if it.Status != model.StatusMentioned && !strings.Contains(strings.Join(gold([]model.WorkItem{it})[0].Evidence, " "), model.ViaToolResult) {
			t.Errorf("%s %s is %s without tool-result evidence", it.Kind, it.Key, it.Status)
		}
	}
	if created != 0 { // !7 went on to merged, #4 to updated; nothing else was created
		t.Errorf("unexpected created items: %d", created)
	}
	if all, _ := json.Marshal(items); strings.Contains(string(all), "SECRET-") {
		t.Errorf("issue text leaked into work items: %s", all)
	}
}

func TestGitLabProviderAndHosts(t *testing.T) {
	// without the host in the config, a self-hosted URL is not read
	items := workitems.Extract(gitlabSession(), workitems.Options{})
	if find(items, "pull_request", "gitlab.example.com/team/app!2") != nil {
		t.Error("unlisted host was read")
	}
	// with gitlab off, issues go and merge requests stay (as pull requests do for github)
	items = workitems.Extract(gitlabSession(), workitems.Options{Providers: []string{"github"}})
	for _, it := range items {
		if it.Kind == "issue" {
			t.Errorf("gitlab off but issue extracted: %+v", it)
		}
	}
	if it := find(items, "pull_request", "acme/platform/api!7"); it == nil || it.Status != "merged" {
		t.Errorf("merge request with gitlab issues off: %+v", it)
	}
	// a number with nothing to say which project falls back to the folder name
	s := &model.RawSession{Ref: model.SessionRef{ID: "s", ProjectKey: "/p/api"}, Turns: []model.Turn{{Role: "assistant", Tools: []model.ToolCall{
		{Name: "Bash", Kind: model.ToolShell, Command: "glab mr merge 4", Result: model.ToolResult{Known: true, OK: true, Text: "ok"}},
	}}}}
	if items := workitems.Extract(s, workitems.Options{}); find(items, "pull_request", "api!4") == nil {
		t.Errorf("short key: %+v", items)
	}
}
