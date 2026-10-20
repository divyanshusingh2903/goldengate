// Package cli builds the goldengate command tree. It holds no global state:
// every dependency comes in through Deps, so commands are testable.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/claudecode"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/codex"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/opencode"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/discover"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/secrets"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Version is set at build time.
var Version = "dev"

// Deps are the command tree's external dependencies.
type Deps struct {
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	Home           string
	NewRegistry    func(home string, getenv func(string) string) (*adapters.Registry, error)
	Now            func() time.Time

	// Server interaction. All optional in tests.
	Stdin       io.Reader
	Interactive bool                                // stdin/stdout are a terminal: prompting is allowed
	ReadSecret  func(prompt string) (string, error) // hidden input
	SecretStore func(stateDir string, warn func(string)) secrets.Store
	HTTP        *http.Client
	Hostname    func() string
}

// DefaultDeps wires the real process environment.
func DefaultDeps() Deps {
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	return Deps{
		Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv, Home: pathutil.Home(),
		NewRegistry: DefaultRegistry, Now: time.Now,
		Stdin: os.Stdin, Interactive: interactive,
		ReadSecret: func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return strings.TrimSpace(string(b)), err
		},
		SecretStore: secrets.Open,
		Hostname:    func() string { h, _ := os.Hostname(); return h },
	}
}

// DefaultRegistry registers every adapter this build ships.
func DefaultRegistry(_ string, _ func(string) string) (*adapters.Registry, error) {
	var r adapters.Registry
	for _, ad := range []adapters.Adapter{claudecode.New(claudecode.Options{}), codex.New(codex.Options{}), opencode.New(opencode.Options{})} {
		if err := r.Register(ad); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

type app struct {
	Deps
	configPath string
}

// New returns the root command.
func New(d Deps) *cobra.Command {
	a := &app{Deps: d}
	root := &cobra.Command{
		Use:           "goldengate",
		Short:         "Share chosen projects' AI-coding history with an assistant, on your terms",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.Interactive {
				fmt.Fprintln(a.Stderr, "Not a terminal, so the interface cannot open. Available commands:")
				return cmd.Help()
			}
			return tui.Run(a.tuiBackend(), tui.Options{Context: cmd.Context(), Now: a.Now})
		},
	}
	root.SetOut(d.Stdout)
	root.SetErr(d.Stderr)
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "config file (default: XDG config dir)")
	root.AddCommand(a.syncCmd(), a.shareCmd(), a.doctorCmd(), a.initCmd(), a.tokenCmd(), a.purgeCmd(), a.importsCmd(), &cobra.Command{
		Use: "version", Short: "Print the version",
		Run: func(*cobra.Command, []string) {
			fmt.Fprintln(a.Stdout, "goldengate", Version, runtime.GOOS+"/"+runtime.GOARCH)
		},
	})
	root.AddCommand(a.importCmds()...)
	return root
}

func (a *app) dirs() config.Dirs { return config.ResolveDirs(a.Getenv, a.Home) }

func (a *app) cfgPath() string {
	if a.configPath != "" {
		return a.configPath
	}
	return a.dirs().ConfigPath()
}

func (a *app) load() (config.Config, error) { return config.Load(a.cfgPath()) }

func (a *app) matcher(c config.Config) (*selection.Matcher, error) {
	return selection.New(selection.Policy{
		Default: c.Share.Default, Allow: c.Share.Allow, Deny: c.Share.Deny, IncludeSources: c.Share.IncludeSources, SourceDeny: c.Share.SourceDeny,
	}, selection.Options{Home: a.Home})
}

func (a *app) openState() (*gsync.State, error) {
	return gsync.OpenState(a.dirs().StatePath())
}

// --- sync ---

