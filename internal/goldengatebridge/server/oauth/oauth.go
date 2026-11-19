// Package oauth is a minimal single-user OAuth 2.1 authorization server built
// into the bridge, so assistants whose connector only speaks OAuth (Gemini's
// custom apps, for example) can connect without a pasted token.
//
// It follows the MCP authorization spec: RFC 9728 protected-resource metadata,
// RFC 8414 server metadata, RFC 7591 dynamic client registration, PKCE (S256
// only), RFC 8707 resource indicators, exact redirect-URI matching, single-use
// authorization codes with replay detection, and rotating refresh tokens.
//
// There is exactly one resource owner: whoever holds the admin secret. The
// owner approves each client on a consent page by entering that secret.
// Issued access tokens are ordinary short-lived read tokens, so every
// existing check (scope, expiry, revocation, audit) applies to them unchanged.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/auth"
	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/store"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// Scope is the only scope this server issues.
const Scope = "read"

// Config configures a Provider.
type Config struct {
	Store  store.Store
	Auth   *auth.Authenticator
	Issuer string // public base URL of the server, e.g. https://bridge.example.com
	// AllowedRedirectHosts restricts https redirect URIs to these hosts.
	// Empty means any https host. Loopback http redirects are always allowed.
	AllowedRedirectHosts []string
	Now                  func() time.Time
	ClientIP             func(*http.Request) string
	// Allow applies the server's rate limits; it must write the 429 itself and return false.
	Allow func(w http.ResponseWriter, r *http.Request, bucket string) bool
	Audit func(ctx context.Context, e store.AuditEvent)
	Log   *slog.Logger

	AccessTTL  time.Duration // default 1h
	RefreshTTL time.Duration // default 30d
	MaxClients int           // default 200
}

// Provider serves the OAuth endpoints.
type Provider struct {
	Config
	issuer *url.URL
	tmpl   *template.Template
}

// New validates the configuration.
func New(cfg Config) (*Provider, error) {
	if cfg.Store == nil || cfg.Auth == nil {
		return nil, errors.New("oauth: store and auth are required")
	}
	if !cfg.Auth.HasAdmin() {
		return nil, errors.New("oauth: requires GOLDENGATE_ADMIN_TOKEN, because the owner approves clients with the admin secret")
	}
	u, err := url.Parse(strings.TrimRight(cfg.Issuer, "/"))
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("oauth: invalid public URL %q (want https://host[:port])", cfg.Issuer)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("oauth: public URL must be https (plain http only for loopback): %q", cfg.Issuer)
	}
	cfg.Issuer = u.Scheme + "://" + u.Host
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ClientIP == nil {
		cfg.ClientIP = func(r *http.Request) string { h, _, _ := net.SplitHostPort(r.RemoteAddr); return h }
	}
	if cfg.Allow == nil {
		cfg.Allow = func(http.ResponseWriter, *http.Request, string) bool { return true }
	}
	if cfg.Audit == nil {
		cfg.Audit = func(context.Context, store.AuditEvent) {}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.AccessTTL <= 0 {
		cfg.AccessTTL = time.Hour
	}
	if cfg.RefreshTTL <= 0 {
		cfg.RefreshTTL = 30 * 24 * time.Hour
	}
	if cfg.MaxClients <= 0 {
		cfg.MaxClients = 200
	}
	return &Provider{Config: cfg, issuer: u, tmpl: template.Must(template.New("consent").Parse(consentHTML))}, nil
}

// Resource is the canonical identifier of the MCP endpoint.
func (p *Provider) Resource() string { return p.Issuer + "/mcp" }

// ResourceMetadataURL is advertised in WWW-Authenticate challenges.
func (p *Provider) ResourceMetadataURL() string {
	return p.Issuer + "/.well-known/oauth-protected-resource"
}

// Register mounts the endpoints.
func (p *Provider) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", p.handleResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", p.handleResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", p.handleServerMetadata)
	mux.HandleFunc("OPTIONS /.well-known/", p.handlePreflight)
	mux.HandleFunc("POST /oauth/register", p.handleRegister)
	mux.HandleFunc("GET /oauth/authorize", p.handleAuthorize)
	mux.HandleFunc("POST /oauth/authorize", p.handleConsent)
	mux.HandleFunc("POST /oauth/token", p.handleToken)
	mux.HandleFunc("POST /oauth/revoke", p.handleRevoke)
}

// --- helpers ---

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func hashOf(kind, v string) string { return auth.HashToken(kind + ":" + v) }

