// Package model holds the normalized types shared by adapters, the summarizer
// and the sync layer. It has no dependencies on any source tool's format.
package model

import "time"

// SessionRef is cheap metadata about one session, produced without parsing
// the session body.
type SessionRef struct {
	Source     string    `json:"source"`
	ID         string    `json:"id"`
	ProjectKey string    `json:"project_key"` // normalized absolute folder path
	Path       string    `json:"-"`           // file or db location; never leaves the machine
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	SizeBytes  int64     `json:"size_bytes"`
}

// ToolKind is the adapter-normalized category of a tool call.
type ToolKind string

const (
	ToolOther     ToolKind = "other"
	ToolShell     ToolKind = "shell"
	ToolFileRead  ToolKind = "file_read"
	ToolFileWrite ToolKind = "file_write" // whole-file write; Created says whether it was new
	ToolFileEdit  ToolKind = "file_edit"
	ToolIssue     ToolKind = "issue" // issue-tracker MCP call (Linear save_issue/get_issue)
)

// ToolResult is the outcome of a tool call. OK is true only when the source
// tool positively recorded success; unknown outcomes are OK=false, Known=false.
type ToolResult struct {
	Known    bool   `json:"known"`
	OK       bool   `json:"ok"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Text     string `json:"-"` // raw output; used for evidence matching, never sent
}

// ToolCall is one tool invocation with its (optional) result.
type ToolCall struct {
	ID      string
	Name    string // source-native tool name
	Kind    ToolKind
	Command string // ToolShell: the command line
	Path    string // file tools: path as recorded by the tool
	Created bool   // ToolFileWrite: true when the result says a new file was created
	Input   []byte // raw JSON input, kept only for ToolIssue calls
	Time    time.Time
	Result  ToolResult
}

// Turn is one message of the conversation.
type Turn struct {
	Role     string // "user" | "assistant"
	Text     string // all text of the turn
	LastText string // assistant turns: the final text block, which is the closing message
	Time     time.Time
	Tools    []ToolCall
}

// HarnessEvent is a structured fact emitted by the tool itself rather than
// by the model (for example Claude Code's pr-link event). It counts as
// evidence like a successful tool result does.
type HarnessEvent struct {
	Kind   string // "pr_link"
	URL    string
	Number int
	Repo   string
	Time   time.Time
}

// RawSession is a fully parsed session. It never leaves the machine.
type RawSession struct {
	Ref           SessionRef
	Title         string
	Turns         []Turn
	FilesTouched  []string // project-relative where possible
	ToolsUsed     map[string]int
	SourceVersion string // version of the tool that wrote the session, if recorded
	GitBranch     string
	GitRemote     string // credentials stripped
	Events        []HarnessEvent
	Warnings      []string
}
