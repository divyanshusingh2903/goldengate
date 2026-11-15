// Package sync plans and performs pushes. Everything that leaves the machine
// goes through Push, the single function to audit. A Batch can only be
// produced by Syncer.Plan, which applies selection, tombstones and
// redaction/summarization, so there is no path from an unselected project to
// a Sink.
package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	stdsync "sync"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/llm"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/project"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/rules"
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
	// auto is set when the session is sent by trust and rules rather than
	// by the user's own approval: Push writes it into the receipt.
	auto *AutoItem
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
		// an automatic send leaves a receipt: what, where to, and why
		if it.auto != nil {
			if err := state.addReceiptItem(ctx, *it.auto, now); err != nil {
				return err
			}
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
	o.ProjectID, o.ProjectName = s.projectOf(ref)
	if e, ok, err := s.State.Edit(ctx, ref.Source, ref.ID); err == nil && ok {
		o.Edit = &e // the user's version; it wins while the content is what they edited
	}
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
			MaxInputChars: max, Length: s.Config.Summarize.Length, OnError: onErr}
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
	// Approve also sends sessions that wait in the inbox. Set it only when
	// the user has seen them (a reviewed plan) or asked for it outright:
	// sending is then the approval.
	Approve bool
	// Offline never calls the model summarizer: summaries come from the
	// rules, or from answers already cached. For listings.
	Offline bool
	// Unattended marks a run nobody is watching (a timer or a hook). It
	// sends exactly what any sync sends, and lists all of it in the receipt:
	// also the sessions the user approved by hand earlier, since they too
	// leave while the user is away.
	Unattended bool
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
	// Refiled counts unchanged, already-pushed sessions that are sent again
	// only because their project was renamed, merged or given an id. They
	// are included in Planned.
	Refiled int
	// Pending counts sessions that wait for approval in the inbox and were
	// therefore not planned; PendingItems lists them. Excluded counts
	// sessions the user excluded or retracted.
	Pending      int
	Excluded     int
	PendingItems []PendingItem
	// Approving counts planned sessions that were waiting in the inbox and
	// are in the plan only because RunOpts.Approve is set: sending the plan
	// approves them.
	Approving      int
	ApprovingItems []PendingItem
	// Auto lists planned sessions that are sent without the user having
	// approved them one by one: the project's trust level is "auto" and its
	// rules approve them. Push writes each one that is sent into the
	// receipt named by Receipt.
	Auto    []AutoItem
	Receipt string
	// Unattended lists the other sessions an unattended run sends (approved
	// by hand earlier); they are in the same receipt. Empty otherwise.
	Unattended []AutoItem
	// Suggested counts waiting sessions the rules approve in a "suggest"
	// project; NewGroupings counts those held in an "auto" project because
	// their folder or chat grouping is new. Both are part of Pending.
	// RuleExcluded counts sessions an exclude rule keeps back; they are part
	// of Excluded.
	Suggested    int
	NewGroupings int
	RuleExcluded int
	// Conflicts counts sessions the user edited that changed afterwards.
	// They are never planned, whatever the project or the options say, until
	// the user has decided about their edit; they are listed in
	// PendingItems with Conflict set.
	Conflicts int
}

