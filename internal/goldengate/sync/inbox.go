package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
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
}
