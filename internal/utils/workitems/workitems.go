// Package workitems extracts structured work items (commits, pull requests,
// issues, files) from a parsed session.
//
// Rule of the package: a status above "mentioned" may only come from a
// successful tool result (or an explicit harness event that proves it). Text
// in the conversation can only ever produce "mentioned".
package workitems

import (
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
	"github.com/divyanshusingh2903/goldengate/internal/utils/redact"
	"github.com/divyanshusingh2903/goldengate/internal/utils/shellcmd"
)

const maxEvidence = 10

// Options configures extraction.
type Options struct {
	// Providers lists enabled issue trackers ("linear", "jira", "github").
	// nil enables all; an empty non-nil slice disables issue extraction.
	Providers []string
	// KnownPrefixes are tracker key prefixes (e.g. "ENG") accepted as bare
	// keys in text. Prefixes learned from URLs or tool results in the same
	// session are accepted too.
	KnownPrefixes []string
}

func (o Options) enabled(p string) bool {
	return o.Providers == nil || slices.Contains(o.Providers, p)
}

// falsePrefixes are common "LETTERS-digits" strings that are not issue keys.
var falsePrefixes = map[string]bool{
	"UTF": true, "SHA": true, "ISO": true, "CVE": true, "RFC": true, "HTTP": true, "TLS": true,
	"AES": true, "MD": true, "GPT": true, "PEP": true, "WCAG": true, "ES": true, "IPV": true,
	"X": true, "TCP": true, "UDP": true, "SSL": true, "CP": true, "GB": true, "KB": true, "MB": true,
	"ECMA": true, "OAUTH": true, "COVID": true, "HS": true, "RS": true, "ES256": true,
}

var (
	reCommitOut  = regexp.MustCompile(`(?m)^\[[^\]\n]*?\s([0-9a-f]{7,40})\]\s*(.*)$`)
	rePRURL      = regexp.MustCompile(`https://github\.com/([\w.\-]+/[\w.\-]+)/pull/(\d+)`)
	reGHIssueURL = regexp.MustCompile(`https://github\.com/([\w.\-]+/[\w.\-]+)/issues/(\d+)`)
	reLinearURL  = regexp.MustCompile(`https://linear\.app/([\w.\-]+)/issue/([A-Z][A-Z0-9]{1,9})-(\d+)`)
	reJiraURL    = regexp.MustCompile(`https://([\w.\-]+)\.atlassian\.net/(?:browse/|[^\s]*selectedIssue=)([A-Z][A-Z0-9]{1,9})-(\d+)`)
	reCommitURL  = regexp.MustCompile(`https://github\.com/[\w.\-]+/[\w.\-]+/commit/([0-9a-f]{7,40})`)
	reBareKey    = regexp.MustCompile(`\b([A-Z][A-Z0-9]{1,9})-(\d+)\b`)
	rePRRef      = regexp.MustCompile(`(?i)\b(?:pr|pull request|pull)\s*#(\d+)`)
	reIssueRef   = regexp.MustCompile(`(?i)\bissue\s*#(\d+)`)
	reSHACtx     = regexp.MustCompile("(?i)(?:commit|sha|cherry-pick|revert|reverted|amend)\\w*\\s*[:`(]?\\s*`?([0-9a-f]{7,40})\\b|`([0-9a-f]{7,40})`")
	reMergedOut  = regexp.MustCompile(`(?i)pull request\s+(?:([\w.\-]+/[\w.\-]+))?#(\d+)`)
)

type extractor struct {
	s      *model.RawSession
	o      Options
	items  map[string]*model.WorkItem
	order  []string
	repo   string // owner/repo seen in session, or project basename
	known  map[string]bool
	lastPR string // key of most recent PR touched by evidence
}

