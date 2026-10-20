package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/discover"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// tuiBackend implements tui.Backend on the same code the CLI commands use, so
// the UI can do exactly what the commands can and nothing else.
type tuiBackend struct {
	a    *app
	mu   sync.Mutex
	undo []shareRules

	// reviewed is what the last clean review (dry run) prepared, so that
	// sending right after it sends exactly that, without reading and redacting
	// everything again. It is used once, and only while the configuration it
	// was built under is unchanged and it is fresh.
	reviewed *reviewedBatch
}

type reviewedBatch struct {
	batch gsync.Batch
	cfg   config.Config
	at    time.Time
}

// reviewMaxAge bounds how stale a review may be and still be sent as is.
const reviewMaxAge = 15 * time.Minute

// takeReview returns the reviewed batch if it may be sent under cfg, and
// forgets it either way: a review is sent at most once.
func (b *tuiBackend) takeReview(cfg config.Config) (gsync.Batch, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.reviewed
	b.reviewed = nil
	if r == nil || b.a.Now().Sub(r.at) > reviewMaxAge || !reflect.DeepEqual(r.cfg, cfg) {
		return gsync.Batch{}, false
	}
	return r.batch, true
}

// shareRules is the part of the config the share actions edit, kept for undo.
type shareRules struct{ allow, deny, sourceDeny []string }

func rulesOf(c config.Config) shareRules {
	return shareRules{slices.Clone(c.Share.Allow), slices.Clone(c.Share.Deny), slices.Clone(c.Share.SourceDeny)}
}

func (a *app) tuiBackend() *tuiBackend { return &tuiBackend{a: a} }

func (b *tuiBackend) syncer(ctx context.Context, cfg config.Config) (*gsync.Syncer, func(), error) {
	m, err := b.a.matcher(cfg)
	if err != nil {
		return nil, nil, err
	}
	reg, err := b.a.registry(ctx)
	if err != nil {
		return nil, nil, err
	}
	st, err := b.a.openState()
	if err != nil {
		return nil, nil, err
	}
	s := &gsync.Syncer{Registry: reg, Config: cfg, Matcher: m, State: st, Home: b.a.Home, Now: b.a.Now, Resolver: b.a.resolver(cfg)}
	return s, func() { _ = st.Close() }, nil
}

// Snapshot implements tui.Backend.
func (b *tuiBackend) Snapshot(ctx context.Context) (tui.Snapshot, error) {
	a := b.a
	cfg, err := a.load()
	if err != nil {
		return tui.Snapshot{}, err
	}
	snap := tui.Snapshot{Level: cfg.Share.Level, Since: orNone(cfg.Share.Since), Server: cfg.Server.URL}
	if _, err := os.Stat(a.cfgPath()); errors.Is(err, os.ErrNotExist) {
		snap.FirstRun = true
	}
	s, closeFn, err := b.syncer(ctx, cfg)
	if err != nil {
		return snap, err
	}
	defer closeFn()
	var since time.Time
	if d, err := config.ParseSince(cfg.Share.Since); err == nil && d > 0 {
		since = a.Now().Add(-d)
	}
	res, err := discover.Projects(ctx, s.Registry, s.Matcher, since, s.Resolver)
	if err != nil {
		return snap, err
	}
	pending, err := s.PendingCounts(ctx)
	if err != nil {
		return snap, err
	}
	stats, err := s.State.ProjectStats(ctx)
	if err != nil {
		return snap, err
	}
	perSource := map[string]int{}
	for _, p := range res.Projects {
		row := tui.Row{Key: p.Key, Label: pathutil.Scrub(p.Key, a.Home), Sessions: p.Sessions, Last: p.LastActivity, Pending: pending[p.Key],
			Synced: stats[p.Key].Synced, LastSync: stats[p.Key].LastSync, Virtual: pathutil.IsVirtualKey(p.Key)}
		if p.Root != "" {
			row.Root = pathutil.Scrub(p.Root, a.Home)
		}
		names := make([]string, 0, len(p.Sources))
		for src := range p.Sources {
			names = append(names, src)
		}
		sort.Strings(names)
		shared := 0
		for _, src := range names {
			d := s.Matcher.Decide(p.Key, src)
			row.Sources = append(row.Sources, tui.SourceInfo{Name: src, Sessions: p.Sources[src], Shared: d.Shared})
			perSource[src] += p.Sources[src]
			if d.Shared {
				shared++
				if row.Reason == "" {
					row.Reason = d.Reason
				}
			} else if row.Reason == "" {
				row.Reason = d.Reason
			}
		}
		switch {
		case shared == len(names) && shared > 0:
			row.State = tui.Shared
		case shared > 0:
			row.State = tui.Partial
		case s.Matcher.Denied(p.Key):
			row.State = tui.Denied
		}
		snap.Rows = append(snap.Rows, row)
	}
	snap.Warnings = res.Warnings
	snap.Sources, err = b.sourceStatus(ctx, cfg, perSource)
	return snap, err
}

