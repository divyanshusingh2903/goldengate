// Package codex reads Codex CLI / Codex Desktop rollouts
// (<codex home>/sessions/YYYY/MM/DD/rollout-*.jsonl).
//
// Format pinned: Codex 0.133 to 0.159, JSONL with an envelope
// {timestamp, type, payload} per line. The conversation is taken from
// response_item events (message, function_call, custom_tool_call and their
// outputs); event_msg duplicates of the same messages are ignored. Unknown
// shapes are skipped with a warning, never fatal.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/applypatch"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/jsonl"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Name is the adapter's registry name.
const Name = "codex"

// TestedVersions documents what the adapter was verified against; other
// versions still load but add a warning.
const TestedVersions = "0.133-0.159"

const (
	defaultMaxFileBytes = 256 << 20
	maxLineBytes        = 64 << 20
	headerMaxLines      = 50
	headerMaxLineBytes  = 1 << 20
	keepResultBytes     = 4 << 10
	keepTextBytes       = 16 << 10
)

// Options configures the adapter.
type Options struct {
	CodexHome    string // default: $CODEX_HOME or ~/.codex
	MaxFileBytes int64
}

// Adapter implements adapters.Adapter for Codex.
type Adapter struct {
	root string // sessions directory
	max  int64
}

// New builds an adapter.
func New(o Options) *Adapter {
	dir := o.CodexHome
	if dir == "" {
		dir = os.Getenv("CODEX_HOME")
	}
	if dir == "" {
		dir = filepath.Join(pathutil.Home(), ".codex")
	}
	m := o.MaxFileBytes
	if m <= 0 {
		m = defaultMaxFileBytes
	}
	return &Adapter{root: filepath.Join(dir, "sessions"), max: m}
}

// Name implements adapters.Adapter.
func (a *Adapter) Name() string { return Name }

// Detect implements adapters.Adapter.
func (a *Adapter) Detect(ctx context.Context) (adapters.Detection, error) {
	if err := ctx.Err(); err != nil {
		return adapters.Detection{}, err
	}
	if st, err := os.Stat(a.root); err != nil || !st.IsDir() {
		return adapters.Detection{Root: a.root, Notes: []string{"sessions directory not found"}}, nil
	}
	return adapters.Detection{Installed: true, Root: a.root}, nil
}

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID         string          `json:"id"`
	Timestamp  string          `json:"timestamp"`
	Cwd        string          `json:"cwd"`
	CLIVersion string          `json:"cli_version"`
	Source     json.RawMessage `json:"source"` // "cli", "vscode", "exec", … (an object for sub-agents)
	Git        struct {
		Branch string `json:"branch"`
		Remote string `json:"repository_url"`
	} `json:"git"`
}

// ListSessions implements adapters.Adapter.
func (a *Adapter) ListSessions(ctx context.Context, opt adapters.ListOpts) iter.Seq2[model.SessionRef, error] {
	return func(yield func(model.SessionRef, error) bool) {
		files, err := a.rollouts()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.root, Reason: err.Error()})
			}
			return
		}
		for _, f := range files {
			if ctx.Err() != nil {
				yield(model.SessionRef{}, ctx.Err())
				return
			}
			ref, err := a.header(f.path, f.info)
			if err != nil {
				if !yield(model.SessionRef{}, err) {
					return
				}
				continue
			}
			if !opt.Since.IsZero() && ref.UpdatedAt.Before(opt.Since) {
				continue
			}
			if !yield(ref, nil) {
				return
			}
		}
	}
}

type rolloutFile struct {
	path string
	info os.FileInfo
}

// rollouts walks YYYY/MM/DD/rollout-*.jsonl, newest first, regular files only.
func (a *Adapter) rollouts() ([]rolloutFile, error) {
	var out []rolloutFile
	years, err := os.ReadDir(a.root)
	if err != nil {
		return nil, err
	}
	for i := len(years) - 1; i >= 0; i-- {
		if !years[i].IsDir() {
			continue
		}
		months, _ := os.ReadDir(filepath.Join(a.root, years[i].Name()))
		for j := len(months) - 1; j >= 0; j-- {
			if !months[j].IsDir() {
				continue
			}
			days, _ := os.ReadDir(filepath.Join(a.root, years[i].Name(), months[j].Name()))
			for k := len(days) - 1; k >= 0; k-- {
				if !days[k].IsDir() {
					continue
				}
				dir := filepath.Join(a.root, years[i].Name(), months[j].Name(), days[k].Name())
				ents, _ := os.ReadDir(dir)
				for _, e := range ents {
					if e.Type() != 0 || !strings.HasPrefix(e.Name(), "rollout-") || !strings.HasSuffix(e.Name(), ".jsonl") {
						continue
					}
					info, err := e.Info()
					if err != nil {
						continue
					}
					out = append(out, rolloutFile{filepath.Join(dir, e.Name()), info})
				}
			}
		}
	}
	return out, nil
}

