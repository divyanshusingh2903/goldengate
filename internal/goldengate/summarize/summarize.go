// Package summarize turns a parsed session into the payload that may leave
// the machine. It is fully local and heuristic. The pipeline is
// redact -> summarize -> redact again -> cap sizes -> hash, because a summary
// can re-emit text that the first pass already saw in a different shape.
package summarize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
	"github.com/divyanshusingh2903/goldengate/internal/utils/redact"
	"github.com/divyanshusingh2903/goldengate/internal/utils/shellcmd"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

// Size caps. A summary stays under MaxSummaryBytes so MCP responses are small.
const (
	MaxSummaryBytes  = 2048
	MaxWorkItems     = 50
	MaxWorkItemBytes = 8 << 10
	maxFiles         = 20
	maxCommands      = 12
	maxTools         = 12
	maxTitle         = 120
	maxGoal          = 300
	maxLast          = 400
	maxEvidenceSent  = 3
)

// ErrLevelUnsupported is returned for levels this build cannot produce.
var ErrLevelUnsupported = errors.New("summarize: share level not supported in this build")

// Options configures Build.
type Options struct {
	Level string // titles-only | summary (full is not implemented yet)
	Home  string // used to scrub the project key; "" = no scrub
	Work  workitems.Options
	// ProjectKey, when set, replaces the session's folder as the project the
	// payload is filed under (a repository root for a worktree session).
	ProjectKey string
	// ExtraRedactions are user regexes added to the built-in rules.
	ExtraRedactions []string
}

// Build produces the payload for one session. It never mutates s.
func Build(s *model.RawSession, o Options) (*model.SessionPayload, error) {
	level := o.Level
	if level == "" {
		level = model.LevelSummary
	}
	if level != model.LevelSummary && level != model.LevelTitlesOnly {
		return nil, fmt.Errorf("%w: %q", ErrLevelUnsupported, level)
	}

	// The redactor must be scoped to this project so absolute paths inside it
	// become relative and every other absolute path is dropped.
	rd, err := redact.New(redact.Options{ProjectRoot: s.Ref.ProjectKey, ExtraPattern: o.ExtraRedactions})
	if err != nil {
		return nil, fmt.Errorf("summarize: %w", err)
	}
	r := &scoped{rd: rd, counts: redact.Counts{}}

	p := &model.SessionPayload{
		Source:    s.Ref.Source,
		SessionID: s.Ref.ID,
		Project:   pathutil.Scrub(firstNonEmpty(o.ProjectKey, s.Ref.ProjectKey), o.Home),
		Level:     level,
	}
	p.StartedAt, p.UpdatedAt = activityWindow(s)

	title := r.str(deriveTitle(s), maxTitle)
	if level == model.LevelTitlesOnly {
		p.TitleOnly = title
	} else {
		sum := heuristic(s, r)
		sum.Title = title
		capSummary(sum)
		p.Summary = sum
		p.WorkItems = workItems(s, o.Work, r)
	}
	p.Redactions = model.Redactions(r.counts)
	h, err := hash(p)
	if err != nil {
		return nil, err
	}
	p.ContentHash = h
	return p, nil
}

// scoped applies the project-scoped redactor, truncates, then redacts the
// truncated text again: truncation can cut a marker or reveal a fragment, and
// this is the "redact again" step of the pipeline.
type scoped struct {
	rd     *redact.Redactor
	counts redact.Counts
}

func (r *scoped) str(s string, max int) string {
	s, c := r.rd.String(s)
	r.counts.Merge(c)
	s = truncate(oneLine(s), max)
	s, c2 := r.rd.String(s)
	r.counts.Merge(c2)
	return s
}

var ws = regexp.MustCompile(`\s+`)

func oneLine(s string) string { return strings.TrimSpace(ws.ReplaceAllString(s, " ")) }

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	rs := []rune(s)
	return string(rs[:max-1]) + "…"
}

func deriveTitle(s *model.RawSession) string {
	if s.Title != "" {
		return s.Title
	}
	if p := firstPrompt(s); p != "" {
		line, _, _ := strings.Cut(strings.TrimSpace(p), "\n")
		return line
	}
	return "(untitled session)"
}

func firstPrompt(s *model.RawSession) string {
	for _, t := range s.Turns {
		if t.Role == "user" && strings.TrimSpace(t.Text) != "" {
			return t.Text
		}
	}
	return ""
}

