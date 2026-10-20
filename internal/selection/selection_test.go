package selection

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func bptr(b bool) *bool { return &b }

func mustNew(t *testing.T, p Policy, ci bool) *Matcher {
	t.Helper()
	m, err := New(p, Options{Home: "/home/u", CaseInsensitive: bptr(ci)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDefaultDeny(t *testing.T) {
	m := mustNew(t, Policy{}, false)
	if d := m.Decide("/home/u/code/x", "claude-code"); d.Shared {
		t.Fatalf("default must deny, got %+v", d)
	}
}

func TestAllowCoversChildren(t *testing.T) {
	m := mustNew(t, Policy{Allow: []string{"~/code/app"}}, false)
	for path, want := range map[string]bool{
		"/home/u/code/app":         true,
		"/home/u/code/app/sub/dir": true,
		"/home/u/code/app2":        false, // prefix of name, not of path
		"/home/u/code":             false,
	} {
		if got := m.Decide(path, "").Shared; got != want {
			t.Errorf("%s: got %v want %v", path, got, want)
		}
	}
}

func TestDenyBeatsAllowGlob(t *testing.T) {
	m := mustNew(t, Policy{
		Allow: []string{"~/code"},
		Deny:  []string{"~/code/**/secrets", "~/work/client-x"},
	}, false)
	for path, want := range map[string]bool{
		"/home/u/code/a":                 true,
		"/home/u/code/a/b/secrets":       false,
		"/home/u/code/a/b/secrets/inner": false,
		"/home/u/code/secrets":           false,
	} {
		if got := m.Decide(path, "").Shared; got != want {
			t.Errorf("%s: got %v want %v", path, got, want)
		}
	}
}

func TestSourcesFilter(t *testing.T) {
	m := mustNew(t, Policy{Default: "allow", IncludeSources: []string{"codex"}}, false)
	if m.Decide("/x", "claude-code").Shared {
		t.Fatal("excluded source must not be shared")
	}
	if !m.Decide("/x", "codex").Shared {
		t.Fatal("included source must be shared")
	}
}

func TestCaseFolding(t *testing.T) {
	ci := mustNew(t, Policy{Allow: []string{"/Work/App"}}, true)
	if !ci.Decide("/work/app/x", "").Shared {
		t.Fatal("case-insensitive match expected")
	}
	cs := mustNew(t, Policy{Allow: []string{"/Work/App"}}, false)
	if cs.Decide("/work/app/x", "").Shared {
		t.Fatal("case-sensitive must not match")
	}
}

func TestSymlinkResolved(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(filepath.Join(real, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	// deny via real path must also catch the symlinked spelling, and vice versa.
	m := mustNew(t, Policy{Default: "allow", Deny: []string{real}}, false)
	if m.Decide(filepath.Join(link, "p"), "").Shared {
		t.Fatal("deny on real path must cover symlink spelling")
	}
	m2 := mustNew(t, Policy{Default: "allow", Deny: []string{link}}, false)
	if m2.Decide(filepath.Join(real, "p"), "").Shared {
		t.Fatal("deny on symlink must cover real spelling")
	}
}

func TestInvalidDefault(t *testing.T) {
	if _, err := New(Policy{Default: "maybe"}, Options{}); err == nil {
		t.Fatal("expected error")
	}
}

// Property: for random paths, if any deny rule matches, the project is never
// shared, whatever the allow rules and default say.
func TestPropertyDenyAlwaysWins(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	segs := []string{"a", "b", "c", "secrets", "x1"}
	randPath := func() string {
		p := "/home/u"
		for i := 0; i < 1+rng.Intn(4); i++ {
			p += "/" + segs[rng.Intn(len(segs))]
		}
		return p
	}
	for i := 0; i < 500; i++ {
		deny := []string{randPath(), "~/**/secrets"}
		allow := []string{randPath(), randPath(), "~"}
		m := mustNew(t, Policy{Default: []string{"allow", "deny"}[rng.Intn(2)], Allow: allow, Deny: deny}, false)
		for j := 0; j < 20; j++ {
			p := randPath()
			if m.Denied(p) && m.Decide(p, "").Shared {
				t.Fatalf("deny lost to allow: path=%s deny=%v allow=%v", p, deny, allow)
			}
		}
	}
}

func TestVirtualChatProjects(t *testing.T) {
	// even default=allow must not share chat history without an explicit rule
	open := mustNew(t, Policy{Default: "allow"}, false)
	if open.Decide("claude-ai:Chats", "claude-ai").Shared || open.Decide("chatgpt:Chats", "chatgpt").Shared {
		t.Fatal("chat projects must need an explicit allow rule")
	}
	if !open.Decide("/home/u/code/app", "").Shared {
		t.Fatal("folders keep default=allow")
	}
	m := mustNew(t, Policy{
		Allow: []string{"claude-ai:Chats", "chatgpt:*", "~/code"},
		Deny:  []string{"chatgpt:private-*"},
	}, false)
	for key, want := range map[string]bool{
		"claude-ai:Chats":       true,
		"claude-ai:Other":       false, // only the exact bucket was allowed
		"chatgpt:Chats":         true,
		"chatgpt:private-diary": false, // deny beats allow
		"/home/u/code/x":        true,
	} {
		if got := m.Decide(key, "").Shared; got != want {
			t.Errorf("%s: got %v want %v", key, got, want)
		}
	}
	// virtual rules never match folders and folder rules never match virtual keys
	f := mustNew(t, Policy{Allow: []string{"claude-ai:*"}}, false)
	if f.Decide("/home/u/code", "").Shared {
		t.Error("a chat rule shared a folder")
	}
	g := mustNew(t, Policy{Allow: []string{"/"}}, false)
	if g.Decide("claude-ai:Chats", "").Shared {
		t.Error("a folder rule shared a chat project")
	}
	if !m.Denied("chatgpt:private-x") || m.Denied("chatgpt:Chats") {
		t.Error("Denied() must follow the same rules")
	}
}
