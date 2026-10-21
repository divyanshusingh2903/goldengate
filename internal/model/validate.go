package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Hard limits applied on both sides: the client refuses to send a payload that
// fails Validate, and the server refuses to store one. They bound what a
// leaked ingest token can stuff into the store.
const (
	MaxPayloadBytes   = 64 << 10
	MaxSummaryJSON    = 4 << 10
	MaxProjectLen     = 300
	MaxTitleLen       = 200
	MaxGoalLen        = 400
	MaxLastLen        = 500
	MaxListItems      = 8 // goals, description and outcomes each
	MaxListItemLen    = 300
	MaxSummarizedBy   = 80
	MaxPathLen        = 200
	MaxFiles          = 20
	MaxCommands       = 12
	MaxTools          = 12
	MaxWorkItemCount  = 50
	MaxKeyLen         = 200
	MaxURLLen         = 300
	MaxEvidenceCount  = 3
	MaxRedactionKinds = 30
)

var (
	validSources   = map[string]bool{"claude-code": true, "codex": true, "opencode": true, "claude-ai": true, "chatgpt": true}
	validKinds     = map[string]bool{KindCommit: true, KindPR: true, KindIssue: true, KindFile: true, KindArtifact: true}
	validProviders = map[string]bool{"": true, "github": true, "linear": true, "jira": true}
	validStatuses  = map[string]bool{StatusMentioned: true, StatusCreated: true, StatusUpdated: true, StatusMerged: true, StatusClosed: true, StatusCompleted: true, StatusCanceled: true}
	validVia       = map[string]bool{ViaToolResult: true, ViaHarnessEvent: true, ViaText: true}
	reSessionID    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	reHash         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidSource reports whether s is a known source name.
func ValidSource(s string) bool { return validSources[s] }

// CheckText rejects invalid UTF-8, control characters (including newlines) and
// bidirectional-override characters, and enforces a rune limit.
func CheckText(field, s string, max int) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s: invalid UTF-8", field)
	}
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%s: longer than %d characters", field, max)
	}
	for _, r := range s {
		if unicode.IsControl(r) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200B || r == 0xFEFF {
			return fmt.Errorf("%s: contains a control or invisible character", field)
		}
	}
	return nil
}

