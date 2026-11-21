package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
)

// Unattended sync is `goldengate sync --auto`, run by the operating system's
// own scheduler or by a tool's hook. The installers below only write the
// small files those mechanisms need, and say exactly what they wrote; every
// one has --print to look first and an uninstall command.

const (
	systemdUnit = "goldengate-sync"
	launchLabel = "dev.goldengate.sync"
	taskName    = "Golden Gate Sync"
)

// step is one thing an installer does: write a file, or run a command.
type step struct {
	path    string // a file to write (or remove) ...
	content string
	remove  bool
	argv    []string // ... or a command to run
	// optional commands may fail without failing the install (stopping a
	// timer that is not running).
	optional bool
}

func (a *app) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return defaultGOOS
}

func (a *app) executable() (string, error) {
	if a.Executable != nil {
		return a.Executable()
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot tell where goldengate is installed: %w", err)
	}
	return filepath.EvalSymlinks(exe)
}

func (a *app) configHome() string {
	if v := a.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(a.Home, ".config")
}

// scheduleSteps are the steps that install (or remove) the timer on this
// operating system.
func (a *app) scheduleSteps(every time.Duration, uninstall bool) ([]step, error) {
	exe, err := a.executable()
	if err != nil {
		return nil, err
	}
	mins := int(every / time.Minute)
	switch a.goos() {
	case "linux":
		dir := filepath.Join(a.configHome(), "systemd", "user")
		svc, tmr := filepath.Join(dir, systemdUnit+".service"), filepath.Join(dir, systemdUnit+".timer")
		if uninstall {
			return []step{
				{argv: []string{"systemctl", "--user", "disable", "--now", systemdUnit + ".timer"}, optional: true},
				{path: tmr, remove: true}, {path: svc, remove: true},
				{argv: []string{"systemctl", "--user", "daemon-reload"}, optional: true},
			}, nil
		}
		service := "[Unit]\nDescription=Golden Gate: send approved sessions (goldengate sync --auto)\n\n" +
			"[Service]\nType=oneshot\nExecStart=" + systemdQuote(exe) + " sync --auto\n"
		timer := "[Unit]\nDescription=Golden Gate: run the background send every " + strconv.Itoa(mins) + " minutes\n\n" +
			"[Timer]\nOnBootSec=5min\nOnUnitActiveSec=" + strconv.Itoa(mins) + "min\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n"
		return []step{
			{path: svc, content: service}, {path: tmr, content: timer},
			{argv: []string{"systemctl", "--user", "daemon-reload"}},
			{argv: []string{"systemctl", "--user", "enable", "--now", systemdUnit + ".timer"}},
		}, nil
	case "darwin":
		plist := filepath.Join(a.Home, "Library", "LaunchAgents", launchLabel+".plist")
		if uninstall {
			return []step{{argv: []string{"launchctl", "unload", "-w", plist}, optional: true}, {path: plist, remove: true}}, nil
		}
		log := filepath.Join(a.Home, "Library", "Logs", "goldengate-sync.log")
		body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + launchLabel + `</string>
  <key>ProgramArguments</key>
  <array><string>` + html.EscapeString(exe) + `</string><string>sync</string><string>--auto</string></array>
  <key>StartInterval</key><integer>` + strconv.Itoa(mins*60) + `</integer>
  <key>StandardErrorPath</key><string>` + html.EscapeString(log) + `</string>
</dict>
</plist>
`
		return []step{{path: plist, content: body}, {argv: []string{"launchctl", "load", "-w", plist}}}, nil
	case "windows":
		if uninstall {
			return []step{{argv: []string{"schtasks", "/Delete", "/TN", taskName, "/F"}}}, nil
		}
		return []step{{argv: []string{"schtasks", "/Create", "/F", "/SC", "MINUTE", "/MO", strconv.Itoa(mins), "/TN", taskName,
			"/TR", `"` + exe + `" sync --auto --quiet`}}}, nil
	}
	return nil, fmt.Errorf("no scheduler is supported on %s: run `goldengate sync --auto` from your own scheduler", a.goos())
}

