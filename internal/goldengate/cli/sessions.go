package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// Session states, as `goldengate sessions list` shows them.
const (
	sessPending   = "pending"   // waits for approval
	sessQueued    = "queued"    // approved or auto-approved: the next sync sends it
	sessSent      = "sent"      // on the server, up to date
	sessChanged   = "changed"   // on the server; a newer version waits or is queued
	sessExcluded  = "excluded"  // kept on this machine
	sessRetracted = "retracted" // was sent, then taken back
	// sessConflict: the user edited its summary and it changed since; it is
	// held until they decide about the edit.
	sessConflict = tui.InboxStateConflict
)

// sessionRow is one session of a shared project and where it stands.
type sessionRow struct {
	Source, ID string
	Project    string // the named project, or the folder
	Title      string // redacted
	State      string
	Edited     bool   // its summary is the user's own version
	Waits      bool   // a version of it waits for approval (pending, or changed and not auto-approved)
	Hash       string // the content a decision would be made for
	Updated    time.Time
}

// sessionRows lists every session of the shared projects with its state. It
// plans a sync offline (no model call, nothing sent) to learn what is new or
// changed, and reads what was sent and decided from the state.
func (a *app) sessionRows(ctx context.Context, cfg config.Config, st *gsync.State) ([]sessionRow, error) {
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
	// with Approve the plan holds everything new or changed; which of it
	// waits is decided below from the project and the decisions
	batch, rep, err := s.Plan(ctx, gsync.RunOpts{Offline: true, Approve: true})
	if err != nil {
		return nil, err
	}
	auto := map[string]bool{}
	for _, p := range cfg.Projects {
		auto[p.ID] = p.AutoApprove
	}
	type key struct{ source, id string }
	rows := map[key]*sessionRow{}
	var order []key
	put := func(r sessionRow) *sessionRow {
		k := key{r.Source, r.ID}
		if cur, ok := rows[k]; ok {
			return cur
		}
		rows[k] = &r
		order = append(order, k)
		return rows[k]
	}
	sent, err := st.Sent(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range sent {
		r := sessionRow{Source: e.Source, ID: e.SessionID, Project: e.Project, State: sessSent, Hash: e.ContentHash, Updated: e.SyncedAt}
		var p model.SessionPayload
		if json.Unmarshal(e.Payload, &p) == nil {
			r.Project, r.Title, r.Updated = p.ProjectLabel(), payloadTitle(&p), p.UpdatedAt
			r.Edited = p.Summary != nil && p.Summary.Edited
		}
		put(r)
	}
	for _, p := range batch.Payloads() {
		waits := false
		if isAuto, named := auto[p.ProjectID]; named && !isAuto {
			d, ok, err := st.Decision(ctx, p.Source, p.SessionID)
			if err != nil {
				return nil, err
			}
			waits = !ok || d.State != gsync.DecisionApproved || d.ContentHash != p.ContentHash
		}
		k := key{p.Source, p.SessionID}
		if cur, ok := rows[k]; ok {
			if cur.Hash != p.ContentHash { // a re-filed or re-summarized session is not a change of content
				cur.State, cur.Waits, cur.Hash, cur.Title, cur.Updated = sessChanged, waits, p.ContentHash, payloadTitle(p), p.UpdatedAt
			}
			cur.Edited = p.Summary != nil && p.Summary.Edited
			continue
		}
		state := sessQueued
		if waits {
			state = sessPending
		}
		put(sessionRow{Source: p.Source, ID: p.SessionID, Project: p.ProjectLabel(), Title: payloadTitle(p), State: state, Waits: waits, Hash: p.ContentHash, Updated: p.UpdatedAt,
			Edited: p.Summary != nil && p.Summary.Edited})
	}
	// sessions held because the user edited an earlier version
	for _, it := range rep.PendingItems {
		if !it.Conflict {
			continue
		}
		name := it.ProjectName
		if name == "" {
			name = it.Project
		}
		k := key{it.Source, it.SessionID}
		if cur, ok := rows[k]; ok {
			cur.State, cur.Edited, cur.Waits, cur.Hash = sessConflict, true, false, it.ContentHash
			continue
		}
		put(sessionRow{Source: it.Source, ID: it.SessionID, Project: name, Title: it.Title, State: sessConflict, Edited: true, Hash: it.ContentHash, Updated: it.Updated})
	}
	decs, err := st.Decisions(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, d := range decs {
		if !d.Blocks() {
			continue
		}
		state := sessExcluded
		if d.State == gsync.DecisionRetracted {
			state = sessRetracted
		}
		k := key{d.Source, d.SessionID}
		if cur, ok := rows[k]; ok { // excluded after it was sent: it stays on the server until retracted
			if state == sessExcluded {
				cur.State, cur.Waits = sessExcluded, false
			}
			continue
		}
		put(sessionRow{Source: d.Source, ID: d.SessionID, Project: d.Project, Title: d.Title, State: state, Hash: d.ContentHash, Updated: d.At})
	}
	out := make([]sessionRow, 0, len(order))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

func payloadTitle(p *model.SessionPayload) string {
	if p.Summary != nil {
		return p.Summary.Title
	}
	return p.TitleOnly
}

// sessionsCmd decides single sessions, wherever their project stands.
func (a *app) sessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "See and decide single sessions: include, exclude, or retract one that was sent",
		Long: `Every session of the projects you share, and where it stands:

  pending    waits for your approval          queued     the next sync sends it
  sent       on the server                    changed    on the server; a newer version waits or is queued
  excluded   kept on this machine             retracted  was sent, then deleted from the server

  goldengate sessions include <id>    approve it (also undoes an exclude or a retract)
  goldengate sessions exclude <id>    never send it
  goldengate sessions retract <id>    delete it from the server and never send it again`,
	}
	var project, state string
	list := &cobra.Command{Use: "list", Short: "List sessions of shared projects with their state", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return a.sessionsList(c.Context(), project, state) }}
	list.Flags().StringVar(&project, "project", "", "only sessions of this project (name)")
	list.Flags().StringVar(&state, "state", "", "only this state: pending, queued, sent, changed, excluded or retracted")
	include := &cobra.Command{Use: "include <session-id>...", Short: "Approve sessions for sending; undoes an exclude or a retract", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return a.sessionsInclude(c.Context(), args) }}
	exclude := &cobra.Command{Use: "exclude <session-id>...", Short: "Never send these sessions", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return a.sessionsExclude(c.Context(), args) }}
	retract := &cobra.Command{Use: "retract <session-id>...", Short: "Delete sent sessions from the server and never send them again", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return a.sessionsRetract(c.Context(), args) }}
	var keep, discard bool
	edit := &cobra.Command{Use: "edit <session-id>", Short: "Edit a session's summary in your editor; your version is what is sent and is never overwritten",
		Long: `Opens the session's summary in $VISUAL or $EDITOR. Change text, delete a line to strike
it, delete a work-item line to unlink it. Your version is redacted like everything else and
no summarizer replaces it (not even sync --resummarize).

If the session continues later, your edit is not applied silently and the new summary is
not sent in its place: the session is held, and editing it again shows the new generated
summary next to your earlier version.

  --keep      use your earlier version, as it is, for the session as it is now
  --discard   remove your edit and use the generated summary`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return a.sessionsEdit(c.Context(), args[0], keep, discard)
		}}
	edit.Flags().BoolVar(&keep, "keep", false, "keep your earlier edit for the changed session, without opening the editor")
	edit.Flags().BoolVar(&discard, "discard", false, "remove your edit")
	cmd.AddCommand(list, include, exclude, retract, edit)
	return cmd
}

