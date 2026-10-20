// Package adapters defines the interface every source tool implements and a
// registry to look them up. Adapters are read-only: they never modify the
// source tool's files.
package adapters

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sort"
	"time"

	"github.com/OWNER/goldengate/internal/model"
)

// Detection says whether a source tool is present on this machine.
type Detection struct {
	Installed bool
	Root      string   // directory or database the adapter reads
	Notes     []string // human-readable hints, e.g. why not installed
}

// ListOpts narrows ListSessions.
type ListOpts struct {
	Since time.Time // zero = no limit; compared against last update
}

// Adapter reads one source tool's session history.
type Adapter interface {
	Name() string
	// Detect reports whether the tool's data is present.
	Detect(ctx context.Context) (Detection, error)
	// ListSessions is cheap: stat plus a bounded header read, no body parse.
	// Per-item problems are yielded as *SkipWarning and must not stop iteration.
	ListSessions(ctx context.Context, opt ListOpts) iter.Seq2[model.SessionRef, error]
	// Load parses one session fully.
	Load(ctx context.Context, ref model.SessionRef) (*model.RawSession, error)
}

// SkipWarning reports that an item was skipped because its shape was
// unknown or unreadable. Callers log it and carry on.
type SkipWarning struct {
	Path   string
	Reason string
}

func (w *SkipWarning) Error() string { return fmt.Sprintf("skipped %s: %s", w.Path, w.Reason) }

// IsSkip reports whether err is a SkipWarning.
func IsSkip(err error) bool {
	var w *SkipWarning
	return errors.As(err, &w)
}

// Registry holds adapters by name. The zero value is ready to use.
type Registry struct {
	byName map[string]Adapter
}

// Register adds an adapter; registering a name twice is an error.
func (r *Registry) Register(a Adapter) error {
	if r.byName == nil {
		r.byName = map[string]Adapter{}
	}
	if _, dup := r.byName[a.Name()]; dup {
		return fmt.Errorf("adapters: %q already registered", a.Name())
	}
	r.byName[a.Name()] = a
	return nil
}

// Get returns the named adapter.
func (r *Registry) Get(name string) (Adapter, bool) {
	a, ok := r.byName[name]
	return a, ok
}

// All returns adapters sorted by name.
func (r *Registry) All() []Adapter {
	out := make([]Adapter, 0, len(r.byName))
	for _, a := range r.byName {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
