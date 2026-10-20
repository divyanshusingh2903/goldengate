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
	stdsync "sync"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/llm"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/project"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/summarize"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
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

// Chunks splits a batch into parts of at most n payloads, in order, so a
// caller can push in steps and report progress. Refreshes ride with the first.
func (b Batch) Chunks(n int) []Batch {
	if n <= 0 || len(b.items) <= n {
		return []Batch{b}
	}
	var out []Batch
	for start := 0; start < len(b.items); start += n {
		c := Batch{items: b.items[start:min(start+n, len(b.items))]}
		if start == 0 {
			c.refresh = b.refresh
		}
		out = append(out, c)
	}
	return out
}

// ByProject splits a batch into one batch per server project, in order, so a
// caller can push and report project by project. Unchanged-stat refreshes ride
// with the first part.
func (b Batch) ByProject() []Batch {
	var order []string
	by := map[string]*Batch{}
	for _, it := range b.items {
		k := it.payload.Project
		if _, ok := by[k]; !ok {
			by[k] = &Batch{}
			order = append(order, k)
		}
		by[k].items = append(by[k].items, it)
	}
	out := make([]Batch, 0, len(order)+1)
	for _, k := range order {
		out = append(out, *by[k])
	}
	if len(b.refresh) > 0 {
		if len(out) == 0 {
			out = append(out, Batch{})
		}
		out[0].refresh = append(out[0].refresh, b.refresh...)
	}
	return out
}

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
	// Summarizer, when set, writes goals, description and outcomes with a
	// language model (opt-in). Only sessions of shared projects reach it.
	Summarizer llm.Client
	// SummarizerWorkers bounds concurrent model calls (default 4).
	SummarizerWorkers int
}

// summarizeOptions are the Build options for one session. offline previews
// use only cached model answers and never call the model.
func (s *Syncer) summarizeOptions(ctx context.Context, ref model.SessionRef, wo workitems.Options, offline bool, onErr func(error)) summarize.Options {
	o := summarize.Options{Level: s.Config.Share.Level, Home: s.Home, Work: wo, ExtraRedactions: s.Config.Redact.ExtraPatterns,
		ProjectKey: s.serverKey(ref)}
	if s.Summarizer != nil {
		now := s.Now
		if now == nil {
			now = time.Now
		}
		max := s.Config.Summarize.MaxInputChars
		if p, ok := llm.Lookup(s.Config.Summarize.Provider); ok && max == 0 {
			max = p.MaxInputChars // 0 keeps the summarizer's own default
		}
		o.Model = &summarize.ModelOptions{Client: s.Summarizer, Ctx: ctx, Cache: s.State.SummaryCache(ctx, now), Offline: offline,
			MaxInputChars: max, OnError: onErr}
	}
	return o
}

// RunOpts narrows one run.
type RunOpts struct {
	Project string        // only this project (absolute path); "" = all selected
	Source  string        // only this source
	Since   time.Duration // 0 = use config
	// OnPlanned, when set, is called after each session is read, summarized
	// and redacted, so a caller can show progress while a big plan builds.
	// It may be called from several goroutines at once.
	OnPlanned func(p *model.SessionPayload)
	// Resummarize rebuilds already-pushed sessions whose file is unchanged but
	// whose summary was not written by the configured model summarizer (the
	// heuristic, or another model), so turning a summarizer on can upgrade
	// what the server holds. Their redacted conversations go to the model
	// unless its answer is cached. Without a summarizer it does nothing.
	Resummarize bool
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
	// ModelFailures counts sessions the model could not summarize; they keep
	// the heuristic summary.
	ModelFailures int
	// Stale counts unchanged, already-pushed sessions whose summary is not
	// the configured summarizer's (only with a summarizer). With Resummarize
	// they are rebuilt and counted in Resummarized instead.
	Stale        int
	Resummarized int
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
	pool := s.newPool(ctx, wo, o.OnPlanned)
	defer pool.stop() // on an early return the workers must not wait forever

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
				stale := s.Summarizer != nil && staleSummary(prev.Payload, s.Summarizer.Name())
				if !stale || !o.Resummarize {
					if stale {
						rep.Stale++
					}
					rep.Unchanged++
					continue
				}
				rep.Resummarized++
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
			if err := pool.add(job{raw: raw, ref: ref, prev: prev, seen: seen, mtime: mtime}); err != nil {
				return b, rep, err
			}
		}
	}
	jobs, err := pool.wait()
	if err != nil {
		return b, rep, err
	}
	rep.ModelFailures = pool.failures
	for _, j := range jobs {
		{
			p, ref, prev, seen, mtime := j.payload, j.ref, j.prev, j.seen, j.mtime
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
			s.log().Debug("planned", "adapter", ref.Source, "session", ref.ID, "redactions", p.Redactions)
			body, _ := json.Marshal(p)
			b.items = append(b.items, planned{payload: p, entry: Entry{
				Source: ref.Source, SessionID: ref.ID, Project: ref.ProjectKey, ContentHash: p.ContentHash,
				Size: ref.SizeBytes, MTimeNS: mtime, Payload: body,
			}})
			rep.Planned++
		}
	}
	return b, rep, nil
}

