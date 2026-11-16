package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/rules"
)

// projectsCmd manages named projects. A project's members are what is shared;
// adding a member shares it, removing one stops sharing it.
func (a *app) projectsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "projects", Short: "Manage named projects and their members (folders and chat groupings)"}
	var purgeRemote, keepRemote, autoApprove bool
	mode := func() (string, error) {
		switch {
		case purgeRemote && keepRemote:
			return "", errors.New("--delete-from-server and --keep-on-server are mutually exclusive")
		case purgeRemote:
			return "yes", nil
		case keepRemote:
			return "no", nil
		}
		return "ask", nil
	}
	list := &cobra.Command{Use: "list", Short: "List projects and their members", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return a.projectsList() }}
	create := &cobra.Command{Use: "create <name> [member...]", Short: "Create a project, optionally with members (they become shared)", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return a.projectsCreate(c.Context(), args[0], args[1:], autoApprove)
		}}
	create.Flags().BoolVar(&autoApprove, "auto-approve", false, "send this project's new and changed sessions without asking")
	rename := &cobra.Command{Use: "rename <project> <new name>", Short: "Rename a project (its id stays the same)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error { return a.projectsRename(args[0], args[1]) }}
	merge := &cobra.Command{Use: "merge <from> <into>", Short: "Move every member of one project into another and remove the first", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error { return a.projectsMerge(args[0], args[1]) }}
	add := &cobra.Command{Use: "add <project> <member>...", Short: "Add folders or chat groupings to a project (created if new); they become shared", Args: cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error { return a.projectsAdd(c.Context(), args[0], args[1:]) }}
	remove := &cobra.Command{Use: "remove <project> <member>...", Short: "Take members out of a project; they stop being shared", Args: cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			m, err := mode()
			if err != nil {
				return err
			}
			return a.projectsRemove(c.Context(), args[0], args[1:], m)
		}}
	del := &cobra.Command{Use: "delete <project>", Short: "Delete a project; its members stop being shared", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			m, err := mode()
			if err != nil {
				return err
			}
			return a.projectsDelete(c.Context(), args[0], m)
		}}
	for _, c := range []*cobra.Command{remove, del} {
		c.Flags().BoolVar(&purgeRemote, "delete-from-server", false, "also delete the un-shared members' data from the server")
		c.Flags().BoolVar(&keepRemote, "keep-on-server", false, "leave data already on the server in place")
		oldFlagNames(c)
	}
	auto := &cobra.Command{Use: "auto <project> on|off", Short: "Auto-approve: send a project's new sessions without asking (off: they wait in the inbox)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error { return a.projectsAuto(args[0], args[1]) }}
	trust := &cobra.Command{Use: "trust <project> ask|suggest|auto", Short: "How far a project's automation goes: ask (everything waits), suggest (rules mark sessions in the inbox), auto (rules send them)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error { return a.projectsTrust(args[0], args[1]) }}
	cmd.AddCommand(list, create, rename, merge, add, remove, del, auto, trust)
	cmd.AddCommand(a.projectOfferCmds()...)
	return cmd
}

func (a *app) projectsList() error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	if len(cfg.Projects) == 0 {
		fmt.Fprintln(a.Stdout, "no projects yet: create one with `goldengate projects create <name> <folder>`, or share a folder")
		return nil
	}
	m, err := a.matcher(cfg)
	if err != nil {
		return err
	}
	for _, p := range cfg.Projects {
		mode := "ask: new sessions wait in the inbox"
		switch p.TrustLevel() {
		case rules.TrustAuto:
			mode = "auto: new sessions are sent"
			if hasApproveRule(p) {
				mode = "auto: sessions its rules approve are sent"
			}
		case rules.TrustSuggest:
			mode = "suggest: new sessions wait, marked for approval"
		}
		fmt.Fprintf(a.Stdout, "%s  (%s)  [%s]\n", p.Name, p.ID, mode)
		if len(p.Members) == 0 {
			fmt.Fprintln(a.Stdout, "  (no members: nothing is shared)")
		}
		for _, mb := range p.Members {
			line := "  " + mb.Path
			if len(mb.ExcludeSources) > 0 {
				line += "  (not sent: " + strings.Join(mb.ExcludeSources, ", ") + ")"
			}
			if !strings.ContainsAny(mb.Path, "*?[{") && m.Denied(a.localKey(mb.Path)) {
				line += "  [blocked: a block always wins, nothing is sent]"
			}
			fmt.Fprintln(a.Stdout, line)
		}
		if len(p.WorkItems) > 0 {
			fmt.Fprintf(a.Stdout, "  work items: %s\n", strings.Join(p.WorkItems, ", "))
		}
		for _, r := range p.Rules {
			fmt.Fprintf(a.Stdout, "  rule %s: %s\n", r.ID, r)
		}
	}
	return nil
}

