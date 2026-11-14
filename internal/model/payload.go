package model

import (
	"strings"
	"time"
)

// Share levels.
const (
	LevelTitlesOnly = "titles-only"
	LevelSummary    = "summary"
	LevelFull       = "full"

	// OriginCLI marks a session started by a command (claude -p, codex exec,
	// an app's background call) rather than typed into a chat.
	OriginCLI = "cli"
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

	// The narrative is prose, written by a model (opt-in) or by the user; the
	// fields above are the facts the local rules extracted. The heuristic
	// writes no narrative. Abstract is one or two sentences for list views;
	// when it is empty, AbstractText derives it from the narrative.
	Abstract    string `json:"abstract,omitempty"`
	Narrative   string `json:"narrative,omitempty"`    // markdown paragraphs, at most MaxNarrativeLen
	NarrativeBy string `json:"narrative_by,omitempty"` // "<provider>:<model>" or NarrativeByUser
	// NarrativeLength is the length setting a model wrote under: "short",
	// "standard" or "detailed". Empty for a user's narrative.
	NarrativeLength string `json:"narrative_length,omitempty"`

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
	Origin      string     `json:"origin,omitempty"` // OriginCLI, or empty for a chat
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

// NarrativeByUser marks a narrative the user wrote or edited. It always wins
// over a model's.
const NarrativeByUser = "user"

// AbstractText is the short form for list views: Abstract, or else the first
// sentence of the narrative, cut to MaxAbstractLen.
func (s *Summary) AbstractText() string {
	if s.Abstract != "" {
		return s.Abstract
	}
	n := strings.TrimSpace(s.Narrative)
	if n == "" {
		return ""
	}
	if i := strings.Index(n, "\n\n"); i >= 0 {
		n = n[:i]
	}
	n = strings.Join(strings.Fields(n), " ")
	if i := firstSentenceEnd(n); i > 0 {
		n = n[:i]
	}
	if r := []rune(n); len(r) > MaxAbstractLen {
		n = string(r[:MaxAbstractLen-1]) + "…"
	}
	return n
}

// firstSentenceEnd is the index just past the first ". ", "! " or "? ", or -1.
func firstSentenceEnd(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if (s[i] == '.' || s[i] == '!' || s[i] == '?') && s[i+1] == ' ' {
			return i + 1
		}
	}
	return -1
}