func (b *tuiBackend) sourceStatus(ctx context.Context, cfg config.Config, counts map[string]int) ([]tui.SourceStatus, error) {
	reg, err := b.a.registry(ctx)
	if err != nil {
		return nil, err
	}
	var out []tui.SourceStatus
	for _, ad := range reg.All() {
		det, err := ad.Detect(ctx)
		if err != nil {
			return nil, err
		}
		enabled := len(cfg.Share.IncludeSources) == 0 || slices.Contains(cfg.Share.IncludeSources, ad.Name())
		out = append(out, tui.SourceStatus{Name: ad.Name(), Installed: det.Installed, Sessions: counts[ad.Name()], Enabled: enabled, Note: strings.Join(det.Notes, "; ")})
	}
	return out, nil
}

// Preview implements tui.Backend.
func (b *tuiBackend) Preview(ctx context.Context, key string, limit int) (tui.Preview, error) {
	cfg, err := b.a.load()
	if err != nil {
		return tui.Preview{}, err
	}
	s, closeFn, err := b.syncer(ctx, cfg)
	if err != nil {
		return tui.Preview{}, err
	}
	defer closeFn()
	pr, err := s.Preview(ctx, key, limit)
	if err != nil {
		return tui.Preview{}, err
	}
	out := tui.Preview{Key: key, Level: cfg.Share.Level, Total: pr.Total, Redactions: pr.Redactions, Warnings: pr.Warnings}
	for _, it := range pr.Items {
		body, _ := json.MarshalIndent(it.Payload, "", "  ")
		out.Items = append(out.Items, tui.PreviewItem{Source: it.Ref.Source, ID: it.Ref.ID, Updated: it.Payload.UpdatedAt, Status: it.Status,
			Payload: *it.Payload, JSON: string(body), Diff: tui.DiffPayload(it.Prev, it.Payload), Redacted: it.Payload.Redactions})
	}
	return out, nil
}

