package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

var clock = time.Date(2026, 1, 6, 12, 0, 0, 0, time.UTC)

// fakeBackend is an in-memory Backend that behaves like the real one closely
// enough for UI tests: share actions change what the next snapshot reports.
type fakeBackend struct {
	mu sync.Mutex

	rows     []Row
	sources  []SourceStatus
	level    string
	since    string
	server   string
	firstRun bool
	patterns []string
	git      bool
	theme    string
	summ     SummarizeSettings
	keys     map[string]string // summarizer keys by provider
	testErr  error
	tests    int
	models   map[string][]string // by provider; absent: an error, like a server that is not running

	previews map[string]Preview
	srvInfo  ServerInfo

	// recorded interactions
	actions    []Action
	undos      int
	remoteDels []any
	srvDels    []string
	saved      []Settings
	inits      []InitParams
	syncs      []bool
	planOpts   []SyncOpts
	syncOpts   []SyncOpts

	// behavior switches
	applyErr   error
	syncFail   map[string]string // project label -> error
	syncBlock  chan struct{}     // when set, Sync waits for it (or ctx)
	remote     *RemotePrompt
	initErr    error
	saveErr    error
	snapshotFn func() (Snapshot, error)
	stale      int // synced sessions with heuristic summaries (a model is configured when > 0)
}

func ago(d time.Duration) time.Time { return clock.Add(-d) }

func newFake() *fakeBackend {
	f := &fakeBackend{
		level: "summary", since: "90d", server: "https://bridge.example.com",
		sources: []SourceStatus{
			{Name: "claude-code", Installed: true, Sessions: 142, Enabled: true},
			{Name: "codex", Installed: true, Sessions: 37, Enabled: true},
			{Name: "opencode", Installed: true, Sessions: 58, Enabled: true},
			{Name: "claude-ai", Installed: false, Enabled: true},
		},
		rows: []Row{
			{Key: "/home/u/code/pharmfindr", Label: "~/code/pharmfindr", Sessions: 47, Last: ago(2 * time.Hour), Pending: 3, Synced: 44, LastSync: ago(2 * time.Hour), State: Shared, Reason: "allowed by ~/code/pharmfindr",
				Sources: []SourceInfo{{"claude-code", 41, true}, {"codex", 6, true}}},
			{Key: "/home/u/code/forge", Label: "~/code/forge", Sessions: 12, Last: ago(5 * time.Hour), Pending: 1, Synced: 11, LastSync: ago(6 * time.Hour), State: Shared, Reason: "allowed by ~/code/forge",
				Sources: []SourceInfo{{"claude-code", 12, true}}},
			{Key: "/home/u/code/vihayas", Label: "~/code/vihayas", Sessions: 8, Last: ago(30 * time.Hour), State: NotShared, Reason: "not selected",
				Sources: []SourceInfo{{"claude-code", 8, false}}},
			{Key: "/home/u/code/website", Label: "~/code/website", Sessions: 9, Last: ago(50 * time.Hour), Synced: 5, LastSync: ago(40 * time.Hour), State: Partial, Reason: "allowed by ~/code/website",
				Sources: []SourceInfo{{"claude-code", 5, true}, {"opencode", 4, false}}},
			{Key: "/home/u/work/client-x", Label: "~/work/client-x", Sessions: 20, Last: ago(100 * time.Hour), State: Denied, Reason: "denied by ~/work/client-x",
				Sources: []SourceInfo{{"claude-code", 20, false}}},
			{Key: "/home/u/scratch/tmp", Label: "~/scratch/tmp", Sessions: 2, Last: ago(200 * time.Hour), State: NotShared, Reason: "not selected",
				Sources: []SourceInfo{{"codex", 2, false}}},
			{Key: "/home/u/code/pharmfindr/.claude/worktrees/fox", Label: "~/code/pharmfindr/.claude/worktrees/fox", Root: "~/code/pharmfindr", Sessions: 4, Last: ago(3 * time.Hour), Pending: 2, State: Shared,
				Reason: "allowed by ~/code/pharmfindr", Sources: []SourceInfo{{"claude-code", 4, true}}},
			{Key: "claude-ai:Chats", Label: "claude-ai:Chats", Virtual: true, Sessions: 3, Last: ago(400 * time.Hour), State: NotShared, Reason: "chat projects need an explicit allow rule",
				Sources: []SourceInfo{{"claude-ai", 3, false}}},
		},
		previews: map[string]Preview{},
		srvInfo: ServerInfo{URL: "https://bridge.example.com", Configured: true, Healthy: true, TokenOK: true, TokenStore: "keyring",
			Projects: []RemoteProject{{"~/code/pharmfindr", 44, ago(2 * time.Hour)}, {"~/code/forge", 11, ago(6 * time.Hour)}}},
		syncFail: map[string]string{},
	}
	f.previews["/home/u/code/pharmfindr"] = f.samplePreview()
	return f
}