func (a *app) syncCmd() *cobra.Command {
	var dry, quiet bool
	var project, source, since string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Redact, summarize and push selected sessions (use --dry-run to only print them)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			m, err := a.matcher(cfg)
			if err != nil {
				return err
			}
			reg, err := a.registry(cmd.Context())
			if err != nil {
				return err
			}
			st, err := a.openState()
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			var sinceD time.Duration
			if since != "" {
				if sinceD, err = config.ParseSince(since); err != nil {
					return err
				}
			}
			level := slog.LevelInfo
			if quiet {
				level = slog.LevelWarn
			}
			s := &gsync.Syncer{
				Registry: reg, Config: cfg, Matcher: m, State: st, Home: a.Home, Now: a.Now, Resolver: a.resolver(cfg),
				Log: slog.New(slog.NewTextHandler(a.Stderr, &slog.HandlerOptions{Level: level})),
			}
			var sink gsync.Sink = gsync.DryRunSink{W: a.Stdout}
			if !dry {
				cl, err := a.ingestClient(cfg)
				if err != nil {
					return err
				}
				sink = cl
			}
			rep, err := s.Run(cmd.Context(), gsync.RunOpts{Project: project, Source: source, Since: sinceD}, sink, dry)
			if err != nil {
				return err
			}
			if !quiet {
				verb := "sent"
				if dry {
					verb = "would be sent (dry run)"
				}
				fmt.Fprintf(a.Stderr, "%d sessions seen, %d not selected, %d unchanged, %d %s, %d warnings\n",
					rep.Seen, rep.NotSelected, rep.Unchanged, rep.Planned, verb, rep.Warnings)
				if len(rep.Redactions) > 0 {
					kinds := make([]string, 0, len(rep.Redactions))
					for k, n := range rep.Redactions {
						kinds = append(kinds, fmt.Sprintf("%s=%d", k, n))
					}
					sort.Strings(kinds)
					fmt.Fprintf(a.Stderr, "redactions: %s\n", strings.Join(kinds, " "))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "print the payloads that would be sent; send and record nothing")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "only print warnings and errors")
	cmd.Flags().StringVar(&project, "project", "", "only this project folder")
	cmd.Flags().StringVar(&source, "source", "", "only this source (e.g. claude-code)")
	cmd.Flags().StringVar(&since, "since", "", "override the since window (e.g. 7d)")
	return cmd
}

// --- share ---

func (a *app) shareCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "share", Short: "Manage which project folders are shared"}
	var purgeRemote, keepRemote bool
	mode := func() (string, error) {
		switch {
		case purgeRemote && keepRemote:
			return "", errors.New("--purge-remote and --keep-remote are mutually exclusive")
		case purgeRemote:
			return "yes", nil
		case keepRemote:
			return "no", nil
		}
		return "ask", nil
	}
	remove := &cobra.Command{Use: "remove <path>", Short: "Stop sharing a folder (and offer to delete its data from the server)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			m, err := mode()
			if err != nil {
				return err
			}
			return a.shareRemove(c.Context(), args[0], m)
		}}
	deny := &cobra.Command{Use: "deny <path>", Short: "Never share a folder (beats any allow rule)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			m, err := mode()
			if err != nil {
				return err
			}
			return a.shareDeny(c.Context(), args[0], m)
		}}
	for _, c := range []*cobra.Command{remove, deny} {
		c.Flags().BoolVar(&purgeRemote, "purge-remote", false, "also delete the un-shared projects' data from the server")
		c.Flags().BoolVar(&keepRemote, "keep-remote", false, "leave data already on the server in place")
	}
	cmd.AddCommand(
		&cobra.Command{Use: "add <path>", Short: "Share a folder (and everything under it)", Args: cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error { return a.shareAdd(c.Context(), args[0]) }},
		remove, deny, a.shareListCmd(),
	)
	return cmd
}

// ruleFor turns a user-typed path into the form stored in the config:
// home-relative with "~" when possible, glob characters left untouched.
// Chat projects ("claude-ai:Chats") are not paths and are stored as typed.
func (a *app) ruleFor(p string) string {
	if strings.ContainsAny(p, "*?[{") || pathutil.IsVirtualKey(p) {
		return p
	}
	abs := pathutil.Normalize(pathutil.ExpandHome(p, a.Home))
	return pathutil.Scrub(abs, pathutil.Normalize(a.Home))
}

// localKey is the project key sessions of this folder (or chat project) carry.
func (a *app) localKey(p string) string {
	if pathutil.IsVirtualKey(p) {
		return p
	}
	return pathutil.Normalize(pathutil.ExpandHome(p, a.Home))
}

func sameRule(a *app, x, y string) bool {
	if x == y {
		return true
	}
	if pathutil.IsVirtualKey(x) || pathutil.IsVirtualKey(y) {
		return false
	}
	nx := pathutil.Fold(pathutil.Normalize(pathutil.ExpandHome(x, a.Home)), pathutil.CaseInsensitiveFS())
	ny := pathutil.Fold(pathutil.Normalize(pathutil.ExpandHome(y, a.Home)), pathutil.CaseInsensitiveFS())
	return nx == ny
}