func (a *app) withRows(ctx context.Context, fn func(cfg config.Config, st *gsync.State, rows []sessionRow) error) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	rows, err := a.sessionRows(ctx, cfg, st)
	if err != nil {
		return err
	}
	return fn(cfg, st, rows)
}

func (a *app) sessionsList(ctx context.Context, project, state string) error {
	return a.withRows(ctx, func(_ config.Config, _ *gsync.State, rows []sessionRow) error {
		n := 0
		for _, r := range rows {
			if (project != "" && !strings.EqualFold(r.Project, project)) || (state != "" && r.State != state) {
				continue
			}
			note := r.State
			if r.State == sessChanged && r.Waits {
				note = "changed, waits for approval"
			} else if r.State == sessChanged {
				note = "changed, queued"
			}
			if r.Edited && r.State != sessConflict {
				note += ", edited"
			}
			fmt.Fprintf(a.Stdout, "%s  %-11s  %-28s  %s  %s\n", r.ID, r.Source, "["+note+"]", r.Project, r.Title)
			n++
		}
		if n == 0 {
			fmt.Fprintln(a.Stdout, "no sessions (share a project first, or widen the filter)")
		}
		return nil
	})
}

// pick finds the rows for the given ids; an id that matches nothing is an error.
func pick(rows []sessionRow, ids []string) ([]sessionRow, error) {
	var out []sessionRow
	for _, id := range ids {
		found := false
		for _, r := range rows {
			if r.ID == id {
				out, found = append(out, r), true
			}
		}
		if !found {
			return nil, fmt.Errorf("no session %s in your shared projects (see `goldengate sessions list`)", id)
		}
	}
	return out, nil
}