var reFileUUID = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

func (a *Adapter) header(path string, info os.FileInfo) (model.SessionRef, error) {
	if info.Size() > a.max {
		return model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: fmt.Sprintf("larger than size cap (%d bytes)", info.Size())}
	}
	fh, err := os.Open(path)
	if err != nil {
		return model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: err.Error()}
	}
	defer func() { _ = fh.Close() }()
	br := bufio.NewReaderSize(fh, 64<<10)
	for i := 0; i < headerMaxLines; i++ {
		line, tooLong, rerr := jsonl.ReadLine(br, headerMaxLineBytes)
		if len(line) > 0 && !tooLong {
			var env envelope
			if json.Unmarshal(line, &env) == nil && env.Type == "session_meta" {
				var m sessionMeta
				if json.Unmarshal(env.Payload, &m) == nil && m.Cwd != "" {
					id := m.ID
					if id == "" {
						if sub := reFileUUID.FindStringSubmatch(path); sub != nil {
							id = sub[1]
						}
					}
					if id == "" {
						break
					}
					started := jsonl.ParseTime(jsonl.FirstNonEmpty(m.Timestamp, env.Timestamp))
					if started.IsZero() {
						started = info.ModTime()
					}
					return model.SessionRef{Source: Name, ID: id, ProjectKey: pathutil.Normalize(m.Cwd), Path: path,
						StartedAt: started, UpdatedAt: info.ModTime(), SizeBytes: info.Size()}, nil
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	return model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: "no session_meta with a cwd near the start (unknown shape)"}
}

// --- parsing ---

type message struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type call struct {
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"` // function_call: a JSON string
	Input     string          `json:"input"`     // custom_tool_call: raw text
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
}

type pending struct{ turn, idx int }

// injected user-role messages that the harness adds; they are not the user's words.
var injectedPrefixes = []string{"<environment_context>", "<turn_aborted>", "<recommended_plugins>", "<task-notification>",
	"<local-command-", "<command-name>", "<user_instructions>", "<permissions", "<collaboration_mode>", "<skill>"}

// Load implements adapters.Adapter.
func (a *Adapter) Load(ctx context.Context, ref model.SessionRef) (*model.RawSession, error) {
	info, err := os.Lstat(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("codex: load %s: %w", ref.ID, err)
	}
	if !info.Mode().IsRegular() {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "not a regular file"}
	}
	if info.Size() > a.max {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "larger than size cap"}
	}
	fh, err := os.Open(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("codex: open %s: %w", ref.ID, err)
	}
	defer func() { _ = fh.Close() }()

	s := &model.RawSession{Ref: ref, ToolsUsed: map[string]int{}}
	pend := map[string]pending{}
	skipped := map[string]int{}
	br := bufio.NewReaderSize(fh, 256<<10)
	for lineNo := 0; ; lineNo++ {
		if lineNo%512 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		line, tooLong, rerr := jsonl.ReadLine(br, maxLineBytes)
		if tooLong {
			skipped["oversized line"]++
		} else if len(bytes.TrimSpace(line)) > 0 {
			var env envelope
			if err := json.Unmarshal(line, &env); err != nil {
				skipped["unparseable line"]++
			} else {
				a.apply(s, &env, pend, skipped)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return nil, fmt.Errorf("codex: read %s: %w", ref.ID, rerr)
			}
			break
		}
	}
	collectFiles(s)
	for reason, n := range skipped {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %d x %s", ref.ID, n, reason))
	}
	return s, nil
}

