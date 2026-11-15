package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// seedThread stores one piece of work done in four tools, plus a finished
// one and an unrelated open one, all in the project "App".
func seedThread(e *env) {
	item := func(kind, provider, key, status, via string, age time.Duration) model.WorkItem {
		return model.WorkItem{Kind: kind, Provider: provider, Key: key, Status: status, ObservedAt: now.Add(-age),
			Evidence: []model.Evidence{{SessionID: "x", Via: via, Turn: -1}}}
	}
	sess := func(source, id, member, title string, age time.Duration, items ...model.WorkItem) model.SessionPayload {
		p := payload(id, member, title, items...)
		p.Source, p.Version, p.ProjectID, p.ProjectName = source, model.PayloadV2, "p_app1", "App"
		p.StartedAt, p.UpdatedAt = now.Add(-age-time.Hour), now.Add(-age)
		p.Summary = &model.Summary{Title: title, Outcomes: []string{"outcome of " + id}}
		return p
	}
	h := time.Hour
	plan := sess("claude-ai", "chat-plan", "claude-ai:App", "Plan the token refresh", 30*h,
		item(model.KindIssue, "linear", "linear:LIN-412", model.StatusMentioned, model.ViaText, 30*h))
	plan.Summary.Narrative, plan.Summary.NarrativeBy = "We decided to refresh tokens five minutes before expiry. The retry queue was rejected.", "anthropic:claude"
	impl := sess("claude-code", "code-impl", "~/code/app", "Implement refresh", 20*h,
		item(model.KindIssue, "linear", "linear:LIN-412", model.StatusMentioned, model.ViaBranch, 20*h))
	impl.Summary.GitBranch = "fix/lin-412-token-refresh"
	pr := item(model.KindPR, "github", "example/app#7", model.StatusCreated, model.ViaToolResult, 10*h)
	pr.Title, pr.URL = "Refresh tokens before expiry", "https://github.com/example/app/pull/7"
	open := sess("codex", "codex-pr", "~/code/app", "Open the pull request", 10*h,
		item(model.KindIssue, "linear", "linear:LIN-412", model.StatusMentioned, model.ViaBranch, 10*h), pr)
	open.Summary.Abstract = "Opened the pull request; CI is failing on the token test."
	e.ingestOK(plan, impl, open,
		// finished: merged by a command
		sess("claude-code", "done-1", "~/code/app", "Ship the old fix", 50*h,
			item(model.KindPR, "github", "example/app#3", model.StatusMerged, model.ViaToolResult, 50*h)),
		// a second open issue, completed in the tracker by a session
		sess("opencode", "done-2", "~/code/app", "Close the flaky test issue", 40*h,
			item(model.KindIssue, "linear", "linear:LIN-300", model.StatusCompleted, model.ViaToolResult, 40*h)),
		sess("chatgpt", "other", "chatgpt:App", "Think about exports", 5*h,
			item(model.KindIssue, "linear", "linear:LIN-500", model.StatusMentioned, model.ViaText, 5*h)),
	)
}

