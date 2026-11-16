package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/summarize"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

// The inbox: the user's decision about one session. A session of a project
// that is not auto-approved is sent only when it was approved for exactly the
// content it has now; an excluded or retracted session is never sent again,
// whatever its project says.
const (
	// DecisionApproved lets the session be sent as long as its content is
	// what was approved.
	DecisionApproved = "approved"
	// DecisionExcluded keeps the session on this machine.
	DecisionExcluded = "excluded"
	// DecisionRetracted marks a session that was sent and then taken back.
	DecisionRetracted = "retracted"
)

// Decision is what the user decided for one session, and for which content.
type Decision struct {
	Source, SessionID string
	State             string
	ContentHash       string // the content the decision was made for
	At                time.Time
	Project, Title    string // for listing; the title is the redacted one
}

// Blocks reports whether the decision keeps the session from being sent.
func (d Decision) Blocks() bool { return d.State == DecisionExcluded || d.State == DecisionRetracted }

// Decision returns the decision for a session, if any.
func (s *State) Decision(ctx context.Context, source, id string) (Decision, bool, error) {
	d := Decision{Source: source, SessionID: id}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT state, content_hash, decided_at, project, title FROM decisions WHERE source=? AND session_id=?`, source, id).
		Scan(&d.State, &d.ContentHash, &at, &d.Project, &d.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return Decision{}, false, nil
	}
	if err != nil {
		return Decision{}, false, fmt.Errorf("state: decision: %w", err)
	}
	d.At = time.Unix(0, at).UTC()
	return d, true, nil
}

// Decide records a decision, replacing an earlier one.
func (s *State) Decide(ctx context.Context, d Decision) error {
	switch d.State {
	case DecisionApproved, DecisionExcluded, DecisionRetracted:
	default:
		return fmt.Errorf("state: unknown decision %q", d.State)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO decisions(source, session_id, state, content_hash, decided_at, project, title) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(source, session_id) DO UPDATE SET state=excluded.state, content_hash=excluded.content_hash, decided_at=excluded.decided_at, project=excluded.project, title=excluded.title`,
		d.Source, d.SessionID, d.State, d.ContentHash, d.At.UnixNano(), d.Project, d.Title); err != nil {
		return fmt.Errorf("state: decide: %w", err)
	}
	return nil
}

// Undecide removes a session's decision, so it is pending again (or sent
// again, in an auto-approved project). It reports whether there was one.
func (s *State) Undecide(ctx context.Context, source, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM decisions WHERE source=? AND session_id=?`, source, id)
	if err != nil {
		return false, fmt.Errorf("state: undecide: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Decisions lists decisions in the given state ("" for all), newest first.
func (s *State) Decisions(ctx context.Context, state string) ([]Decision, error) {
	q, args := `SELECT source, session_id, state, content_hash, decided_at, project, title FROM decisions`, []any{}
	if state != "" {
		q, args = q+` WHERE state=?`, append(args, state)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY decided_at DESC, source, session_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("state: decisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Decision
	for rows.Next() {
		var d Decision
		var at int64
		if err := rows.Scan(&d.Source, &d.SessionID, &d.State, &d.ContentHash, &at, &d.Project, &d.Title); err != nil {
			return nil, fmt.Errorf("state: decisions: %w", err)
		}
		d.At = time.Unix(0, at).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// PendingItem is a session that waits for the user's approval: it is new, or
// it changed since it was approved or sent.
type PendingItem struct {
	Source, SessionID string
	Project           string // local project key
	ProjectName       string // the named project it belongs to, if any
	Title             string // redacted, as it would be sent
	Updated           time.Time
	ContentHash       string
	// Changed is set when an earlier version was approved or sent.
	Changed bool
	// Conflict is set when the user edited an earlier version's summary:
	// the session is held until they decide what happens to that edit.
	Conflict bool
	// Suggested is set when the project's rules approve the session and its
	// trust level is "suggest": it waits, marked for approval.
	Suggested bool
	// NewGrouping is set in an "auto" project when the rules approve the
	// session but nothing was ever sent from its folder or chat grouping,
	// which the project reaches only through a broader member: automation
	// starts there once the user has approved and sent something from it.
	NewGrouping bool
	// RuleID is the rule behind Suggested or NewGrouping; empty when the
	// project has no approve rule and the trust level covers all of it.
	RuleID string
}

// KnownGrouping reports whether anything was ever sent from a folder or
// chat grouping.
func (s *State) KnownGrouping(ctx context.Context, project string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM sessions WHERE project=? LIMIT 1)`, project).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("state: known grouping: %w", err)
	}
	return n > 0, nil
}

