package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/cli"
)

// clock is a test clock the daemon and the test can both use.
type clock struct{ ns atomic.Int64 }

func newClock(t time.Time) *clock    { c := &clock{}; c.ns.Store(t.UnixNano()); return c }
func (c *clock) Now() time.Time      { return time.Unix(0, c.ns.Load()) }
func (c *clock) Add(d time.Duration) { c.ns.Add(int64(d)) }

// syncBuf is a log both the daemon and the test read.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
func (s *syncBuf) passes() int                 { return strings.Count(s.String(), "goldengate background send:") }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// startDaemon runs `goldengate daemon` in the background of the test.
func (l *laptop) startDaemon(ctx context.Context) (logs *syncBuf, done chan error) {
	logs, done = &syncBuf{}, make(chan error, 1)
	d := l.deps(nil)
	d.Stdout, d.Stderr = logs, logs
	go func() {
		cmd := cli.New(d)
		cmd.SetArgs([]string{"daemon", "--interval", "15ms"})
		done <- cmd.ExecuteContext(ctx)
	}()
	return logs, done
}

func (l *laptop) daemonStatus(t *testing.T) (st struct {
	LastResult string    `json:"last_result"`
	LastError  string    `json:"last_error"`
	NextRun    time.Time `json:"next_run"`
	Reachable  *bool     `json:"reachable"`
}) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(l.root, "state", "goldengate", "daemon.json"))
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// Once the user chose automatic sending, the daemon sends on its own: a
// finished session goes out, a session still being written waits, a waiting
// inbox session does not make it loop, a decision or a recovered server
// makes it send, and it stops when switched off (#189).
func TestDaemonSendsByItself(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	clk := newClock(time.Now())
	l.now = clk.Now
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo", "--auto-approve")
	l.touchSessions(clk.Now().Add(-11 * time.Minute))
	holds := func(s string) func() bool {
		return func() bool { return strings.Contains(everythingTheServerHolds(t, b), s) }
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs, done := l.startDaemon(ctx)

	// a finished session goes out without anyone asking
	waitFor(t, "the first session on the server", holds("widget"))
	if out := l.must(nil, "receipts"); strings.Contains(out, "no receipts") {
		t.Errorf("the daemon's send has a receipt: %s", out)
	}

	// a second daemon is refused while this one runs
	if err := l.run(nil, "daemon", "--interval", "15ms"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("a second daemon: %v", err)
	}
	if out := l.must(nil, "daemon", "status"); !strings.Contains(out, "daemon: running") || !strings.Contains(out, "server: reachable") {
		t.Errorf("status: %s", out)
	}

	// a session still being written waits, then goes once it has settled
	l.addClaudeSession("33333333-3333-4333-8333-333333333333", "/home/user/projects/demo", "LIVE work")
	l.touchSessions(clk.Now())
	waitFor(t, "the held session to be reported", func() bool { return strings.Contains(logs.String(), "changed too recently") })
	time.Sleep(150 * time.Millisecond)
	if holds("LIVE")() {
		t.Fatal("a session still being written was sent")
	}
	clk.Add(11 * time.Minute)
	waitFor(t, "the settled session on the server", holds("LIVE"))

	// a project that asks first: its new session waits in the inbox, and
	// waiting is not a reason to send again and again
	l.must(nil, "projects", "trust", "demo", "ask")
	l.addClaudeSession("44444444-4444-4444-8444-444444444444", "/home/user/projects/demo", "PENDING work")
	l.touchSessions(clk.Now().Add(-11 * time.Minute))
	waitFor(t, "the waiting session to be reported", func() bool { return strings.Contains(logs.String(), "wait in the inbox") })
	// the changes above (settings, a new file, new times) may each cause one
	// look; wait until they have all been dealt with
	for i := 0; i < 40; i++ {
		before := logs.passes()
		time.Sleep(150 * time.Millisecond)
		if logs.passes() == before {
			break
		}
	}
	n := logs.passes()
	time.Sleep(400 * time.Millisecond) // about 25 looks
	if logs.passes() != n {
		t.Fatalf("the daemon ran %d more sends with nothing changed:\n%s", logs.passes()-n, logs.String())
	}
	if holds("PENDING")() {
		t.Fatal("an unapproved session was sent")
	}
	// approving it is a reason: it goes out on the next look
	l.must(nil, "inbox", "approve", "--all")
	waitFor(t, "the approved session on the server", holds("PENDING"))

	// the server cannot be reached: the daemon says so and shows when it
	// will try again; it sends once the server is back and the wait is over
	b.down.Store(true)
	l.addClaudeSession("55555555-5555-4555-8555-555555555555", "/home/user/projects/demo", "OFFLINE work")
	l.touchSessions(clk.Now().Add(-11 * time.Minute))
	l.must(nil, "inbox", "approve", "--all") // a decision: reason to look
	waitFor(t, "the daemon to find the server down", func() bool {
		st := l.daemonStatus(t)
		return st.Reachable != nil && !*st.Reachable
	})
	if st := l.daemonStatus(t); st.NextRun.Before(clk.Now().Add(50*time.Second)) || !strings.Contains(st.LastResult, "not reachable") {
		t.Errorf("next_run shows the backoff (%s from now): %+v", st.NextRun.Sub(clk.Now()), st)
	}
	b.down.Store(false)
	clk.Add(2 * time.Minute)
	waitFor(t, "the session after the server came back", holds("OFFLINE"))

	// switched off in the config: the daemon stops by itself
	l.appendConfig("[sync]\ndaemon = \"off\"\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon kept running after it was switched off")
	}
	if out := l.must(nil, "daemon", "status"); !strings.Contains(out, "daemon: not running") || !strings.Contains(out, "background sending: off") {
		t.Errorf("status after stop: %s", out)
	}
}

func TestDaemonStopsOnSignalAndChecksItself(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	l.must(l.initEnv(b), "init", "--non-interactive")

	// --check reads the token and asks the server
	out := l.must(nil, "daemon", "--check")
	for _, want := range []string{"token store:", "upload token: found", "server: reachable"} {
		if !strings.Contains(out, want) {
			t.Errorf("--check says %q:\n%s", want, out)
		}
	}
	// a token this process cannot see: the check fails and says how to fix it
	l.secrets.M = map[string]string{}
	err := l.run(nil, "daemon", "--check")
	if err == nil || !strings.Contains(err.Error(), "no upload token") || !strings.Contains(err.Error(), "DBUS_SESSION_BUS_ADDRESS") {
		t.Errorf("--check without the token: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	logs, done := l.startDaemon(ctx)
	waitFor(t, "the daemon to start", func() bool { return strings.Contains(logs.String(), "started") })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not stop when asked")
	}
	if !strings.Contains(logs.String(), "stopped") {
		t.Errorf("logs: %s", logs.String())
	}
}
