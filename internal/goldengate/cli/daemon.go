package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/secrets"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
)

// The daemon is a small foreground process, kept running by the operating
// system's service manager. Every few minutes it asks one cheap question,
// "did anything that could be sent change?", and only then runs the same
// unattended pass as `goldengate sync --auto` (autoPass): the same plan and
// gate, the same lock, the same settle window and backoff, the same receipts.
// It opens no network listener, reads no folder that is not shared, and
// approves nothing.

// daemonTick is how often the daemon looks, and daemonRetry how long it waits
// after a send failed for a reason other than an unreachable server.
const (
	daemonTick  = 2 * time.Minute
	daemonRetry = 5 * time.Minute
)

const daemonStatusFile = "daemon.json"

// DaemonStatus is what the daemon records about itself in daemon.json, for
// `goldengate daemon status`, the TUI and the web UI.
type DaemonStatus struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// UpdatedAt is when the daemon last looked; it moves every tick.
	UpdatedAt time.Time `json:"updated_at"`
	LastRun   time.Time `json:"last_run,omitempty"`
	// LastResult is the one-line report of the last send, or why it did not
	// run; LastError is set when it failed.
	LastResult string `json:"last_result,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	// NextRun is the earliest time the next send can start: the next look, or
	// later when the server is being left alone after it was unreachable.
	NextRun time.Time `json:"next_run,omitempty"`
	// Reachable is whether the server answered the last time it was asked;
	// nil before the first time.
	Reachable *bool `json:"reachable,omitempty"`
}

func (a *app) daemonStatusPath() string { return filepath.Join(a.dirs().State, daemonStatusFile) }

// readDaemonStatus reads daemon.json; a missing file gives the zero status.
func (a *app) readDaemonStatus() (DaemonStatus, error) {
	var st DaemonStatus
	b, err := os.ReadFile(a.daemonStatusPath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return DaemonStatus{}, nil // a half-written or old file says nothing
	}
	return st, nil
}

func (a *app) writeDaemonStatus(st DaemonStatus) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := a.dirs().State
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".daemon-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), a.daemonStatusPath())
}

// daemonRunning reports whether a daemon holds the daemon lock.
func (a *app) daemonRunning() bool {
	l, err := gsync.AcquireLock(a.dirs().State, gsync.DaemonLockName)
	if err != nil {
		return errors.Is(err, gsync.ErrBusy)
	}
	_ = l.Release()
	return false
}

// daemonMemory is what the daemon remembers between looks, to decide whether
// a send is due. A waiting inbox session is deliberately not a reason: it
// stays until a person decides, and a decision is itself a reason.
type daemonMemory struct {
	have     bool
	fp       string    // sessions that could be sent, and when each changed
	cfgMod   time.Time // the config file's modification time
	decided  int64     // newest inbox decision
	settleAt time.Time // when a session the last pass held back may be sent
	resendAt time.Time // when a changed session held for the re-send gap may be sent
	retryAt  time.Time // when to try again after a failed or blocked pass
}

type daemon struct {
	a      *app
	tick   time.Duration
	status DaemonStatus
	mem    daemonMemory
}

// runDaemon looks every tick until ctx ends or the config switches the
// daemon off.
func (a *app) runDaemon(ctx context.Context, tick time.Duration) error {
	lock, err := gsync.AcquireLock(a.dirs().State, gsync.DaemonLockName)
	if errors.Is(err, gsync.ErrBusy) {
		return errors.New("the daemon is already running (goldengate daemon status)")
	}
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	d := &daemon{a: a, tick: tick, status: DaemonStatus{PID: os.Getpid(), StartedAt: a.Now()}}
	fmt.Fprintf(a.Stderr, "goldengate daemon: started, looking every %s\n", tick)
	for {
		if d.step(ctx) {
			fmt.Fprintln(a.Stderr, "goldengate daemon: switched off in the config; stopping")
			return nil
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(a.Stderr, "goldengate daemon: stopped")
			return nil
		case <-time.After(tick):
		}
	}
}

// step is one look. It reports true when the daemon should stop.
func (d *daemon) step(ctx context.Context) (stop bool) {
	a := d.a
	now := a.Now()
	d.status.UpdatedAt = now
	d.status.NextRun = now.Add(d.tick)
	if d.mem.retryAt.After(d.status.NextRun) {
		// still leaving a down server alone: a look with nothing due must not
		// hide the backoff from `daemon status`
		d.status.NextRun = d.mem.retryAt
	}
	defer func() { _ = a.writeDaemonStatus(d.status) }()
	cfg, err := a.load() // read afresh: a switch or a new trust level counts at once
	if err != nil {
		d.status.LastError = err.Error()
		return false
	}
	if cfg.Sync.Daemon == "off" {
		return true
	}
	if cfg.Server.URL == "" {
		d.status.LastError = "no server configured: run `goldengate init`"
		return false
	}
	reason, fp, mod, decided, err := d.due(ctx, cfg, now)
	if err != nil {
		d.status.LastError = err.Error()
		return false
	}
	d.status.LastError = ""
	if reason == "" {
		return false
	}
	fmt.Fprintf(a.Stderr, "goldengate daemon: looking at the sessions (%s)\n", reason)
	res, perr := a.autoPass(ctx, autoOpts{Settle: cfg.Sync.Settle(), ResendGap: cfg.Sync.Resend()})
	d.status.LastRun = a.Now()
	// what was seen before the pass: anything that changed during it is
	// noticed at the next look
	d.mem = daemonMemory{have: true, fp: fp, cfgMod: mod, decided: decided}
	switch {
	case perr != nil:
		d.status.LastResult, d.status.LastError = "", perr.Error()
		d.mem.retryAt = now.Add(daemonRetry)
		d.status.NextRun = d.mem.retryAt
		fmt.Fprintln(a.Stderr, "goldengate daemon: the send failed:", perr)
	case res.Busy:
		d.status.LastResult = res.Line
		d.mem.retryAt = now.Add(d.tick) // the other send ends soon: look again then
	case res.Offline:
		f := false
		d.status.Reachable, d.status.LastResult = &f, res.Line
		d.mem.retryAt = d.offlineNext(ctx)
		if d.status.NextRun.Before(d.mem.retryAt) {
			d.status.NextRun = d.mem.retryAt
		}
	default:
		t := true
		d.status.Reachable, d.status.LastResult = &t, res.Line
		d.mem.settleAt = res.Report.SettledAt
		d.mem.resendAt = res.Report.ResendAt
		fmt.Fprintln(a.Stderr, res.Line)
	}
	return false
}

// offlineNext is when the server will be tried again after it was found
// unreachable.
func (d *daemon) offlineNext(ctx context.Context) time.Time {
	st, err := d.a.openState()
	if err != nil {
		return d.a.Now().Add(d.tick)
	}
	defer func() { _ = st.Close() }()
	off, err := st.Offline(ctx)
	if err != nil || off.Next().IsZero() {
		return d.a.Now().Add(d.tick)
	}
	return off.Next()
}

// due says whether a send should run now, and why: a short reason, empty when
// nothing is due. It also returns what it saw, for daemonMemory.
func (d *daemon) due(ctx context.Context, cfg config.Config, now time.Time) (reason, fp string, mod time.Time, decided int64, err error) {
	s, closeFn, err := d.a.newSyncer(ctx, cfg)
	if err != nil {
		return "", "", mod, 0, err
	}
	defer closeFn()
	if fp, err = s.Fingerprint(ctx); err != nil {
		return "", "", mod, 0, err
	}
	if decided, err = s.State.LastDecision(ctx); err != nil {
		return "", "", mod, 0, err
	}
	if fi, serr := os.Stat(d.a.cfgPath()); serr == nil {
		mod = fi.ModTime()
	}
	m := d.mem
	switch {
	case !m.have:
		reason = "first look"
	case fp != m.fp:
		reason = "sessions changed"
	case !mod.Equal(m.cfgMod):
		reason = "settings changed"
	case decided != m.decided:
		reason = "inbox decisions changed"
	case !m.settleAt.IsZero() && !m.settleAt.After(now):
		reason = "a held session has settled"
	case !m.resendAt.IsZero() && !m.resendAt.After(now):
		reason = "the wait before sending a session again is over"
	case !m.retryAt.IsZero() && !m.retryAt.After(now):
		reason = "trying again"
	}
	return reason, fp, mod, decided, nil
}

// daemonCheck is `goldengate daemon --check`: can a process started by the
// service manager do what the daemon needs? It reads the upload token (not
// only a probe of the keyring: a token saved in the desktop keyring is
// invisible to a process that cannot reach it) and asks the server.
func (a *app) daemonCheck(ctx context.Context, w io.Writer) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	if cfg.Server.URL == "" {
		return errors.New("no server configured: run `goldengate init` first")
	}
	store := a.secretStore()
	fmt.Fprintf(w, "token store: %s\n", store.Kind())
	cl, err := a.ingestClient(cfg)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) || isMissingToken(err) {
			return fmt.Errorf("%w\n%s", err, tokenFix)
		}
		return err
	}
	fmt.Fprintln(w, "upload token: found")
	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	if err := cl.Health(hctx); err != nil {
		return fmt.Errorf("the server did not answer: %w", err)
	}
	fmt.Fprintln(w, "server: reachable")
	return nil
}

func isMissingToken(err error) bool {
	return errors.Is(err, secrets.ErrNotFound) || strings.Contains(err.Error(), "no upload token stored")
}

// tokenFix says how to get the upload token to a process that cannot reach
// the desktop keyring.
const tokenFix = "This process cannot see the upload token. If it is in your desktop keyring and the keyring is not reachable here\n" +
	"(an SSH login, a container, a service without your desktop session), either let the service reach it:\n" +
	"    systemctl --user import-environment DBUS_SESSION_BUS_ADDRESS\n" +
	"or store the token in goldengate's own file instead: run `goldengate init` again from a shell where the keyring is not\n" +
	"available (for example `env -u DBUS_SESSION_BUS_ADDRESS goldengate init`); it then keeps the token in " +
	"secrets.json in the state directory (readable only by you, not encrypted)."

func (a *app) daemonCmd() *cobra.Command {
	var interval time.Duration
	var check bool
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Send in the background: look every few minutes and send what you approved or your rules approve",
		Long: "Runs until stopped. Every " + daemonTick.String() + " it checks whether anything that could be sent has changed, and only then runs\n" +
			"`goldengate sync --auto`. It opens no network port.\n" + whatAutoSends + "\n\n" +
			"Normally the system starts it for you: goldengate install-service.\n" +
			"  --check   see whether a background process can read the upload token and reach the server, then exit.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if check {
				return a.daemonCheck(cmd.Context(), a.Stdout)
			}
			if interval <= 0 {
				return fmt.Errorf("--interval must be positive, got %s", interval)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return a.runDaemon(ctx, interval)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "check that a background process can read the upload token and reach the server, then exit")
	cmd.Flags().DurationVar(&interval, "interval", daemonTick, "how often to look")
	_ = cmd.Flags().MarkHidden("interval")
	cmd.AddCommand(a.daemonStatusCmd())
	return cmd
}

func (a *app) daemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the background daemon runs, what it did last and when it looks next",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			a.printDaemonStatus(a.Stdout, cfg)
			return nil
		},
	}
}

// printDaemonStatus is the text of `daemon status`, also used by doctor.
func (a *app) printDaemonStatus(w io.Writer, cfg config.Config) {
	st, _ := a.readDaemonStatus()
	running := a.daemonRunning()
	switch cfg.Sync.Daemon {
	case "on":
		fmt.Fprintln(w, "background sending: on")
	case "off":
		fmt.Fprintln(w, "background sending: off (goldengate install-service turns it on)")
	default:
		fmt.Fprintln(w, "background sending: not chosen yet (goldengate install-service turns it on)")
	}
	if !running {
		fmt.Fprintln(w, "daemon: not running")
		if cfg.Sync.Daemon == "on" {
			fmt.Fprintln(w, "  it is on in the config but no daemon is running: check the service (systemctl --user status goldengate-daemon)")
		}
	} else if st.PID == 0 {
		// the lock is held but the daemon has not written its status yet
		fmt.Fprintln(w, "daemon: starting")
	} else {
		fmt.Fprintf(w, "daemon: running (pid %d, since %s)\n", st.PID, st.StartedAt.Local().Format("2006-01-02 15:04"))
	}
	if !st.LastRun.IsZero() {
		fmt.Fprintf(w, "last send: %s: %s\n", st.LastRun.Local().Format("15:04"), orNone(st.LastResult))
	}
	if st.LastError != "" {
		fmt.Fprintf(w, "problem: %s\n", st.LastError)
	}
	if running && !st.NextRun.IsZero() {
		fmt.Fprintf(w, "next look: %s\n", st.NextRun.Local().Format("15:04"))
	}
	if st.Reachable != nil {
		if *st.Reachable {
			fmt.Fprintln(w, "server: reachable")
		} else {
			fmt.Fprintln(w, "server: not reachable")
		}
	}
}

// daemonLine is the one line `goldengate doctor` shows about background
// sending.
func (a *app) daemonLine(cfg config.Config) string {
	running := a.daemonRunning()
	switch {
	case running:
		st, _ := a.readDaemonStatus()
		line := "ok    running; last send " + orNone(st.LastResult)
		if st.LastError != "" {
			line = "FAIL  running, but: " + st.LastError
		}
		return line
	case cfg.Sync.Daemon == "on":
		return "FAIL  on in the config but not running (goldengate daemon status)"
	case cfg.Sync.Daemon == "off":
		return "--    off (goldengate install-service turns it on)"
	}
	return "--    not chosen yet (goldengate install-service turns it on)"
}

// daemonSettings is the daemon as the TUI and the web UI show it.
func (a *app) daemonSettings(cfg config.Config) tui.DaemonSettings {
	st, _ := a.readDaemonStatus()
	d := tui.DaemonSettings{Mode: cfg.Sync.Daemon, Running: a.daemonRunning(), LastResult: st.LastResult, LastError: st.LastError,
		LastRun: st.LastRun, NextRun: st.NextRun, Reachable: st.Reachable}
	if !d.Running { // what a stopped daemon last wrote is history, not a plan
		d.NextRun = time.Time{}
	}
	if cfg.Sync.Daemon == "" && cfg.Server.URL != "" {
		for _, p := range cfg.Projects {
			d.Ask = d.Ask || p.Auto()
		}
	}
	return d
}
