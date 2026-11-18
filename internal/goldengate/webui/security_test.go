package webui_test

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/webui"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/webui/webuitest"
)

// stub answers the two calls these tests make; any other call would panic,
// which is the point: a request that fails a check must never reach it.
type stub struct {
	tui.Backend
	snapshots int
	keys      []string
}

func (s *stub) Snapshot(context.Context) (tui.Snapshot, error) {
	s.snapshots++
	return tui.Snapshot{Server: "https://bridge.example.com"}, nil
}

func (s *stub) SetSummarizerKey(_ context.Context, _, key string) error {
	s.keys = append(s.keys, key)
	return nil
}

func (s *stub) Undo(context.Context) (string, error) { return "undone", nil }

func start(t *testing.T, o webui.Options) (*webuitest.Client, *stub) {
	t.Helper()
	be := &stub{}
	srv, err := webui.New(be, o)
	if err != nil {
		t.Fatal(err)
	}
	return webuitest.Start(t, srv), be
}

func status(c *webuitest.Client, method, path string, body any, edit func(*http.Request)) int {
	res, _ := c.Do(method, path, body, edit)
	return res.StatusCode
}

func TestListensOnLoopbackOnly(t *testing.T) {
	c, _ := start(t, webui.Options{})
	if !strings.HasPrefix(c.S.Addr(), "127.0.0.1:") || !strings.HasPrefix(c.S.Origin(), "http://127.0.0.1:") {
		t.Fatalf("address: %s", c.S.Addr())
	}
	if _, err := webui.New(&stub{}, webui.Options{Port: 70000}); err == nil {
		t.Error("a port out of range is refused")
	}
}