// Apply implements tui.Backend.
func (b *tuiBackend) Apply(ctx context.Context, act tui.Action) (tui.ActionResult, error) {
	a := b.a
	cfg, err := a.load()
	if err != nil {
		return tui.ActionResult{}, err
	}
	m, err := a.matcher(cfg)
	if err != nil {
		return tui.ActionResult{}, err
	}
	var notes []string
	var edit func(*config.Config)
	switch act.Kind {
	case tui.ActAllow:
		edit, err = b.allowEdit(cfg, m, act.Keys, &notes)
	case tui.ActRemove:
		edit = b.removeEdit(cfg, m, act.Keys, &notes)
	case tui.ActDeny:
		edit = func(c *config.Config) {
			for _, k := range act.Keys {
				if r := a.ruleFor(k); !a.hasRule(c.Share.Deny, r) {
					c.Share.Deny = append(c.Share.Deny, r)
				}
			}
		}
		notes = append(notes, fmt.Sprintf("denied %d", len(act.Keys)))
	case tui.ActToggleSource:
		if len(act.Keys) != 1 {
			return tui.ActionResult{}, errors.New("choose one project to change its sources")
		}
		key := act.Keys[0]
		rule := selection.SourceRule(act.Source, a.ruleFor(key))
		anyShared := false
		for _, src := range append([]string{act.Source}, act.Others...) {
			if m.Decide(key, src).Shared {
				anyShared = true
			}
		}
		switch {
		case m.Denied(key):
			err = errors.New("this project is denied; share it first (space on the project)")
		case !anyShared:
			// nothing here is shared yet: share the folder with this one source only
			var allow func(*config.Config)
			var ignored []string
			allow, err = b.allowEdit(cfg, m, act.Keys, &ignored)
			others := make([]string, 0, len(act.Others))
			for _, o := range act.Others {
				others = append(others, selection.SourceRule(o, a.ruleFor(key)))
			}
			edit = func(c *config.Config) {
				if allow != nil {
					allow(c)
				}
				c.Share.SourceDeny = slices.DeleteFunc(c.Share.SourceDeny, func(x string) bool { return x == rule })
				for _, r := range others {
					if !slices.Contains(c.Share.SourceDeny, r) {
						c.Share.SourceDeny = append(c.Share.SourceDeny, r)
					}
				}
			}
			notes = append(notes, "shared this project's "+pretty(act.Source)+" sessions only")
		default:
			on := !slices.Contains(cfg.Share.SourceDeny, rule)
			edit = func(c *config.Config) {
				if on {
					c.Share.SourceDeny = append(c.Share.SourceDeny, rule)
				} else {
					c.Share.SourceDeny = slices.DeleteFunc(c.Share.SourceDeny, func(x string) bool { return x == rule })
				}
			}
			if on {
				notes = append(notes, pretty(act.Source)+" is no longer sent for this project")
			} else {
				notes = append(notes, pretty(act.Source)+" is sent again for this project")
			}
		}
	default:
		return tui.ActionResult{}, errors.New("unknown action")
	}
	if err != nil {
		return tui.ActionResult{}, err
	}
	targets, err := a.applyChange(ctx, cfg, edit)
	if err != nil {
		return tui.ActionResult{}, err
	}
	b.mu.Lock()
	b.undo = append(b.undo, rulesOf(cfg))
	b.mu.Unlock()
	res := tui.ActionResult{Message: strings.Join(notes, "; ")}
	if len(targets) > 0 && cfg.Server.URL != "" {
		res.Remote = &tui.RemotePrompt{Count: len(targets), Handle: targets}
	}
	return res, nil
}

func pretty(source string) string {
	switch source {
	case "claude-code":
		return "Claude Code"
	case "opencode":
		return "OpenCode"
	case "claude-ai":
		return "claude.ai"
	case "chatgpt":
		return "ChatGPT"
	}
	return strings.ToUpper(source[:1]) + source[1:]
}

func (a *app) hasRule(rules []string, rule string) bool {
	return slices.ContainsFunc(rules, func(r string) bool { return sameRule(a, r, rule) })
}

func (b *tuiBackend) allowEdit(cfg config.Config, m *selection.Matcher, keys []string, notes *[]string) (func(*config.Config), error) {
	a := b.a
	for _, k := range keys {
		rule := a.ruleFor(k)
		if a.hasRule(cfg.Share.Deny, rule) {
			continue // the exact deny rule is lifted below
		}
		if m.Denied(a.localKey(k)) {
			return nil, fmt.Errorf("%s is denied by a broader rule (%s); edit the config to change that", rule, m.Decide(a.localKey(k), "").Reason)
		}
	}
	*notes = append(*notes, fmt.Sprintf("sharing %d", len(keys)))
	return func(c *config.Config) {
		for _, k := range keys {
			rule := a.ruleFor(k)
			c.Share.Deny = slices.DeleteFunc(c.Share.Deny, func(r string) bool { return sameRule(a, r, rule) })
			// a source switched off for this project is switched back on when the project is shared explicitly
			c.Share.SourceDeny = slices.DeleteFunc(c.Share.SourceDeny, func(r string) bool {
				_, pat, ok := selection.SplitSourceRule(r)
				return ok && sameRule(a, pat, rule)
			})
			if m2, err := a.matcher(*c); err == nil && !m2.Decide(a.localKey(k), "").Shared && !a.hasRule(c.Share.Allow, rule) {
				c.Share.Allow = append(c.Share.Allow, rule)
			}
		}
	}, nil
}

