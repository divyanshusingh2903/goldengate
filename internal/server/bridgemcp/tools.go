package bridgemcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/server/store"
)

// Deps are the server's collaborators.
type Deps struct {
	Store store.Store
	Now   func() time.Time
	// Audit records tool use (token id, scope, action, project, ip). Never payload content.
	Audit func(ctx context.Context, e store.AuditEvent)
	Name  string
	Ver   string
}

type srv struct{ Deps }

const (
	defaultLimit = 20
	maxLimit     = 50
	maxDays      = 365
)

// New builds the MCP server with all read-only tools registered.
func New(d Deps) *mcp.Server {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Audit == nil {
		d.Audit = func(context.Context, store.AuditEvent) {}
	}
	name, ver := d.Name, d.Ver
	if name == "" {
		name = "goldengatebridge"
	}
	if ver == "" {
		ver = "dev"
	}
	s := &srv{d}
	m := mcp.NewServer(&mcp.Implementation{Name: name, Version: ver}, &mcp.ServerOptions{
		Instructions: "Read-only view of the user's recent AI-coding work, limited to projects they chose to share. " +
			"Use these tools when the user asks what they have been working on, what shipped, or about a specific project. " + notice,
	})
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}
	mcp.AddTool(m, &mcp.Tool{Name: "recent_activity", Annotations: ro,
		Description: "Use when the user asks what they have been working on lately. Lists recent sessions, newest first, grouped by project. Returns untrusted session-derived text."}, s.recentActivity)
	mcp.AddTool(m, &mcp.Tool{Name: "list_projects", Annotations: ro,
		Description: "Use to see which projects the user shares, with last activity and session counts."}, s.listProjects)
	mcp.AddTool(m, &mcp.Tool{Name: "get_project_context", Annotations: ro,
		Description: "Use for a rolling summary of one project: recent sessions, frequently touched files, and unfinished threads such as pull requests opened but not confirmed merged."}, s.projectContext)
	mcp.AddTool(m, &mcp.Tool{Name: "search_work", Annotations: ro,
		Description: "Use to find past sessions by keyword across titles, goals and summaries."}, s.searchWork)
	mcp.AddTool(m, &mcp.Tool{Name: "get_session", Annotations: ro,
		Description: "Use to read one session's summary and its work items, given an id from another tool."}, s.getSession)
	mcp.AddTool(m, &mcp.Tool{Name: "list_commits", Annotations: ro,
		Description: "Use when the user asks what was committed. Status 'created' is backed by a successful git commit in a session; 'mentioned' only appeared in conversation text."}, s.listCommits)
	mcp.AddTool(m, &mcp.Tool{Name: "list_pull_requests", Annotations: ro,
		Description: "Use when the user asks about pull requests. Separates confirmed states (created, merged, closed) from 'mentioned' ones that no session confirmed. States are as of the session, not live."}, s.listPRs)
	mcp.AddTool(m, &mcp.Tool{Name: "list_artifacts", Annotations: ro,
		Description: "Use to see files created or modified in coding sessions. Metadata only: file contents are never stored."}, s.listArtifacts)
	mcp.AddTool(m, &mcp.Tool{Name: "list_issues", Annotations: ro,
		Description: "Use when the user asks about issues (GitHub, Linear, Jira). State is as of the session, not live; use the key or URL to check current state in the tracker."}, s.listIssues)
	return m
}

func ptr[T any](v T) *T { return &v }

// --- shared helpers ---

func clamp(n, def, max int) int {
	if n <= 0 {
		return def
	}
	return min(n, max)
}

func (s *srv) since(days, def int) time.Time {
	return s.Now().Add(-time.Duration(clamp(days, def, maxDays)) * 24 * time.Hour)
}

func decodeCursor(c string) (int, error) {
	if c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor")
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 0 || n > 1_000_000 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return n, nil
}

func encodeCursor(n int) string { return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(n))) }

