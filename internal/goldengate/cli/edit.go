package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/summarize"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// defaultEditFile opens path in the user's editor and waits for it to exit.
func defaultEditFile(getenv func(string) string) func(path string) error {
	return func(path string) error {
		ed := strings.TrimSpace(getenv("VISUAL"))
		if ed == "" {
			ed = strings.TrimSpace(getenv("EDITOR"))
		}
		if ed == "" {
			ed = "vi"
		}
		parts := strings.Fields(ed)
		// no deadline: the editor runs for as long as the user keeps it open
		cmd := exec.CommandContext(context.Background(), parts[0], append(parts[1:], path)...) //nolint:gosec // G204: the user's own $EDITOR, on a file this program just wrote
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("the editor (%s) did not finish cleanly: %w", parts[0], err)
		}
		return nil
	}
}

func (a *app) quietSyncer(ctx context.Context, cfg config.Config, st *gsync.State) (*gsync.Syncer, error) {
	m, err := a.matcher(cfg)
	if err != nil {
		return nil, err
	}
	reg, err := a.registry(ctx)
	if err != nil {
		return nil, err
	}
	return &gsync.Syncer{Registry: reg, Config: cfg, Matcher: m, State: st, Home: a.Home, Now: a.Now, Resolver: a.resolver(cfg),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Summarizer: a.summarizer(cfg)}, nil
}

// editForm is the text the user edits for one session, and what it was made from.
type editForm struct {
	text   string
	base   *model.SessionPayload // the generated summary, without any edit
	old    summarize.Edit        // the user's earlier edit, if had
	had    bool
	stale  bool // the earlier edit was made on other content
	source string
}

// editPrepare builds the edit form for a session: the user's version when
// they edited this content before, else the generated summary; and when the
// session changed after an edit, the new generated summary with their
// earlier version shown above it for comparison.
func (a *app) editPrepare(ctx context.Context, cfg config.Config, st *gsync.State, id string) (editForm, error) {
	s, err := a.quietSyncer(ctx, cfg, st)
	if err != nil {
		return editForm{}, err
	}
	base, err := s.BuildOne(ctx, "", id, true)
	if errors.Is(err, gsync.ErrNoSession) {
		return editForm{}, fmt.Errorf("no session %s in your shared projects (see `goldengate sessions list`)", id)
	}
	if err != nil {
		return editForm{}, err
	}
	f := editForm{base: base, source: base.Source}
	if f.old, f.had, err = st.Edit(ctx, base.Source, id); err != nil {
		return editForm{}, err
	}
	switch {
	case f.had && f.old.ContentHash == base.ContentHash:
		mine, err := s.BuildOne(ctx, base.Source, id, false)
		if err != nil {
			return editForm{}, err
		}
		f.text = summarize.FormatEdit(mine, "This is your edited version. Delete everything you changed to go back to the generated summary: `goldengate sessions edit "+id+" --discard`.")
	case f.had:
		f.stale = true
		f.text = summarize.FormatEdit(base, "This session continued after you edited its summary.\nBelow is the NEW generated summary. Your earlier version is shown here for comparison;\nchange the text below to what should be sent, then save.\n"+editDiff(f.old, base))
	default:
		f.text = summarize.FormatEdit(base, "")
	}
	return f, nil
}

// editDiff lists what the user's earlier version said that the newly
// generated summary does not say, and the other way round.
func editDiff(old summarize.Edit, cur *model.SessionPayload) string {
	now, err := summarize.ParseEdit(summarize.FormatEdit(cur, ""), cur)
	if err != nil {
		return ""
	}
	var b strings.Builder
	line := func(field, was, is string) {
		if was != is {
			if was != "" {
				b.WriteString("  yours  " + field + ": " + oneLineClip(was) + "\n")
			}
			if is != "" {
				b.WriteString("  new    " + field + ": " + oneLineClip(is) + "\n")
			}
		}
	}
	line("title", old.Title, now.Title)
	lists := func(field string, was, is []string) {
		in := func(l []string, v string) bool {
			for _, x := range l {
				if x == v {
					return true
				}
			}
			return false
		}
		for _, v := range was {
			if !in(is, v) {
				b.WriteString("  yours  " + field + ": " + oneLineClip(v) + "\n")
			}
		}
		for _, v := range is {
			if !in(was, v) {
				b.WriteString("  new    " + field + ": " + oneLineClip(v) + "\n")
			}
		}
	}
	lists("goal", old.Goals, now.Goals)
	lists("done", old.Description, now.Description)
	lists("outcome", old.Outcomes, now.Outcomes)
	lists("open problem", old.OpenProblems, now.OpenProblems)
	line("narrative", old.Narrative, now.Narrative)
	for _, k := range old.DropWorkItems {
		b.WriteString("  yours  unlinked work item: " + k + "\n")
	}
	if b.Len() == 0 {
		return "(Your earlier version and the new summary say the same.)"
	}
	return b.String()
}

