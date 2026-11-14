package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/store"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// The agents API manages who may read what. It is admin only: the laptop's
// ingest token cannot change what an assistant sees, so a leaked ingest token
// cannot widen a reader's view (see SECURITY.md).

const maxAgentName = 60

type grantView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type agentView struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	CreatedAt   time.Time   `json:"created_at"`
	MaxDays     int         `json:"max_days"`
	AllProjects bool        `json:"all_projects"`
	Grants      []grantView `json:"grants"`
	Tokens      int         `json:"active_tokens"`
	Clients     []grantView `json:"oauth_clients"`
}

func (s *Server) agentView(ctx context.Context, a store.Agent, toks []store.Token, clients []store.OAuthClient) (agentView, error) {
	v := agentView{ID: a.ID, Name: a.Name, CreatedAt: a.CreatedAt, MaxDays: a.MaxDays, AllProjects: a.AllProjects, Grants: []grantView{}, Clients: []grantView{}}
	ids, err := s.Store.Grants(ctx, a.ID)
	if err != nil {
		return v, err
	}
	for _, id := range ids {
		g := grantView{ID: id, Name: id}
		if p, err := s.Store.ResolveProject(ctx, id); err == nil && p.ID == id {
			g.Name = p.Name
		}
		v.Grants = append(v.Grants, g)
	}
	now := s.Now()
	for _, t := range toks {
		if t.AgentID == a.ID && t.ClientID == "" && t.Active(now) {
			v.Tokens++
		}
	}
	for _, c := range clients {
		if c.AgentID == a.ID {
			v.Clients = append(v.Clients, grantView{ID: c.ID, Name: c.Name})
		}
	}
	return v, nil
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.Log.Error("store failed", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	ctx := r.Context()
	as, err := s.Store.ListAgents(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	toks, err := s.Store.ListTokens(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	clients, err := s.Store.ListOAuthClients(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	out := make([]agentView, 0, len(as))
	for _, a := range as {
		v, err := s.agentView(ctx, a, toks, clients)
		if err != nil {
			s.internal(w, err)
			return
		}
		out = append(out, v)
	}
	s.audit(ctx, ri.P, ri.IP, "list_agents", "")
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func checkAgentName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	return name, name != "" && model.CheckText("name", name, maxAgentName) == nil
}

func newAgentID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("server: system randomness unavailable: " + err.Error())
	}
	return "agt_" + hex.EncodeToString(b[:])
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	var req struct {
		Name        string `json:"name"`
		MaxDays     int    `json:"max_days"`
		AllProjects bool   `json:"all_projects"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	name, ok := checkAgentName(req.Name)
	if !ok || req.MaxDays < 0 || req.MaxDays > 3650 {
		writeErr(w, http.StatusBadRequest, "an agent needs a name of 1 to 60 characters, and max_days between 0 and 3650")
		return
	}
	a := store.Agent{ID: newAgentID(), Name: name, CreatedAt: s.Now().UTC(), MaxDays: req.MaxDays, AllProjects: req.AllProjects}
	if err := s.Store.CreateAgent(r.Context(), a); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeErr(w, http.StatusConflict, "an agent with that name already exists")
			return
		}
		s.internal(w, err)
		return
	}
	s.auditAgent(r.Context(), ri, "create_agent", a.ID, "")
	v, _ := s.agentView(r.Context(), a, nil, nil)
	writeJSON(w, http.StatusCreated, v)
}

// auditAgent records an admin action on an agent: the agent it concerns goes
// in the event's agent field, the project (if any) in its project field.
func (s *Server) auditAgent(ctx context.Context, ri reqInfo, action, agentID, project string) {
	p := ri.P
	p.AgentID = agentID
	s.audit(ctx, p, ri.IP, action, project)
}

// agentFor loads the agent named in the path, writing a 404 when it is unknown.
func (s *Server) agentFor(w http.ResponseWriter, r *http.Request) (*store.Agent, bool) {
	a, err := s.Store.Agent(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no such agent")
		return nil, false
	}
	if err != nil {
		s.internal(w, err)
		return nil, false
	}
	return a, true
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	var req struct {
		Name        *string `json:"name"`
		MaxDays     *int    `json:"max_days"`
		AllProjects *bool   `json:"all_projects"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	a, ok := s.agentFor(w, r)
	if !ok {
		return
	}
	if req.Name != nil {
		name, ok := checkAgentName(*req.Name)
		if !ok {
			writeErr(w, http.StatusBadRequest, "an agent needs a name of 1 to 60 characters")
			return
		}
		a.Name = name
	}
	if req.MaxDays != nil {
		if *req.MaxDays < 0 || *req.MaxDays > 3650 {
			writeErr(w, http.StatusBadRequest, "max_days must be between 0 and 3650")
			return
		}
		a.MaxDays = *req.MaxDays
	}
	if req.AllProjects != nil {
		a.AllProjects = *req.AllProjects
	}
	if err := s.Store.UpdateAgent(r.Context(), *a); err != nil {
		switch {
		case errors.Is(err, store.ErrDuplicate):
			writeErr(w, http.StatusConflict, "an agent with that name already exists")
		case errors.Is(err, store.ErrNotFound):
			writeErr(w, http.StatusNotFound, "no such agent")
		default:
			s.internal(w, err)
		}
		return
	}
	action := "update_agent"
	if req.AllProjects != nil {
		action = "update_agent:all_projects=" + map[bool]string{true: "on", false: "off"}[*req.AllProjects]
	}
	s.auditAgent(r.Context(), ri, action, a.ID, "")
	toks, _ := s.Store.ListTokens(r.Context())
	clients, _ := s.Store.ListOAuthClients(r.Context())
	v, _ := s.agentView(r.Context(), *a, toks, clients)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	id := r.PathValue("id")
	n, err := s.Store.DeleteAgent(r.Context(), id, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no such agent")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.auditAgent(r.Context(), ri, "delete_agent", id, "")
	writeJSON(w, http.StatusOK, map[string]int{"revoked_tokens": n})
}

func (s *Server) handleGrant(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	var req struct {
		Project string `json:"project"` // a project id, name, or the key of a folder in it
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	a, ok := s.agentFor(w, r)
	if !ok {
		return
	}
	if model.CheckText("project", req.Project, model.MaxProjectLen) != nil || req.Project == "" {
		writeErr(w, http.StatusBadRequest, "invalid project")
		return
	}
	p, err := s.Store.ResolveProject(r.Context(), req.Project)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "the server holds no such project yet: sync it first, then grant it")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	if err := s.Store.Grant(r.Context(), a.ID, p.ID, s.Now()); err != nil {
		s.internal(w, err)
		return
	}
	s.auditAgent(r.Context(), ri, "grant", a.ID, p.ID)
	writeJSON(w, http.StatusOK, grantView{ID: p.ID, Name: p.Name})
}

func (s *Server) handleRevokeGrant(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	a, ok := s.agentFor(w, r)
	if !ok {
		return
	}
	ref := r.PathValue("project")
	if model.CheckText("project", ref, model.MaxProjectLen) != nil || ref == "" {
		writeErr(w, http.StatusBadRequest, "invalid project")
		return
	}
	id := ref
	if p, err := s.Store.ResolveProject(r.Context(), ref); err == nil {
		id = p.ID
	}
	if err := s.Store.RevokeGrant(r.Context(), a.ID, id); err != nil {
		s.internal(w, err)
		return
	}
	s.auditAgent(r.Context(), ri, "revoke_grant", a.ID, id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) handleMintAgentToken(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	var req struct {
		Label     string `json:"label"`
		ExpiresIn string `json:"expires_in"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	a, ok := s.agentFor(w, r)
	if !ok {
		return
	}
	var exp time.Duration
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil || d <= 0 || d > 10*365*24*time.Hour {
			writeErr(w, http.StatusBadRequest, "invalid expires_in")
			return
		}
		exp = d
	}
	if req.Label == "" {
		req.Label = a.Name
	}
	secret, tok, err := s.Auth.MintRead(r.Context(), a.ID, req.Label, exp)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auditAgent(r.Context(), ri, "mint_token:read", a.ID, "")
	writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "id": tok.ID, "scope": tok.Scope, "label": tok.Label, "agent_id": a.ID, "expires_at": nullTime(tok.ExpiresAt)})
}