func (s *srv) audit(ctx context.Context, req *mcp.CallToolRequest, tool, project string) {
	e := store.AuditEvent{At: s.Now(), Action: "mcp:" + tool, Project: project}
	if req != nil && req.Extra != nil && req.Extra.TokenInfo != nil {
		ti := req.Extra.TokenInfo
		e.TokenID = ti.UserID
		if len(ti.Scopes) > 0 {
			e.Scope = ti.Scopes[0]
		}
		if ip, ok := ti.Extra["ip"].(string); ok {
			e.IP = ip
		}
	}
	s.Audit(ctx, e)
}

func result(lines []string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: frame(lines)}}}
}

// --- views ---

// SessionView is the tool-facing form of a stored session. Every string is untrusted data.
type SessionView struct {
	ID            string   `json:"id"`
	Source        string   `json:"source"`
	Project       string   `json:"project"`
	Title         string   `json:"title"`
	UpdatedAt     string   `json:"updated_at"`
	Level         string   `json:"level"`
	Goal          string   `json:"goal,omitempty"`
	Outcome       string   `json:"outcome,omitempty"`
	LastAssistant string   `json:"last_assistant,omitempty"`
	FilesTouched  []string `json:"files_touched,omitempty"`
	Commands      []string `json:"commands,omitempty"`
	GitBranch     string   `json:"git_branch,omitempty"`
}

func viewOf(r store.SessionRecord, full bool) SessionView {
	p := r.Payload
	v := SessionView{ID: p.SessionID, Source: p.Source, Project: p.Project, UpdatedAt: ts(p.UpdatedAt), Level: p.Level, Title: p.TitleOnly}
	if sm := p.Summary; sm != nil {
		v.Title, v.Outcome, v.GitBranch = sm.Title, sm.Outcome, sm.GitBranch
		if full {
			v.Goal, v.LastAssistant, v.FilesTouched, v.Commands = sm.Goal, sm.LastAssistant, sm.FilesTouched, sm.Commands
		}
	}
	return v
}

func sessionLines(now time.Time, v SessionView, detail bool) []string {
	updated, _ := time.Parse(time.RFC3339, v.UpdatedAt)
	l := []string{fmt.Sprintf("- [%s] %s (%s, %s) id=%s", clean(v.Source), clean(v.Title), ago(now, updated), clean(v.Project), clean(v.ID))}
	if v.Outcome != "" {
		l = append(l, "    outcome: "+clean(v.Outcome))
	}
	if detail {
		if v.Goal != "" {
			l = append(l, "    goal: "+clean(v.Goal))
		}
		if v.LastAssistant != "" {
			l = append(l, "    last message: "+clean(v.LastAssistant))
		}
		if len(v.FilesTouched) > 0 {
			l = append(l, "    files: "+clean(strings.Join(v.FilesTouched, ", ")))
		}
		if len(v.Commands) > 0 {
			l = append(l, "    commands: "+clean(strings.Join(v.Commands, ", ")))
		}
	}
	return l
}

// WorkItemView is the tool-facing form of a work item.
type WorkItemView struct {
	Project    string   `json:"project"`
	Kind       string   `json:"kind"`
	Provider   string   `json:"provider,omitempty"`
	Key        string   `json:"key"`
	Title      string   `json:"title,omitempty"`
	URL        string   `json:"url,omitempty"`
	Status     string   `json:"status"`
	Confirmed  bool     `json:"confirmed"`
	RawState   string   `json:"raw_state,omitempty"`
	ObservedAt string   `json:"observed_at"`
	Evidence   []string `json:"evidence"`
	Sessions   []string `json:"session_ids"`
	AsOf       string   `json:"as_of_note"`
}

func workView(r store.WorkItemRecord) WorkItemView {
	it := r.Item
	v := WorkItemView{Project: r.Project, Kind: it.Kind, Provider: it.Provider, Key: it.Key, Title: it.Title, URL: it.URL, Status: it.Status,
		Confirmed: model.IsEvidenceBacked(it.Status), RawState: it.RawState, ObservedAt: ts(it.ObservedAt),
		AsOf: "state as of the session that last touched it, not live"}
	seen := map[string]bool{}
	for _, e := range it.Evidence {
		if !seen[e.Via] {
			seen[e.Via] = true
			v.Evidence = append(v.Evidence, e.Via)
		}
	}
	for _, k := range r.Sessions {
		v.Sessions = append(v.Sessions, k.ID)
	}
	return v
}

