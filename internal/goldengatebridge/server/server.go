// Package server wires the bridge's HTTP surface: the ingest API, token
// administration, pairing, and the stateless MCP endpoint. It has no browser
// surface: no CORS, no cookies, no HTML.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/auth"
	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/bridgemcp"
	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/oauth"
	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/store"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// Config configures the server.
type Config struct {
	Store   store.Store
	Auth    *auth.Authenticator
	Log     *slog.Logger
	Now     func() time.Time
	Version string

	Limits       store.Limits
	MaxBodyBytes int64 // ingest/admin request bodies (default 2 MiB)
	MaxBatch     int   // payloads per ingest request (default 50)

	BehindProxy bool // a TLS-terminating proxy is in front; X-Forwarded-For is trusted
	ProxyHops   int  // proxies between the client and us (default 1)

	AuditRetention time.Duration // default 90 days

	// PublicURL, when set, enables the built-in OAuth 2.1 authorization server
	// (needs the admin secret). It is the externally visible https base URL.
	PublicURL          string
	OAuthRedirectHosts []string      // optional allow-list of https redirect hosts
	OAuthAccessTTL     time.Duration // default 1h
}

// Server is the bridge HTTP handler plus background maintenance.
type Server struct {
	Config
	ipLimit, tokLimit, pairLimit *auth.Limiter
	mux                          *http.ServeMux
	oauth                        *oauth.Provider
}

// New builds a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil || cfg.Auth == nil {
		return nil, errors.New("server: store and auth are required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 2 << 20
	}
	if cfg.MaxBatch <= 0 {
		cfg.MaxBatch = 50
	}
	if cfg.ProxyHops <= 0 {
		cfg.ProxyHops = 1
	}
	if cfg.AuditRetention <= 0 {
		cfg.AuditRetention = 90 * 24 * time.Hour
	}
	s := &Server{Config: cfg, mux: http.NewServeMux(),
		ipLimit:   auth.NewLimiter(30, 60, cfg.Now),
		tokLimit:  auth.NewLimiter(10, 40, cfg.Now),
		pairLimit: auth.NewLimiter(0.2, 5, cfg.Now)}
	if cfg.PublicURL != "" {
		p, err := oauth.New(oauth.Config{
			Store: cfg.Store, Auth: cfg.Auth, Issuer: cfg.PublicURL, AllowedRedirectHosts: cfg.OAuthRedirectHosts, AccessTTL: cfg.OAuthAccessTTL,
			Now: cfg.Now, ClientIP: s.clientIP, Allow: s.oauthAllow, Log: cfg.Log,
			Audit: func(ctx context.Context, e store.AuditEvent) {
				if err := cfg.Store.AppendAudit(context.WithoutCancel(ctx), e); err != nil {
					cfg.Log.Error("audit write failed", "err", err)
				}
			},
		})
		if err != nil {
			return nil, err
		}
		s.oauth = p
	}
	s.routes()
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	defer func() {
		if rec := recover(); rec != nil {
			s.Log.Error("panic in handler", "panic", fmt.Sprint(rec)) // never the request, which may carry a bearer token
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
	}()
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	s.mux.Handle("POST /v1/ingest", s.guard(auth.ScopeIngest, s.handleIngest))
	s.mux.Handle("GET /v1/projects", s.guard(auth.ScopeIngest, s.handleListProjects))
	s.mux.Handle("DELETE /v1/projects/{key}", s.guard(auth.ScopeIngest, s.handleDeleteProject))
	s.mux.Handle("DELETE /v1/sessions/{id}", s.guard(auth.ScopeIngest, s.handleDeleteSession))

	s.mux.Handle("POST /v1/admin/tokens", s.guard(auth.ScopeAdmin, s.handleMintToken))
	s.mux.Handle("GET /v1/admin/tokens", s.guard(auth.ScopeAdmin, s.handleListTokens))
	s.mux.Handle("DELETE /v1/admin/tokens/{id}", s.guard(auth.ScopeAdmin, s.handleRevokeToken))

	s.mux.HandleFunc("POST /v1/pair", s.handlePair)
	if s.oauth != nil {
		s.oauth.Register(s.mux)
	}
	s.mux.Handle("/mcp", s.mcpHandler())
	s.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { writeErr(w, http.StatusNotFound, "not found") })
}

