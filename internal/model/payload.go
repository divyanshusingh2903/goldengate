package model

import "time"

// Share levels.
const (
	LevelTitlesOnly = "titles-only"
	LevelSummary    = "summary"
	LevelFull       = "full"
)

// Summary is the compact, redacted description of one session. A coding
// session often covers several tasks, so goals, description and outcomes are
// lists: one entry per task.
type Summary struct {
	Title        string         `json:"title"`
	Goals        []string       `json:"goals,omitempty"`         // what was asked for
	Description  []string       `json:"description,omitempty"`   // what was done, per task (model summarizer only)
	Outcomes     []string       `json:"outcomes,omitempty"`      // facts from command results: tests passed/failing, commit made, PR opened
	OpenProblems []string       `json:"open_problems,omitempty"` // what was left unfinished or broken (model summarizer only)
	FirstMessage string         `json:"first_message,omitempty"`
	LastMessage  string         `json:"last_message,omitempty"` // the assistant's closing message
	FilesTouched []string       `json:"files_touched,omitempty"`
	ToolsUsed    map[string]int `json:"tools_used,omitempty"`
	Commands     []string       `json:"commands,omitempty"` // command names only
	TurnCount    int            `json:"turn_count"`
	DurationSec  int64          `json:"duration_sec"`
	GitBranch    string         `json:"git_branch,omitempty"`
	SummarizedBy string         `json:"summarized_by,omitempty"` // "heuristic" or "<provider>:<model>"

	// Deprecated: sent by older clients and kept so stored records still read.
	// New payloads carry Goals, Outcomes and LastMessage instead.
	Goal          string `json:"goal,omitempty"`
	LastAssistant string `json:"last_assistant,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
}

// GoalList is Goals, or the legacy Goal of an older record.
func (s *Summary) GoalList() []string {
	if len(s.Goals) > 0 || s.Goal == "" {
		return s.Goals
	}
	return []string{s.Goal}
}

// OutcomeList is Outcomes, or the legacy Outcome of an older record.
func (s *Summary) OutcomeList() []string {
	if len(s.Outcomes) > 0 || s.Outcome == "" {
		return s.Outcomes
	}
	return []string{s.Outcome}
}

// Closing is LastMessage, or the legacy LastAssistant of an older record.
func (s *Summary) Closing() string {
	if s.LastMessage != "" {
		return s.LastMessage
	}
	return s.LastAssistant
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