func (a *app) shareAdd(ctx context.Context, p string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	rule := a.ruleFor(p)
	for _, r := range cfg.Share.Allow {
		if sameRule(a, r, rule) {
			fmt.Fprintf(a.Stdout, "already shared: %s\n", rule)
			return nil
		}
	}
	cfg.Share.Allow = append(cfg.Share.Allow, rule)
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return err
	}
	if m, err := a.matcher(cfg); err == nil && m.Denied(a.localKey(p)) {
		fmt.Fprintf(a.Stderr, "warning: %s is also matched by a deny rule; deny wins, nothing will be shared\n", rule)
	}
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err := st.ClearTombstone(ctx, a.localKey(p)); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "sharing %s\n", rule)
	return nil
}

func (a *app) shareRemove(ctx context.Context, p, purge string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	rule := a.ruleFor(p)
	idx := -1
	for i, r := range cfg.Share.Allow {
		if sameRule(a, r, rule) {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Errorf("%s is not in the allow list", rule)
	}
	return a.change(ctx, cfg, func(c *config.Config) {
		c.Share.Allow = append(c.Share.Allow[:idx:idx], c.Share.Allow[idx+1:]...)
	}, "no longer sharing "+rule, purge)
}

func (a *app) shareDeny(ctx context.Context, p, purge string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	rule := a.ruleFor(p)
	for _, r := range cfg.Share.Deny {
		if sameRule(a, r, rule) {
			fmt.Fprintf(a.Stdout, "already denied: %s\n", rule)
			return nil
		}
	}
	return a.change(ctx, cfg, func(c *config.Config) { c.Share.Deny = append(c.Share.Deny, rule) }, "denying "+rule, purge)
}

// change applies an edit that may un-share projects. Every discovered project
// that was shared before and is not after is tombstoned locally, so it is
// never re-pushed.
func (a *app) change(ctx context.Context, before config.Config, edit func(*config.Config), msg, purge string) error {
	targets, err := a.applyChange(ctx, before, edit)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, msg)
	if len(targets) == 0 {
		return nil
	}
	fmt.Fprintf(a.Stdout, "%d project(s) un-shared; they will never be sent again.\n", len(targets))
	return a.offerRemoteDelete(ctx, before, targets, purge)
}

// applyChange edits the share rules, saves the config and does the local
// bookkeeping: projects that stop being shared are tombstoned, projects that
// start being shared lose any tombstone. It prints nothing; the returned
// targets are what the server may still hold for the un-shared projects.
func (a *app) applyChange(ctx context.Context, before config.Config, edit func(*config.Config)) ([]remoteTarget, error) {
	after := before
	after.Share.Allow = append([]string(nil), before.Share.Allow...)
	after.Share.Deny = append([]string(nil), before.Share.Deny...)
	after.Share.SourceDeny = append([]string(nil), before.Share.SourceDeny...)
	edit(&after)

	mBefore, err := a.matcher(before)
	if err != nil {
		return nil, err
	}
	mAfter, err := a.matcher(after)
	if err != nil {
		return nil, err
	}
	reg, err := a.registry(ctx)
	if err != nil {
		return nil, err
	}
	found, err := discover.Projects(ctx, reg, mBefore, time.Time{}, a.resolver(before))
	if err != nil {
		return nil, err
	}
	if err := config.Save(a.cfgPath(), after); err != nil {
		return nil, err
	}
	st, err := a.openState()
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()
	for _, p := range found.Projects { // newly shared projects are eligible to upload again
		if !mBefore.Decide(p.Key, "").Shared && mAfter.Decide(p.Key, "").Shared {
			if err := st.ClearTombstone(ctx, p.Key); err != nil {
				return nil, err
			}
		}
	}
	return a.unshare(ctx, st, found.Projects, mBefore, mAfter)
}

// remoteTarget is what to delete from the server for one un-shared project.
type remoteTarget struct {
	Label    string      // shown to the user
	Project  string      // delete this whole server project, or ...
	Sessions [][2]string // ... just these (source, id) pairs when the server project is shared with others
}