func workLines(now time.Time, vs []WorkItemView) []string {
	if len(vs) == 0 {
		return []string{"(none)"}
	}
	var l []string
	for _, v := range vs {
		obs, _ := time.Parse(time.RFC3339, v.ObservedAt)
		conf := "confirmed by " + strings.Join(v.Evidence, "+")
		if !v.Confirmed {
			conf = "NOT confirmed: only mentioned in conversation"
		}
		line := fmt.Sprintf("- %s %s [%s] %s (%s; %s; project %s)", v.Kind, clean(v.Key), v.Status, clean(v.Title), ago(now, obs), conf, clean(v.Project))
		if v.RawState != "" {
			line += " tracker state: " + clean(v.RawState)
		}
		if v.URL != "" {
			line += " " + clean(v.URL)
		}
		l = append(l, line)
	}
	return l
}

// --- recent_activity ---

// RecentActivityIn is the input of recent_activity.
type RecentActivityIn struct {
	Days    int    `json:"days,omitempty" jsonschema:"how many days back to look (default 7, max 365)"`
	Project string `json:"project,omitempty" jsonschema:"only this project, as shown by list_projects"`
	Source  string `json:"source,omitempty" jsonschema:"only this tool: claude-code, codex, opencode or claude-ai"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum sessions to return (default 20, max 50)"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"opaque cursor from a previous result's next_cursor"`
}

// ProjectActivity groups sessions of one project.
type ProjectActivity struct {
	Project  string        `json:"project"`
	Sessions []SessionView `json:"sessions"`
}