// Extract returns the session's work items, deduplicated within the session.
func Extract(s *model.RawSession, o Options) []model.WorkItem {
	e := &extractor{s: s, o: o, items: map[string]*model.WorkItem{}, known: map[string]bool{}}
	for _, p := range o.KnownPrefixes {
		e.known[strings.ToUpper(p)] = true
	}
	e.repo = e.inferRepo()
	e.learnPrefixes()

	for ti, t := range s.Turns {
		for _, c := range t.Tools {
			e.fromTool(ti, c, t.Time)
		}
		if t.Text != "" {
			e.fromText(ti, t)
		}
	}
	for _, ev := range s.Events {
		if ev.Kind == "pr_link" && ev.Repo != "" && ev.Number > 0 {
			// pr-link also fires for PRs a session merely viewed or checked out
			// (observed without any `gh pr create`), so it proves association,
			// not creation: status stays "mentioned".
			e.touch(model.KindPR, "github", ev.Repo+"#"+strconv.Itoa(ev.Number), "", ev.URL, model.StatusMentioned, "", -1, model.ViaHarnessEvent, ev.Time)
		}
	}
	out := make([]model.WorkItem, 0, len(e.order))
	for _, k := range e.order {
		out = append(out, *e.items[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ObservedAt.After(out[j].ObservedAt) })
	return out
}

func (e *extractor) inferRepo() string {
	count := map[string]int{}
	scan := func(txt string) {
		for _, m := range rePRURL.FindAllStringSubmatch(txt, -1) {
			count[m[1]]++
		}
	}
	for _, t := range e.s.Turns {
		scan(t.Text)
		for _, c := range t.Tools {
			scan(c.Result.Text)
		}
	}
	for _, ev := range e.s.Events {
		if ev.Repo != "" {
			count[ev.Repo] += 2
		}
	}
	best, n := "", 0
	for r, c := range count {
		if c > n || (c == n && r < best) {
			best, n = r, c
		}
	}
	if best != "" {
		return best
	}
	if pathutil.IsVirtualKey(e.s.Ref.ProjectKey) {
		return "chat" // chat exports have no repository to name
	}
	return filepath.Base(e.s.Ref.ProjectKey)
}

// learnPrefixes collects tracker key prefixes proven by URLs or tool results.
func (e *extractor) learnPrefixes() {
	learn := func(txt string) {
		for _, m := range reLinearURL.FindAllStringSubmatch(txt, -1) {
			e.known[m[2]] = true
		}
		for _, m := range reJiraURL.FindAllStringSubmatch(txt, -1) {
			e.known[m[2]] = true
		}
	}
	for _, t := range e.s.Turns {
		learn(t.Text)
		for _, c := range t.Tools {
			learn(c.Result.Text)
			if c.Kind == model.ToolIssue {
				if r := parseLinear(c.Result.Text); r != nil {
					if p, _, ok := splitKey(r.ID); ok {
						e.known[p] = true
					}
				}
			}
		}
	}
}

func splitKey(k string) (prefix, num string, ok bool) {
	i := strings.LastIndex(k, "-")
	if i <= 0 {
		return "", "", false
	}
	return k[:i], k[i+1:], true
}

func evidenceOK(c model.ToolCall) bool { return c.Result.Known && c.Result.OK }

func (e *extractor) fromTool(ti int, c model.ToolCall, turnTime time.Time) {
	at := c.Time
	if at.IsZero() {
		at = turnTime
	}
	switch c.Kind {
	case model.ToolShell:
		if !evidenceOK(c) {
			return // a failed or unfinished command is not evidence of anything
		}
		e.fromShell(ti, c, at)
	case model.ToolFileWrite, model.ToolFileEdit:
		if !evidenceOK(c) || c.Path == "" {
			return
		}
		rel, ok := relPath(e.s.Ref.ProjectKey, c.Path)
		if !ok || redact.IsSecretFile(rel) {
			return
		}
		st := model.StatusUpdated
		if c.Kind == model.ToolFileWrite && c.Created {
			st = model.StatusCreated
		}
		e.touch(model.KindFile, "", rel, path.Base(rel), "", st, "", ti, model.ViaToolResult, at)
	case model.ToolIssue:
		if evidenceOK(c) {
			e.fromIssueTool(ti, c, at)
		}
	case model.ToolArtifact:
		if evidenceOK(c) {
			e.fromArtifact(ti, c, at)
		}
	}
}

func relPath(root, p string) (string, bool) {
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Clean(p)), true
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (e *extractor) fromShell(ti int, c model.ToolCall, at time.Time) {
	out := c.Result.Text
	commits := reCommitOut.FindAllStringSubmatch(out, -1)
	next := 0
	for _, inv := range shellcmd.Parse(c.Command) {
		if sub, args, ok := shellcmd.GitSubcommand(inv); ok {
			if sub == "commit" && !shellcmd.Has(args, "--dry-run") && next < len(commits) {
				m := commits[next]
				next++
				e.touch(model.KindCommit, "", e.repo+"@"+m[1], strings.TrimSpace(m[2]), "", model.StatusCreated, "", ti, model.ViaToolResult, at)
			}
			continue
		}
		if inv.Name() != "gh" || len(inv.Words) < 3 {
			continue
		}
		noun, verb, args := inv.Words[1], inv.Words[2], inv.Words[3:]
		switch {
		case noun == "pr":
			e.ghPR(ti, verb, args, out, at)
		case noun == "issue" && e.o.enabled("github"):
			e.ghIssue(ti, verb, args, out, at)
		}
	}
}

func (e *extractor) ghPR(ti int, verb string, args []string, out string, at time.Time) {
	status := map[string]string{
		"create": model.StatusCreated, "merge": model.StatusMerged, "close": model.StatusClosed,
		"reopen": model.StatusUpdated, "ready": model.StatusUpdated, "edit": model.StatusUpdated, "comment": model.StatusUpdated,
	}[verb]
	if status == "" {
		return
	}
	repo, num := e.target(args, out, rePRURL)
	title, _ := shellcmd.Flag(args, "title", "t")
	if verb == "create" {
		m := rePRURL.FindStringSubmatch(out)
		if m == nil {
			return // created without a URL in the result: no evidence of which PR
		}
		repo, num = m[1], m[2]
	}
	if num == "" {
		// `gh pr merge` without a number acts on the branch's PR: fall back to
		// the most recent PR proven in this session, if any.
		if it := e.items[e.lastPR]; it != nil && verb != "create" {
			e.touch(model.KindPR, "github", it.Key, "", it.URL, status, "", ti, model.ViaToolResult, at)
		}
		return
	}
	if repo == "" {
		repo = e.repo
	}
	key := repo + "#" + num
	url := ""
	if verb == "create" {
		url = "https://github.com/" + repo + "/pull/" + num
	}
	e.touch(model.KindPR, "github", key, title, url, status, "", ti, model.ViaToolResult, at)
	e.lastPR = itemKey(model.KindPR, "github", key)
}

func (e *extractor) ghIssue(ti int, verb string, args []string, out string, at time.Time) {
	status := map[string]string{
		"create": model.StatusCreated, "close": model.StatusCompleted, "reopen": model.StatusUpdated,
		"edit": model.StatusUpdated, "comment": model.StatusUpdated,
	}[verb]
	if status == "" {
		return
	}
	repo, num := e.target(args, out, reGHIssueURL)
	if verb == "create" {
		m := reGHIssueURL.FindStringSubmatch(out)
		if m == nil {
			return
		}
		repo, num = m[1], m[2]
	}
	if num == "" {
		return
	}
	if repo == "" {
		repo = e.repo
	}
	if reason, _ := shellcmd.Flag(args, "reason", "r"); verb == "close" && strings.EqualFold(strings.TrimSpace(reason), "not planned") {
		status = model.StatusCanceled
	}
	url := ""
	if verb == "create" {
		url = "https://github.com/" + repo + "/issues/" + num
	}
	title, _ := shellcmd.Flag(args, "title", "t")
	e.touch(model.KindIssue, "github", "github:"+repo+"#"+num, title, url, status, "", ti, model.ViaToolResult, at)
}

var reNumberArg = regexp.MustCompile(`^#?(\d+)$`)

// target finds the (repo, number) a gh subcommand acted on, from a URL or
// bare number argument, or from the result text.
func (e *extractor) target(args []string, out string, urlRe *regexp.Regexp) (repo, num string) {
	for _, a := range args {
		if m := urlRe.FindStringSubmatch(a); m != nil {
			return m[1], m[2]
		}
	}
	for _, a := range args {
		if m := reNumberArg.FindStringSubmatch(a); m != nil {
			return "", m[1]
		}
	}
	if m := reMergedOut.FindStringSubmatch(out); m != nil {
		return m[1], m[2]
	}
	return "", ""
}

type linearResult struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Status     string `json:"status"`
	StatusType string `json:"statusType"`
}