// unshare tombstones every project that stops being shared and works out what
// the server holds for it. A worktree is filed under its repository root; if
// the root stays shared, only that worktree's sessions are deleted, otherwise
// the whole server project goes (and any still-shared sibling filed under it
// is forgotten locally so its sessions upload again).
func (a *app) unshare(ctx context.Context, st *gsync.State, projects []discover.Project, before, after *selection.Matcher) ([]remoteTarget, error) {
	served := func(p discover.Project) string {
		if p.Root != "" && before.Decide(p.Root, "").Shared {
			return p.Root
		}
		return p.Key
	}
	var targets []remoteTarget
	wholeDeleted := map[string]bool{}
	for _, p := range projects {
		if !before.Decide(p.Key, "").Shared || after.Decide(p.Key, "").Shared {
			continue
		}
		srv := served(p)
		keys, err := st.SessionKeys(ctx, p.Key) // before the tombstone forgets them
		if err != nil {
			return nil, err
		}
		if err := st.Tombstone(ctx, p.Key, a.Now()); err != nil {
			return nil, err
		}
		if srv != p.Key && after.Decide(srv, "").Shared {
			if len(keys) > 0 {
				targets = append(targets, remoteTarget{Label: fmt.Sprintf("%d session(s) of %s", len(keys), pathutil.Scrub(p.Key, a.Home)), Sessions: keys})
			}
			continue
		}
		scrubbed := pathutil.Scrub(srv, a.Home)
		if !wholeDeleted[scrubbed] {
			wholeDeleted[scrubbed] = true
			targets = append(targets, remoteTarget{Label: scrubbed, Project: scrubbed})
		}
	}
	for _, q := range projects { // siblings that stay shared but were filed under a project we are deleting
		if after.Decide(q.Key, "").Shared && wholeDeleted[pathutil.Scrub(served(q), a.Home)] {
			if err := st.ForgetProject(ctx, q.Key); err != nil {
				return nil, err
			}
		}
	}
	return targets, nil
}

// offerRemoteDelete deletes un-shared projects from the server when asked
// to (or when the user answers yes). Failures are reported, not fatal: the
// local un-share has already happened.
func (a *app) offerRemoteDelete(ctx context.Context, cfg config.Config, targets []remoteTarget, purge string) error {
	if cfg.Server.URL == "" {
		return nil
	}
	switch purge {
	case "ask":
		if !a.Interactive {
			fmt.Fprintln(a.Stdout, "Data already on the server was left in place. Delete it with: goldengate purge --project PATH (or rerun with --purge-remote).")
			return nil
		}
		ans, err := a.prompt(fmt.Sprintf("Also delete %d project(s) from the server? [y/N] ", len(targets)))
		if err != nil || !strings.EqualFold(strings.TrimSpace(ans), "y") {
			fmt.Fprintln(a.Stdout, "Data already on the server was left in place.")
			return nil
		}
	case "no":
		fmt.Fprintln(a.Stdout, "Data already on the server was left in place.")
		return nil
	}
	_ = a.deleteTargets(ctx, cfg, targets, a.Stdout, a.Stderr) // reported; the local un-share already happened
	return nil
}

// deleteTargets removes un-shared projects from the server, reporting each
// outcome to out/errOut. Failures are reported, not fatal.
func (a *app) deleteTargets(ctx context.Context, cfg config.Config, targets []remoteTarget, out, errOut io.Writer) error {
	cl, err := a.ingestClient(cfg)
	if err != nil {
		fmt.Fprintf(errOut, "could not delete remote data: %v\n", err)
		return err
	}
	for _, t := range targets {
		if t.Project != "" {
			n, err := cl.DeleteProject(ctx, t.Project)
			if err != nil {
				fmt.Fprintf(errOut, "could not delete %s from the server: %v\n", t.Label, err)
				continue
			}
			fmt.Fprintf(out, "deleted %s from the server (%d sessions)\n", t.Label, n)
			continue
		}
		n := 0
		for _, k := range t.Sessions {
			d, err := cl.DeleteSession(ctx, k[0], k[1])
			if err != nil {
				fmt.Fprintf(errOut, "could not delete a session of %s: %v\n", t.Label, err)
				continue
			}
			n += d
		}
		fmt.Fprintf(out, "deleted %s from the server (%d sessions)\n", t.Label, n)
	}
	return nil
}