func TestProjectContextLeadsWithActiveThreads(t *testing.T) {
	e := newEnv(t)
	seedThread(e)
	cs := e.mcpSession(e.read)

	r, txt := call(t, cs, "get_project_context", map[string]any{"project": "App"})
	lead, rest, ok := strings.Cut(txt, "Recent sessions:")
	if !ok || !strings.Contains(lead, "Active work") {
		t.Fatalf("threads come before sessions:\n%s", txt)
	}
	for _, want := range []string{
		"- issue linear:LIN-412 [mentioned]", "NOT confirmed: only mentioned",
		"with pull_request example/app#7 [created] Refresh tokens before expiry", "confirmed by tool_result", "https://github.com/example/app/pull/7",
		"summary: Opened the pull request; CI is failing on the token test.",
		"touched by claude-ai, claude-code, codex in 3 session(s); latest: [codex] Open the pull request", "id=codex-pr",
		"- issue linear:LIN-500 [mentioned]",
	} {
		if !strings.Contains(lead, want) {
			t.Errorf("active work lacks %q:\n%s", want, lead)
		}
	}
	// finished work is not active: a merged pull request, a completed issue
	for _, gone := range []string{"example/app#3", "LIN-300"} {
		if strings.Contains(lead, gone) {
			t.Errorf("finished %s listed as active:\n%s", gone, lead)
		}
	}
	// newest thread first
	if strings.Index(lead, "LIN-500") > strings.Index(lead, "LIN-412") {
		t.Errorf("threads are newest first:\n%s", lead)
	}
	// the text stays short: sessions are one or two lines here
	if strings.Contains(rest, "last message:") || strings.Contains(rest, "retry queue") {
		t.Errorf("recent sessions must stay short:\n%s", rest)
	}
	var out struct {
		Active []struct {
			ID   string `json:"id"`
			Item struct {
				Key, Status string
				Confirmed   bool
			} `json:"item"`
			Related []struct{ Key, Status string } `json:"related"`
			Sources []string                       `json:"sources"`
			Latest  struct{ ID string }            `json:"latest_session"`
			N       int                            `json:"sessions"`
		} `json:"active_threads"`
	}
	b, _ := json.Marshal(r.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil || len(out.Active) != 2 {
		t.Fatalf("structured: %v %s", err, b)
	}
	th := out.Active[1]
	if th.ID != "linear:LIN-412" || th.Item.Status != "mentioned" || th.Item.Confirmed || len(th.Related) != 1 || th.Related[0].Status != "created" ||
		strings.Join(th.Sources, ",") != "claude-ai,claude-code,codex" || th.Latest.ID != "codex-pr" || th.N != 3 {
		t.Errorf("thread: %+v", th)
	}
}

func TestGetWorkItemReturnsTheThread(t *testing.T) {
	e := newEnv(t)
	seedThread(e)
	cs := e.mcpSession(e.read)

	var first string
	// the tracker key, the stored key, the pull request's key and its URL all find the same thread
	for _, key := range []string{"LIN-412", "linear:LIN-412", "lin-412", "example/app#7", "#7", "https://github.com/example/app/pull/7"} {
		r, txt := call(t, cs, "get_work_item", map[string]any{"key": key})
		b, _ := json.Marshal(r.StructuredContent)
		if first == "" {
			first = string(b)
		} else if string(b) != first {
			t.Errorf("%s found a different thread:\n%s", key, b)
		}
		if !strings.Contains(txt, "issue linear:LIN-412") {
			t.Errorf("%s:\n%s", key, txt)
		}
	}
	_, txt := call(t, cs, "get_work_item", map[string]any{"key": "LIN-412", "project": "App"})
	for _, want := range []string{
		"with pull_request example/app#7 [created]",
		// sessions across sources, newest first, each with what it did
		"[codex] Open the pull request", "touched: pull_request example/app#7 (created)", "touched: issue linear:LIN-412 (mentioned, on its branch)",
		"[claude-code] Implement refresh", "[claude-ai] Plan the token refresh",
		// the full narrative, as get_session gives it
		"The retry queue was rejected.",
		// status history with evidence, oldest first
		"Status history, oldest first",
		"linear:LIN-412 [mentioned] not confirmed: text, [claude-ai] id=chat-plan",
		"linear:LIN-412 [mentioned] not confirmed: branch, [claude-code] id=code-impl",
		"example/app#7 [created] confirmed by tool_result, [codex] id=codex-pr",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("get_work_item lacks %q:\n%s", want, txt)
		}
	}
	if a, b := strings.Index(txt, "[codex] Open"), strings.Index(txt, "[claude-ai] Plan"); a > b {
		t.Errorf("sessions are newest first:\n%s", txt)
	}
	if a, b := strings.Index(txt, "id=chat-plan"), strings.LastIndex(txt, "id=codex-pr"); a > b {
		t.Errorf("history is oldest first:\n%s", txt)
	}
	// other work is not pulled in
	for _, other := range []string{"LIN-500", "LIN-300", "example/app#3"} {
		if strings.Contains(txt, other) {
			t.Errorf("%s leaked into the thread:\n%s", other, txt)
		}
	}

	bad := func(args map[string]any, want string) {
		t.Helper()
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_work_item", Arguments: args})
		if err != nil || !r.IsError || !strings.Contains(rawText(r), want) {
			t.Errorf("%v: want error %q, got %v %s", args, want, err, rawText(r))
		}
	}
	bad(map[string]any{"key": "LIN-999"}, "no such work item")
	bad(map[string]any{"key": ""}, "key is required")
	bad(map[string]any{"key": "LIN-412", "project": "Nope"}, "no such work item")
	// a commit or a file is not a thread
	bad(map[string]any{"key": "a.go"}, "no such work item")
}

