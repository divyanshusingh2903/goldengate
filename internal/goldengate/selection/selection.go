// Package selection decides which projects are shared. The policy is
// default-deny and deny always beats allow.
package selection

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Policy is the user-facing selection configuration.
type Policy struct {
	Default        string   // "deny" (default) or "allow"
	Allow          []string // folders or globs; a plain folder covers its children
	Deny           []string
	IncludeSources []string // empty means all sources
	// SourceDeny excludes one source from a folder or chat project while the
	// rest of it stays shared. Entries look like "codex@~/code/app".
	SourceDeny []string
}

// Decision is the outcome for one (project, source) pair.
type Decision struct {
	Shared bool
	Reason string
}

// Matcher evaluates a Policy. It is immutable and safe for concurrent use.
type Matcher struct {
	defaultAllow    bool
	allow, deny     []rule
	sourceDeny      []sourceRule
	sources         []string
	caseInsensitive bool
	home            string
}

type sourceRule struct {
	source string
	rule
}

type rule struct {
	raw     string
	pattern string // folded, slash-separated, absolute (or the literal key for virtual rules)
	isGlob  bool
	virtual bool // matches chat-export keys like "claude-ai:Chats"; never folders
}

// Options tune a Matcher; the zero value uses the platform defaults.
type Options struct {
	Home            string
	CaseInsensitive *bool // nil = platform default
}

// New compiles a Policy.
func New(p Policy, o Options) (*Matcher, error) {
	m := &Matcher{home: o.Home, sources: p.IncludeSources}
	m.caseInsensitive = pathutil.CaseInsensitiveFS()
	if o.CaseInsensitive != nil {
		m.caseInsensitive = *o.CaseInsensitive
	}
	switch strings.ToLower(p.Default) {
	case "", "deny":
	case "allow":
		m.defaultAllow = true
	default:
		return nil, fmt.Errorf("selection: invalid default %q (want deny or allow)", p.Default)
	}
	var err error
	if m.allow, err = m.compile(p.Allow); err != nil {
		return nil, fmt.Errorf("selection: allow rules: %w", err)
	}
	if m.deny, err = m.compile(p.Deny); err != nil {
		return nil, fmt.Errorf("selection: deny rules: %w", err)
	}
	for _, sd := range p.SourceDeny {
		src, pat, ok := SplitSourceRule(sd)
		if !ok {
			return nil, fmt.Errorf("selection: source rule %q must look like source@path", sd)
		}
		rs, err := m.compile([]string{pat})
		if err != nil {
			return nil, fmt.Errorf("selection: source rule %q: %w", sd, err)
		}
		if len(rs) == 1 {
			m.sourceDeny = append(m.sourceDeny, sourceRule{source: src, rule: rs[0]})
		}
	}
	return m, nil
}

func (m *Matcher) compile(raw []string) ([]rule, error) {
	out := make([]rule, 0, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if pathutil.IsVirtualKey(r) {
			if !doublestar.ValidatePattern(r) {
				return nil, fmt.Errorf("invalid pattern %q", r)
			}
			out = append(out, rule{raw: r, pattern: r, isGlob: strings.ContainsAny(r, "*?[{"), virtual: true})
			continue
		}
		expanded := pathutil.ExpandHome(r, m.home)
		isGlob := strings.ContainsAny(expanded, "*?[{")
		var pat string
		if isGlob {
			pat = normalizeGlob(expanded)
		} else {
			pat = pathutil.Normalize(expanded)
		}
		pat = pathutil.Fold(pat, m.caseInsensitive)
		pat = strings.TrimSuffix(pat, "/")
		if !doublestar.ValidatePattern(pat) {
			return nil, fmt.Errorf("invalid pattern %q", r)
		}
		out = append(out, rule{raw: r, pattern: pat, isGlob: isGlob})
	}
	return out, nil
}

// normalizeGlob resolves symlinks in the literal directory prefix of a glob.
func normalizeGlob(g string) string {
	idx := strings.IndexAny(g, "*?[{")
	cut := strings.LastIndexAny(g[:idx], `/\`)
	if cut <= 0 {
		return g
	}
	return pathutil.Normalize(g[:cut]) + "/" + strings.TrimLeft(g[cut+1:], `/\`)
}

func (r rule) matches(path string) bool {
	if r.virtual {
		if !r.isGlob {
			return path == r.pattern
		}
		ok, _ := doublestar.Match(r.pattern, path)
		return ok
	}
	if !r.isGlob {
		return pathutil.Within(path, r.pattern)
	}
	if ok, _ := doublestar.Match(r.pattern, path); ok {
		return true
	}
	// A glob that names a folder also covers everything beneath it.
	ok, _ := doublestar.Match(r.pattern+"/**", path)
	return ok
}

// Decide evaluates one project (absolute folder path) for one source.
func (m *Matcher) Decide(projectKey, source string) Decision {
	if len(m.sources) > 0 && source != "" && !slices.Contains(m.sources, source) {
		return Decision{Reason: "source not included"}
	}
	virtual := pathutil.IsVirtualKey(projectKey)
	path := projectKey
	if !virtual {
		path = pathutil.Fold(pathutil.Normalize(projectKey), m.caseInsensitive)
	}
	for _, r := range m.deny {
		if r.virtual == virtual && r.matches(path) {
			return Decision{Reason: "denied by " + r.raw}
		}
	}
	for _, r := range m.sourceDeny {
		if source != "" && r.source == source && r.virtual == virtual && r.matches(path) {
			return Decision{Reason: source + " excluded by " + r.raw}
		}
	}
	for _, r := range m.allow {
		if r.virtual == virtual && r.matches(path) {
			return Decision{Shared: true, Reason: "allowed by " + r.raw}
		}
	}
	if virtual {
		// Chat history is personal: it is shared only by an explicit rule, even when default = "allow".
		return Decision{Reason: "chat projects need an explicit allow rule"}
	}
	if m.defaultAllow {
		return Decision{Shared: true, Reason: "default allow"}
	}
	return Decision{Reason: "not selected"}
}

// Denied reports whether an explicit deny rule matches, regardless of allow.
func (m *Matcher) Denied(projectKey string) bool {
	virtual := pathutil.IsVirtualKey(projectKey)
	path := projectKey
	if !virtual {
		path = pathutil.Fold(pathutil.Normalize(projectKey), m.caseInsensitive)
	}
	for _, r := range m.deny {
		if r.virtual == virtual && r.matches(path) {
			return true
		}
	}
	return false
}

// SplitSourceRule parses "source@pattern".
func SplitSourceRule(s string) (source, pattern string, ok bool) {
	source, pattern, ok = strings.Cut(s, "@")
	if !ok || source == "" || pattern == "" {
		return "", "", false
	}
	for _, r := range source {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "", "", false
		}
	}
	return source, pattern, true
}

// SourceRule formats "source@pattern".
func SourceRule(source, pattern string) string { return source + "@" + pattern }