// parseLinear reads only identifier, title, url and state from a Linear
// issue result. Description, comments and every other field are ignored by
// construction: they are never decoded.
func parseLinear(txt string) *linearResult {
	var r linearResult
	if json.Unmarshal([]byte(txt), &r) != nil || r.ID == "" {
		return nil
	}
	if _, _, ok := splitKey(r.ID); !ok {
		return nil
	}
	return &r
}

// fromIssueTool decides from the result's shape which tracker answered.
func (e *extractor) fromIssueTool(ti int, c model.ToolCall, at time.Time) {
	if r := parseLinear(c.Result.Text); r != nil {
		if e.o.enabled("linear") {
			e.fromLinear(ti, c, at, r)
		}
		return
	}
	if e.o.enabled("github") {
		e.fromGitHubTool(ti, c, at)
	}
}

func (e *extractor) fromLinear(ti int, c model.ToolCall, at time.Time, r *linearResult) {
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(c.Input, &in)
	status := model.StatusMentioned // get_issue: read only
	if strings.HasSuffix(strings.ToLower(c.Name), "save_issue") {
		status = model.StatusUpdated
		if in.ID == "" {
			status = model.StatusCreated
		}
		switch r.StatusType {
		case "completed":
			status = model.StatusCompleted
		case "canceled":
			status = model.StatusCanceled
		}
	}
	e.touch(model.KindIssue, "linear", "linear:"+r.ID, r.Title, r.URL, status, r.Status, ti, model.ViaToolResult, at)
}

