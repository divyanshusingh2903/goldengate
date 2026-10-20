package scripts_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The pre-commit guard once failed open (grep -q + pipefail). This keeps it honest.
func TestCheckSecretsBlocksAndPasses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash script")
	}
	for _, tool := range []string{"git", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
	script, err := filepath.Abs("check-secrets.sh")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(context.Background(), name, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "HOME=/nonexistent-home-for-test", "USER=nobody-for-test")
		return cmd.Run()
	}
	if err := run("git", "init", "-q"); err != nil {
		t.Fatal(err)
	}
	stage := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run("git", "add", "-f", name); err != nil {
			t.Fatal(err)
		}
	}
	unstage := func(name string) {
		t.Helper()
		if err := run("git", "rm", "-q", "-f", "--cached", name); err != nil {
			t.Fatal(err)
		}
	}

	stage("ok.go", "package ok\n")
	if err := run("bash", script); err != nil {
		t.Fatalf("clean change must pass: %v", err)
	}
	unstage("ok.go")

	stage("bad.go", "k := \""+"AKIA"+"IOSFODNN7EXAMPLE\"\n")
	if err := run("bash", script); err == nil {
		t.Fatal("secret-shaped string must be blocked")
	}
	unstage("bad.go")

	stage(".env", "X=1\n")
	if err := run("bash", script); err == nil {
		t.Fatal(".env must be blocked")
	}
}