func TestGetWorkItemBoundsNarrativesAndAsksWhenAmbiguous(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("Sentence about the work. ", 150) // about 3750 characters
	mk := func(id, project string, age time.Duration, key string) model.SessionPayload {
		p := payload(id, project, "Session "+id, model.WorkItem{Kind: model.KindPR, Provider: "github", Key: key, Status: model.StatusCreated,
			ObservedAt: now.Add(-age), Evidence: []model.Evidence{{SessionID: "x", Via: model.ViaToolResult}}})
		p.UpdatedAt = now.Add(-age)
		p.Summary = &model.Summary{Title: "Session " + id, Narrative: id + " " + long, NarrativeBy: "user"}
		return p
	}
	e.ingestOK(mk("n1", "~/code/app", time.Hour, "o/app#7"), mk("n2", "~/code/app", 2*time.Hour, "o/app#7"), mk("n3", "~/code/app", 3*time.Hour, "o/app#7"),
		mk("n4", "~/code/lib", 4*time.Hour, "o/lib#7"))
	cs := e.mcpSession(e.read)

	r, txt := call(t, cs, "get_work_item", map[string]any{"key": "o/app#7"})
	if !strings.Contains(txt, "n1 Sentence about the work. Sentence") || !strings.Contains(txt, "n2 Sentence about the work. Sentence") || strings.Contains(txt, "n3 Sentence about the work. Sentence") || !strings.Contains(txt, "Older sessions show their abstract only") {
		t.Errorf("the newest narratives are full, older ones are abstracts:\n%.600s", txt)
	}
	if b, _ := json.Marshal(r.StructuredContent); !strings.Contains(string(b), `"narratives_cut":true`) || len(b) > 14000 {
		t.Errorf("structured output is bounded: %d bytes", len(b))
	}
	// "#7" is two different pull requests here
	if r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_work_item", Arguments: map[string]any{"key": "#7"}}); err != nil || !r.IsError ||
		!strings.Contains(rawText(r), "2 work items match") || strings.Contains(rawText(r), "o/app") {
		t.Errorf("ambiguous key: %v %s", err, rawText(r))
	}
	// naming the project settles it
	if _, txt := call(t, cs, "get_work_item", map[string]any{"key": "#7", "project": "~/code/lib"}); !strings.Contains(txt, "o/lib#7") {
		t.Errorf("project narrows the key:\n%.400s", txt)
	}
}

func TestToolDescriptionsSayWhenToUseEachTool(t *testing.T) {
	e := newEnv(t)
	tools, err := e.mcpSession(e.read).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	desc := map[string]string{}
	for _, tl := range tools.Tools {
		desc[tl.Name] = tl.Description
		if !strings.HasPrefix(tl.Description, "Use ") {
			t.Errorf("%s: a description starts with when to use the tool: %s", tl.Name, tl.Description)
		}
	}
	for tool, wants := range map[string][]string{
		"get_project_context": {"Use first", "get_work_item"},
		"get_work_item":       {"one issue or pull request", "get_session", "not live"},
		"get_session":         {"get_work_item"},
	} {
		for _, w := range wants {
			if !strings.Contains(desc[tool], w) {
				t.Errorf("%s description lacks %q: %s", tool, w, desc[tool])
			}
		}
	}
}