// staleSummary reports whether a pushed payload's summary was written by
// something other than the summarizer named by. A payload stored before
// payloads were kept, or unreadable, counts as stale.
func staleSummary(payload []byte, by string) bool {
	var p struct {
		Summary *struct {
			SummarizedBy string `json:"summarized_by"`
		} `json:"summary"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return true
	}
	if p.Summary == nil {
		return false // titles-only level: there is no summary to upgrade
	}
	return p.Summary.SummarizedBy != by
}

// job is one session to summarize; payload is filled in by the pool.
type job struct {
	raw     *model.RawSession
	ref     model.SessionRef
	prev    Entry
	seen    bool
	mtime   int64
	payload *model.SessionPayload
}

// buildPool summarizes sessions while the plan keeps listing them: with a
// model it runs several calls at once, and it hands the results back in the
// order the sessions were found.
type buildPool struct {
	s        *Syncer
	ctx      context.Context
	wo       workitems.Options
	onDone   func(*model.SessionPayload)
	in       chan int
	closer   stdsync.Once
	jobs     []job
	wg       stdsync.WaitGroup
	mu       stdsync.Mutex
	err      error
	failures int
}

func (s *Syncer) newPool(ctx context.Context, wo workitems.Options, onDone func(*model.SessionPayload)) *buildPool {
	n := 1
	if s.Summarizer != nil {
		n = s.SummarizerWorkers
		if n <= 0 {
			n = 4
		}
	}
	p := &buildPool{s: s, ctx: ctx, wo: wo, onDone: onDone, in: make(chan int)}
	for i := 0; i < n; i++ {
		p.wg.Add(1)
		go p.work()
	}
	return p
}

func (p *buildPool) add(j job) error {
	p.mu.Lock()
	if p.err != nil {
		err := p.err
		p.mu.Unlock()
		return err
	}
	p.jobs = append(p.jobs, j)
	i := len(p.jobs) - 1
	p.mu.Unlock()
	select {
	case p.in <- i:
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

func (p *buildPool) work() {
	defer p.wg.Done()
	for i := range p.in {
		p.mu.Lock()
		j := p.jobs[i]
		p.mu.Unlock()
		onErr := func(err error) {
			p.mu.Lock()
			p.failures++
			p.mu.Unlock()
			p.s.log().Warn("model summary failed; the heuristic summary is used", "session", j.ref.ID, "err", err)
		}
		pl, err := summarize.Build(j.raw, p.s.summarizeOptions(p.ctx, j.ref, p.wo, false, onErr))
		p.mu.Lock()
		if err != nil && p.err == nil {
			p.err = fmt.Errorf("sync: summarize: %w", err)
		}
		p.jobs[i].payload, p.jobs[i].raw = pl, nil // the transcript is no longer needed
		p.mu.Unlock()
		if err == nil && p.onDone != nil {
			p.onDone(pl)
		}
	}
}

func (p *buildPool) stop() { p.closer.Do(func() { close(p.in) }) }

func (p *buildPool) wait() ([]job, error) {
	p.stop()
	p.wg.Wait()
	return p.jobs, p.err
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
