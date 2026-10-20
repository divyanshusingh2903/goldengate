// Package store defines the persistence interface for the bridge server and
// ships memory (tests) and SQLite (self-host) implementations. Queries are
// deliberately simple (by project, by time, text search) so other backends
// (Firestore, Postgres) stay easy to add.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

var (
	// ErrNotFound is returned when a session, token or pairing code is absent.
	ErrNotFound = errors.New("store: not found")
	// ErrQuota is returned when an ingest would exceed a configured quota.
	ErrQuota = errors.New("store: quota exceeded")
)

// Limits are the ingest quotas enforced by PutSession for new sessions.
type Limits struct {
	MaxSessionsPerProject int
	MaxSessionsTotal      int
}

// SessionRecord is a stored payload plus when it arrived.
type SessionRecord struct {
	Payload    model.SessionPayload
	IngestedAt time.Time
}

// ProjectInfo describes what the server holds for one project.
type ProjectInfo struct {
	Project      string
	Sessions     int
	Sources      map[string]int
	LastActivity time.Time
}

// SessionQuery selects sessions, newest first by UpdatedAt.
type SessionQuery struct {
	Project string
	Source  string
	Since   time.Time
	Limit   int
	Offset  int
}

// WorkItemRecord is a de-duplicated work item for one project.
type WorkItemRecord struct {
	Project   string
	Item      model.WorkItem
	FirstSeen time.Time
	LastSeen  time.Time
	Sessions  []SessionKey
}

// SessionKey identifies a session.
type SessionKey struct{ Source, ID string }

// WorkItemQuery selects work items, newest ObservedAt first.
type WorkItemQuery struct {
	Project  string
	Kind     string
	Provider string
	Status   string
	Since    time.Time // filters on ObservedAt
	Limit    int
	Offset   int
}

// Token is a stored credential. Only the hash is kept.
type Token struct {
	ID         string
	Hash       string
	Scope      string
	Label      string
	CreatedAt  time.Time
	ClientID   string    // OAuth client that obtained this token, if any
	Family     string    // OAuth grant family (all tokens from one authorization), if any
	ExpiresAt  time.Time // zero = never
	RevokedAt  time.Time // zero = active
	LastUsedAt time.Time
}

// Active reports whether the token may be used at now.
func (t Token) Active(now time.Time) bool {
	if !t.RevokedAt.IsZero() && !now.Before(t.RevokedAt) {
		return false
	}
	if !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt) {
		return false
	}
	return true
}

// AuditEvent records that a token did something. It never holds payload content.
type AuditEvent struct {
	At      time.Time
	TokenID string
	Scope   string
	Action  string
	Project string
	IP      string
}

// Store is the work-data store plus the auth store.
type Store interface {
	PutSession(ctx context.Context, p *model.SessionPayload, now time.Time, lim Limits) error
	DeleteProject(ctx context.Context, project string) (int, error)
	// DeleteSession removes sessions with this id; an empty source matches any source.
	DeleteSession(ctx context.Context, source, id string) (int, error)
	ListProjects(ctx context.Context) ([]ProjectInfo, error)
	Sessions(ctx context.Context, q SessionQuery) ([]SessionRecord, error)
	GetSession(ctx context.Context, source, id string) (*SessionRecord, error)
	Search(ctx context.Context, query, project string, limit int) ([]SessionRecord, error)
	WorkItems(ctx context.Context, q WorkItemQuery) ([]WorkItemRecord, error)
	Close() error
	AuthStore
	OAuthStore
}

// AuthStore holds tokens, pairing codes and the audit log.
type AuthStore interface {
	CreateToken(ctx context.Context, t Token) error
	TokenByHash(ctx context.Context, hash string) (*Token, error)
	ListTokens(ctx context.Context) ([]Token, error)
	RevokeToken(ctx context.Context, id string, at time.Time) error
	SetTokenExpiry(ctx context.Context, id string, at time.Time) error
	TouchToken(ctx context.Context, id string, at time.Time) error
	CountActiveTokens(ctx context.Context, now time.Time) (int, error)

	CreatePairing(ctx context.Context, hash string, expires time.Time) error
	// ConsumePairing atomically burns a valid, unused, unexpired code.
	ConsumePairing(ctx context.Context, hash string, now time.Time) (bool, error)
	// PairingFailure counts a failed exchange against every live code and
	// burns those that reach maxFailures.
	PairingFailure(ctx context.Context, now time.Time, maxFailures int) error

	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, limit int) ([]AuditEvent, error)
	PruneAudit(ctx context.Context, before time.Time) (int, error)
}

var errDuplicate = errors.New("store: duplicate")

// ErrDuplicate reports a unique-constraint violation (token id/hash, pairing hash).
var ErrDuplicate = errDuplicate
