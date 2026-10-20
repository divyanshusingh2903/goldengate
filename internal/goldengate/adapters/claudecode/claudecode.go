// Package claudecode reads Claude Code session transcripts
// (<config>/projects/<encoded-cwd>/<session-id>.jsonl).
//
// Format pinned: Claude Code 2.1.x, JSONL, one event per line. Observed event
// types: user, assistant, attachment, system, custom-title, pr-link and
// assorted bookkeeping events. Only the fields listed in event/block are
// read; everything else is ignored, and unknown shapes are skipped with a
// warning instead of failing the session.
package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/jsonl"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Name is the adapter's registry name.
const Name = "claude-code"

// TestedMajorMinor is the Claude Code version family this adapter was tested
// against; other versions still load but add a warning.
const TestedMajorMinor = "2.1"

const (
	defaultMaxFileBytes = 256 << 20
	maxLineBytes        = 64 << 20
	headerMaxLines      = 200
	headerMaxLineBytes  = 1 << 20
	keepResultBytes     = 4 << 10 // head and tail of tool output kept for evidence matching
	keepTextBytes       = 16 << 10
)

// Options configures the adapter.
type Options struct {
	ConfigDir    string // default: $CLAUDE_CONFIG_DIR or ~/.claude
	MaxFileBytes int64  // sessions larger than this are skipped (default 256 MiB)
}

// Adapter implements adapters.Adapter for Claude Code.
type Adapter struct {
	root string // projects directory
	max  int64
}

