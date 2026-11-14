package store

import (
	"context"
	"time"
)

// Agent is a named reader of the MCP endpoint ("ChatGPT", "Claude"). Every
// read token and OAuth client belongs to exactly one agent, and an agent sees
// only the projects it has been granted.
type Agent struct {
	ID        string
	Name      string
	CreatedAt time.Time
	// MaxDays bounds how far back the agent may look; 0 means no limit of
	// its own.
	MaxDays int
}

// The default agent owns read tokens and OAuth clients that were created
// without naming one: those that existed before agents did, and those minted
// by flows that do not ask yet.
const (
	DefaultAgentID   = "agt_default"
	DefaultAgentName = "Default"
)

// AgentStore holds agents and the projects each may see. Nothing is granted
// automatically: a new project is visible to no agent until it is granted.
type AgentStore interface {
	// CreateAgent adds an agent. ErrDuplicate when the id or the name
	// (ignoring case) is taken.
	CreateAgent(ctx context.Context, a Agent) error
	// Agent returns one agent by id, or ErrNotFound.
	Agent(ctx context.Context, id string) (*Agent, error)
	// ListAgents lists agents, oldest first.
	ListAgents(ctx context.Context) ([]Agent, error)
	// UpdateAgent sets an agent's name and MaxDays. ErrNotFound, or
	// ErrDuplicate when another agent has the name.
	UpdateAgent(ctx context.Context, a Agent) error
	// DeleteAgent removes an agent with its grants, revokes its tokens at
	// 'at' and deletes its OAuth clients. It returns how many tokens it
	// revoked.
	DeleteAgent(ctx context.Context, id string, at time.Time) (int, error)
	// EnsureDefaultAgent returns the default agent, creating it (with no
	// grants) when it does not exist.
	EnsureDefaultAgent(ctx context.Context, now time.Time) (Agent, error)

	// Grant lets an agent see a project. ErrNotFound when either is unknown.
	// Granting twice is not an error.
	Grant(ctx context.Context, agentID, projectID string, now time.Time) error
	// RevokeGrant takes a project away from an agent; revoking what was not
	// granted is not an error.
	RevokeGrant(ctx context.Context, agentID, projectID string) error
	// Grants lists the ids of the projects an agent may see, sorted.
	Grants(ctx context.Context, agentID string) ([]string, error)

	// SetOAuthClientAgent moves an OAuth client, and the tokens it holds, to
	// an agent. ErrNotFound when either is unknown.
	SetOAuthClientAgent(ctx context.Context, clientID, agentID string) error
}
