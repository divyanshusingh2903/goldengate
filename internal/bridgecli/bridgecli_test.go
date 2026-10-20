package bridgecli

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const goodSecret = "Zx9Qm2Lp7Vb4Nc8Rt1Yw5Ke3Hd6Jf0GaBq+Ux/OwLk2Mn7P="

// syncBuf is a goroutine-safe buffer: the server logs while the test polls.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
func (s *syncBuf) Reset()                      { s.mu.Lock(); defer s.mu.Unlock(); s.b.Reset() }

type h struct {
	t        *testing.T
	out, err *syncBuf
	env      map[string]string
	db       string
}

func newH(t *testing.T) *h {
	return &h{t: t, out: &syncBuf{}, err: &syncBuf{}, env: map[string]string{}, db: filepath.Join(t.TempDir(), "b.db")}
}

func (x *h) run(ctx context.Context, args ...string) error {
	x.out.Reset()
	x.err.Reset()
	cmd := New(Deps{Stdout: x.out, Stderr: x.err, Getenv: func(k string) string { return x.env[k] }, Now: time.Now})
	cmd.SetArgs(append([]string{"--db", x.db}, args...))
	return cmd.ExecuteContext(ctx)
}

func TestServeRefusesToStartUnsafe(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		args []string
		want string
	}{
		"no credentials":     {nil, []string{"--addr", "127.0.0.1:0"}, "without credentials"},
		"weak admin secret":  {map[string]string{"GOLDENGATE_ADMIN_TOKEN": "hunter2"}, []string{"--addr", "127.0.0.1:0"}, "too short"},
		"plain http, public": {map[string]string{"GOLDENGATE_ADMIN_TOKEN": goodSecret}, []string{"--addr", "0.0.0.0:0"}, "plain HTTP"},
		"cert without key":   {map[string]string{"GOLDENGATE_ADMIN_TOKEN": goodSecret}, []string{"--addr", "127.0.0.1:0", "--tls-cert", "x"}, "together"},
		"PORT forces public": {map[string]string{"GOLDENGATE_ADMIN_TOKEN": goodSecret, "PORT": "8080"}, nil, "plain HTTP"},
	}
	for name, c := range cases {
		x := newH(t)
		x.env = c.env
		if x.env == nil {
			x.env = map[string]string{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := x.run(ctx, append([]string{"serve"}, c.args...)...)
		cancel()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", name, c.want, err)
		}
	}
}