func (p *Provider) audit(ctx context.Context, r *http.Request, action, clientID string) {
	p.Audit(ctx, store.AuditEvent{At: p.Now(), TokenID: clientID, Scope: "oauth", Action: action, IP: p.ClientIP(r)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type oauthError struct {
	Code string `json:"error"`
	Desc string `json:"error_description,omitempty"`
}

func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, oauthError{Code: code, Desc: desc})
}

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200B || r == 0xFEFF {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return s
}

// --- discovery ---

func publicCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*") // public metadata only
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, MCP-Protocol-Version")
}

func (p *Provider) handlePreflight(w http.ResponseWriter, _ *http.Request) {
	publicCORS(w)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Provider) handleResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	publicCORS(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource": p.Resource(), "authorization_servers": []string{p.Issuer},
		"scopes_supported": []string{Scope}, "bearer_methods_supported": []string{"header"},
		"resource_name": "Golden Gate bridge",
	})
}

func (p *Provider) handleServerMetadata(w http.ResponseWriter, _ *http.Request) {
	publicCORS(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer": p.Issuer, "authorization_endpoint": p.Issuer + "/oauth/authorize", "token_endpoint": p.Issuer + "/oauth/token",
		"registration_endpoint": p.Issuer + "/oauth/register", "revocation_endpoint": p.Issuer + "/oauth/revoke",
		"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{Scope}, "authorization_response_iss_parameter_supported": true,
	})
}

// --- dynamic client registration (RFC 7591) ---

type registerRequest struct {
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name"`
	AuthMethod   string   `json:"token_endpoint_auth_method"`
	GrantTypes   []string `json:"grant_types"`
	ResponseType []string `json:"response_types"`
	Scope        string   `json:"scope"`
}

// maxRedirectURIs bounds one registration; Google registers a set of its own (sandbox and
// production) for a connector. Each URI is still validated on its own.
const maxRedirectURIs = 20

