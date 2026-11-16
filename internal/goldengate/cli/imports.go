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

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/chatgpt"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/claudeai"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/export"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/project"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
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
Nothing is shared until you share single conversations with 'goldengate chats share',
or a whole grouping with 'goldengate share add'.`,
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
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	imps, err := st.ListImports(ctx)
	if err != nil {
		return err
	}
	// compare with the exports of this product that are already registered
	var have []string
	registered := false
	for _, i := range imps {
		if i.Source != source {
			continue
		}
		if i.Path == abs {
			registered = true
			continue
		}
		have = append(have, i.Path)
	}
	d, err := exportAdapter(source, have).Compare(abs)
	if err != nil {
		return fmt.Errorf("%s does not look like a usable %s export: %w", arg, product, err)
	}
	if d.Total == 0 {
		return fmt.Errorf("%s contains no conversations I can read (%d entries in an unknown shape)", arg, d.Skipped)
	}
	w := a.Stdout
	if registered {
		fmt.Fprintf(w, "%s is already registered (%d conversation(s)); nothing was added.\n", pathutil.Scrub(abs, a.Home), d.Total)
		return nil
	}
	if !d.Adds() {
		// the same conversations under another file name, or an older export
		fmt.Fprintf(w, "Nothing new in this %s export: all %d conversation(s) are already imported", product, d.Total)
		if d.Older > 0 {
			fmt.Fprintf(w, " (%d of them in a more recent version)", d.Older)
		}
		fmt.Fprintln(w, ". It was not registered.")
		return nil
	}
	if err := st.AddImport(ctx, source, abs, a.Now()); err != nil {
		return err
	}
	if err := st.RecordImport(ctx, source, abs, d.Total, d.New, d.Changed); err != nil {
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
	fmt.Fprintf(w, "Registered %s export: %d conversation(s)", product, d.Total)
	if len(have) > 0 {
		fmt.Fprintf(w, ", %d new, %d changed, %d already imported", d.New, d.Changed, d.Same+d.Older)
	}
	if d.Skipped > 0 {
		fmt.Fprintf(w, ", %d skipped (unknown shape)", d.Skipped)
	}
	fmt.Fprintln(w, ".")
	keys := make([]string, 0, len(d.NewByGrouping))
	for k := range d.NewByGrouping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %-40s %d conversation(s)\n", k, d.NewByGrouping[k])
	}
	if len(have) > 0 {
		// what happens to them depends on what is already shared
		m, merr := a.matcher(cfg)
		waiting, loose := 0, 0
		for _, k := range keys {
			limited, _ := cfg.SessionScope(k, "")
			if merr == nil && m.Decide(k, source).Shared && !limited {
				waiting += d.NewByGrouping[k]
			} else {
				loose += d.NewByGrouping[k]
			}
		}
		fmt.Fprintln(w)
		if waiting > 0 {
			fmt.Fprintf(w, "%d new conversation(s) are in groupings you share: they follow those projects' rules (see `goldengate inbox`).\n", waiting)
		}
		if loose > 0 {
			fmt.Fprintf(w, "%d new conversation(s) are in no project: `goldengate chats` lists them.\n", loose)
		}
		if d.Changed > 0 {
			fmt.Fprintf(w, "%d conversation(s) continued since the last export: a shared one waits in the inbox again before its new version is sent.\n", d.Changed)
		}
		fmt.Fprintln(w, "The zip stays where it is; keep it there, or use 'goldengate imports remove'.")
		return nil
	}
	if len(keys) == 0 {
		return nil
	}
	fmt.Fprintf(w, "\nNothing is shared yet.\n  share single conversations:  goldengate chats    (then: goldengate chats share <id> --project <name>)\n  share a whole grouping:      goldengate share add '%s'\nThe zip stays where it is; keep it there, or use 'goldengate imports remove'.\n", keys[0])
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
			fmt.Fprintln(w, "ID\tSOURCE\tADDED\tBROUGHT IN\tFILE")
			for _, i := range imps {
				state := ""
				if _, err := os.Stat(i.Path); err != nil {
					state = "  (missing)"
				}
				brought := "(not recorded)"
				if i.Counted {
					brought = fmt.Sprintf("%d new, %d changed, of %d", i.Added, i.Changed, i.Total)
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s%s\n", i.ID, i.Source, i.AddedAt.Local().Format("2006-01-02"), brought, pathutil.Scrub(i.Path, a.Home), state)
			}
			return w.Flush()
		},
	}, &cobra.Command{
		Use: "remove <id>", Short: "Forget an archive (the file is left alone; conversations already sent stay on the server until you retract or delete them there)", Args: cobra.ExactArgs(1),
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
			fmt.Fprintf(a.Stdout, "forgot import %d. Nothing was deleted: conversations already sent stay on the server until you retract them (`goldengate sessions retract`).\n", id)
			return nil
		},
	})
	return cmd
}
