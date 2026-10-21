package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/summarize"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

// Preview statuses relative to what was last pushed.
const (
	PreviewNew       = "new"
	PreviewChanged   = "changed"
	PreviewUnchanged = "unchanged"
)

// PreviewItem is one session as it would be sent.
type PreviewItem struct {
	Ref     model.SessionRef
	Payload *model.SessionPayload
	Prev    *model.SessionPayload // what the server got last time, if anything
	Status  string
}

// PreviewResult is the exact outgoing payloads of one project, newest first.
type PreviewResult struct {
	Items      []PreviewItem
	Total      int // sessions in the window, of which Items are the newest
	Redactions map[string]int
	Warnings   int
}

// Preview builds the payloads a project's sessions would produce. It works for
// projects that are not shared yet (so a user can look before deciding), never
// contacts a sink, and writes nothing.
func (s *Syncer) Preview(ctx context.Context, project string, limit int) (PreviewResult, error) {
	res := PreviewResult{Redactions: map[string]int{}}
	since, err := s.sinceTime(RunOpts{})
	if err != nil {
		return res, err
	}
	type found struct {
		a   adapters.Adapter
		ref model.SessionRef
	}
	var refs []found
	ci := s.caseInsensitive()
	for _, a := range s.Registry.All() {
		det, err := a.Detect(ctx)
		if err != nil || !det.Installed {
			continue
		}
		for ref, lerr := range a.ListSessions(ctx, adapters.ListOpts{Since: since}) {
			if lerr != nil {
				if adapters.IsSkip(lerr) {
					res.Warnings++
					continue
				}
				return res, fmt.Errorf("sync: list %s: %w", a.Name(), lerr)
			}
			if sameProject(ref.ProjectKey, project, ci) {
				refs = append(refs, found{a, ref})
			}
		}
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].ref.UpdatedAt.After(refs[j].ref.UpdatedAt) })
	res.Total = len(refs)
	if limit > 0 && len(refs) > limit {
		refs = refs[:limit]
	}
	wo := workitems.Options{Providers: s.Config.Issues.Providers}
	wo.KnownPrefixes = append(append([]string{}, s.Config.Issues.LinearTeams...), s.Config.Issues.JiraProjects...)
	for _, f := range refs {
		raw, err := f.a.Load(ctx, f.ref)
		if err != nil {
			if adapters.IsSkip(err) {
				res.Warnings++
				continue
			}
			return res, fmt.Errorf("sync: load: %w", err)
		}
		res.Warnings += len(raw.Warnings)
		// offline: a preview never calls the model (the project may not be
		// shared); it shows the model's summary only when one is cached
		p, err := summarize.Build(raw, s.summarizeOptions(ctx, f.ref, wo, true, nil))
		if err != nil {
			return res, fmt.Errorf("sync: summarize: %w", err)
		}
		it := PreviewItem{Ref: f.ref, Payload: p, Status: PreviewNew}
		if prev, ok, err := s.State.Get(ctx, f.ref.Source, f.ref.ID); err != nil {
			return res, err
		} else if ok {
			it.Status = PreviewChanged
			// the same test a sync makes: an untouched file, or the same conversation
			untouched := prev.Size == f.ref.SizeBytes && prev.MTimeNS == f.ref.UpdatedAt.UnixNano()
			if untouched || prev.ContentHash == p.ContentHash {
				it.Status = PreviewUnchanged
			}
			var old model.SessionPayload
			if len(prev.Payload) > 0 && json.Unmarshal(prev.Payload, &old) == nil {
				it.Prev = &old
			}
		}
		for k, n := range p.Redactions {
			res.Redactions[k] += n
		}
		res.Items = append(res.Items, it)
	}
	return res, nil
}

// PendingCounts counts, per project, the selected sessions that a sync would
// look at again: never pushed, or whose file changed since. It reads only
// listing metadata, so the TUI can show "▲ n" instantly.
func (s *Syncer) PendingCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	since, err := s.sinceTime(RunOpts{})
	if err != nil {
		return nil, err
	}
	for _, a := range s.Registry.All() {
		det, err := a.Detect(ctx)
		if err != nil || !det.Installed {
			continue
		}
		for ref, lerr := range a.ListSessions(ctx, adapters.ListOpts{Since: since}) {
			if lerr != nil {
				continue
			}
			if !s.Matcher.Decide(ref.ProjectKey, ref.Source).Shared {
				continue
			}
			if dead, err := s.State.Tombstoned(ctx, ref.ProjectKey); err != nil {
				return nil, err
			} else if dead {
				continue
			}
			prev, seen, err := s.State.Get(ctx, ref.Source, ref.ID)
			if err != nil {
				return nil, err
			}
			if !seen || prev.Size != ref.SizeBytes || prev.MTimeNS != ref.UpdatedAt.UnixNano() {
				out[ref.ProjectKey]++
			}
		}
	}
	return out, nil
}

// HomeLabel is the display form of a project key.
func (s *Syncer) HomeLabel(key string) string { return pathutil.Scrub(key, s.Home) }
