package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/export"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Chats come from imported exports, where a grouping ("claude-ai:Chats") can
// hold hundreds of unrelated conversations. The unit of sharing is therefore
// the single conversation: `goldengate chats share <id> --project <name>`
// shares that one and nothing else of its grouping.

// chat is one imported conversation and where it stands.
type chat struct {
	export.Head
	Source string
	// Project is the named project that shares it ("" when none does);
	// Whole is set when it is shared because its whole grouping is.
	Project string
	Whole   bool
}

// heads is implemented by the export adapters.
type heads interface{ Heads() []export.Head }

// chats lists every imported conversation, newest first.
func (a *app) chats(ctx context.Context, cfg config.Config) ([]chat, error) {
	reg, err := a.registry(ctx)
	if err != nil {
		return nil, err
	}
	var out []chat
	for _, ad := range reg.All() {
		h, ok := ad.(heads)
		if !ok {
			continue
		}
		for _, hd := range h.Heads() {
			c := chat{Head: hd, Source: ad.Name()}
			if limited, i := cfg.SessionScope(hd.Project, hd.ID); limited {
				if i >= 0 {
					c.Project = cfg.Projects[i].Name
				}
			} else if w := cfg.WholeMember(hd.Project); w >= 0 {
				c.Project, c.Whole = cfg.Projects[w].Name, true
			}
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

func (a *app) chatsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chats",
		Short: "Share single conversations from your imported claude.ai and ChatGPT exports",
		Long: `Lists the conversations of your imported chat exports and shares them one at a
time. Listing shows titles on this machine only; nothing is sent by listing.

  goldengate chats                          conversations that are in no project
  goldengate chats --all                    every conversation, with the project that shares it
  goldengate chats --grep invoice           only titles containing a word
  goldengate chats share <id>... --project "Job search"
                                            share these conversations, and only these; they then
                                            wait in the inbox like any session
  goldengate chats move <id> <project>      put a shared conversation into another project
  goldengate chats unshare <id>...          stop sharing them (a conversation already sent stays
                                            on the server until you retract it)

To share everything of a grouping instead: goldengate projects add <project> claude-ai:Chats`,
		Args: cobra.NoArgs,
	}
	var all bool
	var grep, source string
	var limit int
	cmd.RunE = func(c *cobra.Command, _ []string) error { return a.chatsList(c.Context(), all, grep, source, limit) }
	cmd.Flags().BoolVar(&all, "all", false, "also list conversations that are already in a project")
	cmd.Flags().StringVar(&grep, "grep", "", "only conversations whose title contains this text")
	cmd.Flags().StringVar(&source, "source", "", "only this source (claude-ai, chatgpt)")
	cmd.Flags().IntVar(&limit, "limit", 50, "how many to list (0: all)")
	var project string
	share := &cobra.Command{Use: "share <conversation-id>... --project <name>", Short: "Share single conversations: only they are sent, after you approve them", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return a.chatsShare(c.Context(), args, project) }}
	share.Flags().StringVar(&project, "project", "", "the project to put them in (created if new)")
	move := &cobra.Command{Use: "move <conversation-id> <project>", Short: "Put a conversation into another project", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error { return a.chatsShare(c.Context(), args[:1], args[1]) }}
	unshare := &cobra.Command{Use: "unshare <conversation-id>...", Short: "Stop sharing single conversations", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return a.chatsUnshare(c.Context(), args) }}
	cmd.AddCommand(share, move, unshare)
	return cmd
}

func (a *app) chatsList(ctx context.Context, all bool, grep, source string, limit int) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	cs, err := a.chats(ctx, cfg)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		fmt.Fprintln(a.Stdout, "no imported conversations: import an export first (`goldengate import-claude-ai <zip>` or `goldengate import-chatgpt <zip>`)")
		return nil
	}
	shown, hidden, unassigned := 0, 0, 0
	for _, c := range cs {
		if c.Project == "" {
			unassigned++
		}
		if (!all && c.Project != "") || (source != "" && c.Source != source) || (grep != "" && !strings.Contains(strings.ToLower(c.Title), strings.ToLower(grep))) {
			continue
		}
		if limit > 0 && shown >= limit {
			hidden++
			continue
		}
		if shown == 0 {
			fmt.Fprintln(a.Stdout, "conversations on this machine (nothing here is sent by listing):")
		}
		shown++
		where := "in no project"
		switch {
		case c.Whole:
			where = fmt.Sprintf("in %q with all of %s", c.Project, c.Head.Project)
		case c.Project != "":
			where = fmt.Sprintf("in %q", c.Project)
		}
		title := c.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(a.Stdout, "  %s  %-9s  %s  %s  [%s; from %s]\n", c.ID, c.Source, c.Updated.Local().Format("2006-01-02"), title, where, c.Head.Project)
	}
	if shown == 0 {
		fmt.Fprintf(a.Stdout, "nothing to list: %d conversation(s) imported, %d in no project (try --all, or another --grep)\n", len(cs), unassigned)
		return nil
	}
	if hidden > 0 {
		fmt.Fprintf(a.Stdout, "  ... and %d more (--limit 0 lists all, --grep narrows)\n", hidden)
	}
	fmt.Fprintln(a.Stdout, "\nshare one:  goldengate chats share <conversation-id> --project <name>")
	return nil
}

