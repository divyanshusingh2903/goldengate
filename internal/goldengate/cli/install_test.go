package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installHarness is a harness whose home is a real temporary directory and
// whose scheduler commands are recorded instead of run.
func installHarness(t *testing.T, goos string) (*harness, *[]string, string) {
	t.Helper()
	h := newHarness(t)
	home := t.TempDir()
	var ran []string
	get := h.deps.Getenv
	h.deps.Home, h.deps.GOOS = home, goos
	h.deps.Getenv = func(k string) string {
		switch k {
		case "XDG_CONFIG_HOME":
			return filepath.Join(home, "xdg")
		case "CLAUDE_CONFIG_DIR":
			return filepath.Join(home, "claude-cfg")
		}
		return get(k)
	}
	h.deps.Executable = func() (string, error) { return "/opt/golden gate/bin/goldengate", nil }
	h.deps.RunCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	// a configured server, so the installers do not send the user to init
	cfgDir := filepath.Join(home, "xdg", "goldengate")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("version = 3\n[server]\nurl = \"https://bridge.example.com\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, &ran, home
}

func TestInstallScheduleOnLinuxWritesAUserTimer(t *testing.T) {
	h, ran, home := installHarness(t, "linux")
	dir := filepath.Join(home, "xdg", "systemd", "user")

	// --print changes nothing
	if err := h.run("install-schedule", "--print"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil || len(*ran) != 0 || !strings.Contains(h.out.String(), "nothing was changed") || !strings.Contains(h.out.String(), "sync --auto") {
		t.Fatalf("--print must only show: ran %v\n%s", *ran, h.out)
	}

	if err := h.run("install-schedule", "--every", "15m"); err != nil {
		t.Fatal(err)
	}
	svc, _ := os.ReadFile(filepath.Join(dir, "goldengate-sync.service"))
	tmr, _ := os.ReadFile(filepath.Join(dir, "goldengate-sync.timer"))
	if !strings.Contains(string(svc), `ExecStart="/opt/golden gate/bin/goldengate" sync --auto`+"\n") || !strings.Contains(string(svc), "Type=oneshot") {
		t.Errorf("service:\n%s", svc)
	}
	if !strings.Contains(string(tmr), "OnUnitActiveSec=15min") || !strings.Contains(string(tmr), "WantedBy=timers.target") {
		t.Errorf("timer:\n%s", tmr)
	}
	if got := strings.Join(*ran, " | "); got != "systemctl --user daemon-reload | systemctl --user enable --now goldengate-sync.timer" {
		t.Errorf("commands: %s", got)
	}
	out := h.out.String()
	for _, want := range []string{"runs every 15m", "never what waits in the inbox", "goldengate receipts", "None of your projects is at trust level auto", "goldengate uninstall-schedule"} {
		if !strings.Contains(out, want) {
			t.Errorf("install says %q:\n%s", want, out)
		}
	}
	// the unit never approves and never passes a secret
	for _, bad := range []string{"--approve", "TOKEN", "ggb_"} {
		if strings.Contains(string(svc)+string(tmr), bad) {
			t.Errorf("unit contains %q", bad)
		}
	}

	*ran = nil
	if err := h.run("uninstall-schedule"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "goldengate-sync.timer")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the timer file is removed")
	}
	if !strings.Contains(strings.Join(*ran, " | "), "disable --now goldengate-sync.timer") {
		t.Errorf("uninstall commands: %v", *ran)
	}

	for _, bad := range []string{"1m", "48h", "often"} {
		if err := h.run("install-schedule", "--every", bad); err == nil {
			t.Errorf("--every %s must be refused", bad)
		}
	}
}

func TestInstallScheduleOnMacAndWindows(t *testing.T) {
	h, ran, home := installHarness(t, "darwin")
	if err := h.run("install-schedule"); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "dev.goldengate.sync.plist")
	b, _ := os.ReadFile(plist)
	for _, want := range []string{"<string>/opt/golden gate/bin/goldengate</string><string>sync</string><string>--auto</string>", "<integer>1800</integer>", "dev.goldengate.sync"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %q:\n%s", want, b)
		}
	}
	if len(*ran) != 1 || (*ran)[0] != "launchctl load -w "+plist {
		t.Errorf("launchctl: %v", *ran)
	}

	h, ran, _ = installHarness(t, "windows")
	if err := h.run("install-schedule", "--every", "1h"); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 1 || !strings.Contains((*ran)[0], `schtasks /Create /F /SC MINUTE /MO 60 /TN Golden Gate Sync /TR "/opt/golden gate/bin/goldengate" sync --auto --quiet`) {
		t.Errorf("schtasks: %v", *ran)
	}

	h, _, _ = installHarness(t, "plan9")
	if err := h.run("install-schedule"); err == nil || !strings.Contains(err.Error(), "goldengate sync --auto") {
		t.Errorf("an unsupported system says what to run instead: %v", err)
	}

	// a failing scheduler command fails the install and shows its output
	h, _, _ = installHarness(t, "linux")
	h.deps.RunCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Failed to connect to bus"), errors.New("exit status 1")
	}
	if err := h.run("install-schedule"); err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Errorf("a failed command is reported: %v", err)
	}
	// ... but removing a timer that is not there is fine
	if err := h.run("uninstall-schedule"); err != nil {
		t.Errorf("uninstall tolerates a missing timer: %v", err)
	}
}