func (a *app) shareListCmd() *cobra.Command {
	var projects bool
	cmd := &cobra.Command{
		Use: "list", Short: "Show share rules (and with --projects, every discovered project and its decision)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			w := a.Stdout
			fmt.Fprintf(w, "default: %s   level: %s   since: %s\n", cfg.Share.Default, cfg.Share.Level, orNone(cfg.Share.Since))
			fmt.Fprintf(w, "sources: %s\n", strings.Join(cfg.Share.IncludeSources, ", "))
			printRules(w, "allow", cfg.Share.Allow)
			printRules(w, "deny", cfg.Share.Deny)
			if !projects {
				return nil
			}
			m, err := a.matcher(cfg)
			if err != nil {
				return err
			}
			reg, err := a.registry(c.Context())
			if err != nil {
				return err
			}
			var since time.Time
			if d, err := config.ParseSince(cfg.Share.Since); err == nil && d > 0 {
				since = a.Now().Add(-d)
			}
			res, err := discover.Projects(c.Context(), reg, m, since, a.resolver(cfg))
			if err != nil {
				return err
			}
			fmt.Fprintln(w, "\nprojects (local view, nothing here is sent):")
			for _, p := range res.Projects {
				mark := "[ ]"
				if p.Decision.Shared {
					mark = "[x]"
				} else if m.Denied(p.Key) {
					mark = "[-]"
				}
				extra := ""
				if p.Root != "" {
					extra = "  worktree of " + pathutil.Scrub(p.Root, a.Home)
				}
				fmt.Fprintf(w, "%s %-50s %4d sessions  last %s  (%s)%s\n", mark, pathutil.Scrub(p.Key, a.Home), p.Sessions,
					p.LastActivity.Local().Format("2006-01-02"), p.Decision.Reason, extra)
			}
			for _, wn := range res.Warnings {
				fmt.Fprintln(a.Stderr, "warning:", wn)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&projects, "projects", false, "also list discovered projects")
	return cmd
}

func printRules(w io.Writer, name string, rules []string) {
	if len(rules) == 0 {
		fmt.Fprintf(w, "%s: (none)\n", name)
		return
	}
	fmt.Fprintf(w, "%s:\n", name)
	for _, r := range rules {
		fmt.Fprintf(w, "  %s\n", r)
	}
}

func orNone(s string) string {
	if s == "" {
		return "no limit"
	}
	return s
}

// --- doctor ---

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use: "doctor", Short: "Check installed tools, config and local state (no network)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w := a.Stdout
			problems := 0
			fmt.Fprintf(w, "goldengate %s (%s/%s)\n\n", Version, runtime.GOOS, runtime.GOARCH)

			cfg, err := a.load()
			if err != nil {
				fmt.Fprintf(w, "config  FAIL  %v\n", err)
				problems++
			} else {
				fmt.Fprintf(w, "config  ok    %s (default=%s, level=%s)\n", a.cfgPath(), cfg.Share.Default, cfg.Share.Level)
			}
			if st, err := a.openState(); err != nil {
				fmt.Fprintf(w, "state   FAIL  %v\n", err)
				problems++
			} else {
				n, _ := st.Count(c.Context())
				_ = st.Close()
				fmt.Fprintf(w, "state   ok    %s (%d sessions recorded)\n", a.dirs().StatePath(), n)
			}
			reg, err := a.registry(c.Context())
			if err != nil {
				return err
			}
			for _, ad := range reg.All() {
				det, err := ad.Detect(c.Context())
				switch {
				case err != nil:
					fmt.Fprintf(w, "%-8s FAIL  %v\n", ad.Name(), err)
					problems++
				case !det.Installed:
					fmt.Fprintf(w, "%-8s --    not found (%s)\n", ad.Name(), strings.Join(det.Notes, "; "))
				default:
					n, skipped := 0, 0
					for _, lerr := range ad.ListSessions(c.Context(), adapters.ListOpts{}) {
						if lerr != nil {
							skipped++
							continue
						}
						n++
					}
					fmt.Fprintf(w, "%-8s ok    %s: %d sessions, %d skipped (unknown shape or too large)\n", ad.Name(), filepath.ToSlash(pathutil.Scrub(det.Root, a.Home)), n, skipped)
				}
			}
			if cfg.Server.URL == "" {
				fmt.Fprintln(w, "server  --    not configured (run `goldengate init`)")
			} else {
				problems += a.doctorServer(c.Context(), cfg)
			}
			if problems > 0 {
				return fmt.Errorf("%d problem(s) found", problems)
			}
			return nil
		},
	}
}
