package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProjectsCommandsLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path fixtures")
	}
	h := newHarness(t)
	must := func(args ...string) string {
		t.Helper()
		if err := h.run(args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, h.errOut.String())
		}
		return h.out.String()
	}

	if out := must("projects", "list"); !strings.Contains(out, "no projects yet") {
		t.Fatalf("empty list: %s", out)
	}
	out := must("projects", "create", "Widgets", "~/projects/demo")
	if !strings.Contains(out, `created project "Widgets"`) || !strings.Contains(out, "added ~/projects/demo (shared)") {
		t.Fatalf("create: %s", out)
	}
	if out := must("sync", "--dry-run"); !strings.Contains(out, "please ship the widget") {
		t.Fatalf("a member is shared: %s", out)
	}
	if out := must("share", "list", "--projects"); !strings.Contains(out, "[x] ~/projects/demo") || !strings.Contains(out, "shared in project Widgets") {
		t.Fatalf("share list shows the project as the reason: %s", out)
	}

	must("projects", "create", "Empty")
	must("projects", "rename", "widgets", "Widget work")
	out = must("projects", "add", "Empty", "~/projects/demo")
	if !strings.Contains(out, `moved ~/projects/demo from "Widget work"`) {
		t.Fatalf("a member moves to the new project: %s", out)
	}
	must("projects", "merge", "Empty", "Widget work")
	out = must("projects", "list")
	if !strings.Contains(out, "Widget work  (p_") || !strings.Contains(out, "  ~/projects/demo") || strings.Contains(out, "Empty") {
		t.Fatalf("after rename and merge:\n%s", out)
	}

	// The config file holds projects, not allow rules.
	raw, err := os.ReadFile(filepath.Join(h.xdgCfg, "goldengate", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "[[projects]]") || strings.Contains(string(raw), "allow =") {
		t.Fatalf("config shape:\n%s", raw)
	}

	if err := h.run("projects", "add", "Other", "~/projects/client-secret"); err != nil {
		t.Fatal(err)
	}
	must("share", "deny", "~/projects/client-secret")
	if out := must("projects", "list"); !strings.Contains(out, "~/projects/client-secret  [denied: a deny rule wins, nothing is sent]") {
		t.Fatalf("deny beats membership and the list says so:\n%s", out)
	}
	if out := must("sync", "--dry-run"); strings.Contains(out, "TOPSECRET") {
		t.Fatalf("a denied member must never be sent: %s", out)
	}

	out = must("projects", "remove", "Widget work", "~/projects/demo")
	if !strings.Contains(out, "no longer shared") || !strings.Contains(out, "1 project(s) un-shared") {
		t.Fatalf("remove un-shares and tombstones: %s", out)
	}
	if err := h.run("projects", "remove", "Widget work", "~/projects/demo"); err == nil {
		t.Error("removing a non-member must fail")
	}
	out = must("projects", "delete", "Other")
	if !strings.Contains(out, `deleted project "Other"`) {
		t.Fatalf("delete: %s", out)
	}
	if out := must("projects", "list"); strings.Contains(out, "Other") || !strings.Contains(out, "Widget work") {
		t.Fatalf("an emptied project created by the user stays; a deleted one goes:\n%s", out)
	}
	for _, bad := range [][]string{{"projects", "rename", "nope", "x"}, {"projects", "merge", "Widget work", "Widget work"}, {"projects", "delete", "nope"}, {"projects", "create", "widget work"}} {
		if err := h.run(bad...); err == nil {
			t.Errorf("%v must fail", bad)
		}
	}
}

func TestShareAddCreatesAProjectNamedAfterTheFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path fixtures")
	}
	h := newHarness(t)
	if err := h.run("share", "add", "~/projects/demo"); err != nil {
		t.Fatal(err)
	}
	if err := h.run("projects", "list"); err != nil {
		t.Fatal(err)
	}
	if out := h.out.String(); !strings.Contains(out, "demo  (p_") || !strings.Contains(out, "  ~/projects/demo") {
		t.Fatalf("share add makes a project of the folder:\n%s", out)
	}
}
