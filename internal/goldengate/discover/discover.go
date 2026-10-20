// Package discover lists the projects found across all adapters, grouped by
// normalized folder. It is local-only display data (used by the TUI and
// `share list --projects`); nothing here is ever sent anywhere.
package discover

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/project"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// Project is one folder with sessions from one or more tools.
type Project struct {
	Key          string
	Root         string         // repository root when this folder is a worktree of another folder, else ""
	Sources      map[string]int // source -> session count
	Sessions     int
	LastActivity time.Time
	Decision     selection.Decision // for the best-ranked source: shared if any source is shared
}

// Group is the display key that merges worktrees with their repository.
func (p Project) Group() string {
	if p.Root != "" {
		return p.Root
	}
	return p.Key
}

// Result is the discovery output plus per-item warnings.
type Result struct {
	Projects []Project
	Warnings []string
}

// Projects scans every adapter (metadata only) and groups sessions by project.
func Projects(ctx context.Context, reg *adapters.Registry, m *selection.Matcher, since time.Time, rsv project.Resolver) (Result, error) {
	by := map[string]*Project{}
	var res Result
	for _, a := range reg.All() {
		det, err := a.Detect(ctx)
		if err != nil {
			return res, fmt.Errorf("discover: detect %s: %w", a.Name(), err)
		}
		if !det.Installed {
			continue
		}
		for ref, err := range a.ListSessions(ctx, adapters.ListOpts{Since: since}) {
			if err != nil {
				if adapters.IsSkip(err) {
					res.Warnings = append(res.Warnings, a.Name()+": "+err.Error())
					continue
				}
				return res, fmt.Errorf("discover: %s: %w", a.Name(), err)
			}
			add(by, m, ref, rsv)
		}
	}
	for _, p := range by {
		res.Projects = append(res.Projects, *p)
	}
	sort.Slice(res.Projects, func(i, j int) bool {
		return res.Projects[i].LastActivity.After(res.Projects[j].LastActivity)
	})
	return res, nil
}

func add(by map[string]*Project, m *selection.Matcher, ref model.SessionRef, rsv project.Resolver) {
	p, ok := by[ref.ProjectKey]
	if !ok {
		p = &Project{Key: ref.ProjectKey, Root: rsv.Root(ref), Sources: map[string]int{}}
		by[ref.ProjectKey] = p
	}
	p.Sources[ref.Source]++
	p.Sessions++
	if ref.UpdatedAt.After(p.LastActivity) {
		p.LastActivity = ref.UpdatedAt
	}
	d := m.Decide(ref.ProjectKey, ref.Source)
	if d.Shared || p.Decision.Reason == "" {
		p.Decision = d
	}
}
