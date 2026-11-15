package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
)

// receiptsCmd shows what was sent automatically, and takes it back.
func (a *app) receiptsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "receipts",
		Short: "What was sent automatically, to which projects and why; retract any of it",
		Long: `Every session that a project's trust level and rules sent without asking you
is listed in a receipt: one receipt per sync. Sessions you approved yourself are
not in receipts (see goldengate sessions list).

  goldengate receipts                         recent receipts
  goldengate receipts show [receipt-id]       what one receipt holds (default: the latest)
  goldengate receipts show --agents           also which assistants can see each project (asks for the admin token)
  goldengate receipts retract <receipt-id> --all
  goldengate receipts retract <receipt-id> <session-id>...

Retracting deletes the session from the server and never sends it again.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return a.receiptsList(c.Context(), 20) },
	}
	var limit int
	list := &cobra.Command{Use: "list", Short: "List recent receipts", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return a.receiptsList(c.Context(), limit) }}
	list.Flags().IntVar(&limit, "limit", 20, "how many receipts to list (0: all)")
	var agents, stdinTok bool
	show := &cobra.Command{Use: "show [receipt-id]", Short: "Show a receipt: each session, its project and the rule that sent it", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			return a.receiptsShow(c.Context(), id, agents, stdinTok)
		}}
	show.Flags().BoolVar(&agents, "agents", false, "also show which assistants can see each project now (needs the admin token)")
	show.Flags().BoolVar(&stdinTok, "admin-token-stdin", false, "with --agents: read the admin token from stdin")
	var all bool
	retract := &cobra.Command{Use: "retract <receipt-id> <session-id>... | --all", Short: "Delete automatically sent sessions from the server and never send them again", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return a.receiptsRetract(c.Context(), args[0], args[1:], all)
		}}
	retract.Flags().BoolVar(&all, "all", false, "every session of the receipt that is still on the server")
	cmd.AddCommand(list, show, retract)
	return cmd
}

// receiptLine summarizes a receipt: how many sessions, to which projects.
func receiptLine(r gsync.Receipt) string {
	projects := map[string]int{}
	retracted := 0
	for _, it := range r.Items {
		projects[it.ProjectLabel()]++
		if !it.RetractedAt.IsZero() {
			retracted++
		}
	}
	names := make([]string, 0, len(projects))
	for n := range projects {
		names = append(names, n)
	}
	sort.Strings(names)
	line := fmt.Sprintf("%s  %s  %d session(s) to %s", r.ID, r.At.Local().Format("2006-01-02 15:04"), len(r.Items), strings.Join(names, ", "))
	if retracted > 0 {
		line += fmt.Sprintf("  (%d retracted)", retracted)
	}
	return line
}

func (a *app) receiptsList(ctx context.Context, limit int) error {
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	rs, err := st.Receipts(ctx, limit)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		fmt.Fprintln(a.Stdout, "no receipts: nothing was sent automatically (a project sends on its own only at trust level auto; see `goldengate rules`)")
		return nil
	}
	for _, r := range rs {
		fmt.Fprintln(a.Stdout, receiptLine(r))
	}
	fmt.Fprintln(a.Stdout, "\ndetails:  goldengate receipts show <receipt-id>")
	return nil
}

func (a *app) receiptsShow(ctx context.Context, id string, agents, stdinTok bool) error {
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	var r gsync.Receipt
	if id == "" {
		rs, err := st.Receipts(ctx, 1)
		if err != nil {
			return err
		}
		if len(rs) == 0 {
			fmt.Fprintln(a.Stdout, "no receipts: nothing was sent automatically")
			return nil
		}
		r = rs[0]
	} else {
		var ok bool
		if r, ok, err = st.Receipt(ctx, id); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("no receipt %s (see `goldengate receipts`)", id)
		}
	}
	// who can read each project, asked from the server only on request
	var seenBy map[string][]string
	if agents {
		tok, err := a.adminToken(stdinTok, false)
		if err != nil {
			return err
		}
		info, err := a.tuiBackend().Agents(ctx, tok)
		if err != nil {
			return err
		}
		seenBy = map[string][]string{}
		for _, it := range r.Items {
			if _, done := seenBy[it.ProjectID]; done || it.ProjectID == "" {
				continue
			}
			names := []string{}
			for _, ag := range info.Agents {
				if ag.AllProjects || ag.Grants[it.ProjectID] {
					names = append(names, ag.Name)
				}
			}
			seenBy[it.ProjectID] = names
		}
	}
	fmt.Fprintf(a.Stdout, "receipt %s, %s: %d session(s) sent automatically\n", r.ID, r.At.Local().Format("2006-01-02 15:04"), len(r.Items))
	project := "\x00"
	left := 0
	for _, it := range r.Items {
		if it.ProjectLabel() != project {
			project = it.ProjectLabel()
			fmt.Fprintf(a.Stdout, "\n%s\n", project)
			if agents {
				switch names := seenBy[it.ProjectID]; {
				case it.ProjectID == "":
					fmt.Fprintln(a.Stdout, "  visible to: unknown (this project has no id on the server)")
				case len(names) == 0:
					fmt.Fprintln(a.Stdout, "  visible to: no assistant (not granted to any agent)")
				default:
					fmt.Fprintf(a.Stdout, "  visible to: %s\n", strings.Join(names, ", "))
				}
			}
		}
		mark := ""
		if !it.RetractedAt.IsZero() {
			mark = "  [retracted " + it.RetractedAt.Local().Format("2006-01-02 15:04") + "]"
		} else {
			left++
		}
		fmt.Fprintf(a.Stdout, "  %s  %-11s  %s%s\n      why: %s\n", it.SessionID, it.Source, it.Title, mark, it.Why)
	}
	if left > 0 {
		fmt.Fprintf(a.Stdout, "\ntake back:  goldengate receipts retract %s --all    (or name session ids)\n", r.ID)
	}
	if !agents {
		fmt.Fprintln(a.Stdout, "who can read it:  goldengate receipts show "+r.ID+" --agents")
	}
	return nil
}

func (a *app) receiptsRetract(ctx context.Context, id string, ids []string, all bool) error {
	if all == (len(ids) > 0) {
		return errors.New("name the session ids, or pass --all (not both)")
	}
	st, err := a.openState()
	if err != nil {
		return err
	}
	r, ok, err := st.Receipt(ctx, id)
	_ = st.Close() // the retract below opens the state itself
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no receipt %s (see `goldengate receipts`)", id)
	}
	in := map[string]gsync.ReceiptItem{}
	for _, it := range r.Items {
		in[it.SessionID] = it
	}
	if all {
		for _, it := range r.Items {
			if it.RetractedAt.IsZero() {
				ids = append(ids, it.SessionID)
			}
		}
		if len(ids) == 0 {
			fmt.Fprintf(a.Stdout, "everything in receipt %s is already retracted\n", id)
			return nil
		}
	}
	for _, sid := range ids {
		it, ok := in[sid]
		if !ok {
			return fmt.Errorf("session %s is not in receipt %s", sid, id)
		}
		if !it.RetractedAt.IsZero() {
			return fmt.Errorf("session %s is already retracted", sid)
		}
	}
	return a.sessionsRetract(ctx, ids)
}

// notifyReceipt shows a desktop notification about a sync's automatic
// sends, when the user turned that on. It carries counts only: notification
// centers keep history, so no title or project name goes into it. A failure
// to notify is not a failure to sync.
func (a *app) notifyReceipt(cfg config.Config, rep gsync.Report) {
	items := append(append([]gsync.AutoItem{}, rep.Auto...), rep.Unattended...)
	if !cfg.UI.NotifyReceipts || a.Notify == nil || len(items) == 0 {
		return
	}
	projects := map[string]bool{}
	for _, it := range items {
		projects[it.ProjectID+"\x00"+it.Project] = true
	}
	body := fmt.Sprintf("%d session(s) sent automatically to %d project(s). Details: goldengate receipts show %s", len(items), len(projects), rep.Receipt)
	if err := a.Notify("Golden Gate", body); err != nil {
		fmt.Fprintf(a.Stderr, "could not show the receipt notification: %v\n", err)
	}
}

// desktopNotify shows a notification with the platform's own tool, if there
// is one. The text is passed as arguments, never through a shell.
func desktopNotify(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "linux":
		return exec.CommandContext(ctx, "notify-send", "--app-name=Golden Gate", "--", title, body).Run() //nolint:gosec // G204: a fixed program; the text is our own counts, passed as arguments
	case "darwin":
		script := `on run argv
display notification (item 2 of argv) with title (item 1 of argv)
end run`
		return exec.CommandContext(ctx, "osascript", "-e", script, title, body).Run() //nolint:gosec // G204: a fixed program and script; the text is our own counts, passed as arguments
	}
	return fmt.Errorf("desktop notifications are not supported on %s", runtime.GOOS)
}
