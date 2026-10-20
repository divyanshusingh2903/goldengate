// Package redact scrubs secrets and local paths from text before it leaves
// the machine. Redaction is best-effort: it reduces risk, it does not
// guarantee that no secret survives. Callers run it before and after
// summarization.
package redact

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Counts maps a redaction kind to how many times it fired. It never holds
// the redacted values.
type Counts map[string]int

// Merge adds other into c.
func (c Counts) Merge(other Counts) {
	for k, v := range other {
		c[k] += v
	}
}

// Total is the number of redactions of all kinds.
func (c Counts) Total() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

// Options configures a Redactor.
type Options struct {
	ProjectRoot  string   // absolute paths under it become project-relative
	ExtraPattern []string // user regexes; every match is redacted
}

// Redactor is immutable after New and safe for concurrent use.
type Redactor struct {
	rules []rule
	opts  Options
}

type rule struct {
	kind     string
	re       *regexp.Regexp
	group    int                     // submatch to replace; 0 = whole match
	validate func(match string) bool // optional; false keeps the text
}

const marker = "[REDACTED:%s]"

// New compiles the built-in rules plus any user patterns.
func New(o Options) (*Redactor, error) {
	r := &Redactor{opts: o}
	r.rules = append(r.rules, builtinRules()...)
	for i, p := range o.ExtraPattern {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("redact: extra pattern %d: %w", i, err)
		}
		r.rules = append(r.rules, rule{kind: "custom", re: re})
	}
	return r, nil
}

func builtinRules() []rule {
	mk := func(kind, expr string, group int, v func(string) bool) rule {
		return rule{kind: kind, re: regexp.MustCompile(expr), group: group, validate: v}
	}
	return []rule{
		mk("private-key", `(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|\z)`, 0, nil),
		mk("connection-string", `(?i)\b[a-z][a-z0-9+.\-]{1,20}://[^\s:/@"']+:([^\s@/"']+)@`, 1, nil),
		mk("auth-header", `(?i)\bauthorization\s*:\s*(?:bearer|basic|token)?\s*([A-Za-z0-9._~+/=\-]{8,})`, 1, nil),
		mk("bearer-token", `(?i)\bbearer\s+([A-Za-z0-9._~+/=\-]{16,})`, 1, nil),
		mk("jwt", `\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`, 0, nil),
		mk("aws-access-key", `\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`, 0, nil),
		mk("github-token", `\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})\b`, 0, nil),
		mk("anthropic-key", `\bsk-ant-[A-Za-z0-9_\-]{16,}`, 0, nil),
		mk("openai-key", `\bsk-(?:proj-)?[A-Za-z0-9_\-]{32,}`, 0, nil),
		mk("slack-token", `\bxox[baprs]-[A-Za-z0-9\-]{10,}`, 0, nil),
		mk("google-api-key", `\bAIza[0-9A-Za-z_\-]{35}\b`, 0, nil),
		mk("stripe-key", `\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,}`, 0, nil),
		mk("npm-token", `\bnpm_[A-Za-z0-9]{30,}`, 0, nil),
		mk("goldengate-token", `\bggb_(?:read|ingest|admin)_[A-Za-z0-9_\-]{16,}`, 0, nil),
		mk("secret-assignment",
			`(?i)\b[A-Za-z0-9_.\-]*(?:password|passwd|secret|token|api[_\-]?key|apikey|access[_\-]?key|private[_\-]?key|credentials?)[A-Za-z0-9_.\-]*["']?\s*[:=]\s*("[^"\n]{6,}"|'[^'\n]{6,}'|[^\s"',;]{8,})`,
			1, validAssignment),
	}
}

// validAssignment keeps things like `token: string` or `password = nil`:
// quoted values and values mixing letters with digits or symbols are secrets.
func validAssignment(v string) bool {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		inner := v[1 : len(v)-1]
		return !strings.ContainsAny(inner, " ") || len(inner) >= 12
	}
	hasDigit, hasSym, hasLetter := false, false, false
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		default:
			hasSym = true
		}
	}
	if strings.ContainsAny(v, "(){}<>") { // looks like code, not a literal
		return false
	}
	return hasLetter && (hasDigit || hasSym)
}