// New builds an adapter, resolving the config dir from opts or the environment.
func New(o Options) *Adapter {
	dir := o.ConfigDir
	if dir == "" {
		dir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if dir == "" {
		dir = filepath.Join(pathutil.Home(), ".claude")
	}
	m := o.MaxFileBytes
	if m <= 0 {
		m = defaultMaxFileBytes
	}
	return &Adapter{root: filepath.Join(dir, "projects"), max: m}
}

// Name implements adapters.Adapter.
func (a *Adapter) Name() string { return Name }

// Detect implements adapters.Adapter.
func (a *Adapter) Detect(ctx context.Context) (adapters.Detection, error) {
	if err := ctx.Err(); err != nil {
		return adapters.Detection{}, err
	}
	st, err := os.Stat(a.root)
	if err != nil || !st.IsDir() {
		return adapters.Detection{Root: a.root, Notes: []string{"projects directory not found"}}, nil
	}
	return adapters.Detection{Installed: true, Root: a.root}, nil
}

type headerEvent struct {
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
	Version   string `json:"version"`
}

// ListSessions implements adapters.Adapter. It only looks at top-level
// *.jsonl files of each project folder; subagent logs, tool-results, memory
// and attachments are never read.
func (a *Adapter) ListSessions(ctx context.Context, opt adapters.ListOpts) iter.Seq2[model.SessionRef, error] {
	return func(yield func(model.SessionRef, error) bool) {
		dirs, err := os.ReadDir(a.root)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.root, Reason: err.Error()})
			}
			return
		}
		for _, d := range dirs {
			if ctx.Err() != nil {
				yield(model.SessionRef{}, ctx.Err())
				return
			}
			if !d.IsDir() { // also excludes symlinked project dirs
				continue
			}
			pdir := filepath.Join(a.root, d.Name())
			files, err := os.ReadDir(pdir)
			if err != nil {
				if !yield(model.SessionRef{}, &adapters.SkipWarning{Path: pdir, Reason: err.Error()}) {
					return
				}
				continue
			}
			for _, f := range files {
				if f.Type() != 0 || !strings.HasSuffix(f.Name(), ".jsonl") { // regular files only
					continue
				}
				path := filepath.Join(pdir, f.Name())
				ref, err := a.header(path, f)
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
}

func (a *Adapter) header(path string, de os.DirEntry) (model.SessionRef, error) {
	info, err := de.Info()
	if err != nil {
		return model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: err.Error()}
	}
	if info.Size() > a.max {
		return model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: fmt.Sprintf("larger than size cap (%d bytes)", info.Size())}
	}
	fh, err := os.Open(path)
	if err != nil {
		return model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: err.Error()}
	}
	defer func() { _ = fh.Close() }()

	var cwd string
	var started time.Time
	br := bufio.NewReaderSize(fh, 64<<10)
	for i := 0; i < headerMaxLines && (cwd == "" || started.IsZero()); i++ {
		line, tooLong, rerr := jsonl.ReadLine(br, headerMaxLineBytes)
		if len(line) > 0 && !tooLong {
			var h headerEvent
			if json.Unmarshal(line, &h) == nil {
				if cwd == "" {
					cwd = h.Cwd
				}
				if started.IsZero() {
					started = jsonl.ParseTime(h.Timestamp)
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	if cwd == "" {
		return model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: "no cwd in first events (unknown shape)"}
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if started.IsZero() {
		started = info.ModTime()
	}
	return model.SessionRef{
		Source:     Name,
		ID:         id,
		ProjectKey: pathutil.Normalize(cwd),
		Path:       path,
		StartedAt:  started,
		UpdatedAt:  info.ModTime(),
		SizeBytes:  info.Size(),
	}, nil
}

// --- wire types (only the fields we read) ---

type event struct {
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Timestamp   string          `json:"timestamp"`
	Cwd         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	Version     string          `json:"version"`
	Message     *message        `json:"message"`
	CustomTitle string          `json:"customTitle"`
	PRNumber    int             `json:"prNumber"`
	PRURL       string          `json:"prUrl"`
	PRRepo      string          `json:"prRepository"`
	ToolResult  json.RawMessage `json:"toolUseResult"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   *bool           `json:"is_error"`
}

type toolInput struct {
	Command  string `json:"command"`
	FilePath string `json:"file_path"`
	Path     string `json:"path"`
}

type callRef struct{ turn, idx int }

// Load implements adapters.Adapter.
func (a *Adapter) Load(ctx context.Context, ref model.SessionRef) (*model.RawSession, error) {
	info, err := os.Lstat(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("claudecode: load %s: %w", ref.ID, err)
	}
	if !info.Mode().IsRegular() {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "not a regular file"}
	}
	if info.Size() > a.max {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "larger than size cap"}
	}
	fh, err := os.Open(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("claudecode: open %s: %w", ref.ID, err)
	}
	defer func() { _ = fh.Close() }()

	s := &model.RawSession{Ref: ref, ToolsUsed: map[string]int{}}
	pend := map[string]callRef{}
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
			var ev event
			if err := json.Unmarshal(line, &ev); err != nil {
				skipped["unparseable line"]++
			} else {
				a.apply(s, &ev, pend, skipped)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return nil, fmt.Errorf("claudecode: read %s: %w", ref.ID, rerr)
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

func (a *Adapter) apply(s *model.RawSession, ev *event, pend map[string]callRef, skipped map[string]int) {
	if ev.Version != "" {
		if s.SourceVersion == "" {
			s.SourceVersion = ev.Version
			if !strings.HasPrefix(ev.Version, TestedMajorMinor+".") {
				s.Warnings = append(s.Warnings, fmt.Sprintf("untested Claude Code version %s (tested %s.x)", ev.Version, TestedMajorMinor))
			}
		}
	}
	if ev.GitBranch != "" {
		s.GitBranch = ev.GitBranch
	}
	switch ev.Type {
	case "custom-title":
		if ev.CustomTitle != "" {
			s.Title = ev.CustomTitle
		}
	case "pr-link":
		if ev.PRURL != "" {
			s.Events = append(s.Events, model.HarnessEvent{Kind: "pr_link", URL: ev.PRURL, Number: ev.PRNumber, Repo: ev.PRRepo, Time: jsonl.ParseTime(ev.Timestamp)})
		}
	case "user":
		if ev.IsSidechain || ev.Message == nil {
			return
		}
		a.applyUser(s, ev, pend, skipped)
	case "assistant":
		if ev.IsSidechain || ev.Message == nil {
			return
		}
		a.applyAssistant(s, ev, pend, skipped)
	}
}

func decodeBlocks(raw json.RawMessage) (text string, blocks []block, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", nil, true
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", nil, false
		}
		return s, nil, true
	case '[':
		var bs []block
		if json.Unmarshal(raw, &bs) != nil {
			return "", nil, false
		}
		return "", bs, true
	}
	return "", nil, false
}

func (a *Adapter) applyUser(s *model.RawSession, ev *event, pend map[string]callRef, skipped map[string]int) {
	text, blocks, ok := decodeBlocks(ev.Message.Content)
	if !ok {
		skipped["user message with unknown content shape"]++
		return
	}
	var parts []string
	if text != "" {
		parts = append(parts, text)
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "tool_result":
			a.applyResult(s, &b, ev, pend)
		}
	}
	if ev.IsMeta {
		return
	}
	joined := strings.TrimSpace(strings.Join(parts, "\n"))
	if joined == "" || strings.HasPrefix(joined, "<local-command-") || strings.HasPrefix(joined, "Caveat:") || strings.HasPrefix(joined, "[Request interrupted") {
		return
	}
	s.Turns = append(s.Turns, model.Turn{Role: "user", Text: jsonl.Clip(joined, keepTextBytes), Time: jsonl.ParseTime(ev.Timestamp)})
}

func (a *Adapter) applyAssistant(s *model.RawSession, ev *event, pend map[string]callRef, skipped map[string]int) {
	text, blocks, ok := decodeBlocks(ev.Message.Content)
	if !ok {
		skipped["assistant message with unknown content shape"]++
		return
	}
	if len(s.Turns) == 0 || s.Turns[len(s.Turns)-1].Role != "assistant" {
		s.Turns = append(s.Turns, model.Turn{Role: "assistant", Time: jsonl.ParseTime(ev.Timestamp)})
	}
	ti := len(s.Turns) - 1
	t := &s.Turns[ti]
	if text != "" {
		appendText(t, text)
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			appendText(t, b.Text)
		case "tool_use":
			call := a.toolCall(s, &b, ev)
			s.ToolsUsed[b.Name]++
			t.Tools = append(t.Tools, call)
			if b.ID != "" {
				pend[b.ID] = callRef{turn: ti, idx: len(t.Tools) - 1}
			}
		}
	}
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

func appendText(t *model.Turn, s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	t.LastText = jsonl.Clip(s, keepTextBytes)
	if t.Text != "" {
		t.Text += "\n\n"
	}
	t.Text = jsonl.Clip(t.Text+s, keepTextBytes)
}

func (a *Adapter) toolCall(s *model.RawSession, b *block, ev *event) model.ToolCall {
	var in toolInput
	_ = json.Unmarshal(b.Input, &in) // tolerate any shape
	c := model.ToolCall{ID: b.ID, Name: b.Name, Kind: model.ToolOther, Time: jsonl.ParseTime(ev.Timestamp)}
	switch b.Name {
	case "Bash":
		c.Kind, c.Command = model.ToolShell, in.Command
	case "Write":
		c.Kind, c.Path = model.ToolFileWrite, in.FilePath
	case "Edit", "MultiEdit", "NotebookEdit":
		c.Kind, c.Path = model.ToolFileEdit, jsonl.FirstNonEmpty(in.FilePath, in.Path)
	case "Read":
		c.Kind, c.Path = model.ToolFileRead, in.FilePath
	default:
		if strings.HasPrefix(b.Name, "mcp__") && adapters.IsIssueTool(b.Name) {
			c.Kind, c.Input = model.ToolIssue, adapters.TrimIssueInput(b.Input)
		}
	}
	return c
}

func (a *Adapter) applyResult(s *model.RawSession, b *block, ev *event, pend map[string]callRef) {
	ref, ok := pend[b.ToolUseID]
	if !ok {
		return
	}
	delete(pend, b.ToolUseID)
	call := &s.Turns[ref.turn].Tools[ref.idx]

	txt := resultText(b.Content)
	res := model.ToolResult{Known: true, OK: true}
	if b.IsError != nil && *b.IsError {
		res.OK = false
	}
	// Defense in depth: a failed Bash call reports "Exit code N" even if
	// is_error were ever absent.
	if code, found := parseExitCode(txt); found {
		res.ExitCode = &code
		if code != 0 {
			res.OK = false
		}
	}
	switch call.Kind {
	case model.ToolShell:
		res.Text = jsonl.ClipMiddle(txt, keepResultBytes)
	case model.ToolIssue:
		res.Text = txt // must stay parseable JSON; capped by maxLineBytes already
	}
	if call.Kind == model.ToolFileWrite && res.OK {
		var tr struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(ev.ToolResult, &tr) == nil && tr.Type == "create" {
			call.Created = true
		}
	}
	call.Result = res
}

func resultText(raw json.RawMessage) string {
	text, blocks, ok := decodeBlocks(raw)
	if !ok {
		return ""
	}
	if text != "" {
		return text
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func parseExitCode(txt string) (int, bool) {
	const p = "Exit code "
	if !strings.HasPrefix(txt, p) {
		return 0, false
	}
	n := 0
	i := len(p)
	start := i
	for i < len(txt) && txt[i] >= '0' && txt[i] <= '9' {
		n = n*10 + int(txt[i]-'0')
		i++
		if i-start > 4 {
			return 0, false
		}
	}
	if i == start {
		return 0, false
	}
	return n, true
}
