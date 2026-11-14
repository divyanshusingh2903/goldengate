package store

import (
	"context"
	"strings"
	"time"
)

// Scope limits what the read methods return to one agent's view: only the
// listed projects, and nothing older than NotBefore. It travels in the
// context, so every query of a request is filtered in the store itself and
// an ungranted project behaves exactly like one that does not exist.
//
// The read methods are Sessions, GetSession, Search, WorkItems, ListProjects
// and ResolveProject. Writes (PutSession, DeleteProject, DeleteSession) and
// the auth, OAuth and agent methods ignore it.
type Scope struct {
	// Projects are the ids of the projects that are visible. Empty means
	// none are.
	Projects []string
	// NotBefore hides sessions last updated, and work items last observed,
	// before it. Zero means no bound.
	NotBefore time.Time
}

type scopeKey struct{}

// WithScope returns a context whose store reads are limited to sc.
func WithScope(ctx context.Context, sc Scope) context.Context {
	sc.Projects = append([]string(nil), sc.Projects...)
	return context.WithValue(ctx, scopeKey{}, &sc)
}

// ScopeFrom returns the scope a context carries, if any.
func ScopeFrom(ctx context.Context) (Scope, bool) {
	sc, ok := ctx.Value(scopeKey{}).(*Scope)
	if !ok {
		return Scope{}, false
	}
	return *sc, true
}

// sees reports whether the context's scope shows something in project,
// stamped at. A context without a scope shows everything. A zero at skips
// the time bound.
func sees(ctx context.Context, project string, at time.Time) bool {
	sc, ok := ctx.Value(scopeKey{}).(*Scope)
	if !ok {
		return true
	}
	if !at.IsZero() && !sc.NotBefore.IsZero() && at.Before(sc.NotBefore) {
		return false
	}
	for _, p := range sc.Projects {
		if p == project {
			return true
		}
	}
	return false
}

// scopeSQL is the scope as SQL: predicates (each starting with " AND ") on a
// project-id column and, when timeCol is not empty, a time column, with
// their arguments. Empty for a context without a scope.
func scopeSQL(ctx context.Context, projectCol, timeCol string) (string, []any) {
	sc, ok := ctx.Value(scopeKey{}).(*Scope)
	if !ok {
		return "", nil
	}
	if len(sc.Projects) == 0 {
		return " AND 0", nil
	}
	args := make([]any, 0, len(sc.Projects)+1)
	for _, p := range sc.Projects {
		args = append(args, p)
	}
	q := " AND " + projectCol + " IN (?" + strings.Repeat(",?", len(sc.Projects)-1) + ")"
	if timeCol != "" && !sc.NotBefore.IsZero() {
		q += " AND " + timeCol + ">=?"
		args = append(args, nano(sc.NotBefore))
	}
	return q, args
}
