// Package sync plans and performs pushes. Everything that leaves the machine
// goes through Push, the single function to audit. A Batch can only be
// produced by Syncer.Plan, which applies selection, tombstones and
// redaction/summarization, so there is no path from an unselected project to
// a Sink.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/config"
	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/pathutil"
	"github.com/OWNER/goldengate/internal/project"
	"github.com/OWNER/goldengate/internal/selection"
	"github.com/OWNER/goldengate/internal/summarize"
	"github.com/OWNER/goldengate/internal/workitems"
)

// Sink receives approved payloads. Implementations: DryRunSink here, the HTTP
// ingest client in M2.
type Sink interface {
	Send(ctx context.Context, payloads []*model.SessionPayload) error
}

type planned struct {
	payload *model.SessionPayload
	entry   Entry
}

// Batch is an opaque, already-approved set of payloads. Only Plan creates one.
type Batch struct {
	items   []planned
	refresh []Entry // sessions whose content is unchanged but whose file stat moved
}

// Len is the number of payloads in the batch.
func (b Batch) Len() int { return len(b.items) }

// Payloads returns copies of the payload pointers, for previews.
func (b Batch) Payloads() []*model.SessionPayload {
	out := make([]*model.SessionPayload, len(b.items))
	for i, it := range b.items {
		out[i] = it.payload
	}
	return out
}

