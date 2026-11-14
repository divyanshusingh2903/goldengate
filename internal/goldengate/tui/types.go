// Package tui is the interactive terminal UI (Bubble Tea). It knows nothing
// about adapters, config files or the network: everything it does goes through
// the Backend interface, which the CLI implements on top of the same code the
// non-interactive commands use. That keeps the UI testable with a fake backend
// and guarantees the TUI cannot send anything the CLI would not.
package tui

import (
	"context"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// ShareState is how a project is currently treated.
type ShareState int

// Share states, in the order the legend explains them.
const (
	NotShared ShareState = iota // [ ]
	Shared                      // [x]
	Partial                     // [~] some sources shared
	Denied                      // [-]
)

// SourceInfo is one tool's sessions inside a project.
type SourceInfo struct {
	Name     string
	Sessions int
	Shared   bool // whether this source is shared for this project
}

// Row is one project as the picker shows it.
type Row struct {
	Key      string // normalized folder path, or "claude-ai:Chats"
	Label    string // display form (~/code/app)
	Root     string // label of the repository this folder is a worktree of, if any
	Sources  []SourceInfo
	Sessions int
	Last     time.Time
	Pending  int       // sessions a sync would send (new or changed); shared projects only
	Synced   int       // sessions already pushed to the server
	LastSync time.Time // latest push; zero when nothing was ever pushed
	State    ShareState
	Reason   string
	Virtual  bool   // a chat export bucket rather than a folder
	Project  string // the named project this folder or chat grouping belongs to; "" when none
}

// SourceStatus is a tool's presence and whether it is enabled.
type SourceStatus struct {
	Name      string
	Installed bool
	Sessions  int
	Enabled   bool
	Note      string
}

// Snapshot is everything the picker needs.
type Snapshot struct {
	Rows     []Row
	Sources  []SourceStatus
	Level    string
	Since    string
	Server   string // URL, "" when none is configured
	FirstRun bool   // no config exists yet
	Warnings []string
	// Summarizer names who writes summaries: "" for the local rules, else
	// "<provider>:<model>".
	Summarizer string
}

// PreviewItem is one session exactly as it would be sent.
type PreviewItem struct {
	Source   string
	ID       string
	Updated  time.Time
	Status   string // new | changed | unchanged
	Payload  model.SessionPayload
	JSON     string // the exact bytes that would go on the wire, indented
	Diff     []string
	Redacted map[string]int
}

// Preview is a project's outgoing payloads.
type Preview struct {
	Key        string
	Level      string
	Items      []PreviewItem
	Total      int
	Redactions map[string]int
	Warnings   int
}

// ActionKind is a change to what is shared.
type ActionKind int

// Actions.
const (
	ActAllow ActionKind = iota
	ActRemove
	ActDeny
	ActToggleSource
	ActSetProject    // put Keys in the project named Project, creating it if needed; they become shared
	ActRenameProject // rename the project named Project to To; an existing To merges the two
)

// Action changes the share rules for a set of folders or chat projects.
type Action struct {
	Kind   ActionKind
	Keys   []string
	Source string   // ActToggleSource only
	Others []string // ActToggleSource: the project's other sources
	// Project names the project for ActSetProject and ActRenameProject; To is
	// the new name for ActRenameProject.
	Project, To string
}

// RemotePrompt offers to delete un-shared projects from the server too.
type RemotePrompt struct {
	Count  int
	Handle any // opaque; pass it back to DeleteRemote
}

// ActionResult reports what an action did.
type ActionResult struct {
	Message string
	Remote  *RemotePrompt
}

// SyncEventKind enumerates progress events.
type SyncEventKind int

// Sync events.
const (
	SyncPlanned SyncEventKind = iota
	SyncProjectStart
	SyncProjectDone
	SyncProjectFailed
	SyncFinished
	SyncPrepared // one more session read and redacted (before the plan is complete)
	SyncProgress // Sessions more of Project reached the server
)

// SyncEvent is one progress report.
type SyncEvent struct {
	Kind     SyncEventKind
	Project  string // display label
	Sessions int    // in this project (start/done) or in total (planned)
	Projects int    // planned only
	Err      string
	// SyncPrepared: the session just summarized, and by whom ("heuristic"
	// when the rules wrote it, including a model call that failed).
	Title, By string
	// SyncPlanned: the whole plan, so the review needs no separate pass.
	Plan *PlanInfo
}

// ProjectPlan is what a sync would do for one project.
type ProjectPlan struct {
	Label    string
	Sessions int
}

// PlanInfo is a dry look at the next sync.
type PlanInfo struct {
	Sessions    int
	Projects    []ProjectPlan
	NotSelected int
	Unchanged   int
	Warnings    int
	Redactions  map[string]int
	Server      string // "" means a real sync is impossible (no server configured)
	// Summarizer is the configured model ("" for the heuristic). Stale counts
	// already-synced sessions whose summary it did not write; Resummarized
	// those of them included in this plan (SyncOpts.Resummarize).
	Summarizer   string
	Stale        int
	Resummarized int
}

// SyncOpts narrows a plan and its sync.
type SyncOpts struct {
	// Resummarize includes already-synced sessions whose summary the
	// configured model did not write; their redacted conversations go to it.
	Resummarize bool
}

// RemoteProject is something the server holds.
type RemoteProject struct {
	ID       string // the server's project id; "" when the server has none
	Label    string
	Sessions int
	Last     time.Time
}

// ServerInfo is the Server screen's content.
type ServerInfo struct {
	URL        string
	Configured bool
	Healthy    bool
	TokenOK    bool
	TokenStore string
	Projects   []RemoteProject
	Err        string
}

// Settings are the user-editable options.
type Settings struct {
	Level         string
	Since         string
	Sources       []SourceStatus
	ExtraPatterns []string
	EnrichGit     bool
	Theme         string // color preset; "" is the default (Golden Gate Light)
	Summarize     SummarizeSettings
}

// SummarizeSettings choose how summaries are written. Provider "" is the local
// heuristic; "anthropic" and "openai" send the redacted conversation of shared
// projects to a model (opt-in).
type SummarizeSettings struct {
	Provider, Model, BaseURL string
	// Length is how long a narrative the model writes: "short", "standard"
	// ("" means standard) or "detailed".
	Length    string
	KeySource string // where the API key comes from: "keyring", "$ANTHROPIC_API_KEY", "none" (read-only)
}

// ToolStatus is a detected coding tool, for the wizard.
type ToolStatus struct {
	Name      string
	Installed bool
	Sessions  int
	Note      string
}

// InitParams drive the wizard's server step. AdminToken is held in memory only.
type InitParams struct {
	ServerURL  string
	AdminToken string
	PairCode   string
}

// InitResult is what the wizard shows after connecting.
type InitResult struct {
	MCPURL     string
	ReadToken  string // shown once
	OAuth      bool
	TokenStore string
	// SeesAll is set when this setup made the first assistant able to see
	// every project the user shares.
	SeesAll bool
}

// Backend is everything the UI does to the world.
type Backend interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	Preview(ctx context.Context, key string, limit int) (Preview, error)
	Apply(ctx context.Context, a Action) (ActionResult, error)
	Undo(ctx context.Context) (string, error)
	DeleteRemote(ctx context.Context, handle any) (string, error)
	Plan(ctx context.Context, o SyncOpts) (PlanInfo, error)
	Sync(ctx context.Context, dry bool, o SyncOpts, emit func(SyncEvent)) error
	Server(ctx context.Context) (ServerInfo, error)
	// DeleteServerProject deletes a project from the server; ref is its id
	// (RemoteProject.ID) or, without one, its label.
	DeleteServerProject(ctx context.Context, ref string) error
	Settings(ctx context.Context) (Settings, error)
	SaveSettings(ctx context.Context, s Settings) error
	// SetSummarizerKey stores a provider's API key in the OS keyring; an
	// empty key removes it.
	SetSummarizerKey(ctx context.Context, provider, key string) error
	// TestSummarizer sends one tiny sample (not the user's data) to the
	// configured model and describes the result.
	TestSummarizer(ctx context.Context) (string, error)
	// SummarizerModels lists the models a provider offers at baseURL (""
	// means its default endpoint), using the stored key. For Ollama it also
	// tells whether Ollama is running here.
	SummarizerModels(ctx context.Context, provider, baseURL string) ([]string, error)
	Detect(ctx context.Context) ([]ToolStatus, error)
	Init(ctx context.Context, p InitParams) (InitResult, error)

	// The agents methods manage which assistant sees which project. Each
	// takes the admin secret, which the UI holds in memory for the run and
	// the backend uses for that call only; it is never stored.

	// Agents lists the agents, their grants and tokens, and the projects the
	// server holds.
	Agents(ctx context.Context, admin string) (AgentsInfo, error)
	// ApplyGrants applies reviewed changes in order and stops at the first failure.
	ApplyGrants(ctx context.Context, admin string, changes []GrantChange) error
	// AgentView is what an agent can read right now.
	AgentView(ctx context.Context, admin, agentID string) (AgentView, error)
	// RevokeAgentToken revokes one of an agent's read tokens.
	RevokeAgentToken(ctx context.Context, admin, tokenID string) error
}

