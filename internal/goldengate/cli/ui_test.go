package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// `goldengate ui` serves the real backend on loopback: the link it prints
// opens a session, and the page's first call returns the user's projects.
func TestUICommandServesTheRealBackendOnLoopback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path fixtures")
	}
	h := newHarness(t)
	if err := h.run("share", "add", "~/projects/demo"); err != nil {
		t.Fatal(err)
	}
	out := &lockedBuffer{}
	h.deps.Stdout = out
	var mu sync.Mutex
	var openedLinks []string
	h.deps.OpenBrowser = func(u string) error {
		mu.Lock()
		defer mu.Unlock()
		openedLinks = append(openedLinks, u)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cmd := New(h.deps)
	cmd.SetArgs([]string{"ui"})
	go func() { done <- cmd.ExecuteContext(ctx) }()

	re := regexp.MustCompile(`(http://127\.0\.0\.1:\d+)/launch#([0-9a-f]{64})`)
	var m []string
	for i := 0; i < 200 && m == nil; i++ {
		time.Sleep(10 * time.Millisecond)
		m = re.FindStringSubmatch(out.String())
	}
	if m == nil {
		cancel()
		t.Fatalf("no launch link was printed:\n%s", out.String())
	}
	origin, token := m[1], m[2]
	var opened []string
	for i := 0; i < 200 && len(opened) == 0; i++ { // the link is printed first, then opened
		mu.Lock()
		opened = append([]string(nil), openedLinks...)
		mu.Unlock()
		if len(opened) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if len(opened) != 1 || opened[0] != m[0] || !strings.Contains(out.String(), "this computer only") {
		t.Errorf("the browser is opened with the printed link: %v", opened)
	}

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	do := func(method, path, body, request string) (int, []byte) {
		req, _ := http.NewRequestWithContext(context.Background(), method, origin+path, strings.NewReader(body))
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", origin)
		}
		if request != "" {
			req.Header.Set("X-GoldenGate-Request", request)
		}
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	if code, _ := do("GET", "/api/snapshot", "", ""); code != http.StatusForbidden {
		t.Fatalf("the API answered before the launch: %d", code)
	}
	code, body := do("POST", "/api/session", `{"token":"`+token+`"}`, "")
	if code != 200 {
		t.Fatalf("launch: %d %s", code, body)
	}
	var sess struct {
		RequestToken string `json:"request_token"`
	}
	_ = json.Unmarshal(body, &sess)
	code, body = do("GET", "/api/snapshot", "", sess.RequestToken)
	if code != 200 || !strings.Contains(string(body), "~/projects/demo") || strings.Contains(string(body), "TOPSECRET") {
		t.Fatalf("snapshot from the real backend: %d %s", code, body)
	}
	// the inbox of the real backend: the shared project's session waits
	code, body = do("GET", "/api/inbox", "", sess.RequestToken)
	if code != 200 || !strings.Contains(string(body), "please ship the widget") {
		t.Fatalf("inbox from the real backend: %d %s", code, body)
	}
	if code, body := do("GET", "/launch", "", ""); code != 200 || !strings.Contains(string(body), "/assets/app.js") {
		t.Fatalf("the launch page: %d", code)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ui stopped with: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ui did not stop when cancelled")
	}
	if code, _ := func() (int, error) {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", origin+"/launch", nil)
		res, err := hc.Do(req)
		if err != nil {
			return 0, err
		}
		_ = res.Body.Close()
		return res.StatusCode, nil
	}(); code != 0 {
		t.Errorf("the server still answers after the command stopped: %d", code)
	}
}
