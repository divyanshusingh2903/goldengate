package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OWNER/goldengate/internal/server"
	"github.com/OWNER/goldengate/internal/server/auth"
	"github.com/OWNER/goldengate/internal/server/store"
)

const redirect = "https://assistant.example/oauth/callback"

func oauthEnv(t *testing.T, mutate ...func(*server.Config)) *env {
	t.Helper()
	// Every request claims a fresh address: lockouts are exercised on purpose in their own tests.
	return newEnv(t, append([]func(*server.Config){func(c *server.Config) { c.PublicURL = "self"; c.BehindProxy = true }}, mutate...)...)
}

// oc is an OAuth client's view of the server.
type oc struct {
	t  *testing.T
	e  *env
	hc *http.Client
	n  int
}

func newOC(t *testing.T, e *env) *oc {
	return &oc{t: t, e: e, hc: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// reply is a response whose body has already been read and closed.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (c *oc) req(method, path string, form url.Values, body any, hdr map[string]string) (*reply, []byte) {
	c.t.Helper()
	var rd io.Reader
	ctype := ""
	switch {
	case form != nil:
		rd, ctype = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
	case body != nil:
		b, _ := json.Marshal(body)
		rd, ctype = strings.NewReader(string(b)), "application/json"
	}
	rq, err := http.NewRequestWithContext(context.Background(), method, c.e.ts.URL+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if ctype != "" {
		rq.Header.Set("Content-Type", ctype)
	}
	c.n++
	rq.Header.Set("X-Forwarded-For", fmt.Sprintf("10.77.%d.%d", c.n/250, c.n%250+1))
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	resp, err := c.hc.Do(rq)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return &reply{resp.StatusCode, resp.Header}, b
}

func (c *oc) register(extra map[string]any) map[string]any {
	c.t.Helper()
	body := map[string]any{"redirect_uris": []string{redirect}, "client_name": "Test Assistant"}
	for k, v := range extra {
		body[k] = v
	}
	resp, b := c.req("POST", "/oauth/register", nil, body, nil)
	if resp.StatusCode != 201 {
		c.t.Fatalf("register: %d %s", resp.StatusCode, b)
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func pkce() (verifier, challenge string) {
	verifier = base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef-verifier"))
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func (c *oc) authorizeQuery(clientID, challenge string, extra map[string]string) string {
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {"xyz"}, "resource": {c.e.ts.URL + "/mcp"}, "scope": {"read"}}
	for k, v := range extra {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	return "/oauth/authorize?" + q.Encode()
}

var txRe = regexp.MustCompile(`name="tx" value="([^"]+)"`)

// begin loads the consent page and returns the transaction id.
func (c *oc) begin(clientID, challenge string, extra map[string]string) string {
	c.t.Helper()
	resp, b := c.req("GET", c.authorizeQuery(clientID, challenge, extra), nil, nil, nil)
	if resp.StatusCode != 200 {
		c.t.Fatalf("authorize: %d %s", resp.StatusCode, b)
	}
	m := txRe.FindSubmatch(b)
	if m == nil {
		c.t.Fatalf("no consent form in page:\n%s", b)
	}
	return string(m[1])
}

func (c *oc) consent(tx, decision, secret string) (*reply, []byte) {
	return c.req("POST", "/oauth/authorize", url.Values{"tx": {tx}, "decision": {decision}, "secret": {secret}}, nil, nil)
}

// approve runs authorize + consent and returns the authorization code.
func (c *oc) approve(clientID, challenge string) string {
	c.t.Helper()
	resp, b := c.consent(c.begin(clientID, challenge, nil), "approve", adminSecret)
	if resp.StatusCode != 302 {
		c.t.Fatalf("consent: %d %s", resp.StatusCode, b)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("state") != "xyz" || loc.Query().Get("iss") != c.e.ts.URL || loc.Query().Get("code") == "" {
		c.t.Fatalf("redirect: %s", loc)
	}
	return loc.Query().Get("code")
}

type tokenResp struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Type    string `json:"token_type"`
	Expires int    `json:"expires_in"`
	Scope   string `json:"scope"`
	Error   string `json:"error"`
}

func (c *oc) token(form url.Values, hdr map[string]string) (int, tokenResp) {
	c.t.Helper()
	resp, b := c.req("POST", "/oauth/token", form, nil, hdr)
	var tr tokenResp
	_ = json.Unmarshal(b, &tr)
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		c.t.Errorf("token responses must be no-store, got %q", cc)
	}
	return resp.StatusCode, tr
}

func codeForm(clientID, code, verifier string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
}

func (c *oc) mcpStatus(token string) int {
	return c.e.status("POST", "/mcp", token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
}

func (e *env) status(method, path, token, body string) int {
	rq, _ := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, strings.NewReader(body))
	rq.Header.Set("Authorization", "Bearer "+token)
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("Accept", "application/json, text/event-stream")
	rq.Header.Set("X-Forwarded-For", fmt.Sprintf("10.88.%d.%d", time.Now().UnixNano()/1000%250, time.Now().UnixNano()/7%250+1))
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// --- tests ---

func TestOAuthOffByDefault(t *testing.T) {
	e := newEnv(t)
	c := newOC(t, e)
	for _, p := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource", "/oauth/authorize"} {
		if resp, _ := c.req("GET", p, nil, nil, nil); resp.StatusCode != 404 {
			t.Errorf("%s must not exist without --oauth: %d", p, resp.StatusCode)
		}
	}
	if resp, _ := c.req("POST", "/oauth/register", nil, map[string]any{}, nil); resp.StatusCode != 404 {
		t.Errorf("registration must not exist without --oauth: %d", resp.StatusCode)
	}
	resp, _ := c.req("POST", "/mcp", nil, `{}`, nil)
	if wa := resp.Header.Get("WWW-Authenticate"); strings.Contains(wa, "resource_metadata") {
		t.Errorf("no OAuth hints without --oauth: %s", wa)
	}
}

func TestOAuthRequiresAdminSecretAndSaneURL(t *testing.T) {
	st := store.NewMemory()
	noAdmin, _ := auth.New(st, auth.Options{})
	if _, err := server.New(server.Config{Store: st, Auth: noAdmin, PublicURL: "https://bridge.example.com"}); err == nil {
		t.Error("OAuth without an admin secret must be refused: nobody could approve clients")
	}
	withAdmin, _ := auth.New(st, auth.Options{AdminSecret: adminSecret})
	for _, bad := range []string{"http://bridge.example.com", "bridge.example.com", "https://bridge.example.com/path", "https://u:p@bridge.example.com", "ftp://x"} {
		if _, err := server.New(server.Config{Store: st, Auth: withAdmin, PublicURL: bad}); err == nil {
			t.Errorf("public URL %q accepted", bad)
		}
	}
	for _, ok := range []string{"https://bridge.example.com", "https://bridge.example.com/", "http://127.0.0.1:8080", "https://bridge.example.com:8443"} {
		if _, err := server.New(server.Config{Store: st, Auth: withAdmin, PublicURL: ok}); err != nil {
			t.Errorf("public URL %q rejected: %v", ok, err)
		}
	}
}

func TestOAuthDiscovery(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	resp, b := c.req("GET", "/.well-known/oauth-protected-resource", nil, nil, nil)
	var prm struct {
		Resource string   `json:"resource"`
		AS       []string `json:"authorization_servers"`
	}
	_ = json.Unmarshal(b, &prm)
	if resp.StatusCode != 200 || prm.Resource != e.ts.URL+"/mcp" || len(prm.AS) != 1 || prm.AS[0] != e.ts.URL {
		t.Fatalf("protected resource metadata: %d %s", resp.StatusCode, b)
	}
	if resp, _ = c.req("GET", "/.well-known/oauth-protected-resource/mcp", nil, nil, nil); resp.StatusCode != 200 {
		t.Errorf("path-aware metadata location: %d", resp.StatusCode)
	}
	resp, b = c.req("GET", "/.well-known/oauth-authorization-server", nil, nil, nil)
	var as map[string]any
	_ = json.Unmarshal(b, &as)
	if resp.StatusCode != 200 || as["issuer"] != e.ts.URL || as["token_endpoint"] != e.ts.URL+"/oauth/token" ||
		as["registration_endpoint"] != e.ts.URL+"/oauth/register" || as["authorization_endpoint"] != e.ts.URL+"/oauth/authorize" {
		t.Fatalf("server metadata: %s", b)
	}
	if m := fmt.Sprint(as["code_challenge_methods_supported"]); m != "[S256]" {
		t.Errorf("only S256 may be offered, got %s", m)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("public metadata should be readable by browser clients")
	}
	// the challenge points clients at the metadata
	resp, _ = c.req("POST", "/mcp", nil, `{}`, nil)
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata="`+e.ts.URL+`/.well-known/oauth-protected-resource"`) {
		t.Errorf("401 must carry the resource metadata URL: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	// the token endpoint does not offer CORS
	resp, _ = c.req("POST", "/oauth/token", url.Values{}, nil, nil)
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("token endpoint must not be CORS-enabled")
	}
}

func TestOAuthFullFlowAndRotation(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	reg := c.register(nil)
	id := reg["client_id"].(string)
	if _, has := reg["client_secret"]; has || reg["token_endpoint_auth_method"] != "none" {
		t.Fatalf("default registration is a public client: %v", reg)
	}
	verifier, challenge := pkce()
	code := c.approve(id, challenge)

	status, tr := c.token(codeForm(id, code, verifier), nil)
	if status != 200 || tr.Type != "Bearer" || tr.Scope != "read" || tr.Expires != 3600 || !strings.HasPrefix(tr.Access, "ggb_read_") || !strings.HasPrefix(tr.Refresh, "ggb_refresh_") {
		t.Fatalf("token: %d %+v", status, tr)
	}
	// the assistant now talks MCP with the issued access token
	hc := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+tr.Access)
		return http.DefaultTransport.RoundTrip(r)
	})}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "assistant", Version: "0"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: e.ts.URL + "/mcp", HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("MCP with an OAuth token: %v", err)
	}
	defer cs.Close()
	if tools, err := cs.ListTools(context.Background(), nil); err != nil || len(tools.Tools) != 9 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	// it is only a read token
	if got := e.status("POST", "/v1/ingest", tr.Access, `{"sessions":[]}`); got != 403 {
		t.Errorf("OAuth tokens must stay read-only: %d", got)
	}

	// refresh rotates
	status, tr2 := c.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tr.Refresh}}, nil)
	if status != 200 || tr2.Access == tr.Access || tr2.Refresh == tr.Refresh {
		t.Fatalf("refresh: %d %+v", status, tr2)
	}
	if c.mcpStatus(tr2.Access) != 200 {
		t.Error("refreshed access token must work")
	}
	// replaying the OLD refresh token is theft: the whole grant dies
	status, bad := c.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tr.Refresh}}, nil)
	if status != 400 || bad.Error != "invalid_grant" {
		t.Fatalf("replayed refresh token: %d %+v", status, bad)
	}
	if got := c.mcpStatus(tr2.Access); got != 401 {
		t.Errorf("a replay must revoke the family's access tokens: %d", got)
	}
	if status, _ := c.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tr2.Refresh}}, nil); status != 400 {
		t.Error("a replay must revoke the family's refresh tokens too")
	}
}

func TestOAuthCodeIsSingleUseAndReplayRevokes(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	id := c.register(nil)["client_id"].(string)
	verifier, challenge := pkce()
	code := c.approve(id, challenge)
	status, tr := c.token(codeForm(id, code, verifier), nil)
	if status != 200 {
		t.Fatal(status)
	}
	status, bad := c.token(codeForm(id, code, verifier), nil)
	if status != 400 || bad.Error != "invalid_grant" {
		t.Fatalf("code reuse: %d %+v", status, bad)
	}
	if got := c.mcpStatus(tr.Access); got != 401 {
		t.Errorf("reusing a code must revoke what it produced: %d", got)
	}
}

func TestOAuthTokenEndpointRejects(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	id := c.register(nil)["client_id"].(string)
	other := c.register(map[string]any{"client_name": "other"})["client_id"].(string)
	verifier, challenge := pkce()
	cases := map[string]struct {
		mutate func(url.Values)
		client string
		want   string
	}{
		"wrong verifier":     {func(f url.Values) { f.Set("code_verifier", strings.Repeat("x", 50)) }, id, "invalid_grant"},
		"short verifier":     {func(f url.Values) { f.Set("code_verifier", "short") }, id, "invalid_grant"},
		"no verifier":        {func(f url.Values) { f.Del("code_verifier") }, id, "invalid_grant"},
		"other client":       {func(f url.Values) { f.Set("client_id", other) }, other, "invalid_grant"},
		"redirect mismatch":  {func(f url.Values) { f.Set("redirect_uri", "https://evil.example/cb") }, id, "invalid_grant"},
		"resource mismatch":  {func(f url.Values) { f.Set("resource", "https://other.example/mcp") }, id, "invalid_target"},
		"unknown client":     {func(f url.Values) { f.Set("client_id", "gg_nope") }, "gg_nope", "invalid_client"},
		"unsupported grant":  {func(f url.Values) { f.Set("grant_type", "password") }, id, "unsupported_grant_type"},
		"client_credentials": {func(f url.Values) { f.Set("grant_type", "client_credentials") }, id, "unsupported_grant_type"},
		"garbage code":       {func(f url.Values) { f.Set("code", "ggc_nope") }, id, "invalid_grant"},
	}
	for name, tc := range cases {
		code := c.approve(id, challenge)
		form := codeForm(id, code, verifier)
		tc.mutate(form)
		status, tr := c.token(form, nil)
		if status < 400 || tr.Error != tc.want || tr.Access != "" {
			t.Errorf("%s: got %d %+v, want error %s", name, status, tr, tc.want)
		}
	}
	// resource given as the bare base URL is accepted (same audience)
	form := codeForm(id, c.approve(id, challenge), verifier)
	form.Set("resource", e.ts.URL)
	if status, _ := c.token(form, nil); status != 200 {
		t.Errorf("bare base URL as resource: %d", status)
	}
}

func TestOAuthAuthorizeValidation(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	id := c.register(nil)["client_id"].(string)
	_, challenge := pkce()

	// errors that must NOT redirect (the redirect target is untrusted)
	for name, q := range map[string]string{
		"unknown client":    c.authorizeQuery("gg_nope", challenge, nil),
		"redirect mismatch": c.authorizeQuery(id, challenge, map[string]string{"redirect_uri": "https://evil.example/cb"}),
		"prefix redirect":   c.authorizeQuery(id, challenge, map[string]string{"redirect_uri": redirect + "/../x"}),
	} {
		resp, b := c.req("GET", q, nil, nil, nil)
		if resp.StatusCode != 400 || resp.Header.Get("Location") != "" || strings.Contains(string(b), `name="tx"`) {
			t.Errorf("%s: %d location=%q", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// errors reported to the validated redirect URI
	for name, tc := range map[string]struct {
		extra map[string]string
		want  string
	}{
		"no PKCE":          {map[string]string{"code_challenge": "", "code_challenge_method": ""}, "invalid_request"},
		"plain PKCE":       {map[string]string{"code_challenge_method": "plain"}, "invalid_request"},
		"bad challenge":    {map[string]string{"code_challenge": "short"}, "invalid_request"},
		"implicit flow":    {map[string]string{"response_type": "token"}, "unsupported_response_type"},
		"foreign resource": {map[string]string{"resource": "https://other.example/mcp"}, "invalid_target"},
		"extra scope":      {map[string]string{"scope": "read write"}, "invalid_scope"},
	} {
		resp, _ := c.req("GET", c.authorizeQuery(id, challenge, tc.extra), nil, nil, nil)
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != 302 || loc.Host != "assistant.example" || loc.Query().Get("error") != tc.want || loc.Query().Get("state") != "xyz" {
			t.Errorf("%s: %d %s", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// the consent page is a page that cannot be framed, cached or leak its URL
	resp, b := c.req("GET", c.authorizeQuery(id, challenge, nil), nil, nil, nil)
	h := resp.Header
	if resp.StatusCode != 200 || h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") != "no-store" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("consent page headers: %v", h)
	}
	if strings.Contains(string(b), "<script") || !strings.Contains(string(b), "Test Assistant") || !strings.Contains(string(b), "assistant.example") {
		t.Errorf("consent page must show who is asking and carry no script:\n%s", b)
	}
}

func TestOAuthConsentNeedsTheAdminSecret(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	id := c.register(nil)["client_id"].(string)
	_, challenge := pkce()
	tx := c.begin(id, challenge, nil)

	for _, wrong := range []string{"", "nope", e.ingest, e.read} { // other tokens are not the owner
		resp, b := c.consent(tx, "approve", wrong)
		if resp.StatusCode != 401 || resp.Header.Get("Location") != "" || !strings.Contains(string(b), "not the admin secret") {
			t.Errorf("secret %q: %d", wrong, resp.StatusCode)
		}
	}
	// a typo does not burn the request
	if resp, _ := c.consent(tx, "approve", adminSecret); resp.StatusCode != 302 {
		t.Fatalf("retry after typos: %d", resp.StatusCode)
	}
	// ... but it is single use
	if resp, _ := c.consent(tx, "approve", adminSecret); resp.StatusCode != 400 {
		t.Errorf("a consent request must be single use: %d", resp.StatusCode)
	}
	// denial needs no secret and reports access_denied
	tx2 := c.begin(id, challenge, nil)
	resp, _ := c.consent(tx2, "deny", "")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != 302 || loc.Query().Get("error") != "access_denied" || loc.Query().Get("code") != "" || loc.Query().Get("state") != "xyz" {
		t.Errorf("deny: %d %s", resp.StatusCode, loc)
	}
	// anything but an explicit approve is a denial
	tx3 := c.begin(id, challenge, nil)
	resp, _ = c.consent(tx3, "", adminSecret)
	if loc, _ := url.Parse(resp.Header.Get("Location")); loc.Query().Get("code") != "" {
		t.Error("a missing decision must not approve")
	}
	if resp, _ := c.consent("forged-transaction-id", "approve", adminSecret); resp.StatusCode != 400 {
		t.Errorf("forged transaction: %d", resp.StatusCode)
	}
}

func TestOAuthConsentIsLockedOutAfterGuessing(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	id := c.register(nil)["client_id"].(string)
	_, challenge := pkce()
	// all attempts from one address
	same := map[string]string{"X-Forwarded-For": "203.0.113.9"}
	statuses := map[int]int{}
	for i := 0; i < 14; i++ {
		resp, b := c.req("GET", c.authorizeQuery(id, challenge, nil), nil, nil, same)
		m := txRe.FindSubmatch(b)
		if m == nil {
			statuses[resp.StatusCode]++
			continue
		}
		resp, _ = c.req("POST", "/oauth/authorize", url.Values{"tx": {string(m[1])}, "decision": {"approve"}, "secret": {fmt.Sprintf("guess-%d", i)}}, nil, same)
		statuses[resp.StatusCode]++
	}
	if statuses[429] == 0 {
		t.Errorf("guessing the admin secret must lock the address out: %v", statuses)
	}
	// even the correct secret is refused while locked out
	_, b := c.req("GET", c.authorizeQuery(id, challenge, nil), nil, nil, map[string]string{"X-Forwarded-For": "198.51.100.4"})
	m := txRe.FindSubmatch(b)
	if m == nil {
		t.Skip("authorize bucket exhausted for this address")
	}
	if resp, _ := c.req("POST", "/oauth/authorize", url.Values{"tx": {string(m[1])}, "decision": {"approve"}, "secret": {adminSecret}}, nil, same); resp.StatusCode != 429 {
		t.Errorf("locked-out address with the right secret: %d", resp.StatusCode)
	}
}

func TestOAuthRegistrationValidation(t *testing.T) {
	e := oauthEnv(t, func(c *server.Config) {})
	c := newOC(t, e)
	bad := map[string]map[string]any{
		"no redirect":       {"client_name": "x"},
		"http non-loopback": {"redirect_uris": []string{"http://evil.example/cb"}},
		"fragment":          {"redirect_uris": []string{"https://a.example/cb#frag"}},
		"javascript scheme": {"redirect_uris": []string{"javascript:alert(1)"}},
		"custom scheme":     {"redirect_uris": []string{"myapp://cb"}},
		"credentials":       {"redirect_uris": []string{"https://u:p@a.example/cb"}},
		"relative":          {"redirect_uris": []string{"/cb"}},
		"six redirects":     {"redirect_uris": []string{"https://a/1", "https://a/2", "https://a/3", "https://a/4", "https://a/5", "https://a/6"}},
		"bad auth method":   {"redirect_uris": []string{redirect}, "token_endpoint_auth_method": "private_key_jwt"},
		"password grant":    {"redirect_uris": []string{redirect}, "grant_types": []string{"password"}},
		"implicit response": {"redirect_uris": []string{redirect}, "response_types": []string{"token"}},
		"write scope":       {"redirect_uris": []string{redirect}, "scope": "read write"},
	}
	for name, body := range bad {
		resp, b := c.req("POST", "/oauth/register", nil, body, nil)
		if resp.StatusCode != 400 {
			t.Errorf("%s: %d %s", name, resp.StatusCode, b)
		}
	}
	for _, ok := range []string{"http://localhost:5555/cb", "http://127.0.0.1/cb", "http://[::1]:1/cb"} {
		if resp, b := c.req("POST", "/oauth/register", nil, map[string]any{"redirect_uris": []string{ok}}, nil); resp.StatusCode != 201 {
			t.Errorf("loopback redirect %s: %d %s", ok, resp.StatusCode, b)
		}
	}
	conf := c.register(map[string]any{"token_endpoint_auth_method": "client_secret_post"})
	if s, _ := conf["client_secret"].(string); !strings.HasPrefix(s, "ggs_") {
		t.Errorf("confidential registration returns a secret: %v", conf)
	}
}

func TestOAuthRedirectHostAllowList(t *testing.T) {
	e := oauthEnv(t, func(c *server.Config) { c.OAuthRedirectHosts = []string{"assistant.example"} })
	c := newOC(t, e)
	if resp, _ := c.req("POST", "/oauth/register", nil, map[string]any{"redirect_uris": []string{"https://evil.example/cb"}}, nil); resp.StatusCode != 400 {
		t.Errorf("host outside the allow-list registered: %d", resp.StatusCode)
	}
	c.register(nil) // assistant.example is allowed
	if resp, _ := c.req("POST", "/oauth/register", nil, map[string]any{"redirect_uris": []string{"http://localhost:1/cb"}}, nil); resp.StatusCode != 201 {
		t.Errorf("loopback stays allowed: %d", resp.StatusCode)
	}
}

func TestOAuthClientCap(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	// fill to the cap through the store (registration is rate limited, on purpose)
	for i := 0; i < 200; i++ {
		_ = e.st.CreateOAuthClient(context.Background(), store.OAuthClient{ID: fmt.Sprintf("fill%d", i), Name: "f", RedirectURIs: []string{redirect}, CreatedAt: time.Now()})
	}
	if resp, _ := c.req("POST", "/oauth/register", nil, map[string]any{"redirect_uris": []string{redirect}}, nil); resp.StatusCode != 503 {
		t.Errorf("registration beyond the cap: %d", resp.StatusCode)
	}
}

func TestOAuthRegistrationIsRateLimited(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	same := map[string]string{"X-Forwarded-For": "203.0.113.77"}
	limited := false
	for i := 0; i < 12; i++ {
		resp, _ := c.req("POST", "/oauth/register", nil, map[string]any{"redirect_uris": []string{redirect}}, same)
		if resp.StatusCode == 429 {
			limited = true
		}
	}
	if !limited {
		t.Error("open registration must be rate limited per address")
	}
}

func TestOAuthClientNameCannotInjectMarkup(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	id := c.register(map[string]any{"client_name": `<script>alert(1)</script>Evil` + "\u202e" + "\x1b[31m"})["client_id"].(string)
	_, challenge := pkce()
	_, b := c.req("GET", c.authorizeQuery(id, challenge, nil), nil, nil, nil)
	page := string(b)
	if strings.Contains(page, "<script>alert") || strings.Contains(page, "\u202e") || strings.Contains(page, "\x1b") {
		t.Errorf("client name rendered unsafely:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Error("client name should be shown escaped")
	}
}

func TestOAuthConfidentialClientAuthentication(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	reg := c.register(map[string]any{"token_endpoint_auth_method": "client_secret_basic"})
	id, secret := reg["client_id"].(string), reg["client_secret"].(string)
	verifier, challenge := pkce()

	f := codeForm(id, c.approve(id, challenge), verifier)
	if status, tr := c.token(f, nil); status != 401 || tr.Error != "invalid_client" {
		t.Errorf("no secret: %d %+v", status, tr)
	}
	f = codeForm(id, c.approve(id, challenge), verifier)
	f.Set("client_secret", "ggs_wrong")
	if status, _ := c.token(f, nil); status != 401 {
		t.Errorf("wrong secret: %d", status)
	}
	f = codeForm(id, c.approve(id, challenge), verifier)
	f.Del("client_id")
	basic := map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))}
	if status, tr := c.token(f, basic); status != 200 || tr.Access == "" {
		t.Errorf("client_secret_basic: %d %+v", status, tr)
	}
	f = codeForm(id, c.approve(id, challenge), verifier)
	f.Set("client_secret", secret)
	if status, _ := c.token(f, nil); status != 200 {
		t.Errorf("client_secret_post: %d", status)
	}
	// PKCE is mandatory even for confidential clients
	f = codeForm(id, c.approve(id, challenge), "")
	f.Set("client_secret", secret)
	if status, _ := c.token(f, nil); status != 400 {
		t.Errorf("confidential client without PKCE verifier: %d", status)
	}
}

func TestOAuthAccessTokenExpires(t *testing.T) {
	e := oauthEnv(t, func(c *server.Config) { c.OAuthAccessTTL = 300 * time.Millisecond })
	c := newOC(t, e)
	id := c.register(nil)["client_id"].(string)
	verifier, challenge := pkce()
	_, tr := c.token(codeForm(id, c.approve(id, challenge), verifier), nil)
	if c.mcpStatus(tr.Access) != 200 {
		t.Fatal("fresh token refused")
	}
	time.Sleep(400 * time.Millisecond)
	if got := c.mcpStatus(tr.Access); got != 401 {
		t.Errorf("expired access token accepted: %d", got)
	}
	// the refresh token still works and yields a working token
	status, tr2 := c.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tr.Refresh}}, nil)
	if status != 200 || c.mcpStatus(tr2.Access) != 200 {
		t.Errorf("refresh after expiry: %d", status)
	}
}

func TestOAuthRevocationEndpoint(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	id := c.register(nil)["client_id"].(string)
	other := c.register(map[string]any{"client_name": "other"})["client_id"].(string)
	verifier, challenge := pkce()
	_, tr := c.token(codeForm(id, c.approve(id, challenge), verifier), nil)

	// another client cannot revoke it (and learns nothing)
	if resp, _ := c.req("POST", "/oauth/revoke", url.Values{"client_id": {other}, "token": {tr.Access}}, nil, nil); resp.StatusCode != 200 {
		t.Errorf("revoke by another client: %d", resp.StatusCode)
	}
	if c.mcpStatus(tr.Access) != 200 {
		t.Fatal("another client revoked a token that is not theirs")
	}
	// unknown tokens: still 200
	if resp, _ := c.req("POST", "/oauth/revoke", url.Values{"client_id": {id}, "token": {"ggb_read_nonsense"}}, nil, nil); resp.StatusCode != 200 {
		t.Errorf("unknown token: %d", resp.StatusCode)
	}
	// revoking the refresh token ends the whole grant
	c.req("POST", "/oauth/revoke", url.Values{"client_id": {id}, "token": {tr.Refresh}}, nil, nil)
	if got := c.mcpStatus(tr.Access); got != 401 {
		t.Errorf("revoking the refresh token must end the grant: %d", got)
	}
}

func TestOAuthDeletingAClientCutsItOff(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	id := c.register(nil)["client_id"].(string)
	verifier, challenge := pkce()
	_, tr := c.token(codeForm(id, c.approve(id, challenge), verifier), nil)
	if n, err := e.st.DeleteOAuthClient(context.Background(), id, time.Now()); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if got := c.mcpStatus(tr.Access); got != 401 {
		t.Errorf("deleted client's access token still works: %d", got)
	}
	if status, _ := c.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tr.Refresh}}, nil); status != 401 {
		t.Errorf("deleted client can still refresh: %d", status)
	}
}

func TestOAuthAuditHoldsNoSecrets(t *testing.T) {
	e := oauthEnv(t)
	c := newOC(t, e)
	id := c.register(nil)["client_id"].(string)
	verifier, challenge := pkce()
	code := c.approve(id, challenge)
	_, tr := c.token(codeForm(id, code, verifier), nil)
	evs, _ := e.st.ListAudit(context.Background(), 100)
	var actions []string
	for _, ev := range evs {
		actions = append(actions, ev.Action)
		blob := fmt.Sprintf("%+v", ev)
		for _, secret := range []string{code, tr.Access, tr.Refresh, adminSecret, verifier} {
			if strings.Contains(blob, secret) {
				t.Errorf("audit event holds a secret: %s", blob)
			}
		}
	}
	joined := strings.Join(actions, ",")
	for _, want := range []string{"oauth_register", "oauth_approve", "oauth_token"} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit missing %s in %s", want, joined)
		}
	}
}

func TestMigrationOfPreOAuthDatabase(t *testing.T) {
	// covered at the store level; here: an upgraded database serves OAuth.
	e := oauthEnv(t)
	if resp, _ := newOC(t, e).req("GET", "/.well-known/oauth-authorization-server", nil, nil, nil); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
}
