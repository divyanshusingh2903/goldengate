package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
)

// interactiveLockWait is how long a send someone is watching waits for a
// background send to finish before saying another send is running. A
// variable for tests.
var interactiveLockWait = 5 * time.Second

// healthTimeout bounds the check an unattended run makes before it sends.
const healthTimeout = 5 * time.Second

// errSendRunning is what a person sees when a background send holds the
// lock. It is still gsync.ErrBusy for errors.Is.
var errSendRunning = fmt.Errorf("%w (a timer, a hook or another window): try again when it has finished", gsync.ErrBusy)

// runLock takes the run lock in the state directory. An unattended run does
// not wait; one someone is watching waits a few seconds.
func (a *app) runLock(ctx context.Context, unattended bool) (*gsync.RunLock, error) {
	dir := a.dirs().State
	if unattended {
		return gsync.AcquireRunLock(dir)
	}
	l, err := gsync.WaitRunLock(ctx, dir, interactiveLockWait)
	if errors.Is(err, gsync.ErrBusy) {
		return nil, errSendRunning
	}
	return l, err
}

// healthChecker is the part of the upload client an unattended run needs
// before it sends.
type healthChecker interface {
	Health(ctx context.Context) error
}

// serverReachable decides whether an unattended run goes ahead. After the
// server could not be reached it is left alone for a while (a minute,
// doubling up to an hour), so a laptop offline all afternoon does not try it
// every few minutes. ok is false when the run should send nothing; line then
// says why, for the one-line report. The caller holds the run lock, so the
// backoff record has one writer.
func (a *app) serverReachable(ctx context.Context, st *gsync.State, cl healthChecker) (line string, ok bool) {
	now := a.Now()
	off, err := st.Offline(ctx)
	if err != nil {
		return err.Error(), false
	}
	if next := off.Next(); !next.IsZero() && now.Before(next) {
		return fmt.Sprintf("the server was not reachable at the last %d attempt(s); nothing sent, next try after %s",
			off.Failures, next.Local().Format("15:04")), false
	}
	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	herr := cl.Health(hctx)
	cancel()
	if herr != nil {
		if ctx.Err() != nil {
			return ctx.Err().Error(), false
		}
		off = gsync.Offline{Failures: off.Failures + 1, Last: now}
		if err := st.SetOffline(ctx, off); err != nil {
			return err.Error(), false
		}
		return fmt.Sprintf("the server is not reachable (%v); nothing sent, next try after %s",
			herr, off.Next().Local().Format("15:04")), false
	}
	if off.Failures > 0 {
		if err := st.SetOffline(ctx, gsync.Offline{}); err != nil {
			return err.Error(), false
		}
	}
	return "", true
}