// --- request plumbing ---

type reqInfo struct {
	P  auth.Principal
	IP string
}

func (s *Server) clientIP(r *http.Request) string {
	if s.BehindProxy {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if i := len(parts) - s.ProxyHops; i >= 0 && len(parts) > 0 {
			if ip := strings.TrimSpace(parts[i]); ip != "" && len(ip) <= 64 {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type handler func(w http.ResponseWriter, r *http.Request, ri reqInfo)

// guard authenticates, enforces the scope, rate limits and audits denials.
func (s *Server) guard(scope string, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		if !s.ipLimit.Allow(ip) {
			tooMany(w)
			return
		}
		p, err := s.Auth.Authenticate(r.Context(), r.Header.Get("Authorization"), ip)
		switch {
		case errors.Is(err, auth.ErrTooMany):
			tooMany(w)
			return
		case errors.Is(err, auth.ErrUnauthorized):
			w.Header().Set("WWW-Authenticate", `Bearer realm="goldengatebridge"`)
			writeErr(w, http.StatusUnauthorized, "missing or invalid token")
			return
		case err != nil:
			s.Log.Error("authentication failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if p.Scope != scope {
			s.audit(r.Context(), p, ip, "denied:"+r.Method+" "+r.URL.Path, "")
			writeErr(w, http.StatusForbidden, "this token's scope cannot use this endpoint")
			return
		}
		if !s.tokLimit.Allow(p.TokenID) {
			tooMany(w)
			return
		}
		h(w, r, reqInfo{P: p, IP: ip})
	})
}

func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
}

func (s *Server) audit(ctx context.Context, p auth.Principal, ip, action, project string) {
	e := store.AuditEvent{At: s.Now(), TokenID: p.TokenID, Scope: p.Scope, Action: action, Project: project, IP: ip}
	if err := s.Store.AppendAudit(context.WithoutCancel(ctx), e); err != nil {
		s.Log.Error("audit write failed", "err", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSON reads a strict JSON body: right content type, size-limited, no
// unknown fields, no trailing data.
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request larger than %d bytes", s.MaxBodyBytes))
			return false
		}
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+shortErr(err))
		return false
	}
	if dec.More() {
		writeErr(w, http.StatusBadRequest, "invalid JSON: unexpected data after the request object")
		return false
	}
	return true
}

// shortErr keeps decoder messages (field names, offsets) but bounds their size.
func shortErr(err error) string {
	m := err.Error()
	if len(m) > 200 {
		m = m[:200]
	}
	return m
}

// --- ingest API ---

type ingestRequest struct {
	Sessions []model.SessionPayload `json:"sessions"`
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	var req ingestRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if len(req.Sessions) == 0 || len(req.Sessions) > s.MaxBatch {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("sessions must contain 1 to %d payloads", s.MaxBatch))
		return
	}
	now := s.Now()
	for i := range req.Sessions { // validate everything before storing anything
		if err := req.Sessions[i].Validate(now); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("sessions[%d]: %v", i, err))
			return
		}
	}
	projects := map[string]bool{}
	for i := range req.Sessions {
		p := &req.Sessions[i]
		if err := s.Store.PutSession(r.Context(), p, now, s.Limits); err != nil {
			if errors.Is(err, store.ErrQuota) {
				writeErr(w, http.StatusRequestEntityTooLarge, "storage quota exceeded; delete old projects or raise the limits")
				return
			}
			s.Log.Error("store failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		projects[p.Project] = true
	}
	for p := range projects {
		s.audit(r.Context(), ri.P, ri.IP, "ingest", p)
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": len(req.Sessions)})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	ps, err := s.Store.ListProjects(r.Context())
	if err != nil {
		s.Log.Error("store failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	type pj struct {
		Project      string         `json:"project"`
		Sessions     int            `json:"sessions"`
		Sources      map[string]int `json:"sources"`
		LastActivity time.Time      `json:"last_activity"`
	}
	out := make([]pj, 0, len(ps))
	for _, p := range ps {
		out = append(out, pj{p.Project, p.Sessions, p.Sources, p.LastActivity})
	}
	s.audit(r.Context(), ri.P, ri.IP, "list_projects", "")
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	key := r.PathValue("key")
	if err := model.CheckText("project", key, model.MaxProjectLen); err != nil || key == "" {
		writeErr(w, http.StatusBadRequest, "invalid project key")
		return
	}
	n, err := s.Store.DeleteProject(r.Context(), key)
	if err != nil {
		s.Log.Error("store failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r.Context(), ri.P, ri.IP, "delete_project", key)
	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	id, source := r.PathValue("id"), r.URL.Query().Get("source")
	if !model.ValidSessionID(id) || (source != "" && !model.ValidSource(source)) {
		writeErr(w, http.StatusBadRequest, "invalid session id or source")
		return
	}
	n, err := s.Store.DeleteSession(r.Context(), source, id)
	if err != nil {
		s.Log.Error("store failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r.Context(), ri.P, ri.IP, "delete_session", "")
	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

// --- token administration ---

type mintRequest struct {
	Scope     string `json:"scope"`
	Label     string `json:"label"`
	ExpiresIn string `json:"expires_in"` // Go duration, e.g. "720h"; empty = never
}

func (s *Server) handleMintToken(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	var req mintRequest
	if !s.decodeJSON(w, r, &req) {
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
	secret, tok, err := s.Auth.Mint(r.Context(), req.Scope, req.Label, exp)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r.Context(), ri.P, ri.IP, "mint_token:"+tok.Scope, "")
	writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "id": tok.ID, "scope": tok.Scope, "label": tok.Label, "expires_at": nullTime(tok.ExpiresAt)})
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	toks, err := s.Store.ListTokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	now := s.Now()
	type tv struct {
		ID         string     `json:"id"`
		Scope      string     `json:"scope"`
		Label      string     `json:"label"`
		CreatedAt  time.Time  `json:"created_at"`
		ExpiresAt  *time.Time `json:"expires_at"`
		RevokedAt  *time.Time `json:"revoked_at"`
		LastUsedAt *time.Time `json:"last_used_at"`
		Active     bool       `json:"active"`
	}
	out := make([]tv, 0, len(toks))
	for _, t := range toks {
		out = append(out, tv{t.ID, t.Scope, t.Label, t.CreatedAt, nullTime(t.ExpiresAt), nullTime(t.RevokedAt), nullTime(t.LastUsedAt), t.Active(now)})
	}
	s.audit(r.Context(), ri.P, ri.IP, "list_tokens", "")
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request, ri reqInfo) {
	var grace time.Duration
	if g := r.URL.Query().Get("grace"); g != "" {
		d, err := time.ParseDuration(g)
		if err != nil || d < 0 || d > 24*time.Hour {
			writeErr(w, http.StatusBadRequest, "grace must be a duration up to 24h")
			return
		}
		grace = d
	}
	if err := s.Auth.Revoke(r.Context(), r.PathValue("id"), grace); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no such token")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r.Context(), ri.P, ri.IP, "revoke_token", "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// --- pairing ---

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.pairLimit.Allow(ip) {
		tooMany(w)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if len(req.Code) > 32 {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	secret, tok, err := s.Auth.ExchangePairing(r.Context(), req.Code, ip)
	switch {
	case errors.Is(err, auth.ErrTooMany):
		tooMany(w)
		return
	case errors.Is(err, auth.ErrUnauthorized):
		writeErr(w, http.StatusUnauthorized, "invalid, expired or already used pairing code")
		return
	case err != nil:
		s.Log.Error("pairing failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r.Context(), auth.Principal{TokenID: tok.ID, Scope: tok.Scope}, ip, "pair", "")
	writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "id": tok.ID, "scope": tok.Scope})
}

// --- MCP ---

func (s *Server) mcpHandler() http.Handler {
	m := bridgemcp.New(bridgemcp.Deps{
		Store: s.Store, Now: s.Now, Ver: s.Version,
		Audit: func(ctx context.Context, e store.AuditEvent) {
			if err := s.Store.AppendAudit(context.WithoutCancel(ctx), e); err != nil {
				s.Log.Error("audit write failed", "err", err)
			}
		},
	})
	// Stateless + plain JSON: no server-held sessions and no long-lived SSE, so
	// it runs on scale-to-zero platforms and behind rewriting proxies.
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return m }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 256 << 10,
	})
	verifier := func(ctx context.Context, _ string, req *http.Request) (*sdkauth.TokenInfo, error) {
		ip := s.clientIP(req)
		p, err := s.Auth.Authenticate(ctx, req.Header.Get("Authorization"), ip)
		if err != nil {
			if errors.Is(err, auth.ErrUnauthorized) {
				return nil, fmt.Errorf("%w: missing or invalid token", sdkauth.ErrInvalidToken)
			}
			return nil, err
		}
		return &sdkauth.TokenInfo{ // the SDK middleware compares Expiration with the real clock, so it must not use the injectable one
			Scopes: []string{p.Scope}, Expiration: time.Now().Add(5 * time.Minute), UserID: p.TokenID, Extra: map[string]any{"ip": ip}}, nil
	}
	opts := &sdkauth.RequireBearerTokenOptions{Scopes: []string{auth.ScopeRead}}
	if s.oauth != nil {
		opts.ResourceMetadataURL = s.oauth.ResourceMetadataURL() // 401s tell OAuth clients where to start
	}
	protected := sdkauth.RequireBearerToken(verifier, opts)(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		if !s.ipLimit.Allow(ip) || s.Auth.Blocked(ip) {
			tooMany(w)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

// oauthAllow applies rate limits to the OAuth endpoints: registration and
// consent attempts are expensive to abuse, so they get the strict bucket.
func (s *Server) oauthAllow(w http.ResponseWriter, r *http.Request, bucket string) bool {
	ip := s.clientIP(r)
	ok := false
	switch bucket {
	case "register", "consent":
		ok = s.pairLimit.Allow(bucket + "/" + ip)
	default:
		ok = s.ipLimit.Allow(ip)
	}
	if !ok {
		tooMany(w)
	}
	return ok
}

// --- maintenance ---

// Maintain prunes the audit log hourly until ctx ends.
func (s *Server) Maintain(ctx context.Context) {
	prune := func() {
		if n, err := s.Store.PruneAudit(ctx, s.Now().Add(-s.AuditRetention)); err != nil {
			s.Log.Error("audit prune failed", "err", err)
		} else if n > 0 {
			s.Log.Info("pruned audit log", "rows", n)
		}
		if s.oauth != nil {
			now := s.Now()
			if n, err := s.Store.PruneOAuth(ctx, now, now.Add(-7*24*time.Hour)); err != nil {
				s.Log.Error("oauth prune failed", "err", err)
			} else if n > 0 {
				s.Log.Info("pruned oauth state", "rows", n)
			}
		}
	}
	prune()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}

// CheckListen enforces TLS: a non-loopback bind needs certificates or an
// explicit statement that a proxy terminates TLS.
func CheckListen(addr string, haveTLS, behindProxy bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if haveTLS || behindProxy {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing to serve plain HTTP on %q: pass --tls-cert and --tls-key, or --behind-tls-proxy if a proxy terminates TLS", addr)
}
