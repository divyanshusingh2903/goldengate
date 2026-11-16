package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/client"
)

// agentsCmd manages who may read what on the server: named agents (one per
// assistant), the projects each may see, and their read tokens. Every
// subcommand needs the admin secret, which is asked for and never stored.
func (a *app) agentsCmd() *cobra.Command {
	var stdinTok bool
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Choose which assistant sees which projects (needs the admin token)",
		Long: `An agent is one assistant that reads your work through the server (ChatGPT, Claude, ...).
Each agent has its own assistant token and sees only the projects you grant it. A project you
start sharing is visible to no agent until you grant it, unless the agent is set to see
all projects.

Every command here changes or shows who can read what, so it needs the admin secret
(GOLDENGATE_ADMIN_TOKEN, --admin-token-stdin, or a hidden prompt). It is used for the
call and never stored.`,
	}
	cmd.PersistentFlags().BoolVar(&stdinTok, "admin-token-stdin", false, "read the admin token from stdin")
	admin := func() (*client.Client, error) {
		cfg, err := a.load()
		if err != nil {
			return nil, err
		}
		if cfg.Server.URL == "" {
			return nil, errors.New("no server configured: run `goldengate init` first")
		}
		tok, err := a.adminToken(stdinTok, false)
		if err != nil {
			return nil, err
		}
		return client.New(cfg.Server.URL, tok, a.HTTP, a.userAgent())
	}

	list := &cobra.Command{Use: "list", Short: "List agents and what each may see", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ac, err := admin()
			if err != nil {
				return err
			}
			as, err := ac.ListAgents(c.Context())
			if err != nil {
				return agentsErr(err)
			}
			if len(as) == 0 {
				fmt.Fprintln(a.Stdout, "no agents yet: add one with `goldengate agents add <name>`")
				return nil
			}
			for _, ag := range as {
				a.printAgent(ag)
			}
			return nil
		}}

	var maxDays int
	var all bool
	add := &cobra.Command{Use: "add <name>", Short: "Add an agent; it sees nothing until you grant projects", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ac, err := admin()
			if err != nil {
				return err
			}
			ag, err := ac.CreateAgent(c.Context(), args[0], maxDays, all)
			if err != nil {
				return agentsErr(err)
			}
			fmt.Fprintf(a.Stdout, "added agent %q (%s)\n", ag.Name, ag.ID)
			if ag.AllProjects {
				fmt.Fprintln(a.Stdout, "it can see every project you share, including ones you share later")
			} else {
				fmt.Fprintf(a.Stdout, "it sees nothing yet: grant projects with `goldengate agents grant %s <project>`\n", quoteArg(ag.Name))
			}
			fmt.Fprintf(a.Stdout, "create its token with `goldengate agents token %s`\n", quoteArg(ag.Name))
			return nil
		}}
	add.Flags().IntVar(&maxDays, "max-days", 0, "the furthest back this agent may look, in days (0 = no limit of its own)")
	add.Flags().BoolVar(&all, "all-projects", false, "let this agent see every project you share, including future ones")

	rename := &cobra.Command{Use: "rename <agent> <new name>", Short: "Rename an agent", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			ac, ag, err := a.agentArg(c.Context(), admin, args[0])
			if err != nil {
				return err
			}
			if _, err := ac.UpdateAgent(c.Context(), ag.ID, client.AgentUpdate{Name: &args[1]}); err != nil {
				return agentsErr(err)
			}
			fmt.Fprintf(a.Stdout, "renamed %q to %q\n", ag.Name, strings.TrimSpace(args[1]))
			return nil
		}}

	limit := &cobra.Command{Use: "limit <agent> <days>", Short: "Set the furthest back an agent may look (0 = no limit of its own)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			days, err := strconv.Atoi(args[1])
			if err != nil || days < 0 {
				return fmt.Errorf("days must be a whole number, 0 or more, got %q", args[1])
			}
			ac, ag, err := a.agentArg(c.Context(), admin, args[0])
			if err != nil {
				return err
			}
			if _, err := ac.UpdateAgent(c.Context(), ag.ID, client.AgentUpdate{MaxDays: &days}); err != nil {
				return agentsErr(err)
			}
			if days == 0 {
				fmt.Fprintf(a.Stdout, "%q has no history limit of its own\n", ag.Name)
			} else {
				fmt.Fprintf(a.Stdout, "%q can look back at most %d days\n", ag.Name, days)
			}
			return nil
		}}

	remove := &cobra.Command{Use: "remove <agent>", Short: "Remove an agent and revoke its tokens", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ac, ag, err := a.agentArg(c.Context(), admin, args[0])
			if err != nil {
				return err
			}
			n, err := ac.DeleteAgent(c.Context(), ag.ID)
			if err != nil {
				return agentsErr(err)
			}
			fmt.Fprintf(a.Stdout, "removed agent %q; %d token(s) revoked\n", ag.Name, n)
			return nil
		}}

	var grantAll, revokeAll bool
	grant := &cobra.Command{Use: "grant <agent> <project>...", Short: "Let an agent see projects (by name, id or folder); --all for every project",
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if grantAll == (len(args) > 1) {
				return errors.New("name the projects to grant, or pass --all (not both)")
			}
			ac, ag, err := a.agentArg(c.Context(), admin, args[0])
			if err != nil {
				return err
			}
			if grantAll {
				on := true
				if _, err := ac.UpdateAgent(c.Context(), ag.ID, client.AgentUpdate{AllProjects: &on}); err != nil {
					return agentsErr(err)
				}
				fmt.Fprintf(a.Stdout, "%q can now see every project you share, including ones you share later\n", ag.Name)
				return nil
			}
			for _, raw := range args[1:] {
				p, err := ac.Grant(c.Context(), ag.ID, a.projectRef(raw))
				if err != nil {
					return fmt.Errorf("%s: %w", raw, agentsErr(err))
				}
				fmt.Fprintf(a.Stdout, "%q can now see %s\n", ag.Name, p.Name)
			}
			return nil
		}}
	grant.Flags().BoolVar(&grantAll, "all", false, "every project you share, including ones you share later")

	revoke := &cobra.Command{Use: "revoke <agent> <project>...", Short: "Stop an agent seeing projects; --all turns off 'every project'",
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if revokeAll == (len(args) > 1) {
				return errors.New("name the projects to revoke, or pass --all (not both)")
			}
			ac, ag, err := a.agentArg(c.Context(), admin, args[0])
			if err != nil {
				return err
			}
			if revokeAll {
				off := false
				if _, err := ac.UpdateAgent(c.Context(), ag.ID, client.AgentUpdate{AllProjects: &off}); err != nil {
					return agentsErr(err)
				}
				fmt.Fprintf(a.Stdout, "%q no longer sees every project; it sees only the %d granted to it\n", ag.Name, len(ag.Grants))
				return nil
			}
			for _, raw := range args[1:] {
				if err := ac.RevokeGrant(c.Context(), ag.ID, a.projectRef(raw)); err != nil {
					return fmt.Errorf("%s: %w", raw, agentsErr(err))
				}
				fmt.Fprintf(a.Stdout, "%q can no longer see %s\n", ag.Name, raw)
			}
			if ag.AllProjects {
				fmt.Fprintf(a.Stderr, "note: %q is set to see every project, so this changes nothing until you run `goldengate agents revoke %s --all`\n", ag.Name, quoteArg(ag.Name))
			}
			return nil
		}}
	revoke.Flags().BoolVar(&revokeAll, "all", false, "turn off 'every project' for this agent")

	var label, expires string
	token := &cobra.Command{Use: "token <agent>", Short: "Create an assistant token for an agent (shown once)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ac, ag, err := a.agentArg(c.Context(), admin, args[0])
			if err != nil {
				return err
			}
			m, err := ac.MintAgentToken(c.Context(), ag.ID, label, expires)
			if err != nil {
				return agentsErr(err)
			}
			cfg, _ := a.load()
			fmt.Fprintf(a.Stdout, "Assistant token for %q (shown ONCE; it cannot be recovered):\n\n    %s\n\nConnector URL: %s/mcp\nSend it as \"Authorization: Bearer <token>\". Revoke it by removing the agent, or with `goldengatebridge token revoke %s` on the server.\n",
				ag.Name, m.Secret, cfg.Server.URL, m.ID)
			return nil
		}}
	token.Flags().StringVar(&label, "label", "", "a note stored with the token (default: the agent's name)")
	token.Flags().StringVar(&expires, "expires-in", "", "lifetime as a duration such as 720h (default: no expiry)")

	cmd.AddCommand(list, add, rename, limit, remove, grant, revoke, token)
	return cmd
}

