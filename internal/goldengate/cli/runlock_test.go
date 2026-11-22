package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
)

// Someone pressing Send while a background send runs waits a moment, then
// is told why nothing happened.
func TestInteractiveSendWaitsThenSaysAnotherSendRuns(t *testing.T) {
	h := newHarness(t)
	a := &app{Deps: h.deps}
	held, err := gsync.AcquireRunLock(a.dirs().State)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()
	old := interactiveLockWait
	interactiveLockWait = 100 * time.Millisecond
	defer func() { interactiveLockWait = old }()
	_, err = a.runLock(context.Background(), false)
	if !errors.Is(err, gsync.ErrBusy) || !strings.Contains(err.Error(), "try again when it has finished") {
		t.Fatalf("busy: %v", err)
	}
	if _, err := a.runLock(context.Background(), true); !errors.Is(err, gsync.ErrBusy) {
		t.Fatalf("unattended: %v", err)
	}
}

type fakeHealth struct {
	err   error
	calls int
}

func (f *fakeHealth) Health(context.Context) error { f.calls++; return f.err }

func TestServerReachableBacksOffAndRecovers(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	h.deps.Now = func() time.Time { return now }
	a := &app{Deps: h.deps}
	st, err := a.openState()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	hc := &fakeHealth{err: errors.New("connection refused")}

	if line, ok := a.serverReachable(ctx, st, hc); ok || !strings.Contains(line, "not reachable (connection refused)") {
		t.Fatalf("down: %q %v", line, ok)
	}
	now = now.Add(30 * time.Second)
	if line, ok := a.serverReachable(ctx, st, hc); ok || hc.calls != 1 || !strings.Contains(line, "last 1 attempt(s)") {
		t.Fatalf("inside the wait the server is not asked: %q %d", line, hc.calls)
	}
	now = now.Add(time.Minute)
	hc.err = nil
	if _, ok := a.serverReachable(ctx, st, hc); !ok || hc.calls != 2 {
		t.Fatalf("back up: %v %d", ok, hc.calls)
	}
	if o, _ := st.Offline(ctx); o.Failures != 0 {
		t.Fatalf("a success clears the record: %+v", o)
	}
}

// Between the daemon taking its lock and writing its status file, the status
// must say it is starting, not "running (pid 0)".
func TestDaemonStatusWhileStarting(t *testing.T) {
	h := newHarness(t)
	a := &app{Deps: h.deps}
	held, err := gsync.AcquireLock(a.dirs().State, gsync.DaemonLockName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()
	var out strings.Builder
	a.printDaemonStatus(&out, config.Config{})
	if !strings.Contains(out.String(), "daemon: starting") || strings.Contains(out.String(), "pid 0") {
		t.Errorf("status while starting:\n%s", out.String())
	}
}