// AgentsInfo is the Agents screen's content.
type AgentsInfo struct {
	Agents   []AgentInfo
	Projects []AgentProject
}

// AgentInfo is one assistant and what it may see.
type AgentInfo struct {
	ID, Name    string
	AllProjects bool            // sees every project, including future ones
	MaxDays     int             // 0: no history limit of its own
	Grants      map[string]bool // by project id
	Tokens      []AgentToken
	Clients     []string  // OAuth applications connected as this agent
	LastUsed    time.Time // newest use of any of its tokens; zero: never
}

// AgentToken is a read token of an agent (never the secret).
type AgentToken struct {
	ID, Label string
	Created   time.Time
	LastUsed  time.Time
}

// AgentProject is a project the server holds.
type AgentProject struct {
	ID, Name string
	Sessions int
}

// GrantChange is one change to what an agent may see: a project granted or
// revoked, or, with All set, the agent's "every project" switch.
type GrantChange struct {
	AgentID, AgentName     string
	ProjectID, ProjectName string
	All                    bool
	Grant                  bool
}

// AgentView is what an agent can read right now: its projects, and the
// sessions and work items of the last AgentViewDays days in them.
type AgentView struct {
	Agent    AgentInfo
	Projects []AgentProject
	Sessions []AgentViewSession
	Work     []AgentViewWork
	// Note says where the view comes from and what it may miss.
	Note string
}

// AgentViewDays is the window "view as agent" shows, the same default the
// assistant's tools use.
const AgentViewDays = 5

// AgentViewSession is one session summary the agent can read.
type AgentViewSession struct {
	Project, Source, Title, Abstract string
	Updated                          time.Time
}

// AgentViewWork is one work item the agent can read.
type AgentViewWork struct {
	Project, Kind, Key, Status string
}