func (a *app) sessionsInclude(ctx context.Context, ids []string) error {
	n, err := a.decideInclude(ctx, ids)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "included %d session(s); `goldengate sync` sends them\n", n)
	return nil
}

// decideInclude approves sessions for sending. A blocking decision (exclude,
// retract) is removed first, then the sessions are read again so the
// approval is for the content they have now.
func (a *app) decideInclude(ctx context.Context, ids []string) (int, error) {
	// 1. undo excludes and retracts
	err := a.withRows(ctx, func(_ config.Config, st *gsync.State, rows []sessionRow) error {
		picked, err := pick(rows, ids)
		if err != nil {
			return err
		}
		for _, r := range picked {
			if r.State == sessExcluded || r.State == sessRetracted {
				if _, err := st.Undecide(ctx, r.Source, r.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	// 2. approve what now waits, for the content it has now
	n := 0
	err = a.withRows(ctx, func(_ config.Config, st *gsync.State, rows []sessionRow) error {
		picked, err := pick(rows, ids)
		if err != nil {
			return err
		}
		for _, r := range picked {
			if r.Waits {
				if err := st.Decide(ctx, gsync.Decision{Source: r.Source, SessionID: r.ID, State: gsync.DecisionApproved, ContentHash: r.Hash, At: a.Now(), Project: r.Project, Title: r.Title}); err != nil {
					return err
				}
			}
			n++
		}
		return nil
	})
	return n, err
}

func (a *app) sessionsExclude(ctx context.Context, ids []string) error {
	return a.withRows(ctx, func(_ config.Config, st *gsync.State, rows []sessionRow) error {
		picked, err := pick(rows, ids)
		if err != nil {
			return err
		}
		onServer := 0
		for _, r := range picked {
			if r.State == sessRetracted {
				continue // already kept back for good
			}
			if r.State == sessSent || r.State == sessChanged {
				onServer++
			}
			if err := st.Decide(ctx, gsync.Decision{Source: r.Source, SessionID: r.ID, State: gsync.DecisionExcluded, ContentHash: r.Hash, At: a.Now(), Project: r.Project, Title: r.Title}); err != nil {
				return err
			}
		}
		fmt.Fprintf(a.Stdout, "excluded %d session(s); nothing more of them is sent\n", len(picked))
		if onServer > 0 {
			fmt.Fprintf(a.Stderr, "note: %d of them already reached the server and stay there; remove them with `goldengate sessions retract <session-id>`\n", onServer)
		}
		return nil
	})
}

// retractSession deletes one sent session from the server and records that
// it must never be sent again. The local record of the push is dropped only
// after the server confirmed.
func (a *app) retractSession(ctx context.Context, cfg config.Config, st *gsync.State, r sessionRow) error {
	cl, err := a.ingestClient(cfg)
	if err != nil {
		return err
	}
	if _, err := cl.DeleteSession(ctx, r.Source, r.ID); err != nil {
		return fmt.Errorf("could not delete %s from the server (nothing was changed here): %w", r.ID, err)
	}
	if err := st.Decide(ctx, gsync.Decision{Source: r.Source, SessionID: r.ID, State: gsync.DecisionRetracted, ContentHash: r.Hash, At: a.Now(), Project: r.Project, Title: r.Title}); err != nil {
		return err
	}
	return st.Forget(ctx, r.Source, r.ID)
}

func (a *app) sessionsRetract(ctx context.Context, ids []string) error {
	return a.withRows(ctx, func(cfg config.Config, st *gsync.State, rows []sessionRow) error {
		picked, err := pick(rows, ids)
		if err != nil {
			return err
		}
		for _, r := range picked {
			if sentHere, _, _ := st.Get(ctx, r.Source, r.ID); sentHere.SessionID == "" && r.State != sessSent && r.State != sessChanged {
				return fmt.Errorf("%s was not sent from this computer, so there is nothing to retract; to keep it local use `goldengate sessions exclude %s`", r.ID, r.ID)
			}
		}
		for _, r := range picked {
			if _, seen, err := st.Get(ctx, r.Source, r.ID); err != nil {
				return err
			} else if !seen {
				return errors.New("session " + r.ID + " is not recorded as sent")
			}
			if err := a.retractSession(ctx, cfg, st, r); err != nil {
				return err
			}
		}
		fmt.Fprintf(a.Stdout, "retracted %d session(s): deleted from the server, and never sent again\n", len(picked))
		return nil
	})
}
