package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/rules"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
)

// rulesCmd manages the rules that automate approval inside a project.
func (a *app) rulesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Rules that approve or exclude a project's sessions for you",
		Long: `A rule belongs to a project and acts only on that project's sessions: folders
and chats you do not share are never read, and a block (goldengate share deny) always wins.

A rule matches on facts about a session, never on what a model wrote: the tool
it came from, its folder or chat grouping, the repository remote, the git
branch, and the issues and pull requests it touched.

What an approve rule does depends on the project's trust level
(goldengate projects trust <project> ask|suggest|auto):
  ask      nothing: everything waits in the inbox
  suggest  matching sessions are marked in the inbox; approve them together with
           goldengate inbox approve --suggested
  auto     matching sessions go out the next time you send

With no approve rule, the trust level covers every session of the project.
An exclude rule keeps matching sessions on this machine at every trust level.

  goldengate rules add "My app" --branch 'release/*'
  goldengate rules add "My app" --exclude --source codex
  goldengate rules add Work --work-item linear:ENG`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return a.rulesList("") },
	}
	list := &cobra.Command{Use: "list [project]", Short: "List rules, per project", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				return a.rulesList(args[0])
			}
			return a.rulesList("")
		}}
	var r rules.Rule
	var exclude bool
	add := &cobra.Command{Use: "add <project> [conditions]", Short: "Add a rule to a project; a session must meet every condition given", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			r.Action = rules.ActionApprove
			if exclude {
				r.Action = rules.ActionExclude
			}
			return a.rulesAdd(args[0], r)
		}}
	add.Flags().BoolVar(&exclude, "exclude", false, "keep matching sessions on this machine instead of approving them")
	add.Flags().StringVar(&r.Source, "source", "", "only sessions from this tool (claude-code, codex, chatgpt, ...)")
	add.Flags().StringVar(&r.Folder, "folder", "", "only sessions in this folder, glob or chat grouping (~/code/app, '~/work/*', 'chatgpt:*')")
	add.Flags().StringVar(&r.Remote, "remote", "", "only sessions whose repository remote matches ('github.com/acme/*')")
	add.Flags().StringVar(&r.Branch, "branch", "", "only sessions on a matching git branch ('fix/*')")
	add.Flags().StringVar(&r.WorkItem, "work-item", "", "only sessions that touched an issue or pull request under this key prefix (linear:ENG, acme/app)")
	remove := &cobra.Command{Use: "remove <project> <rule-id>", Short: "Remove a rule", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error { return a.rulesRemove(args[0], args[1]) }}
	cmd.AddCommand(list, add, remove)
	cmd.AddCommand(a.offerCmds()...)
	return cmd
}

// trustWords says in words what a project's trust level and rules do.
func trustWords(p config.Project) string {
	approve := hasApproveRule(p)
	switch p.TrustLevel() {
	case rules.TrustAuto:
		if approve {
			return "auto: sessions an approve rule matches go out the next time you send; the rest wait in the inbox"
		}
		return "auto: new and changed sessions go out the next time you send"
	case rules.TrustSuggest:
		if approve {
			return "suggest: sessions an approve rule matches are marked in the inbox; nothing is sent until you approve"
		}
		return "suggest: every session is marked in the inbox; nothing is sent until you approve"
	}
	if approve {
		return "ask: everything waits in the inbox; approve rules do nothing until the trust level is suggest or auto"
	}
	return "ask: everything waits in the inbox"
}

func (a *app) rulesList(ref string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	if ref != "" && cfg.ProjectIndex(ref) < 0 {
		return fmt.Errorf("no project named %q", ref)
	}
	shown := 0
	for i, p := range cfg.Projects {
		if ref != "" && i != cfg.ProjectIndex(ref) {
			continue
		}
		shown++
		fmt.Fprintf(a.Stdout, "%s\n  trust %s\n", p.Name, trustWords(p))
		if len(p.Rules) == 0 {
			fmt.Fprintln(a.Stdout, "  (no rules)")
		}
		for _, r := range p.Rules {
			fmt.Fprintf(a.Stdout, "  %s  %s\n", r.ID, r)
		}
	}
	if shown == 0 {
		fmt.Fprintln(a.Stdout, "no projects yet: share a folder first (`goldengate share add <path>`)")
	}
	return nil
}

func (a *app) rulesAdd(ref string, r rules.Rule) error {
	lines, err := a.addRule(ref, r)
	for _, l := range lines {
		fmt.Fprintln(a.Stdout, l)
	}
	return err
}