func (a *Adapter) apply(s *model.RawSession, env *envelope, pend map[string]pending, skipped map[string]int) {
	at := jsonl.ParseTime(env.Timestamp)
	switch env.Type {
	case "session_meta":
		var m sessionMeta
		if json.Unmarshal(env.Payload, &m) != nil {
			skipped["session_meta with unknown shape"]++
			return
		}
		s.SourceVersion = m.CLIVersion
		if string(m.Source) == `"exec"` {
			s.Origin = model.OriginCLI // codex exec
		}
		s.GitBranch = m.Git.Branch
		s.GitRemote = stripCredentials(m.Git.Remote)
		if m.CLIVersion != "" && !testedVersion(m.CLIVersion) {
			s.Warnings = append(s.Warnings, fmt.Sprintf("untested Codex version %s (tested %s)", m.CLIVersion, TestedVersions))
		}
	case "response_item":
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(env.Payload, &head) != nil {
			skipped["response_item with unknown shape"]++
			return
		}
		switch head.Type {
		case "message":
			var m message
			if json.Unmarshal(env.Payload, &m) != nil {
				skipped["message with unknown shape"]++
				return
			}
			a.applyMessage(s, &m, at)
		case "function_call", "custom_tool_call":
			var c call
			if json.Unmarshal(env.Payload, &c) != nil {
				skipped["tool call with unknown shape"]++
				return
			}
			a.applyCall(s, &c, head.Type, at, pend)
		case "function_call_output", "custom_tool_call_output":
			var c call
			if json.Unmarshal(env.Payload, &c) != nil {
				skipped["tool output with unknown shape"]++
				return
			}
			a.applyOutput(s, &c, pend)
		case "web_search_call":
			s.ToolsUsed["web_search"]++
		}
	}
}

func testedVersion(v string) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 || parts[0] != "0" {
		return false
	}
	minor, err := strconv.Atoi(strings.SplitN(parts[1], "-", 2)[0])
	return err == nil && minor >= 133 && minor <= 159
}

func stripCredentials(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		u.User = nil
		return u.String()
	}
	if i := strings.LastIndex(raw, "@"); i >= 0 && !strings.Contains(raw[:i], "/") && !strings.Contains(raw[:i], ":") {
		return raw // scp-style git@host:path carries no secret
	}
	return raw
}