func TestLaunchTokenIsSingleUseShortLivedAndRateLimited(t *testing.T) {
	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	c, be := start(t, webui.Options{Now: func() time.Time { return now }})
	link := c.S.LaunchURL()
	// the token travels in the fragment, which a browser never sends
	if !strings.Contains(link, "/launch#") || strings.Contains(link, "?") {
		t.Fatalf("launch link: %s", link)
	}
	token := link[strings.Index(link, "#")+1:]
	if len(token) != 64 {
		t.Fatalf("token: %d characters", len(token))
	}
	// nothing works before the launch
	if status(c, "GET", "/api/snapshot", nil, nil) != http.StatusForbidden {
		t.Fatal("the API answered without a session")
	}
	// a wrong guess is refused like a used or expired token
	if status(c, "POST", "/api/session", map[string]string{"token": strings.Repeat("0", 64)}, nil) != http.StatusForbidden {
		t.Fatal("a guessed token was accepted")
	}
	res, body := c.Do("POST", "/api/session", map[string]string{"token": token}, nil)
	if res.StatusCode != 200 {
		t.Fatalf("the real token: %d", res.StatusCode)
	}
	var cookie *http.Cookie
	for _, k := range res.Cookies() {
		cookie = k
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != 0 ||
		!strings.HasPrefix(cookie.Name, "gg_ui_") || !strings.HasSuffix(c.S.Addr(), strings.TrimPrefix(cookie.Name, "gg_ui_")) {
		t.Fatalf("session cookie: %+v", cookie)
	}
	if strings.Contains(string(body), cookie.Value) {
		t.Error("the cookie's value is also in the body")
	}
	// used once
	if status(c, "POST", "/api/session", map[string]string{"token": token}, nil) != http.StatusForbidden {
		t.Fatal("a launch token worked twice")
	}
	// expired
	token2 := c.S.LaunchURL()
	token2 = token2[strings.Index(token2, "#")+1:]
	now = now.Add(3 * time.Minute)
	if status(c, "POST", "/api/session", map[string]string{"token": token2}, nil) != http.StatusForbidden {
		t.Fatal("an expired launch token worked")
	}
	// after five failures even the right token is refused, until a new link is issued
	now = now.Add(time.Hour)
	token3 := c.S.LaunchURL()
	token3 = token3[strings.Index(token3, "#")+1:]
	for i := 0; i < 5; i++ {
		_ = status(c, "POST", "/api/session", map[string]string{"token": "guess"}, nil)
	}
	if status(c, "POST", "/api/session", map[string]string{"token": token3}, nil) != http.StatusForbidden {
		t.Fatal("guessing was not shut off")
	}
	token4 := c.S.LaunchURL()
	if status(c, "POST", "/api/session", map[string]string{"token": token4[strings.Index(token4, "#")+1:]}, nil) != 200 {
		t.Fatal("a new link works again")
	}
	if be.snapshots != 0 {
		t.Fatal("a refused request reached the backend")
	}
}

func TestEveryRequestNeedsHostOriginCookieAndRequestToken(t *testing.T) {
	c, be := start(t, webui.Options{})
	c.Launch()
	if status(c, "GET", "/api/snapshot", nil, nil) != 200 || status(c, "POST", "/api/undo", struct{}{}, nil) != 200 {
		t.Fatal("the page's own requests work")
	}
	seen := be.snapshots
	port := c.S.Addr()[strings.LastIndex(c.S.Addr(), ":"):]
	hdr := func(k, v string) func(*http.Request) { return func(r *http.Request) { r.Header.Set(k, v) } }
	host := func(h string) func(*http.Request) { return func(r *http.Request) { r.Host = h } }
	for name, tc := range map[string]struct {
		method, path string
		body         any
		edit         func(*http.Request)
		want         int
	}{
		// Host: the DNS-rebinding defence
		"host: another name":        {"GET", "/api/snapshot", nil, host("evil.example" + port), 403},
		"host: right name, no port": {"GET", "/api/snapshot", nil, host("127.0.0.1"), 403},
		"host: another port":        {"GET", "/api/snapshot", nil, host("127.0.0.1:1"), 403},
		"host: ours as a prefix":    {"GET", "/api/snapshot", nil, host("127.0.0.1" + port + ".evil.example"), 403},
		// methods: no preflight can ever succeed
		"OPTIONS": {"OPTIONS", "/api/undo", nil, hdr("Origin", "https://evil.example"), 403},
		"PUT":     {"PUT", "/api/undo", struct{}{}, nil, 403},
		"DELETE":  {"DELETE", "/api/undo", nil, nil, 403},
		// Origin on every POST
		"origin: missing": {"POST", "/api/undo", struct{}{}, func(r *http.Request) { r.Header.Del("Origin") }, 403},
		"origin: foreign": {"POST", "/api/undo", struct{}{}, hdr("Origin", "https://evil.example"), 403},
		"origin: null":    {"POST", "/api/undo", struct{}{}, hdr("Origin", "null"), 403},
		"origin: https":   {"POST", "/api/undo", struct{}{}, hdr("Origin", "https://127.0.0.1"+port), 403},
		// a cross-site page says so itself
		"sec-fetch-site: cross-site on the API": {"GET", "/api/snapshot", nil, hdr("Sec-Fetch-Site", "cross-site"), 403},
		"sec-fetch-site: none on the API":       {"GET", "/api/snapshot", nil, hdr("Sec-Fetch-Site", "none"), 403},
		"sec-fetch-site: cross-site on a page":  {"GET", "/launch", nil, hdr("Sec-Fetch-Site", "cross-site"), 403},
		// body shape: what an HTML form can send is refused
		"form body":      {"POST", "/api/undo", struct{}{}, hdr("Content-Type", "application/x-www-form-urlencoded"), 400},
		"text body":      {"POST", "/api/undo", struct{}{}, hdr("Content-Type", "text/plain"), 400},
		"unknown fields": {"POST", "/api/undo", map[string]int{"x": 1}, nil, 400},
		// the two session secrets, each needed
		"no request token":    {"GET", "/api/snapshot", nil, func(r *http.Request) { r.Header.Del(webui.HeaderRequest) }, 403},
		"wrong request token": {"GET", "/api/snapshot", nil, hdr(webui.HeaderRequest, strings.Repeat("a", 64)), 403},
		"no cookie":           {"GET", "/api/snapshot", nil, hdr("Cookie", ""), 403},
		"wrong cookie":        {"GET", "/api/snapshot", nil, hdr("Cookie", "gg_ui_"+port[1:]+"="+strings.Repeat("a", 64)), 403},
		// unknown paths behind the session are just not found
		"unknown path": {"GET", "/api/nothing", nil, nil, 404},
	} {
		if tc.want == 403 && name == "no cookie" || name == "wrong cookie" {
			// the client's jar would add the real cookie back: send without a jar
			plain := &webuitest.Client{T: t, S: c.S, HTTP: &http.Client{}, Request: c.Request}
			if got := status(plain, tc.method, tc.path, tc.body, tc.edit); got != tc.want {
				t.Errorf("%s: got %d, want %d", name, got, tc.want)
			}
			continue
		}
		if got := status(c, tc.method, tc.path, tc.body, tc.edit); got != tc.want {
			t.Errorf("%s: got %d, want %d", name, got, tc.want)
		}
	}
	if be.snapshots != seen {
		t.Errorf("a refused request reached the backend: %d extra calls", be.snapshots-seen)
	}
	// localhost with our port is the one other accepted name
	ok := func(r *http.Request) { r.Host = "localhost" + port }
	if got := status(c, "GET", "/launch", nil, ok); got != 200 {
		t.Errorf("localhost: %d", got)
	}
	// an oversized body is refused before it is read
	big := map[string]string{"Provider": "x", "Key": strings.Repeat("k", webui.MaxBody+10)}
	if got := status(c, "POST", "/api/summarizer/key", big, nil); got != http.StatusRequestEntityTooLarge || len(be.keys) != 0 {
		t.Errorf("oversized body: %d, %d keys stored", got, len(be.keys))
	}
}

func TestResponsesCarryTheSecurityHeadersAndNoCORS(t *testing.T) {
	c, _ := start(t, webui.Options{})
	c.Launch()
	for _, path := range []string{"/launch", "/assets/app.js", "/assets/app.css", "/api/snapshot", "/api/nothing"} {
		res, _ := c.Do("GET", path, nil, nil)
		h := res.Header
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'", "form-action 'none'", "base-uri 'none'", "connect-src 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP lacks %s: %s", path, want, csp)
			}
		}
		if strings.Contains(csp, "unsafe") || strings.Contains(csp, "http") {
			t.Errorf("%s: CSP allows inline code or another origin: %s", path, csp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "no-referrer" ||
			h.Get("Cross-Origin-Opener-Policy") != "same-origin" || h.Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Errorf("%s: headers: %v", path, h)
		}
		for k := range h {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s sends a CORS header: %s", path, k)
			}
		}
		if strings.HasPrefix(path, "/api/") && h.Get("Cache-Control") != "no-store" {
			t.Errorf("%s may be cached: %q", path, h.Get("Cache-Control"))
		}
	}
	// a refusal says nothing
	res, body := c.Do("GET", "/api/snapshot", nil, func(r *http.Request) { r.Host = "evil.example" })
	if res.StatusCode != 403 || len(bytes.TrimSpace(body)) != 0 {
		t.Errorf("a refusal has no detail: %d %q", res.StatusCode, body)
	}
}