// systemdQuote writes a path as one ExecStart word.
func systemdQuote(p string) string {
	p = strings.ReplaceAll(p, "%", "%%")
	if !strings.ContainsAny(p, " \t\"'\\") {
		return p
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
}

// apply prints the steps, and unless printOnly carries them out.
func (a *app) apply(ctx context.Context, steps []step, printOnly bool) error {
	for _, s := range steps {
		switch {
		case s.remove:
			fmt.Fprintf(a.Stdout, "remove %s\n", s.path)
		case s.path != "":
			fmt.Fprintf(a.Stdout, "write %s:\n%s\n", s.path, indent(s.content))
		default:
			fmt.Fprintf(a.Stdout, "run: %s\n", strings.Join(s.argv, " "))
		}
		if printOnly {
			continue
		}
		switch {
		case s.remove:
			if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		case s.path != "":
			if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil { //nolint:gosec // G301: the scheduler's own directory, which holds no secrets
				return err
			}
			if err := os.WriteFile(s.path, []byte(s.content), 0o644); err != nil { //nolint:gosec // G306: a unit file the scheduler must read; it holds no secrets
				return err
			}
		default:
			out, err := a.runCommand(ctx, s.argv[0], s.argv[1:]...)
			if err != nil && !s.optional {
				return fmt.Errorf("%s failed: %w\n%s", strings.Join(s.argv, " "), err, strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

func (a *app) runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if a.RunCommand != nil {
		return a.RunCommand(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // G204: fixed scheduler tools with arguments built above
}

const whatAutoSends = "It sends only sessions you approved or your rules approve, never what waits in the inbox,\nand lists everything it sent in a receipt (goldengate receipts)."

func (a *app) installScheduleCmd() *cobra.Command {
	var every string
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "install-schedule",
		Short: "Run `goldengate sync --auto` on a timer (systemd, launchd or Task Scheduler)",
		Long: "Installs a timer for your user that runs `goldengate sync --auto`.\n" + whatAutoSends + "\n\n" +
			"  --print shows the files and commands without changing anything.\n  goldengate uninstall-schedule removes it.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			d, err := time.ParseDuration(every)
			if err != nil || d < 5*time.Minute || d > 24*time.Hour {
				return fmt.Errorf("--every takes a duration between 5m and 24h, got %q", every)
			}
			cfg, err := a.load()
			if err != nil {
				return err
			}
			if cfg.Server.URL == "" && !printOnly {
				return errors.New("no server configured: run `goldengate init` first")
			}
			steps, err := a.scheduleSteps(d, false)
			if err != nil {
				return err
			}
			if err := a.apply(c.Context(), steps, printOnly); err != nil {
				return err
			}
			if printOnly {
				fmt.Fprintln(a.Stdout, "\nnothing was changed (--print)")
				return nil
			}
			fmt.Fprintf(a.Stdout, "\ninstalled: `goldengate sync --auto` runs every %s.\n%s\n%sremove with: goldengate uninstall-schedule\n", d, whatAutoSends, autoHint(cfg))
			return nil
		},
	}
	cmd.Flags().StringVar(&every, "every", "30m", "how often to run (5m to 24h)")
	cmd.Flags().BoolVar(&printOnly, "print", false, "show what would be written and run; change nothing")
	return cmd
}

// autoHint tells a user whose projects are all at "ask" that the timer will
// send nothing new until they approve sessions or raise a trust level.
func autoHint(cfg config.Config) string {
	for _, p := range cfg.Projects {
		if p.Auto() {
			return ""
		}
	}
	return "None of your projects is at trust level auto, so it will send only what you approve in the inbox.\nTo let a project send on its own: goldengate projects trust <project> auto\n"
}

func (a *app) uninstallScheduleCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{Use: "uninstall-schedule", Short: "Remove the timer installed by install-schedule", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			steps, err := a.scheduleSteps(30*time.Minute, true)
			if err != nil {
				return err
			}
			if err := a.apply(c.Context(), steps, printOnly); err != nil {
				return err
			}
			if !printOnly {
				fmt.Fprintln(a.Stdout, "\nremoved: nothing runs `goldengate sync --auto` on a timer any more")
			}
			return nil
		}}
	cmd.Flags().BoolVar(&printOnly, "print", false, "show what would be removed and run; change nothing")
	return cmd
}

// --- Claude Code hook ---

// claudeSettings is where Claude Code keeps the user's settings.
func (a *app) claudeSettings() string {
	dir := a.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(a.Home, ".claude")
	}
	return filepath.Join(dir, "settings.json")
}

// hookMarker finds our hook in Claude Code's settings, also as written by
// earlier builds (without --settle).
const hookMarker = "sync --auto --quiet --source claude-code"

// hookCommand runs when a Claude Code session ends. The session is finished,
// so it need not wait for the settle window (--settle 0).
func hookCommand(exe string) string {
	if strings.ContainsAny(exe, " \t") {
		exe = `"` + exe + `"`
	}
	return exe + " " + hookMarker + " --settle 0"
}

// editHook adds (or removes) the SessionEnd hook in a Claude Code settings
// document and reports whether anything changed. Everything else in the
// document is kept; an existing Golden Gate hook is replaced, not doubled.
func editHook(doc map[string]any, command string, remove bool) bool {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		if remove {
			return false
		}
		hooks = map[string]any{}
	}
	entries, _ := hooks["SessionEnd"].([]any)
	var kept []any
	found, same := false, false
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		var keepInner []any
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, hookMarker) {
				found = true
				same = same || cmd == command
				continue
			}
			keepInner = append(keepInner, h)
		}
		if entry == nil || len(keepInner) == len(inner) {
			kept = append(kept, e) // not ours: untouched
			continue
		}
		if len(keepInner) > 0 {
			entry["hooks"] = keepInner
			kept = append(kept, entry)
		}
	}
	if remove {
		if !found {
			return false
		}
	} else {
		if found && same && len(kept) == len(entries)-1 {
			// exactly our hook, alone in its entry: put it back as it was
			return false
		}
		kept = append(kept, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}})
	}
	if len(kept) == 0 {
		delete(hooks, "SessionEnd")
	} else {
		hooks["SessionEnd"] = kept
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooks
	}
	return true
}