// fromGitHubTool handles the GitHub MCP server's issue tools. Only the number,
// URL, state and title are read; bodies and comments are never decoded.
func (e *extractor) fromGitHubTool(ti int, c model.ToolCall, at time.Time) {
	var in struct {
		Method      string `json:"method"`
		Owner       string `json:"owner"`
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
		Title       string `json:"title"`
	}
	_ = json.Unmarshal(c.Input, &in)
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		URL     string `json:"url"`
		State   string `json:"state"`
		Title   string `json:"title"`
	}
	_ = json.Unmarshal([]byte(c.Result.Text), &out)

	repo, num, url := in.Owner+"/"+in.Repo, in.IssueNumber, ""
	for _, u := range []string{out.HTMLURL, out.URL} {
		if m := reGHIssueURL.FindStringSubmatch(u); m != nil {
			repo, url = m[1], m[0]
			num, _ = strconv.Atoi(m[2])
			break
		}
	}
	if num == 0 && out.Number > 0 {
		num = out.Number
	}
	if num == 0 || repo == "/" || !strings.Contains(repo, "/") {
		return // no way to say which issue this was
	}
	title := firstNonEmpty(out.Title, in.Title)
	name := strings.ToLower(c.Name)
	status, raw := model.StatusMentioned, out.State // reading proves nothing was changed
	if strings.HasSuffix(name, "issue_write") || strings.HasSuffix(name, "create_issue") || strings.HasSuffix(name, "update_issue") {
		switch {
		case in.Method == "create" || strings.HasSuffix(name, "create_issue"):
			status = model.StatusCreated
		case in.State == "closed" && in.StateReason == "not_planned":
			status = model.StatusCanceled
		case in.State == "closed":
			status = model.StatusCompleted
		default:
			status = model.StatusUpdated
		}
	}
	e.touch(model.KindIssue, "github", "github:"+repo+"#"+strconv.Itoa(num), title, url, status, raw, ti, model.ViaToolResult, at)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// fromArtifact records a chat-product artifact (metadata only). The export is
