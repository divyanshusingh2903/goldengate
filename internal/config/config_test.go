package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsDenyAndValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Share.Default != "deny" || len(c.Share.Allow) != 0 || c.Enrich.Git || c.Enrich.GitHub || c.Artifacts.IncludeContent {
		t.Fatalf("unsafe defaults: %+v", c)
	}
}

func TestRoundTripAndPerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	c := Default()
	c.Share.Allow = []string{"~/code/a"}
	c.Share.Deny = []string{"~/code/**/secrets"}
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", st.Mode().Perm())
		}
		if st, _ := os.Stat(filepath.Dir(p)); st.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v", st.Mode().Perm())
		}
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Share.Allow, ",") != "~/code/a" || got.Share.Deny[0] != "~/code/**/secrets" {
		t.Fatalf("round trip lost data: %+v", got.Share)
	}
}

func TestMissingFileGivesDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || c.Share.Default != "deny" {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestRejectsUnknownFieldsAndUnsafeValues(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "c.toml")
		_ = os.WriteFile(p, []byte(s), 0o600)
		return p
	}
	cases := map[string]string{
		"unknown field":     "[share]\nallow_all = true\n",
		"full level":        "[share]\nlevel = \"full\"\n",
		"content w/o full":  "[artifacts]\ninclude_content = true\n",
		"bad default":       "[share]\ndefault = \"maybe\"\n",
		"bad since":         "[share]\nsince = \"soon\"\n",
		"plain http server": "[server]\nurl = \"http://example.com\"\n",
		"non-http server":   "[server]\nurl = \"ftp://example.com\"\n",
	}
	for name, body := range cases {
		if _, err := Load(write(body)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	for _, ok := range []string{"http://localhost:8080", "http://127.0.0.1", "https://mcp.example.com"} {
		if err := CheckServerURL(ok); err != nil {
			t.Errorf("%s should be allowed: %v", ok, err)
		}
	}
}

func TestParseSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "12h": 12 * time.Hour, "90d": 90 * 24 * time.Hour, "2w": 14 * 24 * time.Hour} {
		if got, err := ParseSince(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"d", "-1d", "5x", "abc"} {
		if _, err := ParseSince(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestResolveDirsXDG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	d := ResolveDirs(func(k string) string { return map[string]string{"XDG_CONFIG_HOME": "/x/cfg"}[k] }, "/home/u")
	if d.ConfigPath() != "/x/cfg/goldengate/config.toml" || d.StatePath() != "/home/u/.local/state/goldengate/state.db" {
		t.Fatalf("%+v", d)
	}
}