func TestASecretGoesInAndNeverComesBack(t *testing.T) {
	c, be := start(t, webui.Options{})
	c.Launch()
	res, body := c.Do("POST", "/api/summarizer/key", map[string]string{"Provider": "anthropic", "Key": "sk-TOPSECRET"}, nil)
	if res.StatusCode != 200 || len(be.keys) != 1 || be.keys[0] != "sk-TOPSECRET" {
		t.Fatalf("the key reaches the backend: %d %v", res.StatusCode, be.keys)
	}
	if strings.Contains(string(body), "TOPSECRET") || strings.Contains(string(body), "sk-") {
		t.Fatalf("the key came back: %s", body)
	}
	// neither session secret appears in any later response
	_, snap := c.Do("GET", "/api/snapshot", nil, nil)
	if strings.Contains(string(snap), c.Request) {
		t.Error("the request token is echoed")
	}
}

func TestAnIdleSessionEnds(t *testing.T) {
	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	c, _ := start(t, webui.Options{Now: func() time.Time { return now }})
	c.Launch()
	now = now.Add(29 * time.Minute)
	if status(c, "GET", "/api/snapshot", nil, nil) != 200 {
		t.Fatal("a session in use stays open")
	}
	now = now.Add(31 * time.Minute)
	if status(c, "GET", "/api/snapshot", nil, nil) != http.StatusForbidden {
		t.Fatal("an idle session must end")
	}
	now = now.Add(time.Second)
	if status(c, "GET", "/api/snapshot", nil, nil) != http.StatusForbidden {
		t.Fatal("an ended session does not come back")
	}
}

