package cli

import (
	"context"
	"errors"
	"log/slog"
	"time"

	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
)

// autoOpts are the few things an unattended pass takes from its caller.
type autoOpts struct {
	Project, Source string
	Since           time.Duration
	// Settle is how long a new or changed session must be left alone.
	Settle time.Duration
}

// autoResult says what an unattended pass did.
type autoResult struct {
	// Line is the one-line report ("goldengate background send: ...").
	Line string
	// Report is the plan's counts; the zero value when nothing was planned.
	Report gsync.Report
	// Ran is true when a send was planned and pushed; false when the pass
	// stopped first, because another send held the lock or the server could
	// not be reached.
	Ran bool
	// Busy and Offline say why it stopped.
	Busy, Offline bool
}

// autoPass is one unattended send: the config is read afresh, then the same
// plan and gate as every send, under the run lock, after a check that the
// server answers. `goldengate sync --auto` and the daemon both run it, so a
// timer, a hook and the daemon cannot send a session twice.
func (a *app) autoPass(ctx context.Context, o autoOpts) (autoResult, error) {
	var res autoResult
	cfg, err := a.load()
	if err != nil {
		return res, err
	}
	m, err := a.matcher(cfg)
	if err != nil {
		return res, err
	}
	reg, err := a.registry(ctx)
	if err != nil {
		return res, err
	}
	st, err := a.openState()
	if err != nil {
		return res, err
	}
	defer func() { _ = st.Close() }()
	s := &gsync.Syncer{
		Registry: reg, Config: cfg, Matcher: m, State: st, Home: a.Home, Now: a.Now, Resolver: a.resolver(cfg),
		Log:        slog.New(slog.NewTextHandler(a.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Summarizer: a.summarizer(cfg),
	}
	// One send at a time: a timer, a hook, the daemon and a person pressing
	// Send must not send the same session twice.
	lock, err := a.runLock(ctx, true)
	if errors.Is(err, gsync.ErrBusy) {
		res.Busy = true
		res.Line = "goldengate background send: another send is running; nothing done"
		return res, nil
	}
	if err != nil {
		return res, err
	}
	defer func() { _ = lock.Release() }()
	cl, err := a.ingestClient(cfg)
	if err != nil {
		return res, err
	}
	if line, ok := a.serverReachable(ctx, st, cl); !ok {
		res.Offline = true
		res.Line = "goldengate background send: " + line
		return res, nil
	}
	rep, err := s.Run(ctx, gsync.RunOpts{Project: o.Project, Source: o.Source, Since: o.Since, Unattended: true, Settle: o.Settle}, cl, false)
	if err != nil {
		return res, err
	}
	a.notifyReceipt(cfg, rep)
	res.Report, res.Ran = rep, true
	res.Line = autoReport(rep, false)
	return res, nil
}
