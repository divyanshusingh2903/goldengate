package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
)

func daemonSetting(t *testing.T, h *harness) string {
	t.Helper()
	a := &app{Deps: h.deps}
	cfg, err := a.load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sync.Daemon
}

func TestInstallServiceOnLinux(t *testing.T) {
	h, ran, home := installHarness(t, "linux")
	dir := filepath.Join(home, "xdg", "systemd", "user")

	// --print changes nothing, not even the setting
	if err := h.run("install-service", "--print"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil || len(*ran) != 0 || daemonSetting(t, h) != "" || !strings.Contains(h.out.String(), "nothing was changed") {
		t.Fatalf("--print: ran %v setting %q\n%s", *ran, daemonSetting(t, h), h.out)
	}

	// an older timer is replaced
	if err := h.run("install-schedule"); err != nil {
		t.Fatal(err)
	}
	*ran = nil
	if err := h.run("install-service"); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(filepath.Join(dir, "goldengate-daemon.service"))
	if !strings.Contains(string(unit), `ExecStart="/opt/golden gate/bin/goldengate" daemon`+"\n") || !strings.Contains(string(unit), "Restart=on-failure") ||
		!strings.Contains(string(unit), "WantedBy=default.target") {
		t.Errorf("unit:\n%s", unit)
	}
	for _, bad := range []string{"--approve", "TOKEN", "ggb_"} {
		if strings.Contains(string(unit), bad) {
			t.Errorf("unit contains %q", bad)
		}
	}
	for _, f := range []string{"goldengate-sync.timer", "goldengate-sync.service"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is removed when the daemon is installed", f)
		}
	}
	got := strings.Join(*ran, " | ")
	if !strings.Contains(got, "disable --now goldengate-sync.timer") || !strings.Contains(got, "enable --now goldengate-daemon.service") ||
		!strings.Contains(got, "systemd-run --user --wait --pipe --collect --quiet /opt/golden gate/bin/goldengate daemon --check") {
		t.Errorf("commands: %s", got)
	}
	if daemonSetting(t, h) != "on" {
		t.Errorf("the setting follows the service: %q", daemonSetting(t, h))
	}
	if !strings.Contains(h.out.String(), "background sending is on") || !strings.Contains(h.out.String(), "goldengate daemon status") {
		t.Errorf("says:\n%s", h.out)
	}

	*ran = nil
	if err := h.run("uninstall-service"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "goldengate-daemon.service")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the unit is removed")
	}
	if !strings.Contains(strings.Join(*ran, " | "), "disable --now goldengate-daemon.service") || daemonSetting(t, h) != "off" {
		t.Errorf("uninstall: %v setting %q", *ran, daemonSetting(t, h))
	}
}

// With no systemd user session (an SSH login, a container) the install fails
// and the setting stays as it was: it can never say "on" without a service.
func TestInstallServiceFailureLeavesTheSettingAlone(t *testing.T) {
	h, _, _ := installHarness(t, "linux")
	h.deps.RunCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 1 && args[1] == "enable" {
			return []byte("Failed to connect to bus: No medium found"), errors.New("exit status 1")
		}
		return nil, nil
	}
	err := h.run("install-service")
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("the install must fail with the reason: %v", err)
	}
	if daemonSetting(t, h) != "" {
		t.Errorf("setting changed to %q although nothing was installed", daemonSetting(t, h))
	}
}

// The service's own check fails (a token the service cannot read): it is
// installed, and the user is told how to fix it.
func TestInstallServiceWarnsWhenTheServiceCannotReadTheToken(t *testing.T) {
	h, _, _ := installHarness(t, "linux")
	h.deps.RunCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemd-run" {
			return []byte("upload token: not found"), errors.New("exit status 1")
		}
		return nil, nil
	}
	if err := h.run("install-service"); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	if !strings.Contains(out, "could not pass its own check") || !strings.Contains(out, "DBUS_SESSION_BUS_ADDRESS") || !strings.Contains(out, "secrets.json") {
		t.Errorf("the warning says how to fix it:\n%s", out)
	}
}