func (f *fakeBackend) samplePreview() Preview {
	mk := func(src, id, title, goal string, st string, diff []string, work ...model.WorkItem) PreviewItem {
		p := model.SessionPayload{Source: src, SessionID: id, Project: "~/code/pharmfindr", Level: "summary", StartedAt: ago(3 * time.Hour), UpdatedAt: ago(2 * time.Hour),
			ContentHash: "ab" + id,
			Summary: &model.Summary{Title: title, Goals: nonEmpty(goal), Outcomes: []string{"tests passed", "commit made"}, Commands: []string{"git commit", "go test"},
				FilesTouched: []string{"auth/redirect.go", "auth/redirect_test.go"}, LastMessage: "Fixed. Key was [REDACTED:aws-access-key] in the log.", SummarizedBy: "heuristic"},
			WorkItems: work}
		return PreviewItem{Source: src, ID: id, Updated: ago(2 * time.Hour), Status: st, Payload: p, Diff: diff,
			JSON: fmt.Sprintf("{\n  \"source\": %q,\n  \"session_id\": %q,\n  \"summary\": {\n    \"title\": %q,\n    \"last_assistant\": \"Fixed. Key was [REDACTED:aws-access-key] in the log.\"\n  }\n}", src, id, title)}
	}
	pv := Preview{Key: "/home/u/code/pharmfindr", Level: "summary", Total: 47, Redactions: map[string]int{"aws-access-key": 2},
		Items: []PreviewItem{
			mk("claude-code", "s-0001", "Fix OAuth redirect mismatch", "The login redirect fails on staging", "changed", []string{`title: "Fix OAuth redirect" -> "Fix OAuth redirect mismatch"`, "+ file auth/redirect_test.go"},
				model.WorkItem{Kind: "commit", Key: "pharmfindr@abc1234", Status: "created", Evidence: []model.Evidence{{Via: "tool_result"}}},
				model.WorkItem{Kind: "pull_request", Key: "example/pharmfindr#7", Status: "mentioned", Evidence: []model.Evidence{{Via: "text"}}}),
			mk("codex", "s-0002", "Add gRPC health checks", "Expose /healthz over gRPC", "new", nil),
			mk("claude-code", "s-0003", "Refactor token store", "", "unchanged", nil),
		}}
	pv.Items[2].Payload.Origin = model.OriginCLI // started by a command
	return pv
}

func (f *fakeBackend) rowIndex(key string) int {
	for i, r := range f.rows {
		if r.Key == key {
			return i
		}
	}
	return -1
}

func (f *fakeBackend) Snapshot(context.Context) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapshotFn != nil {
		return f.snapshotFn()
	}
	rows := make([]Row, len(f.rows))
	copy(rows, f.rows)
	return Snapshot{Rows: rows, Sources: append([]SourceStatus(nil), f.sources...), Level: f.level, Since: f.since, Server: f.server, FirstRun: f.firstRun}, nil
}

func (f *fakeBackend) Preview(_ context.Context, key string, _ int) (Preview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.previews[key]; ok {
		return p, nil
	}
	return Preview{Key: key, Level: f.level}, nil
}

func (f *fakeBackend) Apply(_ context.Context, a Action) (ActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, a)
	if f.applyErr != nil {
		return ActionResult{}, f.applyErr
	}
	for _, k := range a.Keys {
		i := f.rowIndex(k)
		if i < 0 {
			continue
		}
		switch a.Kind {
		case ActAllow:
			f.rows[i].State = Shared
		case ActRemove:
			f.rows[i].State = NotShared
		case ActDeny:
			f.rows[i].State = Denied
		case ActToggleSource:
			r := &f.rows[i]
			if r.State == NotShared { // share only this source
				for j := range r.Sources {
					r.Sources[j].Shared = r.Sources[j].Name == a.Source
				}
			} else {
				for j := range r.Sources {
					if r.Sources[j].Name == a.Source {
						r.Sources[j].Shared = !r.Sources[j].Shared
					}
				}
			}
			n := 0
			for _, s := range r.Sources {
				if s.Shared {
					n++
				}
			}
			switch {
			case n == 0:
				r.State = NotShared
			case n == len(r.Sources):
				r.State = Shared
			default:
				r.State = Partial
			}
		}
	}
	res := ActionResult{Message: fmt.Sprintf("%d changed", len(a.Keys))}
	if a.Kind == ActRemove || a.Kind == ActDeny { // only un-sharing leaves data on the server
		res.Remote = f.remote
	}
	return res, nil
}

func (f *fakeBackend) Undo(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.undos++
	return "undid the last change", nil
}

func (f *fakeBackend) DeleteRemote(_ context.Context, h any) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remoteDels = append(f.remoteDels, h)
	return "deleted 1 project from the server", nil
}