// addRule adds r to the project ref names and says what changed, in lines.
// The CLI prints them; the web UI shows them.
func (a *app) addRule(ref string, r rules.Rule) ([]string, error) {
	cfg, err := a.load()
	if err != nil {
		return nil, err
	}
	i := cfg.ProjectIndex(ref)
	if i < 0 {
		return nil, fmt.Errorf("no project named %q", ref)
	}
	p := &cfg.Projects[i]
	for _, have := range p.Rules {
		same := have
		same.ID = ""
		if same == r {
			return nil, fmt.Errorf("%q already has this rule (%s)", p.Name, have.ID)
		}
	}
	if r.Action == rules.ActionExclude && r.Unconditional() {
		return nil, errors.New("an exclude rule needs a condition: to stop sharing the whole project, remove its members (`goldengate projects remove`)")
	}
	r.ID = rules.NewID()
	if err := r.Validate(); err != nil {
		return nil, err
	}
	p.Rules = append(p.Rules, r)
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return nil, err
	}
	lines := []string{fmt.Sprintf("%q: added rule %s: %s", p.Name, r.ID, r)}
	if r.Folder != "" && !coveredByMember(*p, r.Folder) {
		lines = append(lines, fmt.Sprintf("note: a rule acts only on sessions the project already shares. If %s is not under one of its members, share it first (`goldengate projects add %q %s`).", r.Folder, p.Name, r.Folder))
	}
	lines = append(lines, "trust "+trustWords(*p))
	if r.Action == rules.ActionApprove && p.TrustLevel() == rules.TrustAsk {
		lines = append(lines, fmt.Sprintf("to let the rule act: goldengate projects trust %q suggest   (or auto)", p.Name))
	}
	return lines, nil
}

// coveredByMember is a cheap check that a rule's folder is one of the
// project's members or textually under one; globs are not resolved.
func coveredByMember(p config.Project, folder string) bool {
	for _, m := range p.Members {
		if m.Path == folder || strings.HasPrefix(folder, strings.TrimRight(m.Path, "/*")+"/") || strings.ContainsAny(m.Path, "*?[{") {
			return true
		}
	}
	return false
}

func (a *app) rulesRemove(ref, id string) error {
	msg, err := a.removeRule(ref, id)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, msg)
	return nil
}

// removeRule removes the rule id from the project ref names.
func (a *app) removeRule(ref, id string) (string, error) {
	cfg, err := a.load()
	if err != nil {
		return "", err
	}
	i := cfg.ProjectIndex(ref)
	if i < 0 {
		return "", fmt.Errorf("no project named %q", ref)
	}
	p := &cfg.Projects[i]
	for j, r := range p.Rules {
		if r.ID != id {
			continue
		}
		p.Rules = append(p.Rules[:j:j], p.Rules[j+1:]...)
		if err := config.Save(a.cfgPath(), cfg); err != nil {
			return "", err
		}
		return fmt.Sprintf("%q: removed rule %s (%s)\ntrust %s", p.Name, id, r, trustWords(*p)), nil
	}
	return "", fmt.Errorf("%q has no rule %s (see `goldengate rules list`)", p.Name, id)
}

func (a *app) projectsTrust(ref, level string) error {
	msg, err := a.setTrust(ref, level)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, msg)
	return nil
}

// setTrust sets the trust level of the project ref names.
func (a *app) setTrust(ref, level string) (string, error) {
	level = strings.ToLower(level)
	if level == "" || !rules.ValidTrust(level) {
		return "", fmt.Errorf("say ask, suggest or auto, got %q", level)
	}
	cfg, err := a.load()
	if err != nil {
		return "", err
	}
	i := cfg.ProjectIndex(ref)
	if i < 0 {
		return "", fmt.Errorf("no project named %q", ref)
	}
	cfg.Projects[i].Trust = level
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return "", err
	}
	return fmt.Sprintf("%q: trust %s", cfg.Projects[i].Name, trustWords(cfg.Projects[i])), nil
}

// Rules implements tui.Backend.
func (b *tuiBackend) Rules(context.Context) ([]tui.ProjectRules, error) {
	cfg, err := b.a.load()
	if err != nil {
		return nil, err
	}
	out := make([]tui.ProjectRules, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		pr := tui.ProjectRules{Name: p.Name, Trust: p.TrustLevel(), Words: trustWords(p), Rules: []tui.RuleInfo{}, Members: []string{}}
		for _, m := range p.Members {
			pr.Members = append(pr.Members, m.Path)
		}
		for _, r := range p.Rules {
			pr.Rules = append(pr.Rules, tui.RuleInfo{ID: r.ID, Action: r.Action, Source: r.Source, Folder: r.Folder, Remote: r.Remote, Branch: r.Branch, WorkItem: r.WorkItem, Words: r.String()})
		}
		out = append(out, pr)
	}
	return out, nil
}

// AddRule implements tui.Backend.
func (b *tuiBackend) AddRule(_ context.Context, project string, ri tui.RuleInfo) (string, error) {
	b.forgetReview()
	r := rules.Rule{Action: ri.Action, Source: strings.TrimSpace(ri.Source), Folder: strings.TrimSpace(ri.Folder), Remote: strings.TrimSpace(ri.Remote), Branch: strings.TrimSpace(ri.Branch), WorkItem: strings.TrimSpace(ri.WorkItem)}
	lines, err := b.a.addRule(project, r)
	return strings.Join(lines, "\n"), err
}

// RemoveRule implements tui.Backend.
func (b *tuiBackend) RemoveRule(_ context.Context, project, id string) (string, error) {
	b.forgetReview()
	return b.a.removeRule(project, id)
}

// SetTrust implements tui.Backend.
func (b *tuiBackend) SetTrust(_ context.Context, project, level string) (string, error) {
	b.forgetReview()
	return b.a.setTrust(project, level)
}

// forgetReview drops the last review: a rule or trust change can change what
// the next send takes.
func (b *tuiBackend) forgetReview() {
	b.mu.Lock()
	b.reviewed = nil
	b.mu.Unlock()
}