func TestInstallServiceOnMacAndWindows(t *testing.T) {
	h, ran, home := installHarness(t, "darwin")
	if err := h.run("install-service"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "dev.goldengate.daemon.plist"))
	if !strings.Contains(string(b), "<string>daemon</string>") || !strings.Contains(string(b), "<key>KeepAlive</key><true/>") || !strings.Contains(string(b), "<key>RunAtLoad</key><true/>") {
		t.Errorf("plist:\n%s", b)
	}
	if !strings.Contains(strings.Join(*ran, " | "), "launchctl load -w "+filepath.Join(home, "Library", "LaunchAgents", "dev.goldengate.daemon.plist")) {
		t.Errorf("commands: %v", *ran)
	}
	if err := h.run("uninstall-service"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", "dev.goldengate.daemon.plist")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the plist is removed")
	}

	h, ran, _ = installHarness(t, "windows")
	if err := h.run("install-service"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*ran, " | "); !strings.Contains(got, `schtasks /Create /F /SC ONLOGON /TN Golden Gate Daemon /TR "/opt/golden gate/bin/goldengate" daemon`) {
		t.Errorf("commands: %s", got)
	}
}

// The question is asked once, where automatic sending starts: a project goes
// to trust auto. Yes installs the service, no is remembered.
func TestOfferDaemonAsksOnceAtTrustAuto(t *testing.T) {
	h, ran, _ := installHarness(t, "linux")
	h.deps.Interactive = true
	if err := h.run("share", "add", "~/projects/demo"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.errOut.String()+h.out.String(), "background") {
		t.Fatalf("no question while nothing sends on its own:\n%s%s", h.out, h.errOut)
	}
	if err := h.run("projects", "list"); err != nil {
		t.Fatal(err)
	}
	var name string
	cfg, _ := (&app{Deps: h.deps}).load()
	name = cfg.Projects[0].Name

	h.deps.Stdin = strings.NewReader("n\n")
	if err := h.run("projects", "trust", name, "auto"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "Send automatically in the background?") || !strings.Contains(h.out.String(), "not now") {
		t.Errorf("asked and remembered:\n%s", h.out)
	}
	if daemonSetting(t, h) != "off" || len(*ran) != 0 {
		t.Fatalf("no: setting %q, ran %v", daemonSetting(t, h), *ran)
	}
	// not asked again
	h.deps.Stdin = strings.NewReader("y\n")
	if err := h.run("projects", "trust", name, "auto"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.out.String(), "Send automatically") {
		t.Error("asked twice")
	}

	// a user who said not now can still turn it on
	if err := h.run("install-service"); err != nil || daemonSetting(t, h) != "on" {
		t.Fatalf("install-service: %v %q", err, daemonSetting(t, h))
	}
}

func TestOfferDaemonYesInstallsAndNoTerminalOnlyHints(t *testing.T) {
	h, ran, _ := installHarness(t, "linux")
	h.deps.Interactive = true
	if err := h.run("share", "add", "~/projects/demo"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := (&app{Deps: h.deps}).load()
	name := cfg.Projects[0].Name
	h.deps.Stdin = strings.NewReader("yes\n")
	if err := h.run("projects", "trust", name, "auto"); err != nil {
		t.Fatal(err)
	}
	if daemonSetting(t, h) != "on" || !strings.Contains(strings.Join(*ran, " | "), "enable --now goldengate-daemon.service") {
		t.Fatalf("yes: setting %q, ran %v", daemonSetting(t, h), *ran)
	}

	// no terminal: nothing is asked, nothing changes, the command is named
	h, ran, _ = installHarness(t, "linux")
	if err := h.run("share", "add", "~/projects/demo", "--auto-approve"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "goldengate install-service") || daemonSetting(t, h) != "" || len(*ran) != 0 {
		t.Errorf("non-interactive: setting %q ran %v\n%s", daemonSetting(t, h), *ran, h.out)
	}
}

func TestDaemonConfigValuesSurviveSave(t *testing.T) {
	h, _, _ := installHarness(t, "linux")
	a := &app{Deps: h.deps}
	cfg, _ := a.load()
	zero := 0
	cfg.Sync = config.Sync{SettleMinutes: &zero, Daemon: "on"}
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := h.run("share", "add", "~/projects/demo"); err != nil { // another command rewrites the file
		t.Fatal(err)
	}
	got, _ := a.load()
	if got.Sync.Settle() != 0 || got.Sync.Daemon != "on" {
		t.Errorf("other commands keep [sync]: %+v", got.Sync)
	}
}
