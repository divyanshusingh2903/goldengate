// Package rules holds the rules a user writes to automate approval inside a
// project, and the trust level that says how far a project's automation goes.
//
// A rule matches on deterministic facts about a session only: which tool it
// came from, its folder or chat grouping, the repository remote, the git
// branch, and the keys of the issues and pull requests it touched. Nothing a
// model wrote (summaries, narratives, titles) is ever part of Facts, so model
// output cannot steer what is sent.
//
// Rules act only on sessions that are already in a shared project: whether a
// folder is shared at all is decided before a session is read, by the
// selection rules, where deny always wins.
package rules

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Trust levels of a project.
const (
	// TrustAsk: every new and changed session waits in the inbox.
	TrustAsk = "ask"
	// TrustSuggest: sessions the rules would approve are marked in the inbox
	// and can be approved together; nothing is sent without that.
	TrustSuggest = "suggest"
	// TrustAuto: sessions the rules approve are sent on the next sync.
	TrustAuto = "auto"
)

// Rule actions.
const (
	ActionApprove = "approve"
	ActionExclude = "exclude"
)

// ValidTrust reports whether t is a trust level. Empty means TrustAsk.
func ValidTrust(t string) bool {
	return t == "" || t == TrustAsk || t == TrustSuggest || t == TrustAuto
}

// Rule approves or excludes the sessions of its project that match every
// condition it sets. A rule with no condition matches every session of the
// project.
type Rule struct {
	ID     string `toml:"id"`
	Action string `toml:"action"`
	// Source is a tool: "codex", "claude-code", "chatgpt".
	Source string `toml:"source,omitempty"`
	// Folder is a folder (it covers what is under it), a glob, or a chat
	// grouping such as "chatgpt:Work" or "chatgpt:*".
	Folder string `toml:"folder,omitempty"`
	// Remote is a pattern for the repository remote, written without scheme
	// or credentials: "github.com/acme/*". * matches any run of characters.
	Remote string `toml:"remote,omitempty"`
	// Branch is a pattern for the git branch: "fix/*".
	Branch string `toml:"branch,omitempty"`
	// WorkItem is a work-item key prefix: "linear:ENG" or "ENG" matches
	// linear:ENG-12; "acme/app" matches acme/app#7.
	WorkItem string `toml:"work_item,omitempty"`
}

// Facts are the deterministic properties of one session that rules see.
type Facts struct {
	Source string
	Folder string // the session's folder (absolute) or chat grouping key
	Remote string // as recorded by the tool
	Branch string
	// WorkItems are the keys of the issues and pull requests the local rules
	// extracted from the session.
	WorkItems []string
}

// NewID returns a fresh rule id.
func NewID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic("rules: system randomness unavailable: " + err.Error())
	}
	return "r_" + hex.EncodeToString(b)
}

