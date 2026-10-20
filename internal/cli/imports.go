package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/adapters/chatgpt"
	"github.com/OWNER/goldengate/internal/adapters/claudeai"
	"github.com/OWNER/goldengate/internal/adapters/export"
	"github.com/OWNER/goldengate/internal/config"
	"github.com/OWNER/goldengate/internal/pathutil"
	"github.com/OWNER/goldengate/internal/project"
)

func (a *app) resolver(cfg config.Config) project.Resolver {
	return project.Resolver{ReadGitPointers: cfg.Enrich.Git}
}

// exportAdapter builds the adapter for a chat-export source.
func exportAdapter(source string, paths []string) *export.Adapter {
	switch source {
	case claudeai.Name:
		return claudeai.New(paths)
	case chatgpt.Name:
		return chatgpt.New(paths)
	}
	return nil
}

// registry is the built-in adapters plus one adapter per source that has
// imported archives. Adapters already registered (tests) are left alone.
func (a *app) registry(ctx context.Context) (*adapters.Registry, error) {
	reg, err := a.NewRegistry(a.Home, a.Getenv)
	if err != nil {
		return nil, err
	}
	st, err := a.openState()
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()
	imps, err := st.ListImports(ctx)
	if err != nil {
		return nil, err
	}
	bySource := map[string][]string{}
	for _, i := range imps {
		bySource[i.Source] = append(bySource[i.Source], i.Path)
	}
	for src, paths := range bySource {
		if _, exists := reg.Get(src); exists {
			continue
		}
		if ad := exportAdapter(src, paths); ad != nil {
			if err := reg.Register(ad); err != nil {
				return nil, err
			}
		}
	}
	return reg, nil
}

func (a *app) importCmds() []*cobra.Command {
	mk := func(use, source, product, where string) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <export.zip>",
			Short: "Register a " + product + " data export so its conversations can be shared",
			Long: `Registers the ` + product + ` data export zip (` + where + `).

The zip is validated and then left where it is: goldengate reads it each time
you sync, never extracts it to disk, and only opens its conversation files (not
account details). Conversations are grouped into projects such as "` + source + `:Chats".
Nothing is shared until you allow a project explicitly with 'goldengate share add'.`,
			Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return a.runImport(cmd.Context(), source, product, args[0])
			},
		}
	}
	return []*cobra.Command{
		mk("import-claude-ai", claudeai.Name, "claude.ai", "Settings > Privacy > Export data"),
		mk("import-chatgpt", chatgpt.Name, "ChatGPT", "Settings > Data controls > Export data"),
	}
}

func (a *app) runImport(ctx context.Context, source, product, arg string) error {
	abs := pathutil.Normalize(pathutil.ExpandHome(arg, a.Home))
	sum, err := exportAdapter(source, nil).Probe(abs)
	if err != nil {
		return fmt.Errorf("%s does not look like a usable %s export: %w", arg, product, err)
	}
	if sum.Conversations == 0 {
		return fmt.Errorf("%s contains no conversations I can read (%d entries in an unknown shape)", arg, sum.Skipped)
	}
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err := st.AddImport(ctx, source, abs, a.Now()); err != nil {
		return err
	}
	cfg, err := a.load()
	if err != nil {
		return err
	}
	if len(cfg.Share.IncludeSources) > 0 && !slices.Contains(cfg.Share.IncludeSources, source) {
		cfg.Share.IncludeSources = append(cfg.Share.IncludeSources, source)
		if err := config.Save(a.cfgPath(), cfg); err != nil {
			return err
		}
	}
	w := a.Stdout
	fmt.Fprintf(w, "Registered %s export: %d conversation(s)", product, sum.Conversations)
	if sum.Skipped > 0 {
		fmt.Fprintf(w, ", %d skipped (unknown shape)", sum.Skipped)
	}
	fmt.Fprintln(w, ".")
	keys := make([]string, 0, len(sum.Buckets))
	for k := range sum.Buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %-40s %d conversation(s)\n", k, sum.Buckets[k])
	}
	fmt.Fprintf(w, "\nNothing is shared yet. To share a project:  goldengate share add '%s'\nThe zip stays where it is; keep it there, or use 'goldengate imports remove'.\n", keys[0])
	return nil
}

func (a *app) importsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "imports", Short: "List or remove registered chat exports"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List registered export archives", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.openState()
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			imps, err := st.ListImports(c.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSOURCE\tADDED\tFILE")
			for _, i := range imps {
				state := ""
				if _, err := os.Stat(i.Path); err != nil {
					state = "  (missing)"
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s%s\n", i.ID, i.Source, i.AddedAt.Local().Format("2006-01-02"), pathutil.Scrub(i.Path, a.Home), state)
			}
			return w.Flush()
		},
	}, &cobra.Command{
		Use: "remove <id>", Short: "Forget an archive (the file is left alone; data already on the server stays until you purge it)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
			if err != nil {
				return fmt.Errorf("%q is not an import id (see 'goldengate imports list')", args[0])
			}
			st, err := a.openState()
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			ok, err := st.RemoveImport(c.Context(), id)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no import with id %d", id)
			}
			fmt.Fprintf(a.Stdout, "forgot import %d\n", id)
			return nil
		},
	})
	return cmd
}