var (
	entropyToken = regexp.MustCompile(`[A-Za-z0-9_\-+=]{28,}`)
	hexOnly      = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	uuidLike     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	unixPath     = regexp.MustCompile("(^|[\\s\"'`(=\\[,;])(/[\\w.@+\\-]+(?:/[\\w.@+\\-]+)+)")
	winPath      = regexp.MustCompile(`[A-Za-z]:\\(?:[\w.@+\- ]+\\)*[\w.@+\-]+`)
)

func looksHighEntropy(s string) bool {
	if hexOnly.MatchString(s) || uuidLike.MatchString(s) {
		return false // commit SHAs and ids are not secrets and are useful work-item keys
	}
	var lower, upper, digit bool
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	if !lower || !upper || !digit {
		return false
	}
	return shannon(s) >= 4.0
}

func shannon(s string) float64 {
	freq := map[rune]float64{}
	n := 0.0
	for _, r := range s {
		freq[r]++
		n++
	}
	h := 0.0
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

// String redacts one string and reports what fired.
func (r *Redactor) String(s string) (string, Counts) {
	counts := Counts{}
	if s == "" {
		return s, counts
	}
	for _, ru := range r.rules {
		s = applyRule(s, ru, counts)
	}
	s = entropyToken.ReplaceAllStringFunc(s, func(m string) string {
		if !looksHighEntropy(m) {
			return m
		}
		counts["high-entropy"]++
		return fmt.Sprintf(marker, "high-entropy")
	})
	s = r.scrubPaths(s, counts)
	return s, counts
}

func applyRule(s string, ru rule, counts Counts) string {
	idx := ru.re.FindAllStringSubmatchIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		start, end := m[2*ru.group], m[2*ru.group+1]
		if start < 0 {
			continue
		}
		if ru.validate != nil && !ru.validate(s[start:end]) {
			continue
		}
		b.WriteString(s[last:start])
		fmt.Fprintf(&b, marker, ru.kind)
		last = end
		counts[ru.kind]++
	}
	b.WriteString(s[last:])
	return b.String()
}

// scrubPaths rewrites absolute paths: inside the project root they become
// relative, in the home directory outside the project they are dropped, and
// everything else absolute is dropped too.
func (r *Redactor) scrubPaths(s string, counts Counts) string {
	s = unixPath.ReplaceAllStringFunc(s, func(m string) string {
		sub := unixPath.FindStringSubmatch(m)
		return sub[1] + r.mapPath(sub[2], counts)
	})
	return winPath.ReplaceAllStringFunc(s, func(m string) string {
		return r.mapPath(strings.ReplaceAll(m, `\`, "/"), counts)
	})
}

func (r *Redactor) mapPath(p string, counts Counts) string {
	if r.opts.ProjectRoot != "" {
		root := strings.ReplaceAll(strings.TrimSuffix(r.opts.ProjectRoot, "/"), `\`, "/")
		if p == root {
			return "."
		}
		if strings.HasPrefix(p, root+"/") {
			return p[len(root)+1:]
		}
	}
	counts["path"]++
	return fmt.Sprintf(marker, "path")
}

// Strings redacts a slice in place of copying callers' loops.
func (r *Redactor) Strings(in []string) ([]string, Counts) {
	counts := Counts{}
	out := make([]string, len(in))
	for i, s := range in {
		v, c := r.String(s)
		out[i] = v
		counts.Merge(c)
	}
	return out, counts
}

// IsSecretFile reports whether a file name matches the never-send patterns.
func IsSecretFile(name string) bool {
	base := strings.ToLower(path.Base(strings.ReplaceAll(name, `\`, "/")))
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return true
	case strings.HasSuffix(base, ".pem"), strings.HasSuffix(base, ".key"), strings.HasSuffix(base, ".p12"), strings.HasSuffix(base, ".pfx"):
		return true
	case strings.HasPrefix(base, "id_rsa"), strings.HasPrefix(base, "id_ed25519"), strings.HasPrefix(base, "id_ecdsa"):
		return true
	case strings.HasPrefix(base, "credentials"), base == ".netrc", base == ".npmrc":
		return true
	}
	return false
}

// SortedKinds returns count kinds in stable order, for display.
func (c Counts) SortedKinds() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