func (b *tuiBackend) removeEdit(cfg config.Config, m *selection.Matcher, keys []string, notes *[]string) func(*config.Config) {
	a := b.a
	excluded := 0
	for _, k := range keys {
		if !a.hasRule(cfg.Share.Allow, a.ruleFor(k)) && m.Decide(a.localKey(k), "").Shared {
			excluded++
		}
	}
	msg := fmt.Sprintf("no longer sharing %d", len(keys))
	if excluded > 0 {
		msg += fmt.Sprintf(" (%d excluded with a deny rule because a broader rule shares them)", excluded)
	}
	*notes = append(*notes, msg)
	return func(c *config.Config) {
		for _, k := range keys {
			rule := a.ruleFor(k)
			if a.hasRule(c.Share.Allow, rule) {
				c.Share.Allow = slices.DeleteFunc(c.Share.Allow, func(r string) bool { return sameRule(a, r, rule) })
				continue
			}
			if m2, err := a.matcher(*c); err == nil && m2.Decide(a.localKey(k), "").Shared && !a.hasRule(c.Share.Deny, rule) {
				c.Share.Deny = append(c.Share.Deny, rule)
			}
		}
	}
}

// Undo implements tui.Backend.
func (b *tuiBackend) Undo(ctx context.Context) (string, error) {
	b.mu.Lock()
	if len(b.undo) == 0 {
		b.mu.Unlock()
		return "", errors.New("nothing to undo")
	}
	prev := b.undo[len(b.undo)-1]
	b.undo = b.undo[:len(b.undo)-1]
	b.mu.Unlock()
	cfg, err := b.a.load()
	if err != nil {
		return "", err
	}
	if _, err := b.a.applyChange(ctx, cfg, func(c *config.Config) {
		c.Share.Allow, c.Share.Deny, c.Share.SourceDeny = slices.Clone(prev.allow), slices.Clone(prev.deny), slices.Clone(prev.sourceDeny)
	}); err != nil {
		return "", err
	}
	return "undid the last change", nil
}

// DeleteRemote implements tui.Backend.
func (b *tuiBackend) DeleteRemote(ctx context.Context, handle any) (string, error) {
	targets, ok := handle.([]remoteTarget)
	if !ok {
		return "", errors.New("nothing to delete")
	}
	cfg, err := b.a.load()
	if err != nil {
		return "", err
	}
	var errs strings.Builder
	var out strings.Builder
	if err := b.a.deleteTargets(ctx, cfg, targets, &out, &errs); err != nil {
		return "", err
	}
	if errs.Len() > 0 {
		return "", errors.New(strings.TrimSpace(errs.String()))
	}
	return fmt.Sprintf("deleted %d project(s) from the server", len(targets)), nil
}

// Plan implements tui.Backend.
func (b *tuiBackend) Plan(ctx context.Context) (tui.PlanInfo, error) {
	cfg, err := b.a.load()
	if err != nil {
		return tui.PlanInfo{}, err
	}
	s, closeFn, err := b.syncer(ctx, cfg)
	if err != nil {
		return tui.PlanInfo{}, err
	}
	defer closeFn()
	batch, rep, err := s.Plan(ctx, gsync.RunOpts{})
	if err != nil {
		return tui.PlanInfo{}, err
	}
	counts := map[string]int{}
	var order []string
	for _, p := range batch.Payloads() {
		if _, ok := counts[p.Project]; !ok {
			order = append(order, p.Project)
		}
		counts[p.Project]++
	}
	info := tui.PlanInfo{Sessions: batch.Len(), NotSelected: rep.NotSelected, Unchanged: rep.Unchanged, Warnings: rep.Warnings, Redactions: rep.Redactions, Server: cfg.Server.URL}
	for _, k := range order {
		info.Projects = append(info.Projects, tui.ProjectPlan{Label: k, Sessions: counts[k]})
	}
	return info, nil
}