// RecentActivityOut is the output of recent_activity.
type RecentActivityOut struct {
	Notice     string            `json:"notice"`
	Projects   []ProjectActivity `json:"projects"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func (s *srv) recentActivity(ctx context.Context, req *mcp.CallToolRequest, in RecentActivityIn) (*mcp.CallToolResult, RecentActivityOut, error) {
	s.audit(ctx, req, "recent_activity", in.Project)
	off, err := decodeCursor(in.Cursor)
	if err != nil {
		return nil, RecentActivityOut{}, err
	}
	limit := clamp(in.Limit, defaultLimit, maxLimit)
	recs, err := s.Store.Sessions(ctx, store.SessionQuery{Project: in.Project, Source: in.Source, Since: s.since(in.Days, 7), Limit: limit + 1, Offset: off})
	if err != nil {
		return nil, RecentActivityOut{}, fmt.Errorf("could not read sessions")
	}
	out := RecentActivityOut{Notice: notice}
	if len(recs) > limit {
		recs, out.NextCursor = recs[:limit], encodeCursor(off+limit)
	}
	idx := map[string]int{}
	now := s.Now()
	var lines []string
	for _, r := range recs {
		v := viewOf(r, false)
		i, ok := idx[v.Project]
		if !ok {
			i = len(out.Projects)
			idx[v.Project] = i
			out.Projects = append(out.Projects, ProjectActivity{Project: v.Project})
			lines = append(lines, "## "+clean(v.Project))
		}
		out.Projects[i].Sessions = append(out.Projects[i].Sessions, v)
		lines = append(lines, sessionLines(now, v, false)...)
	}
	if len(recs) == 0 {
		lines = []string{"(no shared activity in this window)"}
	}
	if out.NextCursor != "" {
		lines = append(lines, "more results: pass cursor "+out.NextCursor)
	}
	return result(lines), out, nil
}

// --- list_projects ---

// ListProjectsIn is the input of list_projects.
type ListProjectsIn struct {
	Source string `json:"source,omitempty" jsonschema:"only projects that have sessions from this tool"`
}

// ProjectView describes a shared project.
type ProjectView struct {
	Project      string         `json:"project"`
	Sessions     int            `json:"sessions"`
	Sources      map[string]int `json:"sources"`
	LastActivity string         `json:"last_activity"`
}

// ListProjectsOut is the output of list_projects.
type ListProjectsOut struct {
	Notice   string        `json:"notice"`
	Projects []ProjectView `json:"projects"`
}

func (s *srv) listProjects(ctx context.Context, req *mcp.CallToolRequest, in ListProjectsIn) (*mcp.CallToolResult, ListProjectsOut, error) {
	s.audit(ctx, req, "list_projects", "")
	ps, err := s.Store.ListProjects(ctx)
	if err != nil {
		return nil, ListProjectsOut{}, fmt.Errorf("could not read projects")
	}
	out := ListProjectsOut{Notice: notice, Projects: []ProjectView{}}
	now := s.Now()
	var lines []string
	for _, p := range ps {
		if in.Source != "" && p.Sources[in.Source] == 0 {
			continue
		}
		out.Projects = append(out.Projects, ProjectView{Project: p.Project, Sessions: p.Sessions, Sources: p.Sources, LastActivity: ts(p.LastActivity)})
		lines = append(lines, fmt.Sprintf("- %s: %d sessions, last activity %s", clean(p.Project), p.Sessions, ago(now, p.LastActivity)))
	}
	if len(lines) == 0 {
		lines = []string{"(no shared projects)"}
	}
	return result(lines), out, nil
}

// --- get_project_context ---

// ProjectContextIn is the input of get_project_context.
type ProjectContextIn struct {
	Project string `json:"project" jsonschema:"project as shown by list_projects"`
}

// ProjectContextOut is the output of get_project_context.
type ProjectContextOut struct {
	Notice          string         `json:"notice"`
	Project         string         `json:"project"`
	Sessions        int            `json:"sessions"`
	LastActivity    string         `json:"last_activity,omitempty"`
	RecentSessions  []SessionView  `json:"recent_sessions"`
	FrequentFiles   []string       `json:"frequent_files"`
	CommonCommands  []string       `json:"common_commands"`
	UnfinishedItems []WorkItemView `json:"unfinished_threads"`
}

func (s *srv) projectContext(ctx context.Context, req *mcp.CallToolRequest, in ProjectContextIn) (*mcp.CallToolResult, ProjectContextOut, error) {
	s.audit(ctx, req, "get_project_context", in.Project)
	if in.Project == "" {
		return nil, ProjectContextOut{}, fmt.Errorf("project is required")
	}
	all, err := s.Store.Sessions(ctx, store.SessionQuery{Project: in.Project, Limit: 200})
	if err != nil {
		return nil, ProjectContextOut{}, fmt.Errorf("could not read sessions")
	}
	if len(all) == 0 {
		return nil, ProjectContextOut{}, fmt.Errorf("no shared project named %q", clean(in.Project))
	}
	out := ProjectContextOut{Notice: notice, Project: in.Project, Sessions: len(all), LastActivity: ts(all[0].Payload.UpdatedAt),
		RecentSessions: []SessionView{}, FrequentFiles: []string{}, CommonCommands: []string{}, UnfinishedItems: []WorkItemView{}}
	files, cmds := map[string]int{}, map[string]int{}
	for i, r := range all {
		if i < 5 {
			out.RecentSessions = append(out.RecentSessions, viewOf(r, true))
		}
		if sm := r.Payload.Summary; sm != nil {
			for _, f := range sm.FilesTouched {
				files[f]++
			}
			for _, c := range sm.Commands {
				cmds[c]++
			}
		}
	}
	out.FrequentFiles, out.CommonCommands = topKeys(files, 10), topKeys(cmds, 8)

	items, err := s.Store.WorkItems(ctx, store.WorkItemQuery{Project: in.Project, Limit: 200})
	if err != nil {
		return nil, out, fmt.Errorf("could not read work items")
	}
	for _, it := range items {
		if unfinished(it.Item) {
			out.UnfinishedItems = append(out.UnfinishedItems, workView(it))
		}
	}
	now := s.Now()
	lines := []string{fmt.Sprintf("Project %s: %d shared sessions, last activity %s", clean(in.Project), out.Sessions, ago(now, all[0].Payload.UpdatedAt)), "", "Recent sessions:"}
	for _, v := range out.RecentSessions {
		lines = append(lines, sessionLines(now, v, true)...)
	}
	if len(out.FrequentFiles) > 0 {
		lines = append(lines, "", "Frequently touched files: "+clean(strings.Join(out.FrequentFiles, ", ")))
	}
	if len(out.CommonCommands) > 0 {
		lines = append(lines, "Common commands: "+clean(strings.Join(out.CommonCommands, ", ")))
	}
	lines = append(lines, "", "Unfinished threads (PRs not confirmed merged, issues not completed):")
	lines = append(lines, workLines(now, out.UnfinishedItems)...)
	return result(lines), out, nil
}

// unfinished: a PR or issue that no session confirmed as done.
func unfinished(it model.WorkItem) bool {
	switch it.Kind {
	case model.KindPR:
		return it.Status != model.StatusMerged && it.Status != model.StatusClosed
	case model.KindIssue:
		return it.Status != model.StatusCompleted && it.Status != model.StatusCanceled
	}
	return false
}

func topKeys(m map[string]int, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

// --- search_work ---

// SearchWorkIn is the input of search_work.
type SearchWorkIn struct {
	Query   string `json:"query" jsonschema:"keywords; every word must match (prefix match)"`
	Project string `json:"project,omitempty" jsonschema:"only this project"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum results (default 10, max 50)"`
}

