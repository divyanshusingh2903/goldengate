package store

import (
	"context"
	"sort"
	"strings"
	"time"
)

func (m *Memory) ensureDefault(now time.Time) {
	if _, ok := m.agents[DefaultAgentID]; !ok {
		m.agents[DefaultAgentID] = &Agent{ID: DefaultAgentID, Name: DefaultAgentName, CreatedAt: now.UTC()}
	}
}

// EnsureDefaultAgent implements AgentStore.
func (m *Memory) EnsureDefaultAgent(_ context.Context, now time.Time) (Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDefault(now)
	return *m.agents[DefaultAgentID], nil
}

func (m *Memory) nameTaken(name, exceptID string) bool {
	for _, a := range m.agents {
		if a.ID != exceptID && strings.EqualFold(a.Name, name) {
			return true
		}
	}
	return false
}

// CreateAgent implements AgentStore.
func (m *Memory) CreateAgent(_ context.Context, a Agent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.agents[a.ID]; ok || m.nameTaken(a.Name, "") {
		return errDuplicate
	}
	a.CreatedAt = a.CreatedAt.UTC()
	m.agents[a.ID] = &a
	return nil
}

// Agent implements AgentStore.
func (m *Memory) Agent(_ context.Context, id string) (*Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *a
	return &cp, nil
}

// ListAgents implements AgentStore.
func (m *Memory) ListAgents(context.Context) ([]Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Agent
	for _, a := range m.agents {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// UpdateAgent implements AgentStore.
func (m *Memory) UpdateAgent(_ context.Context, a Agent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.agents[a.ID]
	if !ok {
		return ErrNotFound
	}
	if m.nameTaken(a.Name, a.ID) {
		return errDuplicate
	}
	cur.Name, cur.MaxDays = a.Name, a.MaxDays
	return nil
}

// DeleteAgent implements AgentStore.
func (m *Memory) DeleteAgent(_ context.Context, id string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.agents[id]; !ok {
		return 0, ErrNotFound
	}
	delete(m.agents, id)
	delete(m.grants, id)
	n := 0
	for _, t := range m.tokens {
		if t.AgentID == id && (t.RevokedAt.IsZero() || at.Before(t.RevokedAt)) {
			t.RevokedAt = at.UTC()
			n++
		}
	}
	for cid, c := range m.oclients {
		if c.AgentID != id {
			continue
		}
		delete(m.oclients, cid)
		for h, x := range m.ocodes {
			if x.c.ClientID == cid {
				delete(m.ocodes, h)
			}
		}
		for h, x := range m.orefresh {
			if x.r.ClientID == cid {
				delete(m.orefresh, h)
			}
		}
		for h, x := range m.otxns {
			if x.t.ClientID == cid {
				delete(m.otxns, h)
			}
		}
	}
	return n, nil
}

// Grant implements AgentStore.
func (m *Memory) Grant(_ context.Context, agentID, projectID string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.agents[agentID]; !ok {
		return ErrNotFound
	}
	if _, ok := m.projects[projectID]; !ok {
		return ErrNotFound
	}
	m.grant(agentID, projectID)
	return nil
}

func (m *Memory) grant(agentID, projectID string) {
	if m.grants[agentID] == nil {
		m.grants[agentID] = map[string]bool{}
	}
	m.grants[agentID][projectID] = true
}

// RevokeGrant implements AgentStore.
func (m *Memory) RevokeGrant(_ context.Context, agentID, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.grants[agentID], projectID)
	return nil
}

// Grants implements AgentStore.
func (m *Memory) Grants(_ context.Context, agentID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for id := range m.grants[agentID] {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// SetOAuthClientAgent implements AgentStore.
func (m *Memory) SetOAuthClientAgent(_ context.Context, clientID, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.oclients[clientID]
	if _, known := m.agents[agentID]; !ok || !known {
		return ErrNotFound
	}
	c.AgentID = agentID
	for _, t := range m.tokens {
		if t.ClientID == clientID {
			t.AgentID = agentID
		}
	}
	return nil
}