// AutoItem is a session sent by a project's trust level and rules.
type AutoItem struct {
	Source, SessionID string
	Project           string // local project key
	ProjectID         string
	ProjectName       string
	Title             string // redacted, as sent
	// RuleID is the approve rule that matched; empty when the project has
	// none and sends everything. Why says it in words, as of the send.
	RuleID string
	Why    string
	// Receipt is the id of the receipt this send is listed in: one per plan.
	Receipt string
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
	wo := workitems.Options{Providers: s.Config.Issues.Providers, GitLabHosts: s.Config.Issues.GitLabHosts}
	wo.KnownPrefixes = append(append([]string{}, s.Config.Issues.LinearTeams...), s.Config.Issues.JiraProjects...)
	pool := s.newPool(ctx, wo, o.OnPlanned)
	pool.offline, pool.approve = o.Offline, o.Approve
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
			// The user's decision about this very session comes before
			// everything else: excluded and retracted sessions are not read.
			dec, decided, err := s.State.Decision(ctx, ref.Source, ref.ID)
			if err != nil {
				return b, rep, err
			}
			if decided && dec.Blocks() {
				rep.Excluded++
				continue
			}
			prev, seen, err := s.State.Get(ctx, ref.Source, ref.ID)
			if err != nil {
				return b, rep, err
			}
			mtime := ref.UpdatedAt.UnixNano()
			resum := false
			if seen && prev.Size == ref.SizeBytes && prev.MTimeNS == mtime {
				stale := s.Summarizer != nil && staleSummary(prev.Payload, s.Summarizer.Name(), s.Config.Summarize.Length)
				if !stale || !o.Resummarize {
					if stale {
						rep.Stale++
					}
					if p, body, ok := s.refiled(prev, ref); ok {
						entry := prev
						entry.Payload = body
						b.items = append(b.items, planned{payload: p, entry: entry})
						rep.Planned++
						rep.Refiled++
						continue
					}
					rep.Unchanged++
					continue
				}
				rep.Resummarized++
				resum = true
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
			if err := pool.add(job{raw: raw, ref: ref, prev: prev, seen: seen, mtime: mtime, resummarize: resum}); err != nil {
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
			body, _ := json.Marshal(p)
			// the fingerprint covers the conversation, not the summary: a
			// re-summarized session is sent when its payload came out different
			wasID, wasName, kept := projectFields(prev.Payload)
			sameProj := !kept || (wasID == p.ProjectID && wasName == p.ProjectName)
			// an edit made (or removed) after the session was sent changes
			// the payload without changing the conversation
			compare := j.resummarize || (p.Summary != nil && p.Summary.Edited) || editedPayload(prev.Payload)
			if seen && prev.ContentHash == p.ContentHash && sameProj && (!compare || bytes.Equal(body, prev.Payload)) {
				// same content, only the file's stat changed: remember the new stat, send nothing
				prev.Size, prev.MTimeNS = ref.SizeBytes, mtime
				b.refresh = append(b.refresh, prev)
				rep.Unchanged++
				continue
			}
			// The user edited an earlier version of this session. The new
			// generated summary is not sent in place of their text: they
			// decide first (keep the edit, edit again, or drop it).
			if p.EditConflict {
				rep.Conflicts++
				rep.PendingItems = append(rep.PendingItems, PendingItem{Source: ref.Source, SessionID: ref.ID, Project: ref.ProjectKey,
					ProjectName: p.ProjectName, Title: payloadTitle(p), Updated: p.UpdatedAt, ContentHash: p.ContentHash, Changed: true, Conflict: true})
				continue
			}
			// New or changed content: sent if the user approved exactly this
			// content, or if the project's trust level and rules send it.
			g := j.gate
			var auto *AutoItem
			switch {
			case g.excluded:
				rep.Excluded++
				rep.RuleExcluded++
				continue
			case !g.send:
				item := PendingItem{Source: ref.Source, SessionID: ref.ID, Project: ref.ProjectKey,
					ProjectName: p.ProjectName, Title: payloadTitle(p), Updated: p.UpdatedAt, ContentHash: p.ContentHash, Changed: seen || g.decided,
					Suggested: g.suggested, NewGrouping: g.newGrouping, RuleID: g.ruleID}
				if o.Approve {
					rep.Approving++
					rep.ApprovingItems = append(rep.ApprovingItems, item)
					break
				}
				rep.Pending++
				if g.suggested {
					rep.Suggested++
				}
				if g.newGrouping {
					rep.NewGroupings++
				}
				rep.PendingItems = append(rep.PendingItems, item)
				continue
			case g.auto || o.Unattended:
				if rep.Receipt == "" {
					rep.Receipt = newReceiptID()
				}
				why := g.why
				switch {
				case g.auto:
				case g.manual:
					why = WhyApprovedByHand
				default:
					why = "its folder is shared outside any project, which sends everything"
				}
				auto = &AutoItem{Source: ref.Source, SessionID: ref.ID, Project: ref.ProjectKey,
					ProjectID: p.ProjectID, ProjectName: p.ProjectName, Title: payloadTitle(p), RuleID: g.ruleID, Why: why, Receipt: rep.Receipt}
				if g.auto {
					rep.Auto = append(rep.Auto, *auto)
				} else {
					rep.Unattended = append(rep.Unattended, *auto)
				}
			}
			for k, n := range p.Redactions {
				rep.Redactions[k] += n
			}
			s.log().Debug("planned", "adapter", ref.Source, "session", ref.ID, "redactions", p.Redactions)
			b.items = append(b.items, planned{payload: p, auto: auto, entry: Entry{
				Source: ref.Source, SessionID: ref.ID, Project: ref.ProjectKey, ContentHash: p.ContentHash,
				Size: ref.SizeBytes, MTimeNS: mtime, Payload: body,
			}})
			rep.Planned++
		}
	}
	return b, rep, nil
}