func (p *Provider) validRedirect(raw string) error {
	if len(raw) > 500 {
		return errors.New("redirect URI too long")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Fragment != "" || u.User != nil {
		return errors.New("redirect URI must be an absolute URL without fragment or credentials")
	}
	switch {
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
		return nil
	case u.Scheme == "https":
		if len(p.AllowedRedirectHosts) > 0 && !slices.Contains(p.AllowedRedirectHosts, strings.ToLower(u.Hostname())) {
			return fmt.Errorf("redirect host %q is not allowed on this server", u.Hostname())
		}
		return nil
	}
	return errors.New("redirect URI must be https (or http on loopback)")
}

func (p *Provider) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !p.Allow(w, r, "register") {
		return
	}
	// The body is client metadata (names and URLs, no secrets); a truncated copy goes in the log
	// of a rejection so a client that registers in an unexpected way can be diagnosed.
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	reject := func(code, desc string) {
		body := cleanName(string(raw))
		if len(body) > 1000 {
			body = body[:1000] + "..."
		}
		p.Log.Warn("oauth register rejected", "code", code, "reason", desc, "body", body, "content_type", r.Header.Get("Content-Type"))
		writeOAuthError(w, http.StatusBadRequest, code, desc)
	}
	var req registerRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		reject("invalid_client_metadata", "request body must be a JSON object")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > maxRedirectURIs {
		reject("invalid_redirect_uri", fmt.Sprintf("provide 1 to %d redirect_uris", maxRedirectURIs))
		return
	}
	for _, u := range req.RedirectURIs {
		if err := p.validRedirect(u); err != nil {
			reject("invalid_redirect_uri", err.Error())
			return
		}
	}
	method := req.AuthMethod
	if method == "" {
		method = "none"
	}
	if !slices.Contains([]string{"none", "client_secret_post", "client_secret_basic"}, method) {
		reject("invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	// Grant types, response types and scope are advertised, not negotiated: the server only ever
	// offers the code flow and the read scope, so a client asking for more is registered for what
	// is supported rather than turned away (assistants such as Gemini send extras). Unsupported
	// values are dropped, and the response says what was registered.
	var grants []string
	for _, g := range req.GrantTypes {
		if (g == "authorization_code" || g == "refresh_token") && !slices.Contains(grants, g) {
			grants = append(grants, g)
		}
	}
	if len(grants) == 0 {
		grants = []string{"authorization_code", "refresh_token"}
	}
	if n, err := p.Store.CountOAuthClients(r.Context()); err != nil {
		p.Log.Error("oauth register failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	} else if n >= p.MaxClients {
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many registered clients; delete unused ones with `goldengatebridge oauth clients delete`")
		return
	}
	name := cleanName(req.ClientName)
	if name == "" {
		name = "(unnamed application)"
	}
	c := store.OAuthClient{ID: auth.RandomSecret("gg_", 18), Name: name, RedirectURIs: req.RedirectURIs, CreatedAt: p.Now().UTC()}
	resp := map[string]any{
		"client_id": c.ID, "client_id_issued_at": c.CreatedAt.Unix(), "client_name": c.Name, "redirect_uris": c.RedirectURIs,
		"grant_types": grants, "response_types": []string{"code"}, "token_endpoint_auth_method": method, "scope": Scope,
	}
	if method != "none" {
		secret := auth.RandomSecret("ggs_", 32)
		c.SecretHash = auth.HashToken(secret)
		resp["client_secret"] = secret
		resp["client_secret_expires_at"] = 0
	}
	if err := p.Store.CreateOAuthClient(r.Context(), c); err != nil {
		p.Log.Error("oauth register failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	p.audit(r.Context(), r, "oauth_register", c.ID)
	writeJSON(w, http.StatusCreated, resp)
}

// --- authorization endpoint ---

// redirectWithError sends the user agent back to the (already validated) redirect URI.
func (p *Provider) redirectWithError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, desc string) {
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	q.Set("error", code)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", p.Issuer)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound) //nolint:gosec // G710: redirectURI was matched exactly against the client's registered URIs
}

func secureHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	// No form-action: browsers apply it to the redirect after submit, which must reach the client.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
}

func (p *Provider) errorPage(w http.ResponseWriter, status int, msg string) {
	secureHeaders(w)
	w.WriteHeader(status)
	_ = p.tmpl.ExecuteTemplate(w, "error", map[string]string{"Message": msg})
}

func (p *Provider) canonicalResource(v string) (string, bool) {
	if v == "" {
		return p.Resource(), true
	}
	v = strings.TrimRight(v, "/")
	if v == p.Issuer || v == p.Resource() {
		return p.Resource(), true
	}
	return "", false
}

func (p *Provider) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if !p.Allow(w, r, "authorize") {
		return
	}
	q := r.URL.Query()
	c, err := p.Store.OAuthClient(r.Context(), q.Get("client_id"))
	if err != nil {
		p.errorPage(w, http.StatusBadRequest, "This sign-in link is invalid: the application is not registered with this server.")
		return
	}
	redirect := q.Get("redirect_uri")
	if redirect == "" && len(c.RedirectURIs) == 1 {
		redirect = c.RedirectURIs[0]
	}
	if !slices.Contains(c.RedirectURIs, redirect) { // exact match only; never redirect on a mismatch
		p.errorPage(w, http.StatusBadRequest, "This sign-in link is invalid: its redirect address does not match the application's registration.")
		return
	}
	state := q.Get("state")
	if len(state) > 512 {
		p.redirectWithError(w, r, redirect, "", "invalid_request", "state too long")
		return
	}
	if q.Get("response_type") != "code" {
		p.redirectWithError(w, r, redirect, state, "unsupported_response_type", "only response_type=code is supported")
		return
	}
	challenge := q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || !validChallenge(challenge) {
		p.redirectWithError(w, r, redirect, state, "invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	// A requested scope is not negotiated: the code only ever carries the read scope (RFC 6749 3.3
	// lets a server grant less than asked), so extra scopes such as openid are ignored.
	res, ok := p.canonicalResource(q.Get("resource"))
	if !ok {
		p.redirectWithError(w, r, redirect, state, "invalid_target", "resource must identify this server's MCP endpoint")
		return
	}
	id := auth.RandomSecret("", 24)
	txn := store.OAuthTxn{ClientID: c.ID, RedirectURI: redirect, State: state, Challenge: challenge, Resource: res, Scope: Scope}
	if err := p.Store.PutOAuthTxn(r.Context(), hashOf("txn", id), txn, p.Now().Add(10*time.Minute)); err != nil {
		p.Log.Error("oauth authorize failed", "err", err)
		p.errorPage(w, http.StatusInternalServerError, "Something went wrong. Try again.")
		return
	}
	p.renderConsent(w, http.StatusOK, c, redirect, id, "")
}

func validChallenge(s string) bool {
	if len(s) != 43 { // base64url(SHA-256) without padding
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}

func (p *Provider) renderConsent(w http.ResponseWriter, status int, c *store.OAuthClient, redirect, txn, errMsg string) {
	host := redirect
	if u, err := url.Parse(redirect); err == nil {
		host = u.Host
	}
	secureHeaders(w)
	w.WriteHeader(status)
	_ = p.tmpl.ExecuteTemplate(w, "consent", map[string]string{
		"ClientName": c.Name, "RedirectURI": redirect, "RedirectHost": host, "Tx": txn, "Error": errMsg, "Server": p.issuer.Host,
	})
}

// --- choosing the agent and its projects (second consent step) ---
//
// The first page asks only for the admin secret and shows nothing about the
// server's contents, because anyone can open it. Agents and project names
// are listed on the second page, which exists only for a request the owner
// has just approved with the secret.

type accessAgent struct {
	ID, Name, Sees string
}

type accessProject struct {
	ID, Name string
	Sessions int
}

const maxAgentName = 60

// renderAccess shows the second step for an approved request.
func (p *Provider) renderAccess(w http.ResponseWriter, r *http.Request, status int, c *store.OAuthClient, txID, errMsg string) {
	ctx := r.Context()
	agents, err := p.Store.ListAgents(ctx)
	if err != nil {
		p.Log.Error("oauth consent failed", "err", err)
		p.errorPage(w, http.StatusInternalServerError, "Something went wrong. Try again.")
		return
	}
	projects, err := p.Store.ListProjects(ctx)
	if err != nil {
		p.Log.Error("oauth consent failed", "err", err)
		p.errorPage(w, http.StatusInternalServerError, "Something went wrong. Try again.")
		return
	}
	names := map[string]string{}
	var ps []accessProject
	for _, pr := range projects {
		names[pr.ID] = pr.Project
		ps = append(ps, accessProject{ID: pr.ID, Name: pr.Project, Sessions: pr.Sessions})
	}
	sort.Slice(ps, func(i, j int) bool { return strings.ToLower(ps[i].Name) < strings.ToLower(ps[j].Name) })
	var as []accessAgent
	suggested := c.Name
	for _, a := range agents {
		if strings.EqualFold(a.Name, suggested) {
			suggested = ""
		}
		sees := "nothing yet"
		if a.AllProjects {
			sees = "every project"
		} else if ids, err := p.Store.Grants(ctx, a.ID); err == nil && len(ids) > 0 {
			var ns []string
			for _, id := range ids {
				if n, ok := names[id]; ok {
					ns = append(ns, n)
				}
			}
			sort.Strings(ns)
			if len(ns) > 0 {
				sees = strings.Join(ns, ", ")
			}
		}
		as = append(as, accessAgent{ID: a.ID, Name: a.Name, Sees: sees})
	}
	secureHeaders(w)
	w.WriteHeader(status)
	_ = p.tmpl.ExecuteTemplate(w, "access", map[string]any{
		"ClientName": c.Name, "Tx": txID, "Error": errMsg, "Server": p.issuer.Host,
		"Agents": as, "Projects": ps, "NewName": suggested,
	})
}

func newAgentID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("oauth: system randomness unavailable: " + err.Error())
	}
	return "agt_" + hex.EncodeToString(b[:])
}

// chooseAccess applies the second step's form: it attaches the client to an
// existing or a new agent and grants that agent the ticked projects. Nothing
// is ticked by default, and ticks only ever add access. It returns the agent
// id, or a message for the user when the form cannot be accepted (nothing
// has been changed then).
func (p *Provider) chooseAccess(ctx context.Context, c *store.OAuthClient, form url.Values) (string, string, error) {
	var grant []string
	seen := map[string]bool{}
	for _, id := range form["project"] {
		if seen[id] {
			continue
		}
		seen[id] = true
		pr, err := p.Store.ResolveProject(ctx, id)
		if err != nil || pr.ID != id {
			return "", "One of the ticked projects no longer exists. Review the list and try again.", nil
		}
		grant = append(grant, id)
	}
	all := form.Get("all") == "1"
	agentID := form.Get("agent")
	switch agentID {
	case "":
		return "", "Choose which assistant this is: an existing one, or a new one with a name.", nil
	case "new":
		name := strings.TrimSpace(form.Get("agent_name"))
		if name == "" || model.CheckText("name", name, maxAgentName) != nil {
			return "", "Give the new assistant a name of 1 to 60 characters.", nil
		}
		a := store.Agent{ID: newAgentID(), Name: name, CreatedAt: p.Now().UTC(), AllProjects: all}
		if err := p.Store.CreateAgent(ctx, a); err != nil {
			if errors.Is(err, store.ErrDuplicate) {
				return "", "An assistant with that name already exists. Pick it from the list, or choose another name.", nil
			}
			return "", "", err
		}
		agentID = a.ID
	default:
		a, err := p.Store.Agent(ctx, agentID)
		if errors.Is(err, store.ErrNotFound) {
			return "", "That assistant no longer exists. Choose another, or create a new one.", nil
		}
		if err != nil {
			return "", "", err
		}
		if all && !a.AllProjects {
			a.AllProjects = true
			if err := p.Store.UpdateAgent(ctx, *a); err != nil {
				return "", "", err
			}
		}
	}
	for _, id := range grant {
		if err := p.Store.Grant(ctx, agentID, id, p.Now()); err != nil {
			return "", "", err
		}
	}
	if err := p.Store.SetOAuthClientAgent(ctx, c.ID, agentID); err != nil {
		return "", "", err
	}
	return agentID, "", nil
}

func (p *Provider) handleConsent(w http.ResponseWriter, r *http.Request) {
	if !p.Allow(w, r, "consent") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		p.errorPage(w, http.StatusBadRequest, "The form could not be read.")
		return
	}
	txID := r.PostForm.Get("tx")
	hash := hashOf("txn", txID)
	txn, err := p.Store.TakeOAuthTxn(r.Context(), hash, p.Now())
	if err != nil {
		p.errorPage(w, http.StatusBadRequest, "This sign-in request expired or was already used. Start the connection again from your assistant.")
		return
	}
	c, err := p.Store.OAuthClient(r.Context(), txn.ClientID)
	if err != nil {
		p.errorPage(w, http.StatusBadRequest, "This application is no longer registered.")
		return
	}
	if txn.Approved {
		// second step: the owner already proved the admin secret for this request
		if r.PostForm.Get("decision") != "grant" {
			p.audit(r.Context(), r, "oauth_deny", c.ID)
			p.redirectWithError(w, r, txn.RedirectURI, txn.State, "access_denied", "the owner declined")
			return
		}
		agentID, msg, err := p.chooseAccess(r.Context(), c, r.PostForm)
		if err != nil {
			p.Log.Error("oauth consent failed", "err", err)
			p.errorPage(w, http.StatusInternalServerError, "Something went wrong. Start the connection again from your assistant.")
			return
		}
		if msg != "" {
			_ = p.Store.PutOAuthTxn(r.Context(), hash, *txn, p.Now().Add(10*time.Minute)) // let them fix the form
			p.renderAccess(w, r, http.StatusBadRequest, c, txID, msg)
			return
		}
		p.issueCode(w, r, c, txn, agentID)
		return
	}
	if r.PostForm.Get("decision") != "approve" {
		p.audit(r.Context(), r, "oauth_deny", c.ID)
		p.redirectWithError(w, r, txn.RedirectURI, txn.State, "access_denied", "the owner declined")
		return
	}
	// Only the holder of the admin secret may approve. This reuses the
	// authenticator, so constant-time comparison and the failure lockout apply.
	pr, err := p.Auth.Authenticate(r.Context(), "Bearer "+r.PostForm.Get("secret"), p.ClientIP(r))
	switch {
	case errors.Is(err, auth.ErrTooMany):
		_ = p.Store.PutOAuthTxn(r.Context(), hash, *txn, p.Now().Add(10*time.Minute))
		p.errorPage(w, http.StatusTooManyRequests, "Too many failed attempts. Wait a few minutes and try again.")
		return
	case err != nil || pr.Scope != auth.ScopeAdmin:
		_ = p.Store.PutOAuthTxn(r.Context(), hash, *txn, p.Now().Add(10*time.Minute)) // let them retry a typo
		p.renderConsent(w, http.StatusUnauthorized, c, txn.RedirectURI, txID, "That is not the admin secret.")
		return
	}
	// The secret is right. Nothing is issued yet: the owner now chooses which
	// assistant this is and what it may see, on a page bound to a fresh
	// single-use id that only this response carries.
	next := auth.RandomSecret("", 24)
	approved := *txn
	approved.Approved = true
	if err := p.Store.PutOAuthTxn(r.Context(), hashOf("txn", next), approved, p.Now().Add(10*time.Minute)); err != nil {
		p.Log.Error("oauth consent failed", "err", err)
		p.errorPage(w, http.StatusInternalServerError, "Something went wrong. Try again.")
		return
	}
	p.renderAccess(w, r, http.StatusOK, c, next, "")
}

// issueCode finishes an approved request: it mints the authorization code
// and sends the browser back to the application.
func (p *Provider) issueCode(w http.ResponseWriter, r *http.Request, c *store.OAuthClient, txn *store.OAuthTxn, agentID string) {
	code := auth.RandomSecret("ggc_", 32)
	family := auth.RandomSecret("fam_", 12)
	oc := store.OAuthCode{ClientID: c.ID, RedirectURI: txn.RedirectURI, Challenge: txn.Challenge, Resource: txn.Resource, Scope: txn.Scope,
		Family: family, Expires: p.Now().Add(5 * time.Minute)}
	if err := p.Store.PutOAuthCode(r.Context(), hashOf("code", code), oc); err != nil {
		p.Log.Error("oauth consent failed", "err", err)
		p.errorPage(w, http.StatusInternalServerError, "Something went wrong. Try again.")
		return
	}
	_ = p.Store.TouchOAuthClient(r.Context(), c.ID, p.Now())
	p.Audit(r.Context(), store.AuditEvent{At: p.Now(), TokenID: c.ID, AgentID: agentID, Scope: "oauth", Action: "oauth_approve", IP: p.ClientIP(r)})
	u, _ := url.Parse(txn.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	if txn.State != "" {
		q.Set("state", txn.State)
	}
	q.Set("iss", p.Issuer)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound) //nolint:gosec // G710: txn.RedirectURI was matched exactly against the client's registered URIs
}

// --- token endpoint ---

// authenticateClient implements none, client_secret_post and client_secret_basic.
func (p *Provider) authenticateClient(r *http.Request) (*store.OAuthClient, *oauthError, int) {
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	} else if cid := r.PostForm.Get("client_id"); cid != "" && cid != id {
		return nil, &oauthError{"invalid_client", "client_id does not match the Authorization header"}, http.StatusUnauthorized
	}
	c, err := p.Store.OAuthClient(r.Context(), id)
	if err != nil {
		return nil, &oauthError{"invalid_client", "unknown client"}, http.StatusUnauthorized
	}
	if c.SecretHash != "" {
		if secret == "" || subtle.ConstantTimeCompare([]byte(auth.HashToken(secret)), []byte(c.SecretHash)) != 1 {
			return nil, &oauthError{"invalid_client", "bad client credentials"}, http.StatusUnauthorized
		}
	}
	return c, nil, 0
}

func (p *Provider) handleToken(w http.ResponseWriter, r *http.Request) {
	if !p.Allow(w, r, "token") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "form body required")
		return
	}
	c, oerr, status := p.authenticateClient(r)
	if oerr != nil {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Basic realm="goldengatebridge"`)
		}
		writeJSON(w, status, oerr)
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		p.grantCode(w, r, c)
	case "refresh_token":
		p.grantRefresh(w, r, c)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (p *Provider) grantCode(w http.ResponseWriter, r *http.Request, c *store.OAuthClient) {
	ctx := r.Context()
	code, replay, err := p.Store.UseOAuthCode(ctx, hashOf("code", r.PostForm.Get("code")), p.Now())
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown, expired or already used code")
		return
	}
	if replay {
		// A replayed code means it may have been stolen: kill everything issued from it.
		_ = p.Store.RevokeFamily(ctx, code.Family, p.Now())
		p.audit(ctx, r, "oauth_code_replay", c.ID)
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown, expired or already used code")
		return
	}
	ru := r.PostForm.Get("redirect_uri")
	if code.ClientID != c.ID || (ru != "" && ru != code.RedirectURI) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code was not issued to this client or redirect_uri")
		return
	}
	if ru == "" && len(c.RedirectURIs) != 1 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is required")
		return
	}
	if res := r.PostForm.Get("resource"); res != "" {
		if canon, ok := p.canonicalResource(res); !ok || canon != code.Resource {
			writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource does not match the authorization")
			return
		}
	}
	v := r.PostForm.Get("code_verifier")
	sum := sha256.Sum256([]byte(v))
	if len(v) < 43 || len(v) > 128 || subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(code.Challenge)) != 1 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	p.issue(w, r, c, code.Family, code.Resource)
}

func (p *Provider) grantRefresh(w http.ResponseWriter, r *http.Request, c *store.OAuthClient) {
	ctx := r.Context()
	rt, replay, err := p.Store.UseOAuthRefresh(ctx, hashOf("refresh", r.PostForm.Get("refresh_token")), p.Now())
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired refresh token")
		return
	}
	if replay {
		_ = p.Store.RevokeFamily(ctx, rt.Family, p.Now())
		p.audit(ctx, r, "oauth_refresh_replay", c.ID)
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired refresh token")
		return
	}
	if rt.ClientID != c.ID {
		_ = p.Store.RevokeFamily(ctx, rt.Family, p.Now())
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token was not issued to this client")
		return
	}
	if sc := strings.TrimSpace(r.PostForm.Get("scope")); sc != "" && sc != Scope {
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope", "only the read scope exists")
		return
	}
	p.issue(w, r, c, rt.Family, rt.Resource)
}

// issue mints an access token and a fresh refresh token for a grant family.
func (p *Provider) issue(w http.ResponseWriter, r *http.Request, c *store.OAuthClient, family, resource string) {
	ctx := r.Context()
	access, _, err := p.Auth.MintOAuthAccess(ctx, c.ID, family, "oauth: "+c.Name, p.AccessTTL)
	if err != nil {
		p.Log.Error("oauth issue failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	refresh := auth.RandomSecret("ggb_refresh_", 32)
	if err := p.Store.PutOAuthRefresh(ctx, store.OAuthRefresh{Hash: hashOf("refresh", refresh), ClientID: c.ID, Family: family,
		Resource: resource, Scope: Scope, Expires: p.Now().Add(p.RefreshTTL)}); err != nil {
		p.Log.Error("oauth issue failed", "err", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	_ = p.Store.TouchOAuthClient(ctx, c.ID, p.Now())
	p.audit(ctx, r, "oauth_token", c.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(p.AccessTTL.Seconds()), "refresh_token": refresh, "scope": Scope,
	})
}

// --- revocation (RFC 7009) ---

func (p *Provider) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if !p.Allow(w, r, "token") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "form body required")
		return
	}
	c, oerr, status := p.authenticateClient(r)
	if oerr != nil {
		writeJSON(w, status, oerr)
		return
	}
	tok := r.PostForm.Get("token")
	ctx := r.Context()
	if strings.HasPrefix(tok, "ggb_refresh_") {
		if rt, _, err := p.Store.UseOAuthRefresh(ctx, hashOf("refresh", tok), p.Now()); err == nil && rt.ClientID == c.ID {
			_ = p.Store.RevokeFamily(ctx, rt.Family, p.Now())
		}
	} else if t, err := p.Store.TokenByHash(ctx, auth.HashToken(tok)); err == nil && t.ClientID == c.ID {
		_ = p.Store.RevokeToken(ctx, t.ID, p.Now())
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK) // always 200: do not reveal whether the token existed
}

const consentHTML = `{{define "page-head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Golden Gate</title><style>
/* Brand tokens (docs/brand/BRAND.md). The page loads nothing, so the display and text faces apply only where installed. */
:root{color-scheme:light dark;--orange:#C0362C;--on-orange:#fff;--bg:#EEF0EE;--surface:#fff;--sunk:#E3E7E5;--line:#CBD2D0;--field:#7D8A90;--ink:#13202A;--dim:#56656D;--pacific:#1F5E80;--headland:#965F00;--alarm:#9B1D5A}
@media (prefers-color-scheme:dark){:root{--orange:#F0563F;--on-orange:#1B0703;--bg:#0A0B0D;--surface:#131518;--sunk:#1B1E22;--line:#26292E;--field:#646B73;--ink:#E9EBED;--dim:#8F969C;--pacific:#7FBADB;--headland:#E8B652;--alarm:#F27EB2}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:400 17px/1.6 "Josefin Sans",system-ui,sans-serif}
main{max-width:40rem;margin:0 auto;padding:2rem 1rem 4rem}
.brand{display:flex;align-items:center;gap:.6rem;font:400 1.3rem/1 "Federo","Josefin Sans",system-ui,sans-serif;letter-spacing:.02em;margin-bottom:2rem}
.brand svg{width:2.2rem;height:auto;color:var(--orange)}
h1{font:400 2rem/1.15 "Federo","Josefin Sans",system-ui,sans-serif;letter-spacing:.02em;margin:0 0 1.25rem;padding-bottom:.6rem;border-bottom:3px double var(--line)}
.box,fieldset{background:var(--surface);border:3px double var(--line);border-radius:0;padding:1rem 1.25rem;margin:1.25rem 0}
.box p,fieldset p{margin:.5rem 0}
legend{padding:0 .4rem;font-weight:600}
dl{margin:.75rem 0;display:grid;grid-template-columns:minmax(0,1fr);gap:.1rem}
dt{color:var(--dim)}dd{margin:0;min-width:0}
code{font:500 .86rem/1.4 "IBM Plex Mono",ui-monospace,Menlo,monospace;overflow-wrap:anywhere}
.warn{border-left:4px solid var(--headland);padding-left:.8rem;margin:1rem 0 .25rem}
.err{border-left:4px solid var(--alarm);padding-left:.8rem;font-weight:600}
label{font-weight:600}
input[type=password],input[type=text]{width:100%;padding:.65rem .75rem;font:inherit;color:var(--ink);background:var(--surface);border:1px solid var(--field);border-radius:0}
input[type=text]{width:auto;min-width:0;max-width:100%}
input[type=radio],input[type=checkbox]{accent-color:var(--pacific);width:1rem;height:1rem;vertical-align:-.15rem}
:focus-visible{outline:2px solid var(--pacific);outline-offset:2px}
small{color:var(--dim);font-size:.9rem}
.actions{display:flex;flex-wrap:wrap;gap:.75rem;margin-top:1.25rem}
button{font:600 1rem "Josefin Sans",system-ui,sans-serif;padding:.65rem 1.25rem;border-radius:0;border:1px solid var(--line);background:var(--surface);color:var(--ink);cursor:pointer}
button:hover{border-color:var(--field)}
button.go{background:var(--orange);border-color:var(--orange);color:var(--on-orange)}
.foot{margin-top:1.5rem;color:var(--dim)}
</style></head><body><main><div class="brand"><svg viewBox="0 0 64 48" fill="none" stroke="currentColor" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 4V44M44 4V44"/><path d="M2 34Q14 30 20 6Q32 46 44 6Q50 30 62 34"/></svg>Golden Gate</div>{{end}}
{{define "consent"}}{{template "page-head"}}
<h1>Connect an application?</h1>
<div class="box"><p><strong>{{.ClientName}}</strong> wants read-only access to the summaries of the projects you share through <strong>{{.Server}}</strong>.</p>
<dl><dt>After approval it will be sent to</dt><dd><code>{{.RedirectURI}}</code></dd></dl>
<p class="warn">Only approve if you just started this connection yourself, from your assistant, and you recognize <strong>{{.RedirectHost}}</strong>.</p></div>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/oauth/authorize">
<input type="hidden" name="tx" value="{{.Tx}}">
<p><label for="secret">Admin secret (proves you own this server)</label></p>
<p><input id="secret" type="password" name="secret" autocomplete="current-password" autofocus></p>
<div class="actions"><button class="go" type="submit" name="decision" value="approve">Approve read-only access</button>
<button type="submit" name="decision" value="deny" formnovalidate>Deny</button></div>
</form>
<p class="foot"><small>You can revoke this later with <code>goldengatebridge oauth clients delete</code>. It can never write or delete anything.</small></p>
</main></body></html>{{end}}
{{define "access"}}{{template "page-head"}}
<h1>What may it see?</h1>
<div class="box"><p><strong>{{.ClientName}}</strong> will get read-only access through <strong>{{.Server}}</strong>. Choose which assistant this is and which projects it may see. Nothing is ticked: it sees only what you tick here or grant later.</p></div>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/oauth/authorize">
<input type="hidden" name="tx" value="{{.Tx}}">
<fieldset><legend>Which assistant is this?</legend>
<p><label><input type="radio" name="agent" value="new" checked> A new assistant named</label>
<input type="text" name="agent_name" value="{{.NewName}}" maxlength="60" autocomplete="off"></p>
{{range .Agents}}<p><label><input type="radio" name="agent" value="{{.ID}}"> {{.Name}}</label> <small>already sees: {{.Sees}}</small></p>
{{end}}</fieldset>
<fieldset><legend>Projects it may see</legend>
{{range .Projects}}<p><label><input type="checkbox" name="project" value="{{.ID}}"> {{.Name}}</label> <small>{{.Sessions}} session(s)</small></p>
{{else}}<p><small>The server holds no projects yet. You can grant projects later with <code>goldengate agents grant</code>.</small></p>
{{end}}<p><label><input type="checkbox" name="all" value="1"> Every project, including ones I share later</label></p>
</fieldset>
<p><small>For an assistant that already exists, ticks add to what it sees; they never take anything away.</small></p>
<div class="actions"><button class="go" type="submit" name="decision" value="grant">Connect with this access</button>
<button type="submit" name="decision" value="deny" formnovalidate>Deny</button></div>
</form>
<p class="foot"><small>Change this later with <code>goldengate agents</code>. It can never write or delete anything.</small></p>
</main></body></html>{{end}}
{{define "error"}}{{template "page-head"}}<h1>Cannot continue</h1><div class="box"><p>{{.Message}}</p></div></main></body></html>{{end}}`