// The user's edits of session summaries live next to the record of what was
// sent, one per session, each for the content it was made on.

// Edit returns the user's edit of a session's summary, if any.
func (s *State) Edit(ctx context.Context, source, id string) (summarize.Edit, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT edit FROM edits WHERE source=? AND session_id=?`, source, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return summarize.Edit{}, false, nil
	}
	if err != nil {
		return summarize.Edit{}, false, fmt.Errorf("state: edit: %w", err)
	}
	var e summarize.Edit
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return summarize.Edit{}, false, fmt.Errorf("state: edit: %w", err)
	}
	return e, true, nil
}

// PutEdit stores the user's edit of a session's summary and makes the next
// sync look at the session again even if its file has not changed.
func (s *State) PutEdit(ctx context.Context, source, id string, e summarize.Edit, now time.Time) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("state: put edit: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO edits(source, session_id, content_hash, edit, edited_at) VALUES(?,?,?,?,?)
		ON CONFLICT(source, session_id) DO UPDATE SET content_hash=excluded.content_hash, edit=excluded.edit, edited_at=excluded.edited_at`,
		source, id, e.ContentHash, string(b), now.UnixNano()); err != nil {
		return fmt.Errorf("state: put edit: %w", err)
	}
	return s.revisit(ctx, source, id)
}

// DeleteEdit removes the user's edit; the generated summary is used again.
func (s *State) DeleteEdit(ctx context.Context, source, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM edits WHERE source=? AND session_id=?`, source, id)
	if err != nil {
		return false, fmt.Errorf("state: delete edit: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, s.revisit(ctx, source, id)
}

// DeleteEdits removes the edit of every session with this id (in whatever
// source) and reports how many there were.
func (s *State) DeleteEdits(ctx context.Context, id string) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM edits WHERE session_id=?`, id)
	if err != nil {
		return 0, fmt.Errorf("state: delete edits: %w", err)
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET size=-1 WHERE session_id=?`, id); err != nil {
		return 0, fmt.Errorf("state: revisit: %w", err)
	}
	return int(n), nil
}

// revisit forgets a pushed session's file stat, so the next sync builds it
// again instead of skipping it as unchanged.
func (s *State) revisit(ctx context.Context, source, id string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET size=-1 WHERE source=? AND session_id=?`, source, id); err != nil {
		return fmt.Errorf("state: revisit: %w", err)
	}
	return nil
}

// ErrNoSession reports that no session of a shared project has the id.
var ErrNoSession = errors.New("sync: no such session in the shared projects")

// BuildOne builds the payload of one session of a shared project, exactly as
// a sync would, without sending anything. It never calls a model (cached
// answers are used). With generated set, the user's edit is left out, so the
// result is what the summarizer alone produces.
func (s *Syncer) BuildOne(ctx context.Context, source, id string, generated bool) (*model.SessionPayload, error) {
	wo := workitems.Options{Providers: s.Config.Issues.Providers, GitLabHosts: s.Config.Issues.GitLabHosts}
	wo.KnownPrefixes = append(append([]string{}, s.Config.Issues.LinearTeams...), s.Config.Issues.JiraProjects...)
	for _, a := range s.Registry.All() {
		if source != "" && a.Name() != source {
			continue
		}
		det, err := a.Detect(ctx)
		if err != nil || !det.Installed {
			continue
		}
		for ref, lerr := range a.ListSessions(ctx, adapters.ListOpts{}) {
			if lerr != nil {
				if adapters.IsSkip(lerr) {
					continue
				}
				return nil, fmt.Errorf("sync: list %s: %w", a.Name(), lerr)
			}
			if ref.ID != id || !s.Shared(ref) {
				continue
			}
			raw, err := a.Load(ctx, ref)
			if err != nil {
				return nil, fmt.Errorf("sync: load: %w", err)
			}
			o := s.summarizeOptions(ctx, ref, wo, true, nil)
			if generated {
				o.Edit = nil
			}
			p, err := summarize.Build(raw, o)
			if err != nil {
				return nil, fmt.Errorf("sync: summarize: %w", err)
			}
			return p, nil
		}
	}
	return nil, ErrNoSession
}