func (a *app) printAgent(ag client.Agent) {
	fmt.Fprintf(a.Stdout, "%s  (%s)\n", ag.Name, ag.ID)
	switch {
	case ag.AllProjects:
		fmt.Fprintln(a.Stdout, "  sees: every project you share, including ones you share later")
	case len(ag.Grants) == 0:
		fmt.Fprintln(a.Stdout, "  sees: nothing (no projects granted)")
	default:
		names := make([]string, 0, len(ag.Grants))
		for _, g := range ag.Grants {
			names = append(names, g.Name)
		}
		fmt.Fprintf(a.Stdout, "  sees: %s\n", strings.Join(names, ", "))
	}
	if ag.MaxDays > 0 {
		fmt.Fprintf(a.Stdout, "  history: the last %d days at most\n", ag.MaxDays)
	}
	conn := fmt.Sprintf("  connected: %d token(s)", ag.Tokens)
	if len(ag.Clients) > 0 {
		names := make([]string, 0, len(ag.Clients))
		for _, c := range ag.Clients {
			names = append(names, c.Name)
		}
		conn += ", OAuth: " + strings.Join(names, ", ")
	}
	fmt.Fprintln(a.Stdout, conn)
}

// agentArg connects with the admin token and finds the agent named by ref
// (its id, or its name ignoring case).
func (a *app) agentArg(ctx context.Context, admin func() (*client.Client, error), ref string) (*client.Client, client.Agent, error) {
	ac, err := admin()
	if err != nil {
		return nil, client.Agent{}, err
	}
	as, err := ac.ListAgents(ctx)
	if err != nil {
		return nil, client.Agent{}, agentsErr(err)
	}
	for _, ag := range as {
		if ag.ID == ref {
			return ac, ag, nil
		}
	}
	for _, ag := range as {
		if strings.EqualFold(ag.Name, strings.TrimSpace(ref)) {
			return ac, ag, nil
		}
	}
	return nil, client.Agent{}, fmt.Errorf("no agent named %q: see `goldengate agents list`", ref)
}

// projectRef turns what the user typed into what the server resolves: the id
// of a local project named so, a folder's key, or the text as given (a
// project id or name on the server).
func (a *app) projectRef(raw string) string {
	if cfg, err := a.load(); err == nil {
		if i := cfg.ProjectIndex(raw); i >= 0 {
			return cfg.Projects[i].ID
		}
	}
	if strings.HasPrefix(raw, "~") || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, ".") {
		return a.ruleFor(raw)
	}
	return raw
}

// agentsErr explains the two failures a user can act on.
func agentsErr(err error) error {
	var api *client.APIError
	if errors.As(err, &api) {
		switch api.Status {
		case 401, 403:
			return fmt.Errorf("the server refused the admin token: %w", err)
		case 404:
			if strings.Contains(api.Message, "not found") {
				return fmt.Errorf("this server does not manage agents yet (upgrade goldengatebridge): %w", err)
			}
		}
	}
	return err
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t'\"") {
		return strconv.Quote(s)
	}
	return s
}