// SearchWorkOut is the output of search_work.
type SearchWorkOut struct {
	Notice  string        `json:"notice"`
	Results []SessionView `json:"results"`
}

func (s *srv) searchWork(ctx context.Context, req *mcp.CallToolRequest, in SearchWorkIn) (*mcp.CallToolResult, SearchWorkOut, error) {
	s.audit(ctx, req, "search_work", in.Project)
	if strings.TrimSpace(in.Query) == "" {
		return nil, SearchWorkOut{}, fmt.Errorf("query is required")
	}
	recs, err := s.Store.Search(ctx, in.Query, in.Project, clamp(in.Limit, 10, maxLimit))
	if err != nil {
		return nil, SearchWorkOut{}, fmt.Errorf("search failed")
	}
	out := SearchWorkOut{Notice: notice, Results: []SessionView{}}
	now := s.Now()
	var lines []string
	for _, r := range recs {
		v := viewOf(r, true)
		out.Results = append(out.Results, v)
		lines = append(lines, sessionLines(now, v, true)...)
	}
	if len(lines) == 0 {
		lines = []string{"(no matches)"}
	}
	return result(lines), out, nil
}

// --- get_session ---

// GetSessionIn is the input of get_session.
type GetSessionIn struct {
	ID     string `json:"id" jsonschema:"session id from another tool's result"`
	Source string `json:"source,omitempty" jsonschema:"disambiguates ids shared by several tools"`
}

// GetSessionOut is the output of get_session.
type GetSessionOut struct {
	Notice    string         `json:"notice"`
	Session   SessionView    `json:"session"`
	WorkItems []WorkItemView `json:"work_items"`
}

func (s *srv) getSession(ctx context.Context, req *mcp.CallToolRequest, in GetSessionIn) (*mcp.CallToolResult, GetSessionOut, error) {
	if in.ID == "" {
		return nil, GetSessionOut{}, fmt.Errorf("id is required")
	}
	rec, err := s.Store.GetSession(ctx, in.Source, in.ID)
	if err != nil {
		s.audit(ctx, req, "get_session", "")
		return nil, GetSessionOut{}, fmt.Errorf("no such session")
	}
	s.audit(ctx, req, "get_session", rec.Payload.Project)
	out := GetSessionOut{Notice: notice, Session: viewOf(*rec, true), WorkItems: []WorkItemView{}}
	items, err := s.Store.WorkItems(ctx, store.WorkItemQuery{Project: rec.Payload.Project, Limit: 500})
	if err != nil {
		return nil, out, fmt.Errorf("could not read work items")
	}
	for _, it := range items {
		for _, k := range it.Sessions {
			if k.ID == rec.Payload.SessionID && k.Source == rec.Payload.Source {
				out.WorkItems = append(out.WorkItems, workView(it))
				break
			}
		}
	}
	now := s.Now()
	lines := append(sessionLines(now, out.Session, true), "", "Work items:")
	lines = append(lines, workLines(now, out.WorkItems)...)
	return result(lines), out, nil
}

