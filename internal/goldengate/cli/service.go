package cli

import (
	"context"
	"errors"
	"fmt"
	"html"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
)

const (
	daemonUnit  = "goldengate-daemon"
	daemonLabel = "dev.goldengate.daemon"
	daemonTask  = "Golden Gate Daemon"
)

// serviceSteps are the steps that install (or remove) the daemon as a user
// service of this operating system. Installing also removes the timer of
// install-schedule, so the two do not both run (the run lock would keep them
// from sending twice, but one is enough).
func (a *app) serviceSteps(uninstall bool) ([]step, error) {
	exe, err := a.executable()
	if err != nil {
		return nil, err
	}
	var steps []step
	// the timer goes first: every command is optional, since there may be none
	if !uninstall {
		old, err := a.scheduleSteps(30*time.Minute, true)
		if err == nil {
			for _, s := range old {
				s.optional = true
				steps = append(steps, s)
			}
		}
	}
	switch a.goos() {
	case "linux":
		dir := filepath.Join(a.configHome(), "systemd", "user")
		unit := filepath.Join(dir, daemonUnit+".service")
		if uninstall {
			return []step{
				{argv: []string{"systemctl", "--user", "disable", "--now", daemonUnit + ".service"}, optional: true},
				{path: unit, remove: true},
				{argv: []string{"systemctl", "--user", "daemon-reload"}, optional: true},
			}, nil
		}
		body := "[Unit]\nDescription=Golden Gate: send approved sessions in the background (goldengate daemon)\n\n" +
			"[Service]\nExecStart=" + systemdQuote(exe) + " daemon\nRestart=on-failure\nRestartSec=30\n\n" +
			"[Install]\nWantedBy=default.target\n"
		return append(steps,
			step{path: unit, content: body},
			step{argv: []string{"systemctl", "--user", "daemon-reload"}},
			step{argv: []string{"systemctl", "--user", "enable", "--now", daemonUnit + ".service"}},
		), nil
	case "darwin":
		plist := filepath.Join(a.Home, "Library", "LaunchAgents", daemonLabel+".plist")
		if uninstall {
			return []step{{argv: []string{"launchctl", "unload", "-w", plist}, optional: true}, {path: plist, remove: true}}, nil
		}
		log := filepath.Join(a.Home, "Library", "Logs", "goldengate-daemon.log")
		body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + daemonLabel + `</string>
  <key>ProgramArguments</key>
  <array><string>` + html.EscapeString(exe) + `</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>` + html.EscapeString(log) + `</string>
</dict>
</plist>
`
		return append(steps, step{path: plist, content: body}, step{argv: []string{"launchctl", "load", "-w", plist}}), nil
	case "windows":
		if uninstall {
			return []step{
				{argv: []string{"schtasks", "/End", "/TN", daemonTask}, optional: true},
				{argv: []string{"schtasks", "/Delete", "/TN", daemonTask, "/F"}, optional: true},
			}, nil
		}
		return append(steps,
			step{argv: []string{"schtasks", "/Create", "/F", "/SC", "ONLOGON", "/TN", daemonTask, "/TR", `"` + exe + `" daemon`}},
			step{argv: []string{"schtasks", "/Run", "/TN", daemonTask}, optional: true},
		), nil
	}
	return nil, fmt.Errorf("no service manager is supported on %s: run `goldengate daemon` from your own", a.goos())
}

// checkFromService runs `goldengate daemon --check` the way the service
// manager will run the daemon, where that can be done: on Linux, in a
// transient unit of the user's systemd. It returns what to tell the user
// ("" when all is well, or when the check cannot be made here).
func (a *app) checkFromService(ctx context.Context) string {
	if a.goos() != "linux" {
		return ""
	}
	exe, err := a.executable()
	if err != nil {
		return ""
	}
	out, err := a.runCommand(ctx, "systemd-run", "--user", "--wait", "--pipe", "--collect", "--quiet", exe, "daemon", "--check")
	if err != nil {
		return "the background process could not pass its own check:\n" + indent(strings.TrimSpace(string(out)))
	}
	return ""
}

