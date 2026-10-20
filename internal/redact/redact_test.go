package redact

import (
	"strings"
	"testing"
)

// Secrets are assembled at runtime so no secret-shaped literal sits in the repo.
func j(parts ...string) string { return strings.Join(parts, "") }

func TestMustCatch(t *testing.T) {
	r, err := New(Options{ProjectRoot: "/work/proj"})
	if err != nil {
		t.Fatal(err)
	}
	pem := j("-----BEGIN RSA PRIV", "ATE KEY-----\nMIIEabc\ndef\n-----END RSA PRIV", "ATE KEY-----")
	cases := map[string]string{
		"aws-access-key":    j("export K=", "AKIA", "IOSFODNN7EXAMPLE"),
		"github-token":      j("token ", "ghp_", strings.Repeat("a1B2", 9)),
		"anthropic-key":     j("key ", "sk-ant-", "api03-", strings.Repeat("Zy9", 8)),
		"openai-key":        j("k=", "sk-", strings.Repeat("Ab3dE", 8)),
		"slack-token":       j("x ", "xoxb-", "123456789012-abcdefghij"),
		"jwt":               j("t ", "eyJhbGciOiJIUzI1NiJ9", ".", "eyJzdWIiOiIxMjM0NTY3ODkwIn0", ".", "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV"),
		"private-key":       pem,
		"connection-string": "postgres://admin:" + "hunter2hunter2" + "@db.internal:5432/app",
		"auth-header":       "Authorization: Bearer " + strings.Repeat("q7Rt", 6),
		"secret-assignment": `DB_PASSWORD="` + "correct-horse-battery" + `"`,
		"goldengate-token":  j("ggb_read_", strings.Repeat("Ab1-", 8)),
		"high-entropy":      "note " + "Zx9Qm2Lp7Vb4Nc8Rt1Yw5Ke3Hd6Jf0Ga",
	}
	for kind, in := range cases {
		out, counts := r.String(in)
		if counts[kind] == 0 {
			t.Errorf("%s did not fire (got %v) for %q", kind, counts, in)
			continue
		}
		if !strings.Contains(out, "[REDACTED:"+kind+"]") {
			t.Errorf("%s: output not marked: %q", kind, out)
		}
		for _, secret := range []string{"IOSFODNN7EXAMPLE", "hunter2hunter2", "correct-horse-battery", "MIIEabc", "Zx9Qm2Lp7Vb4"} {
			if strings.Contains(out, secret) {
				t.Errorf("%s: secret %q survived: %q", kind, secret, out)
			}
		}
	}
}

func TestBenignUntouched(t *testing.T) {
	r, _ := New(Options{ProjectRoot: "/work/proj"})
	benign := []string{
		"Fix OAuth redirect mismatch in the login handler",
		"commit 4b825dc642cb6eb9a060e54bf8d69288fbee4904 on main",
		"session 123e4567-e89b-12d3-a456-426614174000 started",
		"func TestSomethingVeryLongFunctionNameHere(t *testing.T)",
		"token: string",
		"password = nil",
		"see https://example.com/docs/guide/intro for details",
		"run `go test ./...` then `git status`",
		"UTF-8, SHA-256 and ISO-8601 are standards",
		"ENG-123 was closed",
	}
	for _, in := range benign {
		if out, c := r.String(in); out != in {
			t.Errorf("mangled %q -> %q (%v)", in, out, c)
		}
	}
}

func TestPaths(t *testing.T) {
	r, _ := New(Options{ProjectRoot: "/work/proj"})
	out, c := r.String("edited /work/proj/internal/a.go and read /etc/passwd and /work/other/secret.txt")
	if !strings.Contains(out, "internal/a.go") || strings.Contains(out, "/work/proj") {
		t.Errorf("project path not made relative: %q", out)
	}
	if strings.Contains(out, "/etc/passwd") || strings.Contains(out, "/work/other") {
		t.Errorf("outside path leaked: %q", out)
	}
	if c["path"] != 2 {
		t.Errorf("want 2 path redactions, got %v", c)
	}
	out, _ = r.String(`opened C:\Users\bob\proj\x.txt`)
	if strings.Contains(out, "bob") {
		t.Errorf("windows path leaked: %q", out)
	}
}

func TestExtraPatternAndBadPattern(t *testing.T) {
	r, err := New(Options{ExtraPattern: []string{`ACME-\d{4}`}})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := r.String("client ACME-1234 asked"); strings.Contains(out, "ACME-1234") {
		t.Errorf("custom pattern ignored: %q", out)
	}
	if _, err := New(Options{ExtraPattern: []string{"("}}); err == nil {
		t.Error("expected error for invalid pattern")
	}
}

func TestIsSecretFile(t *testing.T) {
	for _, n := range []string{".env", ".env.local", "a/b/server.pem", "id_rsa", "id_rsa.pub", "credentials.json", `c:\x\.npmrc`} {
		if !IsSecretFile(n) {
			t.Errorf("%s should be secret", n)
		}
	}
	for _, n := range []string{"main.go", "environment.md", "README"} {
		if IsSecretFile(n) {
			t.Errorf("%s should not be secret", n)
		}
	}
}

func FuzzString(f *testing.F) {
	r, _ := New(Options{})
	f.Add("Authorization: Bearer abc")
	f.Fuzz(func(t *testing.T, s string) { r.String(s) })
}