func heuristic(s *model.RawSession, r *scoped) *model.Summary {
	sum := &model.Summary{GitBranch: r.str(s.GitBranch, 80)}
	if g := firstPrompt(s); g != "" {
		sum.Goal = r.str(g, maxGoal)
	}
	for i := len(s.Turns) - 1; i >= 0; i-- {
		t := s.Turns[i]
		if t.Role == "assistant" && strings.TrimSpace(t.LastText) != "" {
			sum.LastAssistant = r.str(t.LastText, maxLast)
			break
		}
	}
	for _, t := range s.Turns {
		if t.Role == "user" {
			sum.TurnCount++
		}
	}
	sum.DurationSec = duration(s)

	for _, f := range s.FilesTouched {
		if redact.IsSecretFile(f) || filepath.IsAbs(f) {
			continue
		}
		if len(sum.FilesTouched) >= maxFiles {
			break
		}
		sum.FilesTouched = append(sum.FilesTouched, r.str(f, 120))
	}
	sum.ToolsUsed = topTools(s.ToolsUsed)
	sum.Commands = commandNames(s)
	sum.Outcome = outcome(s)
	return sum
}

// activityWindow is derived from the conversation's own timestamps, not from
// file mtimes, so bookkeeping events appended to a log do not change the
// payload (and therefore do not trigger a re-push). It falls back to the
// file-level times when the session has no timestamped turns.
func activityWindow(s *model.RawSession) (first, last time.Time) {
	for _, t := range s.Turns {
		if t.Time.IsZero() {
			continue
		}
		if first.IsZero() || t.Time.Before(first) {
			first = t.Time
		}
		if t.Time.After(last) {
			last = t.Time
		}
	}
	if first.IsZero() {
		first, last = s.Ref.StartedAt, s.Ref.UpdatedAt
	}
	return first.UTC(), last.UTC()
}

func duration(s *model.RawSession) int64 {
	first, last := activityWindow(s)
	if first.IsZero() || last.Before(first) {
		return 0
	}
	return int64(last.Sub(first).Seconds())
}

// topTools keeps the most-used tools; MCP tool names lose their server id.
func topTools(in map[string]int) map[string]int {
	type kv struct {
		k string
		n int
	}
	agg := map[string]int{}
	for k, n := range in {
		if strings.HasPrefix(k, "mcp__") {
			parts := strings.SplitN(k, "__", 3)
			k = "mcp:" + parts[len(parts)-1]
		}
		agg[k] += n
	}
	list := make([]kv, 0, len(agg))
	for k, n := range agg {
		list = append(list, kv{k, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].k < list[j].k
	})
	out := map[string]int{}
	for i, e := range list {
		if i >= maxTools {
			break
		}
		out[truncate(e.k, 40)] = e.n
	}
	return out
}

var subcommandTools = map[string]bool{"git": true, "gh": true, "go": true, "npm": true, "pnpm": true, "yarn": true, "cargo": true, "docker": true, "kubectl": true, "pip": true, "terraform": true, "gcloud": true}

// trivial builtins add noise, not information.
var trivialCommands = map[string]bool{"cd": true, "echo": true, "printf": true, "export": true, "set": true, "source": true, "true": true, "false": true, "test": true, "[": true, "[[": true, "pwd": true, ":": true, "exit": true, "return": true}

// commandNames returns "git commit"-style names, never arguments. Only real
// invocations count: heredoc bodies, quoted text and substitutions do not.
func commandNames(s *model.RawSession) []string {
	count := map[string]int{}
	first := map[string]int{}
	n := 0
	for _, t := range s.Turns {
		for _, c := range t.Tools {
			if c.Kind != model.ToolShell {
				continue
			}
			for _, inv := range shellcmd.Parse(c.Command) {
				name := filepath.Base(inv.Name())
				if !validName(name) || trivialCommands[name] {
					continue
				}
				if subcommandTools[name] {
					if sub := subcommand(inv); sub != "" {
						name += " " + sub
					}
				}
				if _, ok := first[name]; !ok {
					first[name] = n
					n++
				}
				count[name]++
			}
		}
	}
	names := make([]string, 0, len(count))
	for k := range count {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if count[names[i]] != count[names[j]] {
			return count[names[i]] > count[names[j]]
		}
		return first[names[i]] < first[names[j]]
	})
	if len(names) > maxCommands {
		names = names[:maxCommands]
	}
	return names
}