func (a *app) projectsCreate(ctx context.Context, name string, members []string, auto bool) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	if cfg.ProjectIndex(name) >= 0 {
		return fmt.Errorf("a project named %q already exists; add members with `goldengate projects add`", name)
	}
	if len(members) == 0 {
		if _, err := cfg.CreateProject(name); err != nil {
			return err
		}
		cfg.Projects[cfg.ProjectIndex(name)].SetAuto(auto)
		if err := config.Save(a.cfgPath(), cfg); err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "created project %q (no members yet: nothing is shared)\n", name)
		return nil
	}
	if err := a.projectsAdd(ctx, name, members); err != nil {
		return err
	}
	if !auto {
		fmt.Fprintln(a.Stdout, "its sessions wait for your approval: `goldengate inbox`, or review and send from the Send screen")
		return nil
	}
	return a.projectsAuto(name, "on")
}

func (a *app) projectsRename(ref, name string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	if err := cfg.RenameProject(ref, name); err != nil {
		return err
	}
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "renamed %q to %q\n", ref, name)
	return nil
}

func (a *app) projectsMerge(from, into string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	if err := cfg.MergeProjects(from, into); err != nil {
		return err
	}
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "merged %q into %q\n", from, into)
	return nil
}

// projectsAdd shares the members by putting them in the project. Like
// `share add`, it warns when a deny rule still wins and clears tombstones so
// the members can be sent again.
func (a *app) projectsAdd(ctx context.Context, ref string, members []string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	created := cfg.ProjectIndex(ref) < 0
	var notes []string
	for _, raw := range members {
		rule := a.ruleFor(raw)
		moved, err := cfg.AddMember(ref, rule)
		if err != nil {
			return err
		}
		if moved != "" {
			notes = append(notes, fmt.Sprintf("moved %s from %q", rule, moved))
		} else {
			notes = append(notes, "added "+rule)
		}
	}
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return err
	}
	m, err := a.matcher(cfg)
	if err != nil {
		return err
	}
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	for _, raw := range members {
		if strings.ContainsAny(raw, "*?[{") {
			continue
		}
		if m.Denied(a.localKey(raw)) {
			fmt.Fprintf(a.Stderr, "warning: %s is also blocked (`share deny`); a block always wins, nothing from it will be shared\n", a.ruleFor(raw))
		}
		if err := st.ClearTombstone(ctx, a.localKey(raw)); err != nil {
			return err
		}
	}
	name := ref
	if i := cfg.ProjectIndex(ref); i >= 0 {
		name = cfg.Projects[i].Name
	}
	if created {
		fmt.Fprintf(a.Stdout, "created project %q\n", name)
	}
	for _, n := range notes {
		fmt.Fprintf(a.Stdout, "%s: %s (shared)\n", name, n)
	}
	return nil
}

func (a *app) projectsRemove(ctx context.Context, ref string, members []string, purge string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	i := cfg.ProjectIndex(ref)
	if i < 0 {
		return fmt.Errorf("no project named %q", ref)
	}
	name := cfg.Projects[i].Name
	rules := make([]string, 0, len(members))
	for _, raw := range members {
		rule := a.ruleFor(raw)
		if cfg.ProjectOfMember(rule) != i {
			return fmt.Errorf("%s is not a member of %q", rule, name)
		}
		rules = append(rules, rule)
	}
	return a.change(ctx, cfg, func(c *config.Config) {
		for _, r := range rules {
			_ = c.RemoveMember(name, r)
		}
	}, fmt.Sprintf("removed %d member(s) from %q; they are no longer shared", len(rules), name), purge)
}

func (a *app) projectsDelete(ctx context.Context, ref, purge string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	i := cfg.ProjectIndex(ref)
	if i < 0 {
		return fmt.Errorf("no project named %q", ref)
	}
	p := cfg.Projects[i]
	return a.change(ctx, cfg, func(c *config.Config) { _, _ = c.DeleteProject(p.ID) },
		fmt.Sprintf("deleted project %q; its %d member(s) are no longer shared", p.Name, len(p.Members)), purge)
}

func hasApproveRule(p config.Project) bool {
	for _, r := range p.Rules {
		if r.Action == rules.ActionApprove {
			return true
		}
	}
	return false
}

func (a *app) projectsAuto(ref, v string) error {
	var on bool
	switch strings.ToLower(v) {
	case "on":
		on = true
	case "off":
	default:
		return fmt.Errorf("say on or off, got %q", v)
	}
	cfg, err := a.load()
	if err != nil {
		return err
	}
	i := cfg.ProjectIndex(ref)
	if i < 0 {
		return fmt.Errorf("no project named %q", ref)
	}
	cfg.Projects[i].SetAuto(on)
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return err
	}
	if on {
		fmt.Fprintf(a.Stdout, "%q: new and changed sessions go out the next time you send, without asking\n", cfg.Projects[i].Name)
	} else {
		fmt.Fprintf(a.Stdout, "%q: new and changed sessions wait in the inbox until you approve them\n", cfg.Projects[i].Name)
	}
	return nil
}
