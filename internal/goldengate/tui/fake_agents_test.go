package tui

import (
	"context"
	"errors"
	"time"
)

const fakeAdmin = "admin-ok"

// fakeAgents is the agents half of the test backend: three agents over four
// projects, changed in place by ApplyGrants.
type fakeAgents struct {
	info      AgentsInfo
	applied   [][]GrantChange
	revoked   []string
	secrets   []string // every admin secret a call was made with
	applyErr  error
	viewCalls int
}

func newFakeAgents() *fakeAgents {
	return &fakeAgents{info: AgentsInfo{
		Projects: []AgentProject{
			{ID: "p_forge", Name: "Forge", Sessions: 11},
			{ID: "p_pharm", Name: "Pharmfindr", Sessions: 44},
			{ID: "p_site", Name: "Website", Sessions: 6},
			{ID: "claude-ai:Chats", Name: "claude-ai:Chats", Sessions: 3},
		},
		Agents: []AgentInfo{
			{ID: "agt_default", Name: "Default", AllProjects: true, Grants: map[string]bool{},
				Tokens: []AgentToken{{ID: "tok_1a2b3c4d", Label: "assistant", Created: ago(30 * 24 * time.Hour), LastUsed: ago(2 * time.Hour)}}, LastUsed: ago(2 * time.Hour)},
			{ID: "agt_gpt", Name: "ChatGPT", Grants: map[string]bool{"p_pharm": true}, MaxDays: 30,
				Tokens: []AgentToken{{ID: "tok_aaaa1111", Label: "chatgpt connector", Created: ago(3 * 24 * time.Hour), LastUsed: ago(26 * time.Hour)},
					{ID: "tok_bbbb2222", Label: "", Created: ago(24 * time.Hour)}}, LastUsed: ago(26 * time.Hour)},
			{ID: "agt_claude", Name: "Claude desktop", Grants: map[string]bool{"p_forge": true, "p_site": true}, Clients: []string{"Claude"}},
		},
	}}
}

func (f *fakeBackend) Agents(_ context.Context, admin string) (AgentsInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ag.secrets = append(f.ag.secrets, admin)
	if admin != fakeAdmin {
		return AgentsInfo{}, errors.New("the server refused the admin token")
	}
	out := AgentsInfo{Projects: append([]AgentProject(nil), f.ag.info.Projects...)}
	for _, a := range f.ag.info.Agents {
		g := map[string]bool{}
		for k, v := range a.Grants {
			g[k] = v
		}
		a.Grants = g
		a.Tokens = append([]AgentToken(nil), a.Tokens...)
		out.Agents = append(out.Agents, a)
	}
	return out, nil
}

func (f *fakeBackend) ApplyGrants(_ context.Context, admin string, changes []GrantChange) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if admin != fakeAdmin {
		return errors.New("the server refused the admin token")
	}
	f.ag.applied = append(f.ag.applied, changes)
	if f.ag.applyErr != nil {
		return f.ag.applyErr
	}
	for _, c := range changes {
		for i := range f.ag.info.Agents {
			a := &f.ag.info.Agents[i]
			if a.ID != c.AgentID {
				continue
			}
			if c.All {
				a.AllProjects = c.Grant
			} else if c.Grant {
				a.Grants[c.ProjectID] = true
			} else {
				delete(a.Grants, c.ProjectID)
			}
		}
	}
	return nil
}

func (f *fakeBackend) AgentView(_ context.Context, admin, agentID string) (AgentView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ag.viewCalls++
	if admin != fakeAdmin {
		return AgentView{}, errors.New("the server refused the admin token")
	}
	for _, a := range f.ag.info.Agents {
		if a.ID != agentID {
			continue
		}
		v := AgentView{Agent: a, Note: "From what this computer synced; sessions from another computer are not listed."}
		for _, p := range f.ag.info.Projects {
			if a.AllProjects || a.Grants[p.ID] {
				v.Projects = append(v.Projects, p)
				v.Sessions = append(v.Sessions, AgentViewSession{Project: p.Name, Source: "claude-code", Title: "Work on " + p.Name, Abstract: "Fixed the " + p.Name + " build; key was [REDACTED:aws-access-key].", Updated: ago(3 * time.Hour)})
				v.Work = append(v.Work, AgentViewWork{Project: p.Name, Kind: "pull_request", Key: "example/" + p.ID + "#7", Status: "merged"})
			}
		}
		return v, nil
	}
	return AgentView{}, errors.New("that agent no longer exists")
}

func (f *fakeBackend) RevokeAgentToken(_ context.Context, admin, tokenID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if admin != fakeAdmin {
		return errors.New("the server refused the admin token")
	}
	f.ag.revoked = append(f.ag.revoked, tokenID)
	for i := range f.ag.info.Agents {
		a := &f.ag.info.Agents[i]
		var keep []AgentToken
		for _, t := range a.Tokens {
			if t.ID != tokenID {
				keep = append(keep, t)
			}
		}
		a.Tokens = keep
	}
	return nil
}