type discardSink struct{}

func (discardSink) Send(context.Context, []*model.SessionPayload) error { return nil }

// Sync implements tui.Backend: plan, then push project by project so progress
// is real. A dry run goes through the same validation but sends and records
// nothing.
// syncStep is how many sessions the interactive sync sends per request, so
// its progress bar moves while a big project uploads.
const syncStep = 10

func (b *tuiBackend) Sync(ctx context.Context, dry bool, emit func(tui.SyncEvent)) error {
	cfg, err := b.a.load()
	if err != nil {
		return err
	}
	s, closeFn, err := b.syncer(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeFn()
	var sink gsync.Sink = discardSink{}
	if !dry {
		cl, err := b.a.ingestClient(cfg)
		if err != nil {
			return err
		}
		sink = cl
	}
	batch, reused := gsync.Batch{}, false
	if !dry {
		batch, reused = b.takeReview(cfg)
	} else {
		b.mu.Lock()
		b.reviewed = nil // a new review replaces the old one
		b.mu.Unlock()
	}
	if !reused {
		batch, _, err = s.Plan(ctx, gsync.RunOpts{OnPlanned: func() { emit(tui.SyncEvent{Kind: tui.SyncPrepared, Sessions: 1}) }})
		if err != nil {
			return err
		}
	}
	clean, finished := true, false // a review is kept only if it ran to the end without failures
	defer func() {
		if dry && clean && finished && ctx.Err() == nil {
			b.mu.Lock()
			b.reviewed = &reviewedBatch{batch: batch, cfg: cfg, at: b.a.Now()}
			b.mu.Unlock()
		}
	}()
	emit(tui.SyncEvent{Kind: tui.SyncPlanned, Sessions: batch.Len()})
	st := s.State
	if dry {
		st = nil
	}
	for _, part := range batch.ByProject() {
		if err := ctx.Err(); err != nil {
			return err
		}
		label := ""
		if ps := part.Payloads(); len(ps) > 0 {
			label = ps[0].Project
		}
		if part.Len() == 0 {
			if err := gsync.Push(ctx, sink, st, part, b.a.Now()); err != nil { // only stat refreshes: record them quietly
				return err
			}
			continue
		}
		emit(tui.SyncEvent{Kind: tui.SyncProjectStart, Project: label, Sessions: part.Len()})
		failed := false
		// small steps so the screen can follow; each step is recorded as it
		// lands, so a retry after a failure skips what already arrived
		for _, chunk := range part.Chunks(syncStep) {
			if err := gsync.Push(ctx, sink, st, chunk, b.a.Now()); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				emit(tui.SyncEvent{Kind: tui.SyncProjectFailed, Project: label, Err: err.Error()})
				failed, clean = true, false
				break
			}
			emit(tui.SyncEvent{Kind: tui.SyncProgress, Project: label, Sessions: chunk.Len()})
		}
		if !failed {
			emit(tui.SyncEvent{Kind: tui.SyncProjectDone, Project: label, Sessions: part.Len()})
		}
	}
	finished = true
	emit(tui.SyncEvent{Kind: tui.SyncFinished})
	return nil
}

// Server implements tui.Backend.
func (b *tuiBackend) Server(ctx context.Context) (tui.ServerInfo, error) {
	a := b.a
	cfg, err := a.load()
	if err != nil {
		return tui.ServerInfo{}, err
	}
	if cfg.Server.URL == "" {
		return tui.ServerInfo{}, nil
	}
	info := tui.ServerInfo{URL: cfg.Server.URL, Configured: true, TokenStore: a.secretStore().Kind()}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	probe, err := newProbe(a, cfg.Server.URL)
	if err != nil {
		info.Err = err.Error()
		return info, nil
	}
	if err := probe.Health(tctx); err != nil {
		info.Err = err.Error()
		return info, nil
	}
	info.Healthy = true
	cl, err := a.ingestClient(cfg)
	if err != nil {
		info.Err = err.Error()
		return info, nil
	}
	ps, err := cl.ListProjects(tctx)
	if err != nil {
		info.Err = "the stored ingest token was refused: " + err.Error()
		return info, nil
	}
	info.TokenOK = true
	for _, p := range ps {
		info.Projects = append(info.Projects, tui.RemoteProject{Label: p.Project, Sessions: p.Sessions, Last: p.LastActivity})
	}
	sort.Slice(info.Projects, func(i, j int) bool { return info.Projects[i].Last.After(info.Projects[j].Last) })
	return info, nil
}