// subcommand is the first non-flag word after the program, if it looks like a name.
func subcommand(inv shellcmd.Invocation) string {
	if sub, _, ok := shellcmd.GitSubcommand(inv); ok {
		if validName(sub) {
			return sub
		}
		return ""
	}
	for _, w := range inv.Words[1:] {
		if strings.HasPrefix(w, "-") {
			continue
		}
		if validName(w) {
			return w
		}
		return ""
	}
	return ""
}

var reName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._+-]{0,30}$`)

func validName(s string) bool { return reName.MatchString(s) }

var reTestCmd = regexp.MustCompile(`^(go test|npm (?:run )?test|pnpm test|yarn test|pytest|cargo test|make test|jest|vitest)\b`)

func outcome(s *model.RawSession) string {
	var parts []string
	var lastTest *bool
	commit, pr := false, false
	for _, t := range s.Turns {
		for _, c := range t.Tools {
			if c.Kind != model.ToolShell || !c.Result.Known {
				continue
			}
			for _, inv := range shellcmd.Parse(c.Command) {
				switch {
				case reTestCmd.MatchString(strings.Join(inv.Words, " ")):
					ok := c.Result.OK
					lastTest = &ok
				case c.Result.OK && isGit(inv, "commit"):
					commit = true
				case c.Result.OK && inv.Name() == "gh" && len(inv.Words) > 2 && inv.Words[1] == "pr" && inv.Words[2] == "create":
					pr = true
				}
			}
		}
	}
	if lastTest != nil {
		if *lastTest {
			parts = append(parts, "tests passed")
		} else {
			parts = append(parts, "tests failing")
		}
	}
	if commit {
		parts = append(parts, "commit made")
	}
	if pr {
		parts = append(parts, "PR opened")
	}
	return strings.Join(parts, ", ")
}

func isGit(inv shellcmd.Invocation, sub string) bool {
	got, _, ok := shellcmd.GitSubcommand(inv)
	return ok && got == sub
}

// capSummary shrinks the summary until its JSON fits MaxSummaryBytes.
func capSummary(sum *model.Summary) {
	size := func() int { b, _ := json.Marshal(sum); return len(b) }
	steps := []func(){
		func() { sum.LastAssistant = truncate(sum.LastAssistant, 200) },
		func() { sum.Goal = truncate(sum.Goal, 160) },
		func() { sum.FilesTouched = head(sum.FilesTouched, 10) },
		func() { sum.Commands = head(sum.Commands, 6) },
		func() { sum.LastAssistant = truncate(sum.LastAssistant, 80) },
		func() { sum.FilesTouched = head(sum.FilesTouched, 4) },
		func() { sum.Goal = "" },
		func() { sum.LastAssistant = "" },
		func() { sum.FilesTouched = nil },
		func() { sum.Commands = nil },
		func() { sum.ToolsUsed = nil },
		func() { sum.Title = truncate(sum.Title, 60) },
	}
	for _, step := range steps {
		if size() <= MaxSummaryBytes {
			return
		}
		step()
	}
}

func head[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func workItems(s *model.RawSession, wo workitems.Options, r *scoped) []model.WorkItem {
	items := workitems.Extract(s, wo)
	out := make([]model.WorkItem, 0, len(items))
	for _, it := range items {
		if len(out) >= MaxWorkItems {
			break
		}
		c := model.WorkItem{
			Kind: it.Kind, Provider: it.Provider, Status: it.Status, ObservedAt: it.ObservedAt.UTC(),
			Key:      r.str(it.Key, 160),
			Title:    r.str(it.Title, maxTitle),
			URL:      r.str(stripURLCreds(it.URL), 200),
			RawState: r.str(it.RawState, 40),
			Evidence: head(it.Evidence, maxEvidenceSent),
		}
		for i := range c.Evidence {
			c.Evidence[i].At = c.Evidence[i].At.UTC()
		}
		out = append(out, c)
		if b, _ := json.Marshal(out); len(b) > MaxWorkItemBytes {
			return out[:len(out)-1]
		}
	}
	return out
}

var reURLCreds = regexp.MustCompile(`^([a-z][a-z0-9+.\-]*://)[^/@\s]*@`)

func stripURLCreds(u string) string { return reURLCreds.ReplaceAllString(u, "$1") }

// hash is the idempotency key: it covers everything that would be sent.
func hash(p *model.SessionPayload) (string, error) {
	cp := *p
	cp.ContentHash = ""
	cp.Redactions = nil
	b, err := json.Marshal(cp)
	if err != nil {
		return "", fmt.Errorf("summarize: hash: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