// --- list_commits / list_pull_requests / list_artifacts / list_issues ---

// ListWorkIn is the common input of the list_* work tools.
type ListWorkIn struct {
	Project  string `json:"project,omitempty" jsonschema:"only this project"`
	Days     int    `json:"days,omitempty" jsonschema:"how many days back (default depends on the tool, max 365)"`
	Status   string `json:"status,omitempty" jsonschema:"filter by status: mentioned, created, updated, merged, closed, completed or canceled"`
	Provider string `json:"provider,omitempty" jsonschema:"issues only: github, linear or jira"`
	Source   string `json:"source,omitempty" jsonschema:"artifacts only: claude-code, codex, opencode or claude-ai"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum results (default 20, max 50)"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"opaque cursor from a previous result's next_cursor"`
}

// ListWorkOut is the common output of the list_* work tools.
type ListWorkOut struct {
	Notice     string         `json:"notice"`
	Items      []WorkItemView `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func (s *srv) listWork(ctx context.Context, req *mcp.CallToolRequest, tool, kind string, defDays int, in ListWorkIn) (*mcp.CallToolResult, ListWorkOut, error) {
	s.audit(ctx, req, tool, in.Project)
	off, err := decodeCursor(in.Cursor)
	if err != nil {
		return nil, ListWorkOut{}, err
	}
	limit := clamp(in.Limit, defaultLimit, maxLimit)
	q := store.WorkItemQuery{Project: in.Project, Kind: kind, Status: in.Status, Since: s.since(in.Days, defDays)}
	if kind == model.KindIssue {
		q.Provider = in.Provider
	}
	// A source filter is applied after the query, so page through the store.
	var items []store.WorkItemRecord
	for page := 0; len(items) < off+limit+1 && page < 40; page++ {
		q.Limit, q.Offset = 100, page*100
		batch, err := s.Store.WorkItems(ctx, q)
		if err != nil {
			return nil, ListWorkOut{}, fmt.Errorf("could not read work items")
		}
		for _, b := range batch {
			if in.Source == "" || hasSource(b.Sessions, in.Source) {
				items = append(items, b)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	out := ListWorkOut{Notice: notice, Items: []WorkItemView{}}
	if off > len(items) {
		off = len(items)
	}
	items = items[off:]
	if len(items) > limit {
		items, out.NextCursor = items[:limit], encodeCursor(off+limit)
	}
	for _, it := range items {
		out.Items = append(out.Items, workView(it))
	}
	lines := workLines(s.Now(), out.Items)
	if out.NextCursor != "" {
		lines = append(lines, "more results: pass cursor "+out.NextCursor)
	}
	return result(lines), out, nil
}

func hasSource(ss []store.SessionKey, source string) bool {
	for _, k := range ss {
		if k.Source == source {
			return true
		}
	}
	return false
}

func (s *srv) listCommits(ctx context.Context, req *mcp.CallToolRequest, in ListWorkIn) (*mcp.CallToolResult, ListWorkOut, error) {
	return s.listWork(ctx, req, "list_commits", model.KindCommit, 14, in)
}

func (s *srv) listPRs(ctx context.Context, req *mcp.CallToolRequest, in ListWorkIn) (*mcp.CallToolResult, ListWorkOut, error) {
	return s.listWork(ctx, req, "list_pull_requests", model.KindPR, 30, in)
}

func (s *srv) listArtifacts(ctx context.Context, req *mcp.CallToolRequest, in ListWorkIn) (*mcp.CallToolResult, ListWorkOut, error) {
	return s.listWork(ctx, req, "list_artifacts", model.KindFile, 14, in)
}

func (s *srv) listIssues(ctx context.Context, req *mcp.CallToolRequest, in ListWorkIn) (*mcp.CallToolResult, ListWorkOut, error) {
	return s.listWork(ctx, req, "list_issues", model.KindIssue, 14, in)
}
