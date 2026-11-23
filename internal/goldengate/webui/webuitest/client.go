// Package webuitest is a test browser for the web UI server. It is imported
// by tests only.
package webuitest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/webui"
)

// Client is a test browser for a Server: it follows the launch flow and then
// sends what the page sends (the cookie, the request token, the Origin).
type Client struct {
	T       testing.TB
	S       *webui.Server
	HTTP    *http.Client
	Request string // the request token; empty before Launch
	Docs    string // where the page's help links point, from the session reply
}

// Start serves be on a loopback port for the length of the test.
func Start(t testing.TB, s *webui.Server) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	jar, _ := cookiejar.New(nil)
	return &Client{T: t, S: s, HTTP: &http.Client{Jar: jar}}
}

// Launch exchanges a fresh launch link for a session.
func (c *Client) Launch() {
	c.T.Helper()
	link := c.S.LaunchURL()
	token := link[strings.Index(link, "#")+1:]
	res, body := c.Do(http.MethodPost, "/api/session", map[string]string{"token": token}, nil)
	if res.StatusCode != http.StatusOK {
		c.T.Fatalf("launch: %d %s", res.StatusCode, body)
	}
	var out struct {
		RequestToken string `json:"request_token"`
		Docs         string `json:"docs"`
	}
	_ = json.Unmarshal(body, &out)
	c.Request = out.RequestToken
	c.Docs = out.Docs
}

// Do sends one request the way the page would. edit may change it first, to
// send what the page never would.
// Result is what came back, with the body already read and closed.
type Result struct {
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
}

// Cookies are the cookies the response set.
func (r Result) Cookies() []*http.Cookie { return r.cookies }

func (c *Client) Do(method, path string, body any, edit func(*http.Request)) (Result, []byte) {
	c.T.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.S.Origin()+path, rd)
	if err != nil {
		c.T.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", c.S.Origin())
	}
	if c.Request != "" {
		req.Header.Set(webui.HeaderRequest, c.Request)
	}
	if edit != nil {
		edit(req)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		c.T.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return Result{StatusCode: res.StatusCode, Header: res.Header, cookies: res.Cookies()}, b
}

// JSON calls an endpoint and decodes a successful answer into out.
func (c *Client) JSON(method, path string, body, out any) {
	c.T.Helper()
	res, b := c.Do(method, path, body, nil)
	if res.StatusCode != http.StatusOK {
		c.T.Fatalf("%s %s: %d %s", method, path, res.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.T.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
}
