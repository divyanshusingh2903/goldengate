// Package adapters defines the interface every source tool implements and a
// registry to look them up. Adapters are read-only: they never modify the
// source tool's files.
package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"
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

// issueToolSuffixes are the base names of issue-tracker MCP tools whose results
// can prove an issue was created or changed (Linear, GitHub). Which tracker a
// result belongs to is decided from the result's shape, not from the server name.
var issueToolSuffixes = []string{"save_issue", "get_issue", "issue_write", "issue_read", "create_issue", "update_issue"}

// IsIssueTool reports whether a tool name (Claude Code's mcp__<server>__<tool>,
// OpenCode's <server>_<tool>, or a bare name) is an issue-tracker tool.
func IsIssueTool(name string) bool {
	n := strings.ToLower(name)
	for _, s := range issueToolSuffixes {
		if n == s || strings.HasSuffix(n, "__"+s) || strings.HasSuffix(n, "_"+s) {
			return true
		}
	}
	return false
}

// issueInputKeys are the only tool-input fields kept for issue-tracker calls:
// enough to tell create from update and to identify the issue. Descriptions,
// bodies and comments are dropped at the door.
var issueInputKeys = map[string]bool{"id": true, "method": true, "owner": true, "repo": true, "issue_number": true,
	"state": true, "state_reason": true, "title": true, "team": true}

// TrimIssueInput keeps only the identifying fields of an issue tool's input.
func TrimIssueInput(raw []byte) []byte {
	var in map[string]json.RawMessage
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	out := map[string]json.RawMessage{}
	for k, v := range in {
		if issueInputKeys[k] {
			out[k] = v
		}
	}
	b, _ := json.Marshal(out)
	return b
}
