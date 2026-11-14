package cli

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/client"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// adminClient talks to the server with the admin secret the UI passes in for
// this call. The secret is not stored.
func (b *tuiBackend) adminClient(admin string) (*client.Client, error) {
	cfg, err := b.a.load()
	if err != nil {
		return nil, err
	}
	if cfg.Server.URL == "" {
		return nil, errors.New("no server configured: run `goldengate init` first")
	}
	if admin == "" {
		return nil, errors.New("the admin secret is required")
	}
	return client.New(cfg.Server.URL, admin, b.a.HTTP, b.a.userAgent())
}

// Agents implements tui.Backend.
func (b *tuiBackend) Agents(ctx context.Context, admin string) (tui.AgentsInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ac, err := b.adminClient(admin)
	if err != nil {
		return tui.AgentsInfo{}, err
	}
	as, err := ac.ListAgents(ctx)
	if err != nil {
		return tui.AgentsInfo{}, agentsErr(err)
	}
	toks, err := ac.ListTokens(ctx)
	if err != nil {
		return tui.AgentsInfo{}, agentsErr(err)
	}
	var info tui.AgentsInfo
	seen := map[string]bool{}
	// The projects the server holds come with the laptop's own token; a
	// project known only from a grant is listed too.
	if cfg, err := b.a.load(); err == nil {
		if ic, err := b.a.ingestClient(cfg); err == nil {
			if ps, err := ic.ListProjects(ctx); err == nil {
				for _, p := range ps {
					id := p.ID
					if id == "" {
						id = p.Project
					}
					if !seen[id] {
						seen[id] = true
						info.Projects = append(info.Projects, tui.AgentProject{ID: id, Name: p.Project, Sessions: p.Sessions})
					}
				}
			}
		}
	}
	for _, a := range as {
		ai := tui.AgentInfo{ID: a.ID, Name: a.Name, AllProjects: a.AllProjects, MaxDays: a.MaxDays, Grants: map[string]bool{}}
		for _, g := range a.Grants {
			ai.Grants[g.ID] = true
			if !seen[g.ID] {
				seen[g.ID] = true
				info.Projects = append(info.Projects, tui.AgentProject{ID: g.ID, Name: g.Name})
			}
		}
		for _, c := range a.Clients {
			ai.Clients = append(ai.Clients, c.Name)
		}
		for _, t := range toks {
			if t.AgentID != a.ID || !t.Active {
				continue
			}
			var used time.Time
			if t.LastUsedAt != nil {
				used = *t.LastUsedAt
			}
			if used.After(ai.LastUsed) {
				ai.LastUsed = used
			}
			ai.Tokens = append(ai.Tokens, tui.AgentToken{ID: t.ID, Label: t.Label, Created: t.CreatedAt, LastUsed: used})
		}
		info.Agents = append(info.Agents, ai)
	}
	sort.SliceStable(info.Projects, func(i, j int) bool { return info.Projects[i].Name < info.Projects[j].Name })
	return info, nil
}

// ApplyGrants implements tui.Backend.
func (b *tuiBackend) ApplyGrants(ctx context.Context, admin string, changes []tui.GrantChange) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ac, err := b.adminClient(admin)
	if err != nil {
		return err
	}
	for _, c := range changes {
		switch {
		case c.All:
			on := c.Grant
			_, err = ac.UpdateAgent(ctx, c.AgentID, client.AgentUpdate{AllProjects: &on})
		case c.Grant:
			_, err = ac.Grant(ctx, c.AgentID, c.ProjectID)
		default:
			err = ac.RevokeGrant(ctx, c.AgentID, c.ProjectID)
		}
		if err != nil {
			return agentsErr(err)
		}
	}
	return nil
}

// RevokeAgentToken implements tui.Backend.
func (b *tuiBackend) RevokeAgentToken(ctx context.Context, admin, tokenID string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ac, err := b.adminClient(admin)
	if err != nil {
		return err
	}
	return agentsErr(ac.RevokeToken(ctx, tokenID, 0))
}

// AgentView implements tui.Backend. The admin secret cannot read work data
// (by design), so the view is built here: the agent's grants from the
// server, applied to what this computer has synced. That is exactly what the
// server holds for sessions synced from here.
func (b *tuiBackend) AgentView(ctx context.Context, admin, agentID string) (tui.AgentView, error) {
	info, err := b.Agents(ctx, admin)
	if err != nil {
		return tui.AgentView{}, err
	}
	var v tui.AgentView
	found := false
	for _, a := range info.Agents {
		if a.ID == agentID {
			v.Agent, found = a, true
		}
	}
	if !found {
		return tui.AgentView{}, errors.New("that agent no longer exists")
	}
	names := map[string]string{}
	for _, p := range info.Projects {
		if v.Agent.AllProjects || v.Agent.Grants[p.ID] {
			names[p.ID] = p.Name
			v.Projects = append(v.Projects, p)
		}
	}
	v.Note = "From what this computer synced; sessions from another computer are not listed."
	st, err := b.a.openState()
	if err != nil {
		return v, err
	}
	defer func() { _ = st.Close() }()
	payloads, err := st.Payloads(ctx)
	if err != nil {
		return v, err
	}
	now := b.a.Now()
	days := tui.AgentViewDays
	if v.Agent.MaxDays > 0 && v.Agent.MaxDays < days {
		days = v.Agent.MaxDays
	}
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	work := map[string]tui.AgentViewWork{}
	var order []string
	for _, raw := range payloads {
		var p model.SessionPayload
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		name, ok := names[p.ProjectRef()]
		if !ok {
			continue
		}
		if !p.UpdatedAt.Before(since) {
			s := tui.AgentViewSession{Project: name, Source: p.Source, Title: p.TitleOnly, Updated: p.UpdatedAt}
			if p.Summary != nil {
				s.Title, s.Abstract = p.Summary.Title, p.Summary.AbstractText()
			}
			v.Sessions = append(v.Sessions, s)
		}
		for _, w := range p.WorkItems {
			if w.ObservedAt.Before(since) {
				continue
			}
			k := p.ProjectRef() + "\x00" + w.Kind + "\x00" + w.Key
			if _, dup := work[k]; !dup {
				order = append(order, k)
			}
			if cur, dup := work[k]; !dup || model.IsEvidenceBacked(w.Status) || !model.IsEvidenceBacked(cur.Status) {
				work[k] = tui.AgentViewWork{Project: name, Kind: w.Kind, Key: w.Key, Status: w.Status}
			}
		}
	}
	sort.SliceStable(v.Sessions, func(i, j int) bool { return v.Sessions[i].Updated.After(v.Sessions[j].Updated) })
	sort.Strings(order)
	for _, k := range order {
		v.Work = append(v.Work, work[k])
	}
	return v, nil
}