func (f *fakeBackend) Plan(_ context.Context, o SyncOpts) (PlanInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.planOpts = append(f.planOpts, o)
	p := PlanInfo{Sessions: 7, NotSelected: 120, Unchanged: 30, Warnings: 1, Redactions: map[string]int{"aws-access-key": 2}, Server: f.server,
		Projects: []ProjectPlan{{"~/code/pharmfindr", 4}, {"~/code/forge", 1}, {"~/code/pharmfindr/.claude/worktrees/fox", 2}}}
	if f.stale > 0 {
		p.Summarizer = "anthropic:claude-haiku-5-5"
		if o.Resummarize {
			p.Resummarized = f.stale
		} else {
			p.Stale = f.stale
		}
	}
	return p, nil
}

func (f *fakeBackend) Sync(ctx context.Context, dry bool, o SyncOpts, emit func(SyncEvent)) error {
	f.mu.Lock()
	f.syncs = append(f.syncs, dry)
	f.syncOpts = append(f.syncOpts, o)
	block, fails := f.syncBlock, f.syncFail
	if dry { // a dry pass never talks to the server, so it neither blocks nor fails on it
		block, fails = nil, nil
	}
	f.mu.Unlock()
	var plan *PlanInfo
	if dry { // the review plans as it goes, like the real backend
		p, _ := f.Plan(ctx, o)
		for i := 0; i < p.Sessions; i++ {
			emit(SyncEvent{Kind: SyncPrepared, Sessions: 1, Project: "~/code/pharmfindr", Title: fmt.Sprintf("Session %d", i+1), By: "heuristic"})
		}
		plan = &p
	}
	emit(SyncEvent{Kind: SyncPlanned, Sessions: 7, Projects: 3, Plan: plan})
	for _, p := range []struct {
		l string
		n int
	}{{"~/code/pharmfindr", 4}, {"~/code/forge", 1}, {"~/code/pharmfindr/.claude/worktrees/fox", 2}} {
		emit(SyncEvent{Kind: SyncProjectStart, Project: p.l, Sessions: p.n})
		if block != nil {
			select {
			case <-block:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if msg, bad := fails[p.l]; bad {
			emit(SyncEvent{Kind: SyncProjectFailed, Project: p.l, Err: msg})
			continue
		}
		emit(SyncEvent{Kind: SyncProjectDone, Project: p.l, Sessions: p.n})
	}
	emit(SyncEvent{Kind: SyncFinished})
	return nil
}

func (f *fakeBackend) Server(context.Context) (ServerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.server == "" {
		return ServerInfo{}, nil
	}
	return f.srvInfo, nil
}

func (f *fakeBackend) DeleteServerProject(_ context.Context, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.srvDels = append(f.srvDels, label)
	var keep []RemoteProject
	for _, p := range f.srvInfo.Projects {
		if p.Label != label {
			keep = append(keep, p)
		}
	}
	f.srvInfo.Projects = keep
	return nil
}

func (f *fakeBackend) Settings(context.Context) (Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Settings{Level: f.level, Since: f.since, Sources: append([]SourceStatus(nil), f.sources...), ExtraPatterns: append([]string(nil), f.patterns...), EnrichGit: f.git, Theme: f.theme, Summarize: f.summ}, nil
}

func (f *fakeBackend) SaveSettings(_ context.Context, s Settings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, s)
	if f.saveErr != nil {
		return f.saveErr
	}
	f.level, f.since, f.patterns, f.git, f.theme, f.summ = s.Level, s.Since, s.ExtraPatterns, s.EnrichGit, s.Theme, s.Summarize
	f.summ.KeySource = "none"
	if f.keys[f.summ.Provider] != "" {
		f.summ.KeySource = "keyring"
	}
	f.sources = append([]SourceStatus(nil), s.Sources...)
	return nil
}

func (f *fakeBackend) Detect(context.Context) ([]ToolStatus, error) {
	return []ToolStatus{{"claude-code", true, 142, ""}, {"codex", true, 37, ""}, {"opencode", false, 0, ""}}, nil
}

func (f *fakeBackend) Init(_ context.Context, p InitParams) (InitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inits = append(f.inits, p)
	if f.initErr != nil {
		return InitResult{}, f.initErr
	}
	f.server = p.ServerURL
	r := InitResult{MCPURL: p.ServerURL + "/mcp", TokenStore: "keyring", OAuth: true}
	if p.AdminToken != "" {
		r.ReadToken = "ggb_read_EXAMPLE"
	}
	return r, nil
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func (f *fakeBackend) SetSummarizerKey(_ context.Context, provider, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys == nil {
		f.keys = map[string]string{}
	}
	f.keys[provider] = key
	f.summ.KeySource = "none"
	if key != "" && provider == f.summ.Provider {
		f.summ.KeySource = "keyring"
	}
	return nil
}

func (f *fakeBackend) TestSummarizer(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tests++
	if f.testErr != nil {
		return "", f.testErr
	}
	return f.summ.Provider + ":model ok in 812ms", nil
}

func (f *fakeBackend) SummarizerModels(_ context.Context, provider, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.models[provider]
	if !ok {
		return nil, fmt.Errorf("connection refused")
	}
	return l, nil
}