// setDaemon turns background sending on or off in one place, so the setting
// and the service cannot disagree: the service is installed (or removed)
// first, and sync.daemon is written only when that worked. With printOnly
// nothing changes. warn is what to tell the user when the service was
// installed but its own check failed (it will run, and say what is wrong in
// `goldengate daemon status`).
func (a *app) setDaemon(ctx context.Context, on, printOnly bool) (warn string, err error) {
	cfg, err := a.load()
	if err != nil {
		return "", err
	}
	if on && cfg.Server.URL == "" && !printOnly {
		return "", errors.New("no server configured: run `goldengate init` first")
	}
	steps, err := a.serviceSteps(!on)
	if err != nil {
		return "", err
	}
	if err := a.apply(ctx, steps, printOnly); err != nil {
		return "", err
	}
	if printOnly {
		return "", nil
	}
	cfg.Sync.Daemon = "off"
	if on {
		cfg.Sync.Daemon = "on"
	}
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return "", err
	}
	if on {
		warn = a.checkFromService(ctx)
	}
	return warn, nil
}

func (a *app) serviceCmd(uninstall bool) *cobra.Command {
	var printOnly bool
	use, short := "install-service", "Send in the background: run `goldengate daemon` as a service of your user"
	if uninstall {
		use, short = "uninstall-service", "Stop sending in the background and remove the service installed by install-service"
	}
	cmd := &cobra.Command{Use: use, Short: short,
		Long: short + ".\n" + whatAutoSends + "\n\n" +
			"  systemd user service on Linux, launchd agent on macOS, Task Scheduler task at logon on Windows.\n" +
			"  --print shows the files and commands without changing anything.\n" +
			"  Installing replaces the timer of install-schedule.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			warn, err := a.setDaemon(c.Context(), !uninstall, printOnly)
			if err != nil {
				return err
			}
			if printOnly {
				fmt.Fprintln(a.Stdout, "\nnothing was changed (--print)")
				return nil
			}
			if uninstall {
				fmt.Fprintln(a.Stdout, "\nbackground sending is off: the daemon is stopped and removed.")
				return nil
			}
			cfg, _ := a.load()
			fmt.Fprintf(a.Stdout, "\nbackground sending is on: `goldengate daemon` runs as a service.\n%s\n%scheck it with: goldengate daemon status\nturn it off with: goldengate uninstall-service\n", whatAutoSends, autoHint(cfg))
			if warn != "" {
				fmt.Fprintf(a.Stdout, "\nwarning: %s\n%s\n", warn, tokenFix)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "show what would be written and run; change nothing")
	return cmd
}

// daemonQuestion is what the user is asked, once, when automatic sending
// starts to matter.
const daemonQuestion = "Send automatically in the background? Golden Gate will run a small background process that sends\n" +
	"only what you or your rules approved (a receipt lists each send), and does nothing else."

// offerDaemon asks the one question, at the moment a project goes to trust
// auto (or a rule offer is accepted, or init finds one): the user will not
// remember to send by hand. It asks only when the answer is not known yet and
// some project sends on its own; "not now" is remembered. Where nobody can
// answer, it says what to run. It never fails the command that called it.
func (a *app) offerDaemon(ctx context.Context) {
	cfg, err := a.load()
	if err != nil || cfg.Sync.Daemon != "" || cfg.Server.URL == "" {
		return
	}
	anyAuto := false
	for _, p := range cfg.Projects {
		anyAuto = anyAuto || p.Auto()
	}
	if !anyAuto {
		return
	}
	if !a.Interactive {
		fmt.Fprintln(a.Stdout, "\nTo send in the background without remembering: goldengate install-service")
		return
	}
	fmt.Fprintln(a.Stdout, "\n"+daemonQuestion)
	ans, err := a.prompt("Turn on background sending? [y/N] ")
	if err != nil {
		return
	}
	if ans = strings.ToLower(strings.TrimSpace(ans)); ans == "y" || ans == "yes" {
		warn, err := a.setDaemon(ctx, true, false)
		if err != nil {
			fmt.Fprintf(a.Stdout, "could not turn it on: %v\nnothing was changed; try again with: goldengate install-service\n", err)
			return
		}
		fmt.Fprintln(a.Stdout, "background sending is on (goldengate daemon status shows what it does)")
		if warn != "" {
			fmt.Fprintf(a.Stdout, "warning: %s\n%s\n", warn, tokenFix)
		}
		return
	}
	cfg.Sync.Daemon = "off"
	if err := config.Save(a.cfgPath(), cfg); err == nil {
		fmt.Fprintln(a.Stdout, "ok, not now. Turn it on any time: goldengate install-service")
	}
}
