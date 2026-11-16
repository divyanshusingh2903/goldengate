// Package webui serves the local web UI of goldengate: a JSON API over
// tui.Backend, plus the static page that talks to it. It is a second front
// end, not a second implementation: every handler calls one Backend method,
// so the browser can do nothing the terminal UI and the commands cannot.
//
// The security model is DESIGN.md section 6.1. In short: the server listens
// on loopback only; a single-use launch token (carried in the URL fragment)
// is exchanged for a session cookie and a request token; every other request
// must pass, in order, an exact Host check, a method and body-shape check,
// an Origin check, and both session secrets. There is no CORS.
package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
)

//go:embed assets
var assetFS embed.FS

const (
	launchTTL      = 2 * time.Minute
	idleTTL        = 30 * time.Minute
	maxLaunchFails = 5
	maxBody        = 1 << 20
	// HeaderRequest carries the request token on every API call.
	HeaderRequest = "X-GoldenGate-Request"
)

// Options configure a Server.
type Options struct {
	// Port to listen on; 0 lets the system pick one. The address is always
	// 127.0.0.1: there is no option to listen anywhere else.
	Port int
	Now  func() time.Time
}

// Server is one run of the local web UI: one listener, one browser session.
type Server struct {
	be   tui.Backend
	ln   net.Listener
	port int
	now  func() time.Time
	http *http.Server

	mu          sync.Mutex
	launch      string // the current launch token; "" once used or never issued
	launchUntil time.Time
	launchFails int
	cookie      string // the session cookie's value; "" until a launch succeeds
	request     string // the request token the page sends on every call
	lastSeen    time.Time
	admin       string // the admin secret, for this run only; never stored
	handles     map[string]any
	edits       map[string]edit
	run         *syncRun
}

type edit struct {
	item tui.InboxItem
	path string
}

// New starts listening on loopback. Call Serve to handle requests.
func New(be tui.Backend, o Options) (*Server, error) {
	if o.Port < 0 || o.Port > 65535 {
		return nil, fmt.Errorf("port %d is out of range", o.Port)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port)))
	if err != nil {
		return nil, fmt.Errorf("cannot listen on 127.0.0.1: %w", err)
	}
	s := &Server{be: be, ln: ln, port: ln.Addr().(*net.TCPAddr).Port, now: o.Now, handles: map[string]any{}, edits: map[string]edit{}}
	if s.now == nil {
		s.now = time.Now
	}
	s.http = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 << 10}
	return s, nil
}

// Addr is the address the server listens on: always 127.0.0.1 and a port.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Origin is the only origin the server answers to.
func (s *Server) Origin() string { return "http://127.0.0.1:" + strconv.Itoa(s.port) }

// LaunchURL issues a fresh single-use launch token and returns the link that
// carries it. The token is in the URL fragment, which a browser never sends
// in a request and which therefore reaches no log. An earlier unused token
// stops working.
func (s *Server) LaunchURL() string {
	tok := randomToken()
	s.mu.Lock()
	s.launch, s.launchUntil, s.launchFails = tok, s.now().Add(launchTTL), 0
	s.mu.Unlock()
	return s.Origin() + "/launch#" + tok
}

// Serve handles requests until ctx is done.
func (s *Server) Serve(ctx context.Context) error {
	done := make(chan struct{})
	go func() { //nolint:gosec // G118: it runs after ctx is done, so the shutdown needs a context of its own
		select {
		case <-ctx.Done():
		case <-done:
		}
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shut)
		s.mu.Lock()
		if s.run != nil {
			s.run.cancel()
		}
		s.admin = ""
		s.mu.Unlock()
	}()
	err := s.http.Serve(s.ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("webui: system randomness unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func same(a, b string) bool {
	return a != "" && len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) cookieName() string { return "gg_ui_" + strconv.Itoa(s.port) }

// hostOK is the DNS-rebinding defence: the Host header must be exactly the
// loopback address or localhost, with our port.
func (s *Server) hostOK(host string) bool {
	p := strconv.Itoa(s.port)
	return host == "127.0.0.1:"+p || host == "localhost:"+p
}

// originOK accepts only our own origin, on the host the request came in on.
func (s *Server) originOK(r *http.Request) bool {
	return r.Header.Get("Origin") == "http://"+r.Host
}

func securityHeaders(h http.Header, api bool) {
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if api {
		h.Set("Cache-Control", "no-store")
	}
}

// deny answers a failed check: a bare status, no detail, no CORS headers.
func deny(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
}

// public paths are served without a session: the launch page, what it loads,
// and the call that turns a launch token into a session.
func public(r *http.Request) bool {
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/launch" || strings.HasPrefix(r.URL.Path, "/assets/")):
		return true
	case r.Method == http.MethodPost && r.URL.Path == "/api/session":
		return true
	}
	return false
}

// Handler is the whole server: the checks, then the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	assets, _ := fs.Sub(assetFS, "assets")
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("GET /launch", func(w http.ResponseWriter, _ *http.Request) {
		page, err := fs.ReadFile(assets, "index.html")
		if err != nil {
			deny(w, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", files))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api := strings.HasPrefix(r.URL.Path, "/api/")
		securityHeaders(w.Header(), api)
		// 1. Host
		if !s.hostOK(r.Host) {
			deny(w, http.StatusForbidden)
			return
		}
		// 2. method and shape
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			deny(w, http.StatusForbidden) // OPTIONS included: no preflight ever succeeds
			return
		}
		if r.Method == http.MethodPost {
			ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
			if strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
				deny(w, http.StatusBadRequest)
				return
			}
			if r.ContentLength > maxBody {
				deny(w, http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
			// 3. Origin
			if !s.originOK(r) {
				deny(w, http.StatusForbidden)
				return
			}
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && (api || site != "none") {
			deny(w, http.StatusForbidden)
			return
		}
		// 4. session
		if !public(r) && !s.authorized(r) {
			deny(w, http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// authorized checks both session secrets: the cookie the browser sends by
// itself, and the request token only our page can add. Either alone opens
// nothing.
func (s *Server) authorized(r *http.Request) bool {
	c, err := r.Cookie(s.cookieName())
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookie == "" || s.now().Sub(s.lastSeen) > idleTTL {
		s.cookie, s.request, s.admin = "", "", "" // an idle session is over
		return false
	}
	if !same(c.Value, s.cookie) || !same(r.Header.Get(HeaderRequest), s.request) {
		return false
	}
	s.lastSeen = s.now()
	return true
}

// session exchanges the launch token for the session cookie and the request
// token. Wrong, used and expired tokens get the same answer; after a few
// failures no token is accepted until a new link is issued.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.mu.Lock()
	ok := s.launchFails < maxLaunchFails && s.now().Before(s.launchUntil) && same(in.Token, s.launch)
	if !ok {
		s.launchFails++
		s.mu.Unlock()
		deny(w, http.StatusForbidden)
		return
	}
	s.launch = "" // single use
	s.cookie, s.request, s.lastSeen = randomToken(), randomToken(), s.now()
	cookie, request := s.cookie, s.request
	s.mu.Unlock()
	// Not marked Secure on purpose: the page is plain HTTP on loopback, where
	// not every browser accepts a Secure cookie (DESIGN.md 6.1).
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode}) //nolint:gosec // G124: see above
	reply(w, map[string]string{"request_token": request})
}