// findChats resolves conversation ids, refusing ids that are unknown or that
// two sources share.
func findChats(cs []chat, ids []string) ([]chat, error) {
	var out []chat
	for _, id := range ids {
		var hit []chat
		for _, c := range cs {
			if c.ID == id {
				hit = append(hit, c)
			}
		}
		switch len(hit) {
		case 0:
			return nil, fmt.Errorf("no imported conversation %s (see `goldengate chats --all`)", id)
		case 1:
			out = append(out, hit[0])
		default:
			return nil, fmt.Errorf("conversation id %s exists in several sources; this is not supported", id)
		}
	}
	return out, nil
}

func (a *app) chatsShare(ctx context.Context, ids []string, project string) error {
	if strings.TrimSpace(project) == "" {
		return errors.New("say which project: --project <name> (created if new)")
	}
	cfg, err := a.load()
	if err != nil {
		return err
	}
	cs, err := a.chats(ctx, cfg)
	if err != nil {
		return err
	}
	picked, err := findChats(cs, ids)
	if err != nil {
		return err
	}
	m, err := a.matcher(cfg)
	if err != nil {
		return err
	}
	created := cfg.ProjectIndex(project) < 0
	var lines []string
	for _, c := range picked {
		if m.Denied(c.Head.Project) {
			return fmt.Errorf("%s is matched by a deny rule; deny wins, so nothing from it can be shared", c.Head.Project)
		}
		moved, err := cfg.ShareSession(project, c.Head.Project, c.ID)
		if err != nil {
			return err
		}
		title := c.Title
		if title == "" {
			title = c.ID
		}
		if moved != "" {
			lines = append(lines, fmt.Sprintf("moved %q from %q", title, moved))
		} else {
			lines = append(lines, fmt.Sprintf("shared %q", title))
		}
	}
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return err
	}
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	for _, c := range picked {
		if !pathutil.IsVirtualKey(c.Head.Project) {
			continue
		}
		if err := st.ClearTombstone(ctx, c.Head.Project); err != nil {
			return err
		}
	}
	i := cfg.ProjectIndex(project)
	name := cfg.Projects[i].Name
	if created {
		fmt.Fprintf(a.Stdout, "created project %q\n", name)
	}
	for _, l := range lines {
		fmt.Fprintf(a.Stdout, "%s: %s\n", name, l)
	}
	if cfg.Projects[i].Auto() && !hasApproveRule(cfg.Projects[i]) {
		fmt.Fprintf(a.Stdout, "%q is at trust level auto: these conversations are sent on the next sync. Nothing else of their grouping is shared.\n", name)
	} else {
		fmt.Fprintln(a.Stdout, "Only these conversations are shared, nothing else of their grouping. They wait for your approval: `goldengate inbox`.")
	}
	return nil
}

func (a *app) chatsUnshare(ctx context.Context, ids []string) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	cs, err := a.chats(ctx, cfg)
	if err != nil {
		return err
	}
	picked, err := findChats(cs, ids)
	if err != nil {
		return err
	}
	n := 0
	for _, c := range picked {
		if c.Whole {
			return fmt.Errorf("%s is shared because all of %s is in project %q; to keep this one conversation back use `goldengate sessions exclude %s`", c.ID, c.Head.Project, c.Project, c.ID)
		}
		if cfg.UnshareSession(c.Head.Project, c.ID) != "" {
			n++
		}
	}
	if n == 0 {
		fmt.Fprintln(a.Stdout, "none of these conversations was shared")
		return nil
	}
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "%d conversation(s) are no longer shared. One that was already sent stays on the server until you retract it: `goldengate sessions retract <id>`.\n", n)
	return nil
}