// Push is the only function that hands payloads to a Sink. When state is
// non-nil (a real push), successful sends are recorded so the next run skips
// them. A dry run passes a nil state and so changes nothing.
func Push(ctx context.Context, sink Sink, state *State, b Batch, now time.Time) error {
	if state != nil {
		for _, e := range b.refresh {
			if err := state.Put(ctx, e); err != nil {
				return err
			}
		}
	}
	if b.Len() == 0 {
		return nil
	}
	for _, it := range b.items {
		if it.payload == nil || it.payload.ContentHash == "" || it.payload.Project == "" {
			return errors.New("sync: refusing to push an incomplete payload")
		}
		// The server enforces the same limits; failing here keeps one bad
		// session from sinking a whole batch and spares a leaked-token style
		// round trip.
		if err := it.payload.Validate(now); err != nil {
			return fmt.Errorf("sync: refusing to push session %s: %w", it.payload.SessionID, err)
		}
	}
	if err := sink.Send(ctx, b.Payloads()); err != nil {
		return fmt.Errorf("sync: send: %w", err)
	}
	if state == nil {
		return nil
	}
	for _, it := range b.items {
		e := it.entry
		e.SyncedAt = now
		if err := state.Put(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// Syncer wires adapters, policy and state together.
type Syncer struct {
	Registry *adapters.Registry
	Config   config.Config
	Matcher  *selection.Matcher
	State    *State
	Home     string
	Resolver project.Resolver
	Log      *slog.Logger
	Now      func() time.Time
}

// RunOpts narrows one run.
type RunOpts struct {
	Project string        // only this project (absolute path); "" = all selected
	Source  string        // only this source
	Since   time.Duration // 0 = use config
}

// Report summarizes a run. Counts only: no project names or content.
type Report struct {
	Seen        int
	NotSelected int
	Tombstoned  int
	Unchanged   int
	Planned     int
	Warnings    int
	Redactions  map[string]int
}

func (s *Syncer) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Log
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Plan decides what would be sent. It never contacts a sink. Unselected
// sessions are counted and dropped before they are loaded, and are never
// named in logs.
func (s *Syncer) Plan(ctx context.Context, o RunOpts) (Batch, Report, error) {
	var b Batch
	rep := Report{Redactions: map[string]int{}}
	since, err := s.sinceTime(o)
	if err != nil {
		return b, rep, err
	}
	wantProject := ""
	if o.Project != "" {
		if pathutil.IsVirtualKey(o.Project) {
			wantProject = o.Project
		} else {
			wantProject = pathutil.Normalize(pathutil.ExpandHome(o.Project, s.Home))
		}
	}
	wo := workitems.Options{Providers: s.Config.Issues.Providers}
	wo.KnownPrefixes = append(append([]string{}, s.Config.Issues.LinearTeams...), s.Config.Issues.JiraProjects...)

	for _, a := range s.Registry.All() {
		if o.Source != "" && a.Name() != o.Source {
			continue
		}
		det, err := a.Detect(ctx)
		if err != nil {
			return b, rep, fmt.Errorf("sync: detect %s: %w", a.Name(), err)
		}
		if !det.Installed {
			continue
		}
		for ref, lerr := range a.ListSessions(ctx, adapters.ListOpts{Since: since}) {
			if lerr != nil {
				if adapters.IsSkip(lerr) {
					rep.Warnings++
					s.log().Warn("skipped item with unknown shape", "adapter", a.Name())
					continue
				}
				return b, rep, fmt.Errorf("sync: list %s: %w", a.Name(), lerr)
			}
			rep.Seen++
			if wantProject != "" && !sameProject(ref.ProjectKey, wantProject, s.caseInsensitive()) {
				rep.NotSelected++
				continue
			}
			if d := s.Matcher.Decide(ref.ProjectKey, ref.Source); !d.Shared {
				rep.NotSelected++
				continue
			}
			if dead, err := s.State.Tombstoned(ctx, ref.ProjectKey); err != nil {
				return b, rep, err
			} else if dead {
				rep.Tombstoned++
				continue
			}
			prev, seen, err := s.State.Get(ctx, ref.Source, ref.ID)
			if err != nil {
				return b, rep, err
			}
			mtime := ref.UpdatedAt.UnixNano()
			if seen && prev.Size == ref.SizeBytes && prev.MTimeNS == mtime {
				rep.Unchanged++
				continue
			}
			raw, err := a.Load(ctx, ref)
			if err != nil {
				if adapters.IsSkip(err) {
					rep.Warnings++
					s.log().Warn("skipped session with unknown shape", "adapter", a.Name(), "session", ref.ID)
					continue
				}
				return b, rep, fmt.Errorf("sync: load: %w", err)
			}
			rep.Warnings += len(raw.Warnings)
			p, err := summarize.Build(raw, summarize.Options{
				Level: s.Config.Share.Level, Home: s.Home, Work: wo, ExtraRedactions: s.Config.Redact.ExtraPatterns,
				ProjectKey: s.serverKey(ref),
			})
			if err != nil {
				return b, rep, fmt.Errorf("sync: summarize: %w", err)
			}
			if seen && prev.ContentHash == p.ContentHash {
				// same content, only the file's stat changed: remember the new stat, send nothing
				prev.Size, prev.MTimeNS = ref.SizeBytes, mtime
				b.refresh = append(b.refresh, prev)
				rep.Unchanged++
				continue
			}
			for k, n := range p.Redactions {
				rep.Redactions[k] += n
			}
			s.log().Debug("planned", "adapter", a.Name(), "session", ref.ID, "redactions", p.Redactions)
			b.items = append(b.items, planned{payload: p, entry: Entry{
				Source: ref.Source, SessionID: ref.ID, Project: ref.ProjectKey, ContentHash: p.ContentHash,
				Size: ref.SizeBytes, MTimeNS: mtime,
			}})
			rep.Planned++
		}
	}
	return b, rep, nil
}

// serverKey is the project a session is filed under on the server: the
// repository root for a worktree session, but only when that root is itself
// selected, so an unselected folder's name is never sent.
func (s *Syncer) serverKey(ref model.SessionRef) string {
	root := s.Resolver.Root(ref)
	if root == "" || root == ref.ProjectKey {
		return ref.ProjectKey
	}
	if s.Matcher.Decide(root, ref.Source).Shared {
		return root
	}
	return ref.ProjectKey
}

func sameProject(a, b string, ci bool) bool {
	if pathutil.IsVirtualKey(a) || pathutil.IsVirtualKey(b) {
		return a == b
	}
	return pathutil.Fold(a, ci) == pathutil.Fold(b, ci)
}

func (s *Syncer) caseInsensitive() bool { return pathutil.CaseInsensitiveFS() }

func (s *Syncer) sinceTime(o RunOpts) (time.Time, error) {
	d := o.Since
	if d == 0 {
		var err error
		if d, err = config.ParseSince(s.Config.Share.Since); err != nil {
			return time.Time{}, err
		}
	}
	if d == 0 {
		return time.Time{}, nil
	}
	return s.now().Add(-d), nil
}

// Run plans and pushes. With dryRun the batch goes to sink but state is left
// untouched.
func (s *Syncer) Run(ctx context.Context, o RunOpts, sink Sink, dryRun bool) (Report, error) {
	b, rep, err := s.Plan(ctx, o)
	if err != nil {
		return rep, err
	}
	st := s.State
	if dryRun {
		st = nil
	}
	if err := Push(ctx, sink, st, b, s.now()); err != nil {
		return rep, err
	}
	return rep, nil
}

// DryRunSink prints each payload as indented JSON.
type DryRunSink struct{ W io.Writer }

// Send implements Sink.
func (d DryRunSink) Send(_ context.Context, ps []*model.SessionPayload) error {
	enc := json.NewEncoder(d.W)
	enc.SetIndent("", "  ")
	for _, p := range ps {
		if err := enc.Encode(p); err != nil {
			return fmt.Errorf("dry-run: %w", err)
		}
	}
	return nil
}
