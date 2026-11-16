package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

func TestGitLabJoinsTheDefaultProviders(t *testing.T) {
	load := func(body string) Config {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// the list every earlier build wrote gains gitlab, once
	c := load("version = 2\n[issues]\nproviders = [\"linear\", \"jira\", \"github\"]\n")
	if !slices.Contains(c.Issues.Providers, "gitlab") || c.Version != CurrentVersion {
		t.Errorf("old default: %v v%d", c.Issues.Providers, c.Version)
	}
	// a list the user changed is left alone
	if c := load("version = 2\n[issues]\nproviders = [\"github\"]\n"); len(c.Issues.Providers) != 1 {
		t.Errorf("changed list: %v", c.Issues.Providers)
	}
	// and so is the old default once the file is at this version
	if c := load("version = 3\n[issues]\nproviders = [\"linear\", \"jira\", \"github\"]\n"); slices.Contains(c.Issues.Providers, "gitlab") {
		t.Errorf("current file: %v", c.Issues.Providers)
	}
	if !slices.Contains(Default().Issues.Providers, "gitlab") {
		t.Error("new installs read GitLab issues")
	}
	c = Default()
	c.Issues.GitLabHosts = []string{"gitlab.example.com"}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"https://gitlab.example.com", "gitlab.example.com/group", "", "a b"} {
		c.Issues.GitLabHosts = []string{bad}
		if c.Validate() == nil {
			t.Errorf("host %q accepted", bad)
		}
	}
}

func TestMembersLimitedToConversations(t *testing.T) {
	c := Default()
	if _, err := c.ShareSession("Research", "chatgpt:Chats", "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ShareSession("Travel", "chatgpt:Chats", "c2"); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("one grouping in two projects, by conversation: %v", err)
	}
	if err := c.validateProjects(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"c1": 0, "c2": 1, "c3": -1} {
		if limited, i := c.SessionScope("chatgpt:Chats", id); !limited || i != want {
			t.Errorf("%s: limited=%v project=%d, want %d", id, limited, i, want)
		}
	}
	if limited, _ := c.SessionScope("claude-ai:Chats", "c1"); limited {
		t.Error("another grouping is not limited")
	}
	if n := slices.Index(c.Share.Allow, "chatgpt:Chats"); n < 0 || strings.Count(strings.Join(c.Share.Allow, ","), "chatgpt:Chats") != 1 {
		t.Errorf("the grouping is selected once: %v", c.Share.Allow)
	}
	// a conversation is in one project: sharing it elsewhere moves it
	if moved, err := c.ShareSession("Travel", "chatgpt:Chats", "c1"); err != nil || moved != "Research" {
		t.Fatalf("move: %q %v", moved, err)
	}
	if _, i := c.SessionScope("chatgpt:Chats", "c1"); i != 1 || len(c.Projects[0].Members) != 0 {
		t.Fatalf("after the move: %+v", c.Projects)
	}
	// unsharing the last conversation removes the member rather than leaving an empty list
	if c.UnshareSession("chatgpt:Chats", "c1") != "Travel" || c.UnshareSession("chatgpt:Chats", "c2") != "Travel" || c.UnshareSession("chatgpt:Chats", "c2") != "" {
		t.Fatal("unshare")
	}
	if limited, _ := c.SessionScope("chatgpt:Chats", "c1"); limited || len(c.Share.Allow) != 0 {
		t.Fatalf("nothing of the grouping is shared any more: %+v %v", c.Projects, c.Share.Allow)
	}

	// what the file may not say
	bad := func(name string, ps ...Project) {
		t.Helper()
		c := Default()
		c.Projects = ps
		if err := c.validateProjects(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	bad("a conversation in two projects",
		Project{ID: "p_1", Name: "A", Members: []Member{{Path: "chatgpt:Chats", Sessions: []string{"c1"}}}},
		Project{ID: "p_2", Name: "B", Members: []Member{{Path: "chatgpt:Chats", Sessions: []string{"c1"}}}})
	bad("whole and by conversation",
		Project{ID: "p_1", Name: "A", Members: []Member{{Path: "chatgpt:Chats"}}},
		Project{ID: "p_2", Name: "B", Members: []Member{{Path: "chatgpt:Chats", Sessions: []string{"c1"}}}})
	bad("by conversation and whole",
		Project{ID: "p_1", Name: "A", Members: []Member{{Path: "chatgpt:Chats", Sessions: []string{"c1"}}}},
		Project{ID: "p_2", Name: "B", Members: []Member{{Path: "chatgpt:Chats"}}})
	bad("conversations of a folder", Project{ID: "p_1", Name: "A", Members: []Member{{Path: "~/code/app", Sessions: []string{"s1"}}}})
	bad("conversations of a pattern", Project{ID: "p_1", Name: "A", Members: []Member{{Path: "chatgpt:*", Sessions: []string{"s1"}}}})

	// adding the whole grouping replaces the list; a whole grouping refuses single conversations
	c = Default()
	_, _ = c.ShareSession("A", "chatgpt:Chats", "c1")
	if _, err := c.AddMember("B", "chatgpt:Chats"); err != nil {
		t.Fatal(err)
	}
	if limited, _ := c.SessionScope("chatgpt:Chats", "c1"); limited || c.WholeMember("chatgpt:Chats") != 1 {
		t.Fatalf("whole replaces the list: %+v", c.Projects)
	}
	if _, err := c.ShareSession("A", "chatgpt:Chats", "c1"); err == nil {
		t.Error("a conversation cannot be added next to the whole grouping")
	}
}