func (a *Adapter) applyMessage(s *model.RawSession, m *message, at time.Time) {
	var texts []string
	for _, b := range m.Content {
		if b.Type == "input_text" || b.Type == "output_text" || b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	text := strings.TrimSpace(strings.Join(texts, "\n"))
	switch m.Role {
	case "user":
		if text == "" {
			return
		}
		for _, p := range injectedPrefixes {
			if strings.HasPrefix(text, p) {
				return
			}
		}
		s.Turns = append(s.Turns, model.Turn{Role: "user", Text: jsonl.Clip(text, keepTextBytes), Time: at})
	case "assistant":
		t := a.assistantTurn(s, at)
		if text != "" {
			if t.Text != "" {
				t.Text += "\n\n"
			}
			t.Text = jsonl.Clip(t.Text+text, keepTextBytes)
			t.LastText = jsonl.Clip(text, keepTextBytes)
		}
	}
	// developer / system messages are instructions, not conversation
}

func (a *Adapter) assistantTurn(s *model.RawSession, at time.Time) *model.Turn {
	if len(s.Turns) == 0 || s.Turns[len(s.Turns)-1].Role != "assistant" {
		s.Turns = append(s.Turns, model.Turn{Role: "assistant", Time: at})
	}
	return &s.Turns[len(s.Turns)-1]
}

func (a *Adapter) applyCall(s *model.RawSession, c *call, typ string, at time.Time, pend map[string]pending) {
	t := a.assistantTurn(s, at)
	ti := len(s.Turns) - 1
	s.ToolsUsed[c.Name]++
	tc := model.ToolCall{ID: c.CallID, Name: c.Name, Kind: model.ToolOther, Time: at}

	switch {
	case typ == "function_call" && (c.Name == "exec_command" || c.Name == "shell" || c.Name == "container.exec" || c.Name == "shell_command"):
		tc.Kind, tc.Command = model.ToolShell, shellCommand(c.Arguments)
	case typ == "custom_tool_call" && c.Name == "apply_patch":
		for i, f := range applypatch.Parse(c.Input) {
			pc := model.ToolCall{ID: fmt.Sprintf("%s#%d", c.CallID, i), Name: c.Name, Kind: model.ToolFileEdit, Path: f.Path, Time: at}
			if f.Op == applypatch.Add {
				pc.Kind, pc.Created = model.ToolFileWrite, true
			}
			t.Tools = append(t.Tools, pc)
			pend[pc.ID] = pending{ti, len(t.Tools) - 1}
		}
		// the patch call itself carries no extra information beyond its files
		pend[c.CallID] = pending{-1, -1}
		return
	}
	t.Tools = append(t.Tools, tc)
	if c.CallID != "" {
		pend[c.CallID] = pending{ti, len(t.Tools) - 1}
	}
}

// shellCommand extracts the command line from exec_command ({cmd}) and older
// shell ({command: [...]}) arguments.
func shellCommand(args string) string {
	var a struct {
		Cmd     string          `json:"cmd"`
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return ""
	}
	if a.Cmd != "" {
		return a.Cmd
	}
	var list []string
	if json.Unmarshal(a.Command, &list) == nil && len(list) > 0 {
		if len(list) >= 3 && (list[1] == "-lc" || list[1] == "-c") { // ["bash","-lc","script"]
			return list[2]
		}
		return strings.Join(list, " ")
	}
	var one string
	if json.Unmarshal(a.Command, &one) == nil {
		return one
	}
	return ""
}

var (
	reExited  = regexp.MustCompile(`(?m)^Process exited with code (-?\d+)`)
	reRunning = regexp.MustCompile(`(?m)^Process running with session ID`)
	reExit    = regexp.MustCompile(`(?mi)^Exit code:\s*(-?\d+)`)
)

func (a *Adapter) applyOutput(s *model.RawSession, c *call, pend map[string]pending) {
	ref, ok := pend[c.CallID]
	if !ok {
		return
	}
	delete(pend, c.CallID)
	txt := outputText(c.Output)

	// apply_patch: one result for all of the patch's per-file calls
	if ref.turn == -1 {
		res := patchResult(txt)
		for id, p := range pend {
			if strings.HasPrefix(id, c.CallID+"#") {
				s.Turns[p.turn].Tools[p.idx].Result = res
				delete(pend, id)
			}
		}
		return
	}
	call := &s.Turns[ref.turn].Tools[ref.idx]
	if call.Kind != model.ToolShell {
		call.Result = model.ToolResult{Known: true, OK: !looksFailed(txt)}
		return
	}
	call.Result = shellResult(txt)
}

func patchResult(txt string) model.ToolResult {
	res := model.ToolResult{Known: true, OK: true}
	if m := reExit.FindStringSubmatch(txt); m != nil {
		n, _ := strconv.Atoi(m[1])
		res.ExitCode = &n
		res.OK = n == 0
	} else if looksFailed(txt) {
		res.OK = false
	}
	return res
}

func looksFailed(txt string) bool {
	l := strings.ToLower(txt)
	return strings.HasPrefix(l, "error") || strings.Contains(l, "failed to") || strings.Contains(l, "apply_patch verification failed")
}

// shellResult parses exec_command output ("Process exited with code N" ...) and
// the older JSON form {"output": ..., "metadata": {"exit_code": N}}.
func shellResult(txt string) model.ToolResult {
	if m := reExited.FindStringSubmatch(txt); m != nil {
		n, _ := strconv.Atoi(m[1])
		out := txt
		if i := strings.Index(txt, "\nOutput:\n"); i >= 0 {
			out = txt[i+len("\nOutput:\n"):]
		}
		return model.ToolResult{Known: true, OK: n == 0, ExitCode: &n, Text: jsonl.ClipMiddle(out, keepResultBytes)}
	}
	if reRunning.MatchString(txt) {
		return model.ToolResult{} // still running: outcome unknown, so no evidence
	}
	var legacy struct {
		Output   string `json:"output"`
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(txt), &legacy) == nil && legacy.Metadata.ExitCode != nil {
		n := *legacy.Metadata.ExitCode
		return model.ToolResult{Known: true, OK: n == 0, ExitCode: &n, Text: jsonl.ClipMiddle(legacy.Output, keepResultBytes)}
	}
	return model.ToolResult{} // unknown shape: never claim success
}

// outputText flattens a string or a list of {type,text} blocks.
func outputText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			parts = append(parts, b.Text)
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// collectFiles lists project-relative files whose write/edit succeeded.
func collectFiles(s *model.RawSession) {
	seen := map[string]bool{}
	for _, t := range s.Turns {
		for _, c := range t.Tools {
			if (c.Kind != model.ToolFileWrite && c.Kind != model.ToolFileEdit) || !c.Result.Known || !c.Result.OK {
				continue
			}
			if rel, ok := jsonl.RelativeTo(s.Ref.ProjectKey, c.Path); ok && !seen[rel] {
				seen[rel] = true
				s.FilesTouched = append(s.FilesTouched, rel)
			}
		}
	}
}
