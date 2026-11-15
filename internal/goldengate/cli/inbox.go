package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
)

// pendingSessions plans a sync without sending anything and returns the
// sessions that wait for approval. Like every plan, it reads and summarizes
// only sessions of shared projects, and with a model it uses cached answers
// only when offline is set.
func (a *app) pendingSessions(ctx context.Context, cfg config.Config, st *gsync.State, project string) ([]gsync.PendingItem, error) {
	m, err := a.matcher(cfg)
	if err != nil {
		return nil, err
	}
	reg, err := a.registry(ctx)
	if err != nil {
		return nil, err
	}
	s := &gsync.Syncer{Registry: reg, Config: cfg, Matcher: m, State: st, Home: a.Home, Now: a.Now, Resolver: a.resolver(cfg),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Summarizer: a.summarizer(cfg)}
	_, rep, err := s.Plan(ctx, gsync.RunOpts{Project: project, Offline: true}) // listing never calls a model
	if err != nil {
		return nil, err
	}
	items := rep.PendingItems
	sort.SliceStable(items, func(i, j int) bool { return items[i].Updated.After(items[j].Updated) })
	return items, nil
}

// inboxCmd shows and decides the sessions that wait for approval.
func (a *app) inboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Sessions waiting for your approval before they are sent",
		Long: `New and changed sessions of a project you share wait here until you approve them,
unless the project is set to auto-approve (goldengate projects auto <project> on).
A session that changes after you approved it comes back here.

  goldengate inbox                     list what is waiting
  goldengate sync --dry-run --approve  print exactly what approving would send
  goldengate inbox approve --all       approve everything listed
  goldengate sync                      send what is approved`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return a.inboxList(c.Context()) },
	}
	list := &cobra.Command{Use: "list", Short: "List sessions waiting for approval, and the ones you excluded", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return a.inboxList(c.Context()) }}
	var all bool
	var project string
	approve := &cobra.Command{Use: "approve <session-id>... | --all", Short: "Approve waiting sessions; the next sync sends them",
		RunE: func(c *cobra.Command, args []string) error {
			return a.inboxDecide(c.Context(), gsync.DecisionApproved, args, all, project)
		}}
	exclude := &cobra.Command{Use: "exclude <session-id>... | --all", Short: "Keep waiting sessions on this machine: they are never sent",
		RunE: func(c *cobra.Command, args []string) error {
			return a.inboxDecide(c.Context(), gsync.DecisionExcluded, args, all, project)
		}}
	for _, c := range []*cobra.Command{approve, exclude} {
		c.Flags().BoolVar(&all, "all", false, "every waiting session (of --project, if given)")
		c.Flags().StringVar(&project, "project", "", "only sessions of this folder")
	}
	reset := &cobra.Command{Use: "reset <session-id>...", Short: "Undo a decision: the session waits for approval again", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return a.inboxReset(c.Context(), args) }}
	cmd.AddCommand(list, approve, exclude, reset)
	return cmd
}

func (a *app) inboxList(ctx context.Context) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	items, err := a.pendingSessions(ctx, cfg, st, "")
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(a.Stdout, "nothing is waiting for approval")
	} else {
		fmt.Fprintf(a.Stdout, "%d session(s) waiting for approval:\n", len(items))
		for _, it := range items {
			note := "new"
			if it.Changed {
				note = "changed since you approved or sent it"
			}
			fmt.Fprintf(a.Stdout, "  %s  %-11s  %s  %s  (%s)\n", it.SessionID, it.Source, projectLabel(it), it.Title, note)
		}
		fmt.Fprintln(a.Stdout, "\nlook first:  goldengate sync --dry-run --approve\napprove:     goldengate inbox approve --all    (or name session ids)\nkeep local:  goldengate inbox exclude <session-id>")
	}
	ex, err := st.Decisions(ctx, gsync.DecisionExcluded)
	if err != nil {
		return err
	}
	if len(ex) > 0 {
		fmt.Fprintf(a.Stdout, "\n%d excluded session(s), never sent:\n", len(ex))
		for _, d := range ex {
			fmt.Fprintf(a.Stdout, "  %s  %-11s  %s  %s\n", d.SessionID, d.Source, d.Project, d.Title)
		}
		fmt.Fprintln(a.Stdout, "undo with:   goldengate inbox reset <session-id>")
	}
	return nil
}

func projectLabel(it gsync.PendingItem) string {
	if it.ProjectName != "" {
		return it.ProjectName
	}
	return it.Project
}

func (a *app) inboxDecide(ctx context.Context, state string, ids []string, all bool, project string) error {
	if all == (len(ids) > 0) {
		return errors.New("name the session ids, or pass --all (not both)")
	}
	cfg, err := a.load()
	if err != nil {
		return err
	}
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	items, err := a.pendingSessions(ctx, cfg, st, project)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	n := 0
	for _, it := range items {
		if !all && !want[it.SessionID] {
			continue
		}
		delete(want, it.SessionID)
		if err := st.Decide(ctx, gsync.Decision{Source: it.Source, SessionID: it.SessionID, State: state, ContentHash: it.ContentHash,
			At: a.Now(), Project: projectLabel(it), Title: it.Title}); err != nil {
			return err
		}
		n++
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return fmt.Errorf("not waiting in the inbox: %s (see `goldengate inbox`)", strings.Join(missing, ", "))
	}
	if state == gsync.DecisionApproved {
		fmt.Fprintf(a.Stdout, "approved %d session(s); `goldengate sync` sends them\n", n)
	} else {
		fmt.Fprintf(a.Stdout, "excluded %d session(s); they stay on this machine\n", n)
	}
	return nil
}