func TestInstallHookKeepsTheRestOfClaudeCodeSettings(t *testing.T) {
	h, _, home := installHarness(t, "linux")
	path := filepath.Join(home, "claude-cfg", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := `{"model":"opus","permissions":{"allow":["Bash(go test:*)"]},"n":12345678901234567890,
	 "hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo pre"}]}],
	          "SessionEnd":[{"hooks":[{"type":"command","command":"echo bye"}]}]}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := h.run("install-hook", "claude-code", "--print"); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(path); string(now) != before {
		t.Fatal("--print changed the settings")
	}
	if err := h.run("install-hook", "claude-code"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var doc struct {
		Model       string
		N           json.Number
		Permissions map[string][]string
		Hooks       map[string][]struct {
			Matcher string
			Hooks   []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the result is valid JSON: %v\n%s", err, raw)
	}
	const cmd = `"/opt/golden gate/bin/goldengate" sync --auto --quiet --source claude-code --settle 0 --resend-gap 0`
	end := doc.Hooks["SessionEnd"]
	if doc.Model != "opus" || doc.N.String() != "12345678901234567890" || doc.Permissions["allow"][0] != "Bash(go test:*)" ||
		doc.Hooks["PreToolUse"][0].Hooks[0].Command != "echo pre" || len(end) != 2 || end[0].Hooks[0].Command != "echo bye" ||
		end[1].Hooks[0].Command != cmd || end[1].Hooks[0].Type != "command" {
		t.Fatalf("everything else is kept and the hook is added:\n%s", raw)
	}
	if bak, _ := os.ReadFile(path + ".goldengate-backup"); string(bak) != before {
		t.Error("the file as it was is kept as a backup")
	}
	if strings.Contains(string(raw), "--approve") {
		t.Error("the hook never approves")
	}

	// installing again changes nothing
	if err := h.run("install-hook", "claude-code"); err != nil || !strings.Contains(h.out.String(), "already in") {
		t.Fatalf("idempotent: %v %s", err, h.out)
	}
	if again, _ := os.ReadFile(path); string(again) != string(raw) {
		t.Error("a second install rewrote the file")
	}
	// a moved binary replaces the old hook instead of adding a second one
	h.deps.Executable = func() (string, error) { return "/usr/local/bin/goldengate", nil }
	if err := h.run("install-hook", "claude-code"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Count(string(raw), "sync --auto") != 1 || !strings.Contains(string(raw), "/usr/local/bin/goldengate sync --auto --quiet --source claude-code") {
		t.Fatalf("one hook, with the new path:\n%s", raw)
	}

	// a hook written by a build before the settle window (no --settle) is
	// replaced too: the session has ended, so it must not wait
	old := strings.Replace(string(raw), " --settle 0 --resend-gap 0", "", 1)
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.run("install-hook", "claude-code"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Count(string(raw), "sync --auto") != 1 || !strings.Contains(string(raw), "claude-code --settle 0 --resend-gap 0") {
		t.Fatalf("the old hook is replaced:\n%s", raw)
	}

	// uninstall removes only ours
	if err := h.run("uninstall-hook", "claude-code"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "goldengate") || !strings.Contains(string(raw), "echo bye") || !strings.Contains(string(raw), "echo pre") {
		t.Fatalf("after uninstall:\n%s", raw)
	}
	if err := h.run("uninstall-hook", "claude-code"); err != nil || !strings.Contains(h.out.String(), "nothing to remove") {
		t.Fatalf("uninstall twice: %v %s", err, h.out)
	}

	// no settings file yet: one is created with only the hook
	_ = os.Remove(path)
	if err := h.run("install-hook", "claude-code"); err != nil {
		t.Fatal(err)
	}
	if raw, _ = os.ReadFile(path); !strings.Contains(string(raw), `"SessionEnd"`) {
		t.Fatalf("created:\n%s", raw)
	}
	// broken JSON is left alone
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.run("install-hook", "claude-code"); err == nil || !strings.Contains(err.Error(), "left alone") {
		t.Fatalf("broken settings: %v", err)
	}
	if now, _ := os.ReadFile(path); string(now) != "{not json" {
		t.Error("a broken settings file was overwritten")
	}
	if err := h.run("install-hook", "codex"); err == nil || !strings.Contains(err.Error(), "install-schedule") {
		t.Errorf("other tools are sent to the schedule: %v", err)
	}
}