// The page loads nothing from anywhere else and runs no inline code, so the
// strict policy holds without exceptions.
func TestThePageIsSelfContained(t *testing.T) {
	err := fs.WalkDir(webui.AssetFS, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// Only what the browser runs or lays out: font files and their
		// licences are data, and their metadata may name a web address.
		if !strings.HasSuffix(path, ".html") && !strings.HasSuffix(path, ".css") && !strings.HasSuffix(path, ".js") {
			return nil
		}
		b, _ := fs.ReadFile(webui.AssetFS, path)
		text := string(b)
		for _, bad := range []string{"http://", "https://", "//cdn", "innerHTML", "eval(", "document.write", "onclick=", "<style", "style=\""} {
			if strings.Contains(text, bad) {
				t.Errorf("%s contains %q", path, bad)
			}
		}
		if strings.HasSuffix(path, ".html") && strings.Contains(strings.ReplaceAll(text, `<script type="module" src="/assets/app.js"></script>`, ""), "<script") {
			t.Errorf("%s has an inline or foreign script", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Warnings name long paths with no break points; they wrap instead of
// pushing the page wider than a phone screen (#122).
func TestWarningsWrapLongWords(t *testing.T) {
	b, err := fs.ReadFile(webui.AssetFS, "assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	rule := regexp.MustCompile(`(?m)^\.warn \{[^}]*\}`).FindString(string(b))
	if !strings.Contains(rule, "overflow-wrap: anywhere") {
		t.Errorf(".warn must wrap long words: %q", rule)
	}
}

// Where the summarizer key is kept comes from the backend, never a fixed
// word (#123).
func TestSettingsSaysWhereTheKeyIs(t *testing.T) {
	b, err := fs.ReadFile(webui.AssetFS, "assets/settings.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	if strings.Contains(js, "(keyring)") || strings.Contains(js, "goes into this computer's keyring") {
		t.Error("settings.js claims the keyring whatever the backend says")
	}
	if !strings.Contains(js, "KeySource") || !strings.Contains(js, `src === "file"`) {
		t.Error("settings.js must show the backend's KeySource, including the file fallback")
	}
}

// Every script the page imports is one of its own embedded files, served as
// JavaScript, and the page starts from app.js.
func TestThePagesScriptsAllExistAndAreServed(t *testing.T) {
	c, _ := start(t, webui.Options{})
	imports := regexp.MustCompile(`from "(/assets/[a-z]+\.js)"`)
	seen := map[string]bool{"/assets/app.js": true, "/assets/app.css": true}
	_ = fs.WalkDir(webui.AssetFS, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		b, _ := fs.ReadFile(webui.AssetFS, path)
		for _, m := range imports.FindAllStringSubmatch(string(b), -1) {
			seen[m[1]] = true
		}
		if strings.Contains(string(b), "import(") {
			t.Errorf("%s loads a script at run time", path)
		}
		return nil
	})
	for _, screen := range []string{"inbox", "receipts", "agents"} {
		if !seen["/assets/"+screen+".js"] {
			t.Errorf("the %s screen is not imported", screen)
		}
	}
	if len(seen) < 11 {
		t.Fatalf("expected the screens to be imported, found %v", seen)
	}
	for path := range seen {
		res, body := c.Do("GET", path, nil, nil)
		want := "text/javascript"
		if strings.HasSuffix(path, ".css") {
			want = "text/css"
		}
		if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), want) || len(body) == 0 {
			t.Errorf("%s: %d %q", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
	_, page := c.Do("GET", "/launch", nil, nil)
	for _, want := range []string{`<script type="module" src="/assets/app.js"></script>`, `<nav id="nav"`, `role="status"`, `<main id="main"`} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the page lacks %s", want)
		}
	}
}

// Every API path a screen calls is one the server routes: a screen cannot
// ship pointing at an endpoint that does not exist.
func TestEveryPathTheScreensCallIsServed(t *testing.T) {
	c, _ := start(t, webui.Options{})
	paths := regexp.MustCompile("[\"`](/api/[a-z/-]+)")
	called := map[string]bool{}
	_ = fs.WalkDir(webui.AssetFS, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		b, _ := fs.ReadFile(webui.AssetFS, path)
		for _, m := range paths.FindAllStringSubmatch(string(b), -1) {
			called[strings.TrimSuffix(m[1], "/")] = true
		}
		return nil
	})
	for _, want := range []string{"/api/inbox", "/api/inbox/decide", "/api/edit/begin", "/api/edit/finish", "/api/offers/decide", "/api/receipts", "/api/admin", "/api/admin/forget", "/api/agents", "/api/agents/view", "/api/agents/grants", "/api/agents/revoke-token"} {
		if !called[want] {
			t.Errorf("no screen calls %s", want)
		}
	}
	for path := range called {
		if path == "/api/sync" { // also the prefix of /api/sync/{id}/events, built at run time
			continue
		}
		// 404 means no route; 405 means the route exists under the other method.
		get, _ := c.Do("GET", path, nil, nil)
		if get.StatusCode == http.StatusNotFound {
			t.Errorf("a screen calls %s, which the server does not route", path)
		}
	}
}

// The page's typefaces and mark are its own files, served with their types,
// and each embedded font travels with its licence.
func TestThePagesFontsAndMarkAreServed(t *testing.T) {
	c, _ := start(t, webui.Options{})
	css, err := fs.ReadFile(webui.AssetFS, "assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	urls := regexp.MustCompile(`url\("(/assets/[a-z0-9/.-]+)"\)`).FindAllStringSubmatch(string(css), -1)
	if len(urls) < 5 {
		t.Fatalf("expected the five font faces in app.css, found %d", len(urls))
	}
	check := func(path, want string) {
		res, body := c.Do("GET", path, nil, nil)
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != want || len(body) == 0 {
			t.Errorf("%s: %d %q, want %q", path, res.StatusCode, res.Header.Get("Content-Type"), want)
		}
	}
	for _, m := range urls {
		check(m[1], "font/woff2")
	}
	check("/assets/mark.svg", "image/svg+xml")
	for _, face := range []string{"federo", "josefin-sans", "ibm-plex-mono"} {
		lic, err := fs.ReadFile(webui.AssetFS, "assets/fonts/LICENSE-"+face+".txt")
		if err != nil || !strings.Contains(string(lic), "SIL Open Font License") {
			t.Errorf("%s has no licence beside it: %v", face, err)
		}
	}
}