func (a *app) inboxReset(ctx context.Context, ids []string) error {
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	all, err := st.Decisions(ctx, "")
	if err != nil {
		return err
	}
	n := 0
	for _, id := range ids {
		found := false
		for _, d := range all {
			if d.SessionID != id {
				continue
			}
			found = true
			if d.State == gsync.DecisionRetracted {
				return fmt.Errorf("%s was retracted from the server; share it again with `goldengate sessions include %s`", id, id)
			}
			if _, err := st.Undecide(ctx, d.Source, d.SessionID); err != nil {
				return err
			}
			n++
		}
		if !found {
			return fmt.Errorf("no decision recorded for %s", id)
		}
	}
	fmt.Fprintf(a.Stdout, "%d decision(s) removed; those sessions wait for approval again\n", n)
	return nil
}

// Inbox implements tui.Backend.
func (b *tuiBackend) Inbox(ctx context.Context) (tui.Inbox, error) {
	var in tui.Inbox
	err := b.a.withRows(ctx, func(_ config.Config, _ *gsync.State, rows []sessionRow) error {
		for _, r := range rows {
			it := tui.InboxItem{Source: r.Source, ID: r.ID, Project: r.Project, Title: r.Title, Updated: r.Updated, Hash: r.Hash, State: r.State,
				Changed: r.State == sessChanged, Edited: r.Edited}
			switch {
			case r.State == sessConflict: // needs the user's attention like anything that waits
				in.Pending = append(in.Pending, it)
			case r.State == sessExcluded:
				in.Excluded = append(in.Excluded, it)
			case r.State == sessRetracted:
				in.Retracted = append(in.Retracted, it)
			case r.Waits:
				in.Pending = append(in.Pending, it)
			default:
				in.Sent = append(in.Sent, it)
			}
		}
		return nil
	})
	return in, err
}

// InboxDecide implements tui.Backend.
func (b *tuiBackend) InboxDecide(ctx context.Context, items []tui.InboxItem, decision string) error {
	defer func() {
		b.mu.Lock()
		b.reviewed = nil // what was reviewed may no longer be what would be sent
		b.mu.Unlock()
	}()
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	switch decision {
	case tui.InboxApprove:
		_, err := b.a.decideInclude(ctx, ids)
		return err
	case tui.InboxKeepEdit, tui.InboxDropEdit:
		cfg, err := b.a.load()
		if err != nil {
			return err
		}
		st, err := b.a.openState()
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		for _, id := range ids {
			if decision == tui.InboxKeepEdit {
				_, err = b.a.editKeep(ctx, cfg, st, id)
			} else {
				_, err = b.a.editDiscard(ctx, st, id)
			}
			if err != nil {
				return err
			}
		}
		return nil
	case tui.InboxExclude, tui.InboxReset, tui.InboxRetract:
		return b.a.withRows(ctx, func(cfg config.Config, st *gsync.State, rows []sessionRow) error {
			picked, err := pick(rows, ids)
			if err != nil {
				return err
			}
			for _, r := range picked {
				switch decision {
				case tui.InboxExclude:
					err = st.Decide(ctx, gsync.Decision{Source: r.Source, SessionID: r.ID, State: gsync.DecisionExcluded, ContentHash: r.Hash, At: b.a.Now(), Project: r.Project, Title: r.Title})
				case tui.InboxReset:
					_, err = st.Undecide(ctx, r.Source, r.ID)
				default:
					if _, seen, gerr := st.Get(ctx, r.Source, r.ID); gerr != nil {
						err = gerr
					} else if !seen {
						err = fmt.Errorf("session %s was not sent from this computer: there is nothing to retract", r.ID)
					} else {
						err = b.a.retractSession(ctx, cfg, st, r)
					}
				}
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	return fmt.Errorf("unknown inbox decision %q", decision)
}

// EditBegin implements tui.Backend.
func (b *tuiBackend) EditBegin(ctx context.Context, item tui.InboxItem) (string, error) {
	cfg, err := b.a.load()
	if err != nil {
		return "", err
	}
	st, err := b.a.openState()
	if err != nil {
		return "", err
	}
	defer func() { _ = st.Close() }()
	f, err := b.a.editPrepare(ctx, cfg, st, item.ID)
	if err != nil {
		return "", err
	}
	path, err := b.a.editPath(item.ID)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(f.text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// EditFinish implements tui.Backend. An empty path only removes the form.
func (b *tuiBackend) EditFinish(ctx context.Context, item tui.InboxItem, path string) (string, error) {
	want, err := b.a.editPath(item.ID)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(want) }()
	if path == "" {
		return "", nil
	}
	if path != want {
		return "", errors.New("not the file this session's form was written to")
	}
	text, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	cfg, err := b.a.load()
	if err != nil {
		return "", err
	}
	st, err := b.a.openState()
	if err != nil {
		return "", err
	}
	defer func() { _ = st.Close() }()
	f, err := b.a.editPrepare(ctx, cfg, st, item.ID)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.reviewed = nil
	b.mu.Unlock()
	return b.a.editSave(ctx, cfg, st, f, string(text))
}
