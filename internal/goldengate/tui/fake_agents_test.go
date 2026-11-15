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

func (f *fakeBackend) Inbox(context.Context) (Inbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inbox, nil
}

// InboxDecide records the decision and moves the item between the lists the
// way the real backend's next listing would show it.
func (f *fakeBackend) InboxDecide(_ context.Context, items []InboxItem, decision string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inboxErr != nil {
		return f.inboxErr
	}
	for _, it := range items {
		f.inboxDecs = append(f.inboxDecs, decision+":"+it.ID)
		drop := func(l []InboxItem) []InboxItem {
			var keep []InboxItem
			for _, x := range l {
				if x.ID != it.ID {
					keep = append(keep, x)
				}
			}
			return keep
		}
		in := &f.inbox
		in.Pending, in.Excluded, in.Sent, in.Retracted = drop(in.Pending), drop(in.Excluded), drop(in.Sent), drop(in.Retracted)
		switch decision {
		case InboxApprove:
			it.State = "queued"
			in.Sent = append(in.Sent, it)
		case InboxExclude:
			it.State = "excluded"
			in.Excluded = append(in.Excluded, it)
		case InboxRetract:
			it.State = "retracted"
			in.Retracted = append(in.Retracted, it)
		case InboxReset:
			it.State = "pending"
			in.Pending = append(in.Pending, it)
		}
	}
	return nil
}

// sampleInbox is an inbox with something in every section.
func sampleInbox() Inbox {
	return Inbox{
		Pending: []InboxItem{
			{Source: "claude-code", ID: "s-pend-1", Project: "Pharmfindr", Title: "Fix OAuth redirect mismatch", Updated: ago(2 * time.Hour), State: "pending", Hash: "h1"},
			{Source: "codex", ID: "s-pend-2", Project: "Forge", Title: "Add gRPC health checks; key [REDACTED:aws-access-key]", Updated: ago(5 * time.Hour), State: "changed", Changed: true, Hash: "h2"},
		},
		Excluded: []InboxItem{{Source: "claude-code", ID: "s-excl-1", Project: "Pharmfindr", Title: "Salary spreadsheet cleanup", Updated: ago(30 * time.Hour), State: "excluded", Hash: "h3"}},
		Sent: []InboxItem{
			{Source: "claude-code", ID: "s-sent-1", Project: "Website", Title: "Refactor token store", Updated: ago(3 * time.Hour), State: "sent", Hash: "h4"},
			{Source: "opencode", ID: "s-queued-1", Project: "Website", Title: "Tidy the footer", Updated: ago(4 * time.Hour), State: "queued", Hash: "h5"},
		},
		Retracted: []InboxItem{{Source: "claude-code", ID: "s-retr-1", Project: "Forge", Title: "Customer data migration", Updated: ago(50 * time.Hour), State: "retracted", Hash: "h6"}},
	}
}
