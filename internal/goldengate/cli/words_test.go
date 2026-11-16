package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// developerWords are words the glossary pass replaced in user-facing text:
// ingest and read tokens (upload and assistant tokens), synced (sent), deny
// rules and denied (blocked), purge-remote (delete from the server), bucket
// (grouping). Command and flag names keep working under their old spelling;
// this guards the words people read.
var developerWords = regexp.MustCompile(`(?i)\bingest\b|\bread token|\bsynced\b|\bdenied\b|\bdeny rule|purge-remote|keep-remote|\bbuckets?\b`)

// Every command's help uses the glossary's words.
func TestHelpTextUsesPlainWords(t *testing.T) {
	root := New(newHarness(t).deps)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		texts := map[string]string{"short": c.Short, "long": c.Long, "example": c.Example}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden {
				texts["flag --"+f.Name] = f.Name + " " + f.Usage
			}
		})
		for where, text := range texts {
			if m := developerWords.FindString(text); m != "" {
				t.Errorf("%s (%s) says %q: %s", c.CommandPath(), where, m, strings.TrimSpace(text))
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// The old spellings still work, so scripts, hooks and timers keep running.
func TestOldCommandAndFlagNamesStillWork(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"sync", "--dry-run"}, {"send", "--dry-run"},
		{"share", "add", "~/projects/demo"},
		{"share", "deny", "~/projects/demo/a", "--keep-remote"},
		{"share", "block", "~/projects/demo/b", "--keep-on-server"},
		{"share", "remove", "~/projects/demo", "--keep-remote"},
	} {
		if err := h.run(args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := h.run("share", "list"); err != nil || strings.Count(h.out.String(), "~/projects/demo/") != 2 {
		t.Fatalf("both spellings blocked a folder: %v\n%s", err, h.out)
	}
	if err := h.run("share", "remove", "~/projects/demo", "--purge-remote", "--keep-remote"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("the old flag names still take part in the same checks: %v", err)
	}
	for _, scope := range []string{"read", "ingest", "assistant", "upload"} {
		// all four are accepted; without a server they fail later, not on the word
		if err := h.run("token", "rotate", scope); err == nil || strings.Contains(err.Error(), "say assistant or upload") {
			t.Errorf("token rotate %s: %v", scope, err)
		}
	}
	if err := h.run("token", "rotate", "admin"); err == nil || !strings.Contains(err.Error(), "say assistant or upload") {
		t.Errorf("an unknown kind is refused in plain words: %v", err)
	}
}