// the product's own record that the artifact exists, so the evidence is a
// harness event rather than a model claim.
func (e *extractor) fromArtifact(ti int, c model.ToolCall, at time.Time) {
	var in struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal(c.Input, &in) // the artifact's content is never decoded
	if in.ID == "" || len(in.ID) > 120 || redact.IsSecretFile(in.Title) {
		return
	}
	status := model.StatusCreated
	if in.Command == "update" || in.Command == "rewrite" {
		status = model.StatusUpdated
	}
	e.touch(model.KindArtifact, "", "artifact:"+in.ID, in.Title, "", status, "", ti, model.ViaHarnessEvent, at)
}

func (e *extractor) fromText(ti int, t model.Turn) {
	txt := t.Text
	at := t.Time
	seen := map[string]bool{}
	mention := func(kind, provider, key, url string) {
		k := itemKey(kind, provider, key)
		if seen[k] {
			return
		}
		seen[k] = true
		e.touch(kind, provider, key, "", url, model.StatusMentioned, "", ti, model.ViaText, at)
	}
	for _, m := range rePRURL.FindAllStringSubmatch(txt, -1) {
		mention(model.KindPR, "github", m[1]+"#"+m[2], m[0])
	}
	for _, m := range rePRRef.FindAllStringSubmatch(txt, -1) {
		mention(model.KindPR, "github", e.repo+"#"+m[1], "")
	}
	if e.o.enabled("github") {
		for _, m := range reGHIssueURL.FindAllStringSubmatch(txt, -1) {
			mention(model.KindIssue, "github", "github:"+m[1]+"#"+m[2], m[0])
		}
		for _, m := range reIssueRef.FindAllStringSubmatch(txt, -1) {
			mention(model.KindIssue, "github", "github:"+e.repo+"#"+m[1], "")
		}
	}
	if e.o.enabled("linear") {
		for _, m := range reLinearURL.FindAllStringSubmatch(txt, -1) {
			mention(model.KindIssue, "linear", "linear:"+m[2]+"-"+m[3], m[0])
		}
	}
	if e.o.enabled("jira") {
		for _, m := range reJiraURL.FindAllStringSubmatch(txt, -1) {
			mention(model.KindIssue, "jira", "jira:"+m[2]+"-"+m[3], m[0])
		}
	}
	for _, m := range reCommitURL.FindAllStringSubmatch(txt, -1) {
		mention(model.KindCommit, "", e.repo+"@"+m[1], m[0])
	}
	for _, m := range reSHACtx.FindAllStringSubmatch(txt, -1) {
		sha := m[1] + m[2]
		if looksLikeSHA(sha) {
			mention(model.KindCommit, "", e.repo+"@"+sha, "")
		}
	}
	for _, m := range reBareKey.FindAllStringSubmatch(txt, -1) {
		prefix := m[1]
		if falsePrefixes[prefix] || !e.known[prefix] {
			continue
		}
		if e.o.enabled("linear") && e.prefixProvider(prefix) == "linear" {
			mention(model.KindIssue, "linear", "linear:"+prefix+"-"+m[2], "")
		} else if e.o.enabled("jira") && e.prefixProvider(prefix) == "jira" {
			mention(model.KindIssue, "jira", "jira:"+prefix+"-"+m[2], "")
		}
	}
}

// prefixProvider guesses which tracker owns a known prefix, from what the
// session itself showed. Unknown prefixes from config default to linear.
func (e *extractor) prefixProvider(prefix string) string {
	for _, t := range e.s.Turns {
		for _, m := range reJiraURL.FindAllStringSubmatch(t.Text, -1) {
			if m[2] == prefix {
				return "jira"
			}
		}
	}
	return "linear"
}

func looksLikeSHA(s string) bool {
	hasDigit, hasLetter := false, false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			hasDigit = true
		} else {
			hasLetter = true
		}
	}
	return hasDigit && hasLetter
}