func oneLineClip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:159]) + "…"
	}
	return s
}

// editSave stores what the user wrote in the form, or removes the edit when
// their text is the generated summary. It returns a line saying what happened.
func (a *app) editSave(ctx context.Context, cfg config.Config, st *gsync.State, f editForm, text string) (string, error) {
	id := f.base.SessionID
	e, err := summarize.ParseEdit(text, f.base)
	if err != nil {
		return "", fmt.Errorf("the edit was not saved: %w", err)
	}
	if e.SameText(f.base) {
		if f.had {
			if _, err := st.DeleteEdit(ctx, f.source, id); err != nil {
				return "", err
			}
			return "your edit was removed: the generated summary is used", nil
		}
		return "nothing changed: the generated summary is kept", nil
	}
	if err := st.PutEdit(ctx, f.source, id, e, a.Now()); err != nil {
		return "", err
	}
	// the edited payload must be one the server accepts; if not, the edit is taken back
	s, err := a.quietSyncer(ctx, cfg, st)
	if err != nil {
		return "", err
	}
	p, err := s.BuildOne(ctx, f.source, id, false)
	if err == nil {
		err = p.Validate(a.Now())
	}
	if err != nil {
		if f.had {
			_ = st.PutEdit(ctx, f.source, id, f.old, a.Now())
		} else {
			_, _ = st.DeleteEdit(ctx, f.source, id)
		}
		return "", fmt.Errorf("the edit was not saved, it would not make a valid summary: %w", err)
	}
	// a session whose content is already on the server stays approved: the
	// edit is the user's decision about what is sent for it
	if prev, seen, err := st.Get(ctx, f.source, id); err != nil {
		return "", err
	} else if seen && prev.ContentHash == f.base.ContentHash {
		if err := st.Decide(ctx, gsync.Decision{Source: f.source, SessionID: id, State: gsync.DecisionApproved, ContentHash: f.base.ContentHash, At: a.Now(),
			Project: p.ProjectLabel(), Title: payloadTitle(p)}); err != nil {
			return "", err
		}
		return "saved your version; the next sync sends it in place of what the server has", nil
	}
	return "saved your version; it is what will be sent for this session", nil
}

// editKeep carries the user's earlier edit over to the session's new
// content, as it is. editDiscard removes the edit.
func (a *app) editKeep(ctx context.Context, cfg config.Config, st *gsync.State, id string) (string, error) {
	f, err := a.editPrepare(ctx, cfg, st, id)
	if err != nil {
		return "", err
	}
	if !f.had {
		return "", fmt.Errorf("session %s has no edit to keep", id)
	}
	if !f.stale {
		return "your edit already applies to this session as it is", nil
	}
	e := f.old
	e.ContentHash = f.base.ContentHash
	// work items that no longer exist cannot stay unlinked
	have := map[string]bool{}
	for _, it := range f.base.WorkItems {
		have[it.Kind+" "+it.Key] = true
	}
	var drop []string
	for _, k := range e.DropWorkItems {
		if have[k] {
			drop = append(drop, k)
		}
	}
	e.DropWorkItems = drop
	if err := st.PutEdit(ctx, f.source, id, e, a.Now()); err != nil {
		return "", err
	}
	return "kept your version for the session as it is now", nil
}

func (a *app) editDiscard(ctx context.Context, st *gsync.State, id string) (string, error) {
	n, err := st.DeleteEdits(ctx, id)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("session %s has no edit", id)
	}
	return "your edit was removed: the generated summary is used", nil
}

// editPath is where the form for a session is written while it is edited:
// inside the state directory, readable only by the user.
func (a *app) editPath(id string) (string, error) {
	dir := filepath.Join(filepath.Dir(a.dirs().StatePath()), "edit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	safe := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, id)
	return filepath.Join(dir, safe+".txt"), nil
}

// sessionsEdit is `goldengate sessions edit`.
func (a *app) sessionsEdit(ctx context.Context, id string, keep, discard bool) error {
	if keep && discard {
		return errors.New("--keep and --discard are mutually exclusive")
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
	var msg string
	switch {
	case discard:
		msg, err = a.editDiscard(ctx, st, id)
	case keep:
		msg, err = a.editKeep(ctx, cfg, st, id)
	default:
		if a.EditFile == nil {
			return errors.New("no editor available: set $EDITOR")
		}
		var f editForm
		if f, err = a.editPrepare(ctx, cfg, st, id); err != nil {
			return err
		}
		var path string
		if path, err = a.editPath(id); err != nil {
			return err
		}
		if err = os.WriteFile(path, []byte(f.text), 0o600); err != nil {
			return err
		}
		defer func() { _ = os.Remove(path) }()
		if err = a.EditFile(path); err != nil {
			return err
		}
		var b []byte
		if b, err = os.ReadFile(path); err != nil {
			return err
		}
		msg, err = a.editSave(ctx, cfg, st, f, string(b))
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, msg)
	return nil
}
