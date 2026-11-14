package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func names(c Config) []string {
	var out []string
	for _, p := range c.Projects {
		out = append(out, p.Name)
	}
	return out
}

func TestLegacyAllowRulesBecomeProjectsAndRoundTrip(t *testing.T) {
	p := write(t, `[share]
default = "deny"
allow = ["~/code/app", "~/code/site/", "claude-ai:Chats", "~/work/**/shared"]
deny = ["~/code/app/secrets"]
deny_source = ["codex@~/code/app", "opencode@~/elsewhere"]
level = "summary"
since = "90d"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(c), []string{"app", "site", "claude-ai:Chats", "~/work/**/shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project names = %v, want %v", got, want)
	}
	app := c.Projects[0]
	if len(app.Members) != 1 || app.Members[0].Path != "~/code/app" || !reflect.DeepEqual(app.Members[0].ExcludeSources, []string{"codex"}) {
		t.Errorf("app project = %+v", app)
	}
	if !strings.HasPrefix(app.ID, "p_") {
		t.Errorf("id = %q", app.ID)
	}
	// The views the rest of the code reads are unchanged by the migration.
	if want := []string{"~/code/app", "~/code/site/", "claude-ai:Chats", "~/work/**/shared"}; !reflect.DeepEqual(c.Share.Allow, want) {
		t.Errorf("allow view = %v", c.Share.Allow)
	}
	if want := []string{"codex@~/code/app", "opencode@~/elsewhere"}; !reflect.DeepEqual(c.Share.SourceDeny, want) {
		t.Errorf("source-deny view = %v", c.Share.SourceDeny)
	}
	// Loading the same legacy file twice gives the same ids (nothing random
	// before the first save).
	again, _ := Load(p)
	if again.Projects[0].ID != app.ID {
		t.Errorf("migrated ids must be stable: %s vs %s", again.Projects[0].ID, app.ID)
	}

	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "allow =") || strings.Contains(string(raw), "codex@") {
		t.Errorf("the new shape must not write allow or member source rules:\n%s", raw)
	}
	if !strings.Contains(string(raw), "opencode@~/elsewhere") {
		t.Errorf("a source rule naming no member must be kept:\n%s", raw)
	}
	back, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Projects, c.Projects) || !reflect.DeepEqual(back.Share, c.Share) {
		t.Errorf("round trip changed the config:\n got %+v\n%+v\nwant %+v\n%+v", back.Projects, back.Share, c.Projects, c.Share)
	}
}

func TestAFolderBelongsToOneProject(t *testing.T) {
	p := write(t, `[[projects]]
id = "p_a"
name = "A"
[[projects.members]]
path = "~/code/app"

[[projects]]
id = "p_b"
name = "B"
[[projects.members]]
path = "~/code/app/"
`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "in two projects") {
		t.Fatalf("want a two-projects error, got %v", err)
	}
	for body, want := range map[string]string{
		"[[projects]]\nid = \"p_a\"\nname = \"A\"\n[[projects]]\nid = \"p_a\"\nname = \"B\"\n": "share the id",
		"[[projects]]\nid = \"p_a\"\nname = \"A\"\n[[projects]]\nid = \"p_b\"\nname = \"a\"\n": "two projects are named",
		"[[projects]]\nid = \"p_a\"\nname = \"\"\n":                                           "needs a name",
		"[[projects]]\nname = \"A\"\n":                                                        "has no id",
	} {
		if _, err := Load(write(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want %q, got %v", body, want, err)
		}
	}
}

func TestProjectOperations(t *testing.T) {
	c := Default()
	if _, err := c.AddMember("Golden Gate", "~/code/goldengate"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddMember("Golden Gate", "claude-ai:Golden Gate"); err != nil {
		t.Fatal(err)
	}
	if got := c.Share.Allow; !reflect.DeepEqual(got, []string{"~/code/goldengate", "claude-ai:Golden Gate"}) {
		t.Fatalf("members are shared: %v", got)
	}
	if _, err := c.CreateProject("golden gate"); err == nil {
		t.Error("names are unique ignoring case")
	}
	id := c.Projects[0].ID
	if err := c.RenameProject("Golden Gate", "GG"); err != nil || c.Projects[0].ID != id || c.Projects[0].Name != "GG" {
		t.Fatalf("rename keeps the id: %v %+v", err, c.Projects[0])
	}

	if _, err := c.AddMember("Site", "~/code/site"); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameProject("Site", "gg"); err == nil {
		t.Error("renaming onto another project's name must be refused (merge instead)")
	}
	moved, err := c.AddMember("Site", "~/code/goldengate")
	if err != nil || moved != "GG" || c.ProjectOfMember("~/code/goldengate") != c.ProjectIndex("Site") {
		t.Fatalf("a member moves between projects: moved=%q err=%v", moved, err)
	}
	if err := c.MergeProjects("Site", "GG"); err != nil {
		t.Fatal(err)
	}
	if len(c.Projects) != 1 || len(c.Projects[0].Members) != 3 || c.Projects[0].ID != id {
		t.Fatalf("merge keeps the target: %+v", c.Projects)
	}
	if err := c.RemoveMember("GG", "~/code/site"); err != nil || len(c.Share.Allow) != 2 {
		t.Fatalf("remove: %v %v", err, c.Share.Allow)
	}
	if err := c.RemoveMember("GG", "~/nope"); err == nil {
		t.Error("removing a non-member must fail")
	}
	if _, err := c.DeleteProject("gg"); err != nil || len(c.Projects) != 0 || len(c.Share.Allow) != 0 {
		t.Fatalf("delete un-shares every member: %v %+v %v", err, c.Projects, c.Share.Allow)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFoldKeepsProjectsInStepWithShareEdits(t *testing.T) {
	c := Default()
	if _, err := c.AddMember("Work", "~/code/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddMember("Work", "~/code/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateProject("Later"); err != nil {
		t.Fatal(err)
	}
	// Edits made through the views, as `share add` and the TUI do.
	c.Share.Allow = append(c.Share.Allow, "~/code/new")
	c.Share.SourceDeny = append(c.Share.SourceDeny, "codex@~/code/b")
	c.Fold()
	if got := names(c); !reflect.DeepEqual(got, []string{"Work", "Later", "new"}) {
		t.Fatalf("a new allow rule becomes its own project: %v", got)
	}
	if m := c.memberIndex("~/code/b"); m == nil || !reflect.DeepEqual(m.ExcludeSources, []string{"codex"}) {
		t.Errorf("a source rule lands on its member: %+v", m)
	}
	// Un-sharing every member of a project removes it; a project created
	// empty stays.
	c.Share.Allow = []string{"~/code/new"}
	c.Fold()
	if got := names(c); !reflect.DeepEqual(got, []string{"Later", "new"}) {
		t.Fatalf("emptied projects go, created-empty ones stay: %v", got)
	}
}

func TestCloneProjectsIsDeep(t *testing.T) {
	ps := []Project{{ID: "p_1", Name: "A", Members: []Member{{Path: "~/a", ExcludeSources: []string{"codex"}}}, WorkItems: []string{"linear:ENG"}}}
	cp := CloneProjects(ps)
	cp[0].Members[0].ExcludeSources[0] = "x"
	cp[0].Members[0].Path = "~/b"
	cp[0].WorkItems[0] = "y"
	if ps[0].Members[0].ExcludeSources[0] != "codex" || ps[0].Members[0].Path != "~/a" || ps[0].WorkItems[0] != "linear:ENG" {
		t.Errorf("clone shares memory with the original: %+v", ps)
	}
}