// Validate checks a rule as written.
func (r Rule) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("rule: id is empty")
	}
	if r.Action != ActionApprove && r.Action != ActionExclude {
		return fmt.Errorf("rule %s: action must be approve or exclude, got %q", r.ID, r.Action)
	}
	for name, v := range map[string]string{"source": r.Source, "folder": r.Folder, "remote": r.Remote, "branch": r.Branch, "work_item": r.WorkItem} {
		if v != strings.TrimSpace(v) || strings.ContainsAny(v, "\n\r\x00") {
			return fmt.Errorf("rule %s: %s has stray whitespace", r.ID, name)
		}
	}
	if r.Folder != "" && !doublestar.ValidatePattern(strings.ReplaceAll(r.Folder, `\`, "/")) {
		return fmt.Errorf("rule %s: folder %q is not a valid pattern", r.ID, r.Folder)
	}
	return nil
}

// Unconditional reports whether the rule matches every session of its project.
func (r Rule) Unconditional() bool {
	return r.Source == "" && r.Folder == "" && r.Remote == "" && r.Branch == "" && r.WorkItem == ""
}

// Match reports whether every condition the rule sets holds for f. home
// expands a leading ~ in Folder.
func (r Rule) Match(f Facts, home string) bool {
	if r.Source != "" && r.Source != f.Source {
		return false
	}
	if r.Folder != "" && !folderMatch(r.Folder, f.Folder, home) {
		return false
	}
	if r.Remote != "" && (f.Remote == "" || !wildcard(strings.ToLower(NormalizeRemote(r.Remote)), strings.ToLower(NormalizeRemote(f.Remote)))) {
		return false
	}
	if r.Branch != "" && (f.Branch == "" || !wildcard(r.Branch, f.Branch)) {
		return false
	}
	if r.WorkItem != "" && !slices.ContainsFunc(f.WorkItems, func(k string) bool { return KeyHasPrefix(k, r.WorkItem) }) {
		return false
	}
	return true
}

// String describes the rule in words.
func (r Rule) String() string {
	var c []string
	add := func(what, v string) {
		if v != "" {
			c = append(c, what+" "+v)
		}
	}
	add("from", r.Source)
	add("in", r.Folder)
	add("with remote", r.Remote)
	add("on branch", r.Branch)
	add("touching", r.WorkItem)
	what := "every session"
	if len(c) > 0 {
		what = "sessions " + strings.Join(c, ", ")
	}
	if r.Action == ActionExclude {
		return "exclude " + what
	}
	return "approve " + what
}

// Verdict is what a project's rules say about one session.
type Verdict struct {
	// Exclude is the exclude rule that matched, if any. It wins over Approve.
	Exclude *Rule
	// Approve is the first approve rule that matched. In a project with no
	// approve rule at all, every session counts as matched (Approve is nil
	// and All is set): the trust level then covers the whole project.
	Approve *Rule
	All     bool
}

// Approved reports whether the rules approve the session.
func (v Verdict) Approved() bool { return v.Exclude == nil && (v.Approve != nil || v.All) }

// RuleID is the id of the rule behind the verdict, or "" for the whole
// project.
func (v Verdict) RuleID() string {
	switch {
	case v.Exclude != nil:
		return v.Exclude.ID
	case v.Approve != nil:
		return v.Approve.ID
	}
	return ""
}

// Evaluate applies a project's rules to one session.
func Evaluate(rs []Rule, f Facts, home string) Verdict {
	var v Verdict
	approvers := 0
	for i := range rs {
		r := &rs[i]
		switch r.Action {
		case ActionExclude:
			if v.Exclude == nil && r.Match(f, home) {
				v.Exclude = r
			}
		case ActionApprove:
			approvers++
			if v.Approve == nil && r.Match(f, home) {
				v.Approve = r
			}
		}
	}
	v.All = approvers == 0
	return v
}

// folderMatch matches a session's folder or chat grouping against a rule's
// folder: a plain folder covers what is under it, a glob matches by pattern
// (and covers what is under a folder it names), a chat key matches exactly
// or by pattern.
func folderMatch(pattern, key, home string) bool {
	if key == "" {
		return false
	}
	glob := strings.ContainsAny(pattern, "*?[{")
	if pathutil.IsVirtualKey(pattern) || pathutil.IsVirtualKey(key) {
		if pathutil.IsVirtualKey(pattern) != pathutil.IsVirtualKey(key) {
			return false
		}
		if !glob {
			return pattern == key
		}
		ok, _ := doublestar.Match(pattern, key)
		return ok
	}
	pat := strings.ReplaceAll(pathutil.ExpandHome(pattern, home), `\`, "/")
	key = strings.ReplaceAll(key, `\`, "/")
	if !glob {
		return pathutil.Within(key, pathutil.Normalize(pat))
	}
	if ok, _ := doublestar.Match(pat, key); ok {
		return true
	}
	ok, _ := doublestar.Match(pat+"/**", key)
	return ok
}

// NormalizeRemote reduces a git remote to host/path: no scheme, no
// credentials, no trailing ".git". "git@github.com:acme/app.git" and
// "https://github.com/acme/app" both become "github.com/acme/app".
func NormalizeRemote(r string) string {
	r = strings.TrimSpace(r)
	if i := strings.Index(r, "://"); i >= 0 {
		r = r[i+3:]
	}
	if i := strings.LastIndex(r, "@"); i >= 0 && !strings.Contains(r[:i], "/") {
		r = r[i+1:]
	}
	if i := strings.Index(r, ":"); i >= 0 && !strings.Contains(r[:i], "/") {
		// scp form host:path, or host:port/path
		rest := r[i+1:]
		if j := strings.Index(rest, "/"); j > 0 && isDigits(rest[:j]) {
			rest = rest[j+1:]
		}
		r = r[:i] + "/" + rest
	}
	return strings.TrimSuffix(strings.TrimSuffix(r, "/"), ".git")
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// KeyHasPrefix reports whether a work-item key belongs under a prefix: the
// key is the prefix itself or continues with -, # or !. A prefix without a
// provider ("ENG") also matches a key that has one ("linear:ENG-12").
func KeyHasPrefix(key, prefix string) bool {
	under := func(k string) bool {
		if !strings.HasPrefix(k, prefix) {
			return false
		}
		return len(k) == len(prefix) || strings.ContainsRune("-#!", rune(k[len(prefix)]))
	}
	if prefix == "" {
		return false
	}
	if under(key) {
		return true
	}
	_, bare, ok := strings.Cut(key, ":")
	return ok && !strings.Contains(prefix, ":") && under(bare)
}

// wildcard matches s against a pattern in which * is any run of characters
// (including /) and ? is any one character. Everything else is literal.
func wildcard(pattern, s string) bool {
	p, n := []rune(pattern), []rune(s)
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(n) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == n[si]):
			pi, si = pi+1, si+1
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			mark++
			pi, si = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
