// Package client talks to goldengatebridge. It never follows redirects (a
// redirect could carry the bearer token somewhere unintended) and never
// includes a token in an error message.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OWNER/goldengate/internal/config"
	"github.com/OWNER/goldengate/internal/model"
)

// BatchSize is how many payloads go in one ingest request.
const BatchSize = 25

// APIError is a non-2xx response from the server.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("server returned %d: %s", e.Status, e.Message) }

// Client is a bridge client for one server and token.
type Client struct {
	base  string
	token string
	hc    *http.Client
	ua    string
}

// New validates the server URL (https, or http only for loopback).
func New(serverURL, token string, hc *http.Client, userAgent string) (*Client, error) {
	if err := config.CheckServerURL(serverURL); err != nil {
		return nil, err
	}
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("client: invalid server URL")
	}
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(serverURL, "/"), token: token, hc: &c, ua: userAgent}, nil
}

// MCPURL is the endpoint to paste into an assistant's connector.
func (c *Client) MCPURL() string { return c.base + "/mcp" }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("client: encode: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("client: %s %s: %w", method, path, scrubURLErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("client: read response: %w", err)
	}
	if resp.StatusCode/100 == 3 {
		return &APIError{Status: resp.StatusCode, Message: "unexpected redirect (refused to follow it)"}
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("client: decode response: %w", err)
		}
	}
	return nil
}

// scrubURLErr drops the *url.Error wrapper's URL (which could embed credentials).
func scrubURLErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// Health checks GET /healthz.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil)
}

// Send implements sync.Sink: it uploads payloads in batches.
func (c *Client) Send(ctx context.Context, ps []*model.SessionPayload) error {
	for start := 0; start < len(ps); start += BatchSize {
		end := min(start+BatchSize, len(ps))
		batch := make([]model.SessionPayload, 0, end-start)
		for _, p := range ps[start:end] {
			batch = append(batch, *p)
		}
		if err := c.do(ctx, http.MethodPost, "/v1/ingest", map[string]any{"sessions": batch}, nil); err != nil {
			return err
		}
	}
	return nil
}

// DeleteProject removes a project's data from the server.
func (c *Client) DeleteProject(ctx context.Context, project string) (int, error) {
	var out struct {
		Deleted int `json:"deleted"`
	}
	err := c.do(ctx, http.MethodDelete, "/v1/projects/"+url.PathEscape(project), nil, &out)
	return out.Deleted, err
}

// RemoteProject is what the server holds for a project.
type RemoteProject struct {
	Project      string         `json:"project"`
	Sessions     int            `json:"sessions"`
	Sources      map[string]int `json:"sources"`
	LastActivity time.Time      `json:"last_activity"`
}

// ListProjects lists what the server holds.
func (c *Client) ListProjects(ctx context.Context) ([]RemoteProject, error) {
	var out struct {
		Projects []RemoteProject `json:"projects"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/projects", nil, &out)
	return out.Projects, err
}

// Minted is a freshly created token; Secret is shown once.
type Minted struct {
	Secret string
	ID     string
	Scope  string
}

// MintToken creates a token (admin token required).
func (c *Client) MintToken(ctx context.Context, scope, label, expiresIn string) (Minted, error) {
	var out struct {
		Token string `json:"token"`
		ID    string `json:"id"`
		Scope string `json:"scope"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/admin/tokens", map[string]string{"scope": scope, "label": label, "expires_in": expiresIn}, &out)
	return Minted{Secret: out.Token, ID: out.ID, Scope: out.Scope}, err
}

// TokenInfo describes a stored token (no secret).
type TokenInfo struct {
	ID        string     `json:"id"`
	Scope     string     `json:"scope"`
	Label     string     `json:"label"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
	Active    bool       `json:"active"`
}

// ListTokens lists tokens (admin token required).
func (c *Client) ListTokens(ctx context.Context) ([]TokenInfo, error) {
	var out struct {
		Tokens []TokenInfo `json:"tokens"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/admin/tokens", nil, &out)
	return out.Tokens, err
}

// RevokeToken revokes a token now, or after grace (admin token required).
func (c *Client) RevokeToken(ctx context.Context, id string, grace time.Duration) error {
	path := "/v1/admin/tokens/" + url.PathEscape(id)
	if grace > 0 {
		path += "?grace=" + url.QueryEscape(grace.String())
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// Pair exchanges a pairing code for an ingest token. No token is needed.
func Pair(ctx context.Context, serverURL, code string, hc *http.Client, userAgent string) (string, error) {
	c, err := New(serverURL, "", hc, userAgent)
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/pair", map[string]string{"code": code}, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// OAuthEnabled reports whether the server runs its built-in OAuth server
// (it then publishes authorization server metadata).
func (c *Client) OAuthEnabled(ctx context.Context) bool {
	return c.do(ctx, http.MethodGet, "/.well-known/oauth-authorization-server", nil, nil) == nil
}