func payloadTitle(p *model.SessionPayload) string {
	if p.Summary != nil {
		return p.Summary.Title
	}
	return p.TitleOnly
}

// editedPayload reports whether a stored payload carries a user-edited summary.
func editedPayload(payload []byte) bool {
	var p struct {
		Summary *struct {
			Edited bool `json:"edited"`
		} `json:"summary"`
	}
	return json.Unmarshal(payload, &p) == nil && p.Summary != nil && p.Summary.Edited
}

// staleSummary reports whether a pushed payload's summary was written by
// something other than the summarizer named by, or holds a model's narrative
// written under another length setting. A narrative the user wrote is never
// stale. A payload stored before payloads were kept, or unreadable, counts
// as stale.
func staleSummary(payload []byte, by, length string) bool {
	var p struct {
		Summary *struct {
			SummarizedBy    string `json:"summarized_by"`
			NarrativeBy     string `json:"narrative_by"`
			NarrativeLength string `json:"narrative_length"`
		} `json:"summary"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return true
	}
	if p.Summary == nil {
		return false // titles-only level: there is no summary to upgrade
	}
	if editedPayload(payload) {
		return false // the user's own text is never offered for re-summarizing
	}
	if p.Summary.SummarizedBy != by {
		return true
	}
	return p.Summary.NarrativeBy == by && p.Summary.NarrativeLength != summarize.NormalLength(length)
}

// job is one session to summarize; payload is filled in by the pool.
type job struct {
	raw     *model.RawSession
	ref     model.SessionRef
	prev    Entry
	seen    bool
	mtime   int64
	payload *model.SessionPayload
	// resummarize sends the session even though its conversation is unchanged
	resummarize bool
	gate        gate
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
	// offline never calls the model; approve means sessions waiting in the
	// inbox are part of the plan (so they may be summarized by the model).
	offline, approve bool
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
		// A session that waits for approval is not sent to the model: it
		// is built with the rules (and cached answers) first, and only one
		// that will be in the plan is summarized by the model.
		offline := p.offline
		var pl *model.SessionPayload
		var err error
		var g gate
		gated := false
		if !offline && p.s.Summarizer != nil && !p.approve {
			if pl, err = summarize.Build(j.raw, p.s.summarizeOptions(p.ctx, j.ref, p.wo, true, nil)); err == nil {
				if g, err = p.s.gate(p.ctx, j.ref, j.raw, pl); err == nil {
					gated = true
					if g.send {
						pl = nil // it will be sent: build it for real below
					} else {
						offline = true // it waits or is excluded: keep the offline build
					}
				}
			}
		}
		if err == nil && pl == nil {
			pl, err = summarize.Build(j.raw, p.s.summarizeOptions(p.ctx, j.ref, p.wo, offline, onErr))
		}
		if err == nil && !gated {
			g, err = p.s.gate(p.ctx, j.ref, j.raw, pl)
		}
		p.mu.Lock()
		if err != nil && p.err == nil {
			p.err = fmt.Errorf("sync: summarize: %w", err)
		}
		p.jobs[i].gate = g
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

// projectOf is the named project a session belongs to (its id and name), or
// two empty strings when its folder is shared without being in a project.
func (s *Syncer) projectOf(ref model.SessionRef) (id, name string) {
	name = s.Matcher.Project(ref.ProjectKey)
	if name == "" {
		if k := s.serverKey(ref); k != ref.ProjectKey {
			name = s.Matcher.Project(k)
		}
	}
	if name == "" {
		return "", ""
	}
	for _, p := range s.Config.Projects {
		if p.Name == name {
			return p.ID, p.Name
		}
	}
	return "", ""
}

// gate is what happens to new or changed content of one session.
type gate struct {
	send     bool // it goes into the plan without being asked about
	auto     bool // send because of the project's trust level and rules, not a decision of the user's
	manual   bool // send because the user approved exactly this content
	excluded bool // an exclude rule keeps it back
	// decided is set when the user decided about an earlier version.
	decided bool
	// Why it waits, when the rules approve it: see PendingItem.
	suggested, newGrouping bool
	ruleID                 string
	why                    string // the rule in words, or that the whole project is automatic
}

// Facts are the deterministic properties of a session that rules may see.
// They come from the tool's own record and the local rules' extraction,
// never from a summary.
func Facts(ref model.SessionRef, raw *model.RawSession, p *model.SessionPayload) rules.Facts {
	f := rules.Facts{Source: ref.Source, Folder: ref.ProjectKey}
	if raw != nil {
		f.Remote, f.Branch = raw.GitRemote, raw.GitBranch
	}
	if p != nil {
		for _, it := range p.WorkItems {
			if it.Kind == model.KindIssue || it.Kind == model.KindPR {
				f.WorkItems = append(f.WorkItems, it.Key)
			}
		}
	}
	return f
}

// gate decides about a session's new or changed content, in this order: the
// user's own approval of exactly this content; an exclude rule; then the
// project's trust level applied to what its rules approve. A folder shared
// outside any named project (a default-allow configuration) has always sent
// everything and still does.
func (s *Syncer) gate(ctx context.Context, ref model.SessionRef, raw *model.RawSession, p *model.SessionPayload) (gate, error) {
	id, _ := s.projectOf(ref)
	var proj *config.Project
	for i := range s.Config.Projects {
		if s.Config.Projects[i].ID == id && id != "" {
			proj = &s.Config.Projects[i]
		}
	}
	if proj == nil {
		return gate{send: true}, nil
	}
	dec, decided, err := s.State.Decision(ctx, ref.Source, ref.ID)
	if err != nil {
		return gate{}, err
	}
	g := gate{decided: decided}
	if decided && dec.State == DecisionApproved && dec.ContentHash == p.ContentHash {
		g.send, g.manual = true, true
		return g, nil
	}
	v := rules.Evaluate(proj.Rules, Facts(ref, raw, p), s.Home)
	g.ruleID = v.RuleID()
	switch {
	case v.Exclude != nil:
		g.excluded = true
	case !v.Approved():
		g.ruleID = ""
	case proj.TrustLevel() == rules.TrustSuggest:
		g.suggested = true
	case proj.TrustLevel() == rules.TrustAuto:
		known, err := s.knownGrouping(ctx, proj, ref)
		if err != nil {
			return gate{}, err
		}
		if known {
			g.send, g.auto = true, true
			g.why = "trust auto: every session of the project"
			if v.Approve != nil {
				g.why = "rule " + v.Approve.ID + ": " + v.Approve.String()
			}
		} else {
			g.newGrouping = true
		}
	default:
		g.ruleID = ""
	}
	return g, nil
}

// knownGrouping reports whether automation may act on a folder or chat
// grouping without asking: the user named it as a member of the project, or
// something from it was already sent. A grouping the project
// reaches only through a broader member (a parent folder, a glob such as
// chatgpt:*) is asked about once.
func (s *Syncer) knownGrouping(ctx context.Context, p *config.Project, ref model.SessionRef) (bool, error) {
	// a worktree is its repository: the key it is filed under counts too
	key, filed := ref.ProjectKey, s.serverKey(ref)
	for _, m := range p.Members {
		if strings.ContainsAny(m.Path, "*?[{") {
			continue
		}
		mk := m.Path
		if !pathutil.IsVirtualKey(mk) {
			mk = pathutil.Normalize(pathutil.ExpandHome(mk, s.Home))
		}
		for _, k := range []string{key, filed} {
			if mk == k || (s.caseInsensitive() && strings.EqualFold(mk, k)) {
				return true, nil
			}
		}
	}
	return s.State.KnownGrouping(ctx, key)
}

// projectFields reads which project a stored payload was sent under. ok is
// false when the payload was not kept or cannot be read.
func projectFields(payload []byte) (id, name string, ok bool) {
	var p struct {
		ID   string `json:"project_id"`
		Name string `json:"project_name"`
	}
	if len(payload) == 0 || json.Unmarshal(payload, &p) != nil {
		return "", "", false
	}
	return p.ID, p.Name, true
}

// refiled returns the payload already sent for an unchanged session with its
// project fields brought up to date, when the session's project was renamed,
// merged, or first given an id since. Nothing is read or summarized again:
// only the project the server files it under changes.
func (s *Syncer) refiled(prev Entry, ref model.SessionRef) (*model.SessionPayload, []byte, bool) {
	wasID, wasName, ok := projectFields(prev.Payload)
	id, name := s.projectOf(ref)
	if !ok || (wasID == id && wasName == name) {
		return nil, nil, false
	}
	var p model.SessionPayload
	if json.Unmarshal(prev.Payload, &p) != nil {
		return nil, nil, false
	}
	p.Version, p.ProjectID, p.ProjectName = 0, "", ""
	if id != "" {
		p.Version, p.ProjectID, p.ProjectName = model.PayloadV2, id, name
	}
	body, err := json.Marshal(&p)
	if err != nil {
		return nil, nil, false
	}
	return &p, body, true
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