func (a *app) hookCmd(uninstall bool) *cobra.Command {
	var printOnly bool
	use, short := "install-hook claude-code", "Run `goldengate sync --auto` when a Claude Code session ends"
	if uninstall {
		use, short = "uninstall-hook claude-code", "Remove the hook installed by install-hook"
	}
	cmd := &cobra.Command{Use: use, Short: short,
		Long: short + ".\nThe hook is a SessionEnd entry in Claude Code's settings.json; everything else in that file is kept,\nand a copy of the file as it was is left next to it (settings.json.goldengate-backup).\n" + whatAutoSends,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if args[0] != "claude-code" {
				return fmt.Errorf("only claude-code has a hook; for %s use `goldengate install-schedule`", args[0])
			}
			exe, err := a.executable()
			if err != nil {
				return err
			}
			path := a.claudeSettings()
			raw, err := os.ReadFile(path) //nolint:gosec // G304: Claude Code's own settings file
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			doc := map[string]any{}
			if len(bytes.TrimSpace(raw)) > 0 {
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.UseNumber() // numbers go back exactly as they were
				if err := dec.Decode(&doc); err != nil {
					return fmt.Errorf("%s is not valid JSON, so it was left alone: %w", path, err)
				}
			}
			if !editHook(doc, hookCommand(exe), uninstall) {
				if uninstall {
					fmt.Fprintf(a.Stdout, "no Golden Gate hook in %s: nothing to remove\n", path)
				} else {
					fmt.Fprintf(a.Stdout, "the hook is already in %s\n", path)
				}
				return nil
			}
			out, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return err
			}
			out = append(out, '\n')
			if printOnly {
				fmt.Fprintf(a.Stdout, "%s would become:\n%s\nnothing was changed (--print)\n", path, indent(string(out)))
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if len(raw) > 0 {
				if err := os.WriteFile(path+".goldengate-backup", raw, 0o600); err != nil {
					return err
				}
			}
			if err := os.WriteFile(path, out, 0o600); err != nil {
				return err
			}
			if uninstall {
				fmt.Fprintf(a.Stdout, "removed the Golden Gate hook from %s\n", path)
				return nil
			}
			fmt.Fprintf(a.Stdout, "added to %s: when a Claude Code session ends, it runs\n    %s\n%s\nremove with: goldengate uninstall-hook claude-code\n", path, hookCommand(exe), whatAutoSends)
			return nil
		}}
	cmd.Flags().BoolVar(&printOnly, "print", false, "show the resulting settings file; change nothing")
	return cmd
}