// Validate checks a payload strictly. now is injected for testing.
func (p *SessionPayload) Validate(now time.Time) error {
	if !validSources[p.Source] {
		return fmt.Errorf("source: unknown %q", p.Source)
	}
	if !reSessionID.MatchString(p.SessionID) {
		return fmt.Errorf("session_id: invalid")
	}
	if p.Project == "" {
		return fmt.Errorf("project: empty")
	}
	if err := CheckText("project", p.Project, MaxProjectLen); err != nil {
		return err
	}
	if p.Origin != "" && p.Origin != OriginCLI {
		return fmt.Errorf("origin: unsupported %q", p.Origin)
	}
	switch p.Level {
	case LevelSummary, LevelTitlesOnly:
	default:
		return fmt.Errorf("level: unsupported %q", p.Level)
	}
	for name, t := range map[string]time.Time{"started_at": p.StartedAt, "updated_at": p.UpdatedAt} {
		if t.Year() < 2000 || t.After(now.Add(24*time.Hour)) {
			return fmt.Errorf("%s: out of range", name)
		}
	}
	if !reHash.MatchString(p.ContentHash) {
		return fmt.Errorf("content_hash: must be 64 hex characters")
	}
	if len(p.Redactions) > MaxRedactionKinds {
		return fmt.Errorf("redactions: too many kinds")
	}
	for k, v := range p.Redactions {
		if err := CheckText("redactions key", k, 40); err != nil || v < 0 {
			return fmt.Errorf("redactions: invalid entry")
		}
	}
	if p.Level == LevelTitlesOnly {
		if p.Summary != nil || len(p.WorkItems) > 0 {
			return fmt.Errorf("titles-only payload must not carry a summary or work items")
		}
		if err := CheckText("title_only", p.TitleOnly, MaxTitleLen); err != nil {
			return err
		}
	} else {
		if p.TitleOnly != "" {
			return fmt.Errorf("title_only: only valid at titles-only level")
		}
		if p.Summary == nil {
			return fmt.Errorf("summary: missing")
		}
		if err := p.Summary.validate(); err != nil {
			return err
		}
		if len(p.WorkItems) > MaxWorkItemCount {
			return fmt.Errorf("work_items: more than %d", MaxWorkItemCount)
		}
		for i := range p.WorkItems {
			if err := p.WorkItems[i].validate(i); err != nil {
				return err
			}
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	if len(b) > MaxPayloadBytes {
		return fmt.Errorf("payload: %d bytes exceeds %d", len(b), MaxPayloadBytes)
	}
	return nil
}

func (s *Summary) validate() error {
	checks := []struct {
		f string
		v string
		n int
	}{{"summary.title", s.Title, MaxTitleLen}, {"summary.goal", s.Goal, MaxGoalLen}, {"summary.last_assistant", s.LastAssistant, MaxLastLen},
		{"summary.outcome", s.Outcome, 100}, {"summary.git_branch", s.GitBranch, 100},
		{"summary.first_message", s.FirstMessage, MaxLastLen}, {"summary.last_message", s.LastMessage, MaxLastLen},
		{"summary.summarized_by", s.SummarizedBy, MaxSummarizedBy}}
	for _, c := range checks {
		if err := CheckText(c.f, c.v, c.n); err != nil {
			return err
		}
	}
	for _, l := range []struct {
		f  string
		vs []string
	}{{"summary.goals", s.Goals}, {"summary.description", s.Description}, {"summary.outcomes", s.Outcomes}, {"summary.open_problems", s.OpenProblems}} {
		if len(l.vs) > MaxListItems {
			return fmt.Errorf("%s: more than %d items", l.f, MaxListItems)
		}
		for _, v := range l.vs {
			if err := CheckText(l.f, v, MaxListItemLen); err != nil {
				return err
			}
		}
	}
	if s.Title == "" {
		return fmt.Errorf("summary.title: empty")
	}
	if len(s.FilesTouched) > MaxFiles || len(s.Commands) > MaxCommands || len(s.ToolsUsed) > MaxTools {
		return fmt.Errorf("summary: too many files, commands or tools")
	}
	for _, f := range s.FilesTouched {
		if err := CheckText("summary.files_touched", f, MaxPathLen); err != nil {
			return err
		}
	}
	for _, c := range s.Commands {
		if err := CheckText("summary.commands", c, 40); err != nil {
			return err
		}
	}
	for k, n := range s.ToolsUsed {
		if err := CheckText("summary.tools_used", k, 40); err != nil || n < 0 {
			return fmt.Errorf("summary.tools_used: invalid entry")
		}
	}
	if s.TurnCount < 0 || s.DurationSec < 0 {
		return fmt.Errorf("summary: negative counts")
	}
	b, _ := json.Marshal(s)
	if len(b) > MaxSummaryJSON {
		return fmt.Errorf("summary: %d bytes exceeds %d", len(b), MaxSummaryJSON)
	}
	return nil
}

func (w *WorkItem) validate(i int) error {
	f := fmt.Sprintf("work_items[%d]", i)
	if !validKinds[w.Kind] {
		return fmt.Errorf("%s.kind: unknown %q", f, w.Kind)
	}
	if !validProviders[w.Provider] {
		return fmt.Errorf("%s.provider: unknown %q", f, w.Provider)
	}
	if !validStatuses[w.Status] {
		return fmt.Errorf("%s.status: unknown %q", f, w.Status)
	}
	if w.Key == "" {
		return fmt.Errorf("%s.key: empty", f)
	}
	for _, c := range []struct {
		n string
		v string
		m int
	}{{"key", w.Key, MaxKeyLen}, {"title", w.Title, MaxTitleLen}, {"raw_state", w.RawState, 40}} {
		if err := CheckText(f+"."+c.n, c.v, c.m); err != nil {
			return err
		}
	}
	if w.URL != "" {
		if err := CheckText(f+".url", w.URL, MaxURLLen); err != nil {
			return err
		}
		rest, ok := strings.CutPrefix(w.URL, "https://")
		host, _, _ := strings.Cut(rest, "/")
		if !ok || strings.ContainsAny(w.URL, " \t") || strings.Contains(host, "@") || host == "" {
			return fmt.Errorf("%s.url: must be a plain https URL without credentials", f)
		}
	}
	if w.ObservedAt.Year() < 2000 {
		return fmt.Errorf("%s.observed_at: out of range", f)
	}
	if len(w.Evidence) > MaxEvidenceCount {
		return fmt.Errorf("%s.evidence: more than %d", f, MaxEvidenceCount)
	}
	for _, e := range w.Evidence {
		if !validVia[e.Via] || e.Turn < -1 {
			return fmt.Errorf("%s.evidence: invalid", f)
		}
		if err := CheckText(f+".evidence.session_id", e.SessionID, 128); err != nil {
			return err
		}
	}
	return nil
}

// ValidSessionID reports whether id is acceptable as a session id.
func ValidSessionID(id string) bool { return reSessionID.MatchString(id) }
