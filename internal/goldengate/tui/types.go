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
	Virtual  bool // a chat export bucket rather than a folder
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
)

// Action changes the share rules for a set of folders or chat projects.
type Action struct {
	Kind   ActionKind
	Keys   []string
	Source string   // ActToggleSource only
	Others []string // ActToggleSource: the project's other sources
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
}

// RemoteProject is something the server holds.
type RemoteProject struct {
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
}

// Backend is everything the UI does to the world.
type Backend interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	Preview(ctx context.Context, key string, limit int) (Preview, error)
	Apply(ctx context.Context, a Action) (ActionResult, error)
	Undo(ctx context.Context) (string, error)
	DeleteRemote(ctx context.Context, handle any) (string, error)
	Plan(ctx context.Context) (PlanInfo, error)
	Sync(ctx context.Context, dry bool, emit func(SyncEvent)) error
	Server(ctx context.Context) (ServerInfo, error)
	DeleteServerProject(ctx context.Context, label string) error
	Settings(ctx context.Context) (Settings, error)
	SaveSettings(ctx context.Context, s Settings) error
	Detect(ctx context.Context) ([]ToolStatus, error)
	Init(ctx context.Context, p InitParams) (InitResult, error)
}
