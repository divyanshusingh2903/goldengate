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
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/OWNER/goldengate/internal/server/auth"
	"github.com/OWNER/goldengate/internal/server/store"
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
	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "request body must be a JSON object")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 5 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "provide 1 to 5 redirect_uris")
		return
	}
	for _, u := range req.RedirectURIs {
		if err := p.validRedirect(u); err != nil {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	method := req.AuthMethod
	if method == "" {
		method = "none"
	}
	if !slices.Contains([]string{"none", "client_secret_post", "client_secret_basic"}, method) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	grants := req.GrantTypes
	if len(grants) == 0 {
		grants = []string{"authorization_code", "refresh_token"}
	}
	for _, g := range grants {
		if g != "authorization_code" && g != "refresh_token" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant_type "+cleanName(g))
			return
		}
	}
	for _, rt := range req.ResponseType {
		if rt != "code" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only response_type code is supported")
			return
		}
	}
	if req.Scope != "" && strings.TrimSpace(req.Scope) != Scope {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only the read scope exists")
		return
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
	if sc := strings.TrimSpace(q.Get("scope")); sc != "" && sc != Scope {
		p.redirectWithError(w, r, redirect, state, "invalid_scope", "only the read scope exists")
		return
	}
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
	p.audit(r.Context(), r, "oauth_approve", c.ID)
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
body{font:16px/1.5 system-ui,sans-serif;max-width:34rem;margin:3rem auto;padding:0 1rem;color:#1b1b1f;background:#fafafa}
@media (prefers-color-scheme:dark){body{color:#e6e6ea;background:#16161a}.box{background:#202026;border-color:#3a3a44}input[type=password]{background:#16161a;color:inherit;border-color:#555}}
.box{border:1px solid #d6d6dc;border-radius:10px;padding:1rem 1.25rem;background:#fff;margin:1rem 0}
code{word-break:break-all}.err{color:#b00020;font-weight:600}
input[type=password]{width:100%;padding:.6rem;font-size:1rem;border:1px solid #999;border-radius:6px;box-sizing:border-box}
button{font-size:1rem;padding:.6rem 1.1rem;border-radius:6px;border:1px solid #888;cursor:pointer}
button.go{background:#1a5fb4;color:#fff;border-color:#1a5fb4}
</style></head><body>{{end}}
{{define "consent"}}{{template "page-head"}}
<h1>Connect an application?</h1>
<div class="box"><p><strong>{{.ClientName}}</strong> wants read-only access to the summaries of the projects you share through <strong>{{.Server}}</strong>.</p>
<p>After approval it will be sent to:<br><code>{{.RedirectURI}}</code></p>
<p>Only approve if you just started this connection yourself, from your assistant, and you recognize <strong>{{.RedirectHost}}</strong>.</p></div>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/oauth/authorize">
<input type="hidden" name="tx" value="{{.Tx}}">
<p><label for="secret">Admin secret (proves you own this server)</label></p>
<p><input id="secret" type="password" name="secret" autocomplete="current-password" autofocus></p>
<p><button class="go" type="submit" name="decision" value="approve">Approve read-only access</button>
<button type="submit" name="decision" value="deny" formnovalidate>Deny</button></p>
</form>
<p><small>You can revoke this later with <code>goldengatebridge oauth clients delete</code>. It can never write or delete anything.</small></p>
</body></html>{{end}}
{{define "error"}}{{template "page-head"}}<h1>Cannot continue</h1><div class="box"><p>{{.Message}}</p></div></body></html>{{end}}`