func itemKey(kind, provider, key string) string { return kind + "|" + provider + "|" + key }

// touch applies one observation to the item map using the merge rules.
func (e *extractor) touch(kind, provider, key, title, url, status, rawState string, turn int, via string, at time.Time) {
	// A short commit SHA mention and a full SHA created later are one item.
	if kind == model.KindCommit {
		key = e.canonicalCommit(key)
	}
	k := itemKey(kind, provider, key)
	obs := model.WorkItem{
		Kind: kind, Provider: provider, Key: key, Title: title, URL: url, Status: status, RawState: rawState,
		ObservedAt: at,
		Evidence:   []model.Evidence{{SessionID: e.s.Ref.ID, Turn: turn, Via: via, At: at}},
	}
	if cur, ok := e.items[k]; ok {
		m := Merge(*cur, obs)
		e.items[k] = &m
		return
	}
	e.items[k] = &obs
	e.order = append(e.order, k)
}

// canonicalCommit maps abbreviated/full SHAs of the same commit to one key.
func (e *extractor) canonicalCommit(key string) string {
	i := strings.LastIndex(key, "@")
	if i < 0 {
		return key
	}
	repo, sha := key[:i], key[i+1:]
	for k, it := range e.items {
		if it.Kind != model.KindCommit {
			continue
		}
		j := strings.LastIndex(it.Key, "@")
		if it.Key[:j] != repo {
			continue
		}
		other := it.Key[j+1:]
		if strings.HasPrefix(other, sha) || strings.HasPrefix(sha, other) {
			if len(sha) > len(other) { // keep the longest spelling
				nk := repo + "@" + sha
				it.Key = nk
				e.items[itemKey(model.KindCommit, "", nk)] = it
				delete(e.items, k)
				for idx, o := range e.order {
					if o == k {
						e.order[idx] = itemKey(model.KindCommit, "", nk)
					}
				}
				return nk
			}
			return it.Key
		}
	}
	return key
}

// Merge combines two observations of the same work item.
//
//   - A mention never changes an existing evidence-backed status.
//   - Among evidence-backed statuses the latest by ObservedAt wins, so a
//     reopened issue moves back from completed.
//   - Commits are immutable once created.
//   - Titles and URLs fill in when missing and are otherwise kept.
func Merge(a, b model.WorkItem) model.WorkItem {
	out := a
	if out.Title == "" {
		out.Title = b.Title
	}
	if out.URL == "" {
		out.URL = b.URL
	}
	out.Evidence = append(slices.Clone(a.Evidence), b.Evidence...)
	out.Evidence = trimEvidence(out.Evidence)
	aBacked, bBacked := model.IsEvidenceBacked(a.Status), model.IsEvidenceBacked(b.Status)
	switch {
	case !bBacked:
		// b is a mention: keep a's status; a mention-only item just refreshes its time
		if !aBacked && b.ObservedAt.After(out.ObservedAt) {
			out.ObservedAt = b.ObservedAt
		}
		if b.RawState != "" && !aBacked {
			out.RawState = b.RawState
		}
	case !aBacked:
		out.Status, out.RawState, out.ObservedAt = b.Status, b.RawState, b.ObservedAt
	case a.Kind == model.KindCommit && a.Status == model.StatusCreated:
		// immutable
	case !b.ObservedAt.Before(a.ObservedAt):
		out.Status, out.ObservedAt = b.Status, b.ObservedAt
		if b.RawState != "" {
			out.RawState = b.RawState
		}
	}
	return out
}

// trimEvidence caps the list, dropping text-only evidence first so the
// evidence that backs the status survives a pile of later mentions.
func trimEvidence(ev []model.Evidence) []model.Evidence {
	for len(ev) > maxEvidence {
		drop := 0
		for i, x := range ev {
			if x.Via == model.ViaText {
				drop = i
				break
			}
		}
		ev = slices.Delete(ev, drop, drop+1)
	}
	return ev
}
