package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func ensureDefaultAgent(ctx context.Context, x execer, now time.Time) error {
	if _, err := x.ExecContext(ctx, `INSERT OR IGNORE INTO agents(id, name, created_at, max_days) VALUES(?,?,?,0)`, DefaultAgentID, DefaultAgentName, nano(now)); err != nil {
		return fmt.Errorf("sqlite store: default agent: %w", err)
	}
	return nil
}

// EnsureDefaultAgent implements AgentStore.
func (s *SQLite) EnsureDefaultAgent(ctx context.Context, now time.Time) (Agent, error) {
	if err := ensureDefaultAgent(ctx, s.db, now); err != nil {
		return Agent{}, err
	}
	a, err := s.Agent(ctx, DefaultAgentID)
	if err != nil {
		return Agent{}, err
	}
	return *a, nil
}

// CreateAgent implements AgentStore.
func (s *SQLite) CreateAgent(ctx context.Context, a Agent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents(id, name, created_at, max_days) VALUES(?,?,?,?)`, a.ID, a.Name, nano(a.CreatedAt), a.MaxDays)
	if isUnique(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("sqlite store: create agent: %w", err)
	}
	return nil
}

func scanAgent(r rowScanner) (Agent, error) {
	var a Agent
	var c int64
	if err := r.Scan(&a.ID, &a.Name, &c, &a.MaxDays); err != nil {
		return Agent{}, err
	}
	a.CreatedAt = fromNano(c)
	return a, nil
}

// Agent implements AgentStore.
func (s *SQLite) Agent(ctx context.Context, id string) (*Agent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT id, name, created_at, max_days FROM agents WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite store: agent: %w", err)
	}
	return &a, nil
}

// ListAgents implements AgentStore.
func (s *SQLite) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, max_days FROM agents ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: list agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite store: list agents: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAgent implements AgentStore.
func (s *SQLite) UpdateAgent(ctx context.Context, a Agent) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET name=?, max_days=? WHERE id=?`, a.Name, a.MaxDays, a.ID)
	if isUnique(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("sqlite store: update agent: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAgent implements AgentStore.
func (s *SQLite) DeleteAgent(ctx context.Context, id string, at time.Time) (int, error) {
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("sqlite store: delete agent: %w", err)
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return ErrNotFound
		}
		res, err = tx.ExecContext(ctx, `UPDATE tokens SET revoked_at=? WHERE agent_id=? AND (revoked_at=0 OR revoked_at>?)`, nano(at), id, nano(at))
		if err != nil {
			return fmt.Errorf("sqlite store: revoke agent tokens: %w", err)
		}
		c, _ := res.RowsAffected()
		n = int(c)
		for _, q := range []string{
			`DELETE FROM agent_grants WHERE agent_id=?`,
			`DELETE FROM oauth_codes WHERE client_id IN (SELECT id FROM oauth_clients WHERE agent_id=?)`,
			`DELETE FROM oauth_refresh WHERE client_id IN (SELECT id FROM oauth_clients WHERE agent_id=?)`,
			`DELETE FROM oauth_txns WHERE client_id IN (SELECT id FROM oauth_clients WHERE agent_id=?)`,
			`DELETE FROM oauth_clients WHERE agent_id=?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return fmt.Errorf("sqlite store: delete agent state: %w", err)
			}
		}
		return nil
	})
	return n, err
}

// Grant implements AgentStore.
func (s *SQLite) Grant(ctx context.Context, agentID, projectID string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO agent_grants(agent_id, project_id, created_at)
		SELECT a.id, p.id, ? FROM agents a, projects p WHERE a.id=? AND p.id=?`, nano(now), agentID, projectID)
	if err != nil {
		return fmt.Errorf("sqlite store: grant: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	// nothing inserted: already granted, or the agent or project is unknown
	var one int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM agent_grants WHERE agent_id=? AND project_id=?`, agentID, projectID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("sqlite store: grant: %w", err)
	}
	return nil
}

// RevokeGrant implements AgentStore.
func (s *SQLite) RevokeGrant(ctx context.Context, agentID, projectID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM agent_grants WHERE agent_id=? AND project_id=?`, agentID, projectID); err != nil {
		return fmt.Errorf("sqlite store: revoke grant: %w", err)
	}
	return nil
}

// Grants implements AgentStore.
func (s *SQLite) Grants(ctx context.Context, agentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id FROM agent_grants WHERE agent_id=? ORDER BY project_id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: grants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlite store: grants: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetOAuthClientAgent implements AgentStore.
func (s *SQLite) SetOAuthClientAgent(ctx context.Context, clientID, agentID string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE oauth_clients SET agent_id=? WHERE id=? AND EXISTS (SELECT 1 FROM agents WHERE id=?)`, agentID, clientID, agentID)
		if err != nil {
			return fmt.Errorf("sqlite store: set client agent: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tokens SET agent_id=? WHERE client_id=?`, agentID, clientID); err != nil {
			return fmt.Errorf("sqlite store: set client agent: %w", err)
		}
		return nil
	})
}
