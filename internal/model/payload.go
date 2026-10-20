package model

import "time"

// Share levels.
const (
	LevelTitlesOnly = "titles-only"
	LevelSummary    = "summary"
	LevelFull       = "full"
)

// Summary is the compact, redacted description of one session.
type Summary struct {
	Title         string         `json:"title"`
	Goal          string         `json:"goal,omitempty"`
	FilesTouched  []string       `json:"files_touched,omitempty"`
	ToolsUsed     map[string]int `json:"tools_used,omitempty"`
	Commands      []string       `json:"commands,omitempty"` // command names only
	LastAssistant string         `json:"last_assistant,omitempty"`
	Outcome       string         `json:"outcome,omitempty"`
	TurnCount     int            `json:"turn_count"`
	DurationSec   int64          `json:"duration_sec"`
	GitBranch     string         `json:"git_branch,omitempty"`
}

// SessionPayload is the unit that leaves the machine. It is built only by the
// summarize package and sent only through sync.Push.
type SessionPayload struct {
	Source      string     `json:"source"`
	SessionID   string     `json:"session_id"`
	Project     string     `json:"project"` // scrubbed project key, e.g. ~/code/foo
	Level       string     `json:"level"`
	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Summary     *Summary   `json:"summary,omitempty"` // nil at titles-only except Title
	TitleOnly   string     `json:"title_only,omitempty"`
	WorkItems   []WorkItem `json:"work_items,omitempty"`
	ContentHash string     `json:"content_hash"`
	Redactions  Redactions `json:"redactions"`
}

// Redactions counts redaction events by kind. Never contains the secrets.
type Redactions map[string]int