// DeleteServerProject implements tui.Backend.
func (b *tuiBackend) DeleteServerProject(ctx context.Context, label string) error {
	cfg, err := b.a.load()
	if err != nil {
		return err
	}
	cl, err := b.a.ingestClient(cfg)
	if err != nil {
		return err
	}
	if _, err := cl.DeleteProject(ctx, label); err != nil {
		return err
	}
	st, err := b.a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	// still-shared sessions must upload again on the next sync
	return st.ForgetUnder(ctx, b.a.localKey(label))
}

// Settings implements tui.Backend.
func (b *tuiBackend) Settings(ctx context.Context) (tui.Settings, error) {
	cfg, err := b.a.load()
	if err != nil {
		return tui.Settings{}, err
	}
	srcs, err := b.sourceStatus(ctx, cfg, nil)
	if err != nil {
		return tui.Settings{}, err
	}
	return tui.Settings{Level: cfg.Share.Level, Since: cfg.Share.Since, Sources: srcs, ExtraPatterns: slices.Clone(cfg.Redact.ExtraPatterns), EnrichGit: cfg.Enrich.Git,
		Theme: cfg.UI.Theme}, nil
}

// SaveSettings implements tui.Backend. Nothing is written unless the whole
// result validates.
func (b *tuiBackend) SaveSettings(ctx context.Context, s tui.Settings) error {
	cfg, err := b.a.load()
	if err != nil {
		return err
	}
	for _, p := range s.ExtraPatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("redaction pattern %q is not a valid regular expression: %w", p, err)
		}
	}
	cfg.Share.Level, cfg.Share.Since = s.Level, strings.TrimSpace(s.Since)
	cfg.Redact.ExtraPatterns = slices.Clone(s.ExtraPatterns)
	cfg.Enrich.Git = s.EnrichGit
	cfg.UI.Theme = s.Theme
	var include []string
	for _, src := range s.Sources {
		if src.Enabled {
			include = append(include, src.Name)
		}
	}
	cfg.Share.IncludeSources = include
	if len(s.Sources) > 0 && len(include) == 0 {
		return errors.New("at least one source must stay enabled")
	}
	return config.Save(b.a.cfgPath(), cfg)
}

// Detect implements tui.Backend.
func (b *tuiBackend) Detect(ctx context.Context) ([]tui.ToolStatus, error) {
	reg, err := b.a.registry(ctx)
	if err != nil {
		return nil, err
	}
	var out []tui.ToolStatus
	for _, ad := range reg.All() {
		det, err := ad.Detect(ctx)
		if err != nil {
			return nil, err
		}
		t := tui.ToolStatus{Name: ad.Name(), Installed: det.Installed, Note: strings.Join(det.Notes, "; ")}
		if det.Installed {
			for _, lerr := range ad.ListSessions(ctx, adapters.ListOpts{}) {
				if lerr == nil {
					t.Sessions++
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// Init implements tui.Backend. The admin token lives in memory for this call.
func (b *tuiBackend) Init(ctx context.Context, p tui.InitParams) (tui.InitResult, error) {
	res, err := b.a.connect(ctx, p.ServerURL, strings.TrimSpace(p.PairCode), p.AdminToken)
	if err != nil {
		return tui.InitResult{}, err
	}
	return tui.InitResult{MCPURL: res.ServerURL + "/mcp", ReadToken: res.ReadSecret, OAuth: res.OAuth, TokenStore: res.StoreKind}, nil
}

// NewTUIBackend returns the backend the interactive UI uses, built on d. It is
// exported so end-to-end tests can drive the UI against real code.
func NewTUIBackend(d Deps) tui.Backend { return (&app{Deps: d}).tuiBackend() }