func TestServeStartsWithAdminSecretOrStoredToken(t *testing.T) {
	for name, setup := range map[string]func(*h){
		"admin secret": func(x *h) { x.env["GOLDENGATE_ADMIN_TOKEN"] = goodSecret },
		"stored token": func(x *h) {
			if err := x.run(context.Background(), "token", "create", "--scope", "ingest"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		x := newH(t)
		setup(x)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- x.run(ctx, "serve", "--addr", "127.0.0.1:0") }()
		addr := ""
		re := regexp.MustCompile(`listening on (\S+)`)
		for i := 0; i < 100 && addr == ""; i++ {
			if m := re.FindStringSubmatch(x.err.String()); m != nil {
				addr = m[1]
			} else {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if addr == "" {
			cancel()
			t.Fatalf("%s: server did not start: %v %s", name, <-done, x.err.String())
		}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("%s: healthz: %v", name, err)
		} else {
			resp.Body.Close()
		}
		cancel()
		if err := <-done; err != nil {
			t.Errorf("%s: shutdown: %v", name, err)
		}
		if strings.Contains(x.err.String(), goodSecret) {
			t.Errorf("%s: admin secret was logged", name)
		}
	}
}

func TestTokenCreateListRevoke(t *testing.T) {
	x := newH(t)
	ctx := context.Background()
	if err := x.run(ctx, "token", "create", "--scope", "read", "--label", "gemini", "--expires", "24h"); err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(x.out.String())
	if !strings.HasPrefix(secret, "ggb_read_") || strings.Contains(x.err.String(), secret) {
		t.Fatalf("secret goes to stdout only: %q / %q", secret, x.err.String())
	}
	if err := x.run(ctx, "token", "create", "--scope", "admin"); err == nil {
		t.Error("admin tokens are not mintable")
	}
	if err := x.run(ctx, "token", "list"); err != nil {
		t.Fatal(err)
	}
	list := x.out.String()
	if !strings.Contains(list, "gemini") || !strings.Contains(list, "active") || strings.Contains(list, secret) {
		t.Fatalf("list:\n%s", list)
	}
	id := strings.Fields(strings.Split(list, "\n")[1])[0]
	if err := x.run(ctx, "token", "revoke", id); err != nil {
		t.Fatal(err)
	}
	_ = x.run(ctx, "token", "list")
	if !strings.Contains(x.out.String(), "inactive") {
		t.Errorf("revoked token still active:\n%s", x.out.String())
	}
	if err := x.run(ctx, "token", "revoke", "tok_missing"); err == nil {
		t.Error("revoking an unknown id must fail")
	}
}

func TestPairPrintsSingleUseCode(t *testing.T) {
	x := newH(t)
	if err := x.run(context.Background(), "pair", "--ttl", "5m"); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`Pairing code: [A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}`).MatchString(x.out.String()) {
		t.Fatalf("%s", x.out.String())
	}
	if err := x.run(context.Background(), "pair", "--ttl", "0s"); err == nil {
		t.Error("zero ttl")
	}
}

func TestServeOAuthFlagValidation(t *testing.T) {
	x := newH(t)
	x.env["GOLDENGATE_ADMIN_TOKEN"] = goodSecret
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := x.run(ctx, "serve", "--addr", "127.0.0.1:0", "--oauth"); err == nil || !strings.Contains(err.Error(), "--public-url") {
		t.Errorf("--oauth without a public URL: %v", err)
	}
	if err := x.run(ctx, "serve", "--addr", "127.0.0.1:0", "--oauth", "--public-url", "http://bridge.example.com"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("plain-http public URL: %v", err)
	}
	// OAuth needs the admin secret: a stored token alone cannot approve clients
	y := newH(t)
	if err := y.run(ctx, "token", "create", "--scope", "ingest"); err != nil {
		t.Fatal(err)
	}
	if err := y.run(ctx, "serve", "--addr", "127.0.0.1:0", "--oauth", "--public-url", "https://bridge.example.com"); err == nil || !strings.Contains(err.Error(), "GOLDENGATE_ADMIN_TOKEN") {
		t.Errorf("--oauth without an admin secret: %v", err)
	}
}

func TestOAuthClientsCommands(t *testing.T) {
	x := newH(t)
	ctx := context.Background()
	if err := x.run(ctx, "oauth", "clients", "create", "--name", "gemini"); err == nil {
		t.Error("a redirect URI is required")
	}
	if err := x.run(ctx, "oauth", "clients", "create", "--redirect-uri", "http://evil.example/cb"); err == nil {
		t.Error("plain http redirect must be refused")
	}
	if err := x.run(ctx, "oauth", "clients", "create", "--name", "gemini", "--redirect-uri", "https://a.example/cb"); err != nil {
		t.Fatal(err)
	}
	out := x.out.String()
	id := regexp.MustCompile(`client_id:\s+(\S+)`).FindStringSubmatch(out)
	sec := regexp.MustCompile(`client_secret:\s+(\S+)`).FindStringSubmatch(out)
	if id == nil || sec == nil || !strings.HasPrefix(sec[1], "ggs_") || strings.Contains(x.err.String(), sec[1]) {
		t.Fatalf("create output: %q / %q", out, x.err.String())
	}
	if err := x.run(ctx, "oauth", "clients", "create", "--name", "pub", "--redirect-uri", "https://b.example/cb", "--public"); err != nil || strings.Contains(x.out.String(), "client_secret") {
		t.Fatalf("public client has no secret: %v %q", err, x.out.String())
	}
	if err := x.run(ctx, "oauth", "clients", "list"); err != nil {
		t.Fatal(err)
	}
	list := x.out.String()
	if !strings.Contains(list, id[1]) || !strings.Contains(list, "confidential") || !strings.Contains(list, "public") || strings.Contains(list, sec[1]) {
		t.Fatalf("list:\n%s", list)
	}
	if err := x.run(ctx, "oauth", "clients", "delete", id[1]); err != nil || !strings.Contains(x.out.String(), "deleted client") {
		t.Fatalf("%v %s", err, x.out.String())
	}
	if err := x.run(ctx, "oauth", "clients", "delete", id[1]); err == nil {
		t.Error("deleting twice must fail")
	}
}
