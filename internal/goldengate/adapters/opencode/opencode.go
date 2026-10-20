// Package opencode reads OpenCode's SQLite database
// ($XDG_DATA_HOME/opencode/opencode.db, default ~/.local/share/opencode).
//
// Format pinned: OpenCode 1.18 to 1.19 (the SQLite layout; the older JSON
// "storage" tree is not supported). The database also holds credentials and
// account tables: this adapter only ever queries session, message, part and
// project (a test enforces the allow-list), opens the file read-only, and
// never reads auth.json or any other file next to it.
package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/applypatch"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/jsonl"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Name is the adapter's registry name.
const Name = "opencode"

// TestedVersions documents what the adapter was verified against.
const TestedVersions = "1.18-1.19"

const (
	keepResultBytes = 4 << 10
	keepTextBytes   = 16 << 10
	keepIssueBytes  = 64 << 10
)

// Options configures the adapter.
type Options struct {
	DataDir string // default: $XDG_DATA_HOME/opencode or ~/.local/share/opencode
}

// Adapter implements adapters.Adapter for OpenCode.
type Adapter struct{ dbPath string }

// New builds an adapter.
func New(o Options) *Adapter {
	dir := o.DataDir
	if dir == "" {
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			dir = filepath.Join(x, "opencode")
		} else {
			dir = filepath.Join(pathutil.Home(), ".local", "share", "opencode")
		}
	}
	return &Adapter{dbPath: filepath.Join(dir, "opencode.db")}
}

// Name implements adapters.Adapter.
func (a *Adapter) Name() string { return Name }

// Detect implements adapters.Adapter.
func (a *Adapter) Detect(ctx context.Context) (adapters.Detection, error) {
	if err := ctx.Err(); err != nil {
		return adapters.Detection{}, err
	}
	st, err := os.Stat(a.dbPath)
	if err != nil || !st.Mode().IsRegular() {
		d := adapters.Detection{Root: a.dbPath, Notes: []string{"opencode.db not found"}}
		if _, e := os.Stat(filepath.Join(filepath.Dir(a.dbPath), "storage", "session")); e == nil {
			d.Notes = append(d.Notes, "found the legacy JSON storage layout, which is not supported")
		}
		return d, nil
	}
	return adapters.Detection{Installed: true, Root: a.dbPath}, nil
}

// open opens the database read-only. A busy timeout keeps a running OpenCode
// from making reads fail.
func (a *Adapter) open(ctx context.Context) (*sql.DB, error) {
	st, err := os.Lstat(a.dbPath)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, &adapters.SkipWarning{Path: a.dbPath, Reason: "not a regular file"}
	}
	db, err := sql.Open("sqlite", "file:"+a.dbPath+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func ms(n int64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(n).UTC()
}

// ListSessions implements adapters.Adapter. Child (sub-agent) sessions are not
// listed on their own: they belong to the work of their parent.
func (a *Adapter) ListSessions(ctx context.Context, opt adapters.ListOpts) iter.Seq2[model.SessionRef, error] {
	return func(yield func(model.SessionRef, error) bool) {
		db, err := a.open(ctx)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: err.Error()})
			}
			return
		}
		defer func() { _ = db.Close() }()
		since := int64(0)
		if !opt.Since.IsZero() {
			since = opt.Since.UnixMilli()
		}
		rows, err := db.QueryContext(ctx, `SELECT s.id, s.directory, s.time_created, s.time_updated, COALESCE(p.worktree, ''),
			(SELECT COUNT(*) FROM message m WHERE m.session_id = s.id)
			FROM session s LEFT JOIN project p ON p.id = s.project_id
			WHERE s.parent_id IS NULL AND s.time_updated >= ? ORDER BY s.time_updated DESC`, since)
		if err != nil {
			yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: "unexpected database layout: " + err.Error()})
			return
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if ctx.Err() != nil {
				yield(model.SessionRef{}, ctx.Err())
				return
			}
			var id, dir, worktree string
			var created, updated, nMsg int64
			if err := rows.Scan(&id, &dir, &created, &updated, &worktree, &nMsg); err != nil {
				yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: "unreadable session row: " + err.Error()})
				return
			}
			if dir == "" {
				if !yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: "session " + id + " has no directory"}) {
					return
				}
				continue
			}
			ref := model.SessionRef{Source: Name, ID: id, ProjectKey: pathutil.Normalize(dir), Path: a.dbPath,
				StartedAt: ms(created), UpdatedAt: ms(updated), SizeBytes: nMsg} // SizeBytes counts messages here: it only has to change when the session grows
			if worktree != "" && worktree != "/" {
				ref.RepoRoot = pathutil.Normalize(worktree)
			}
			if !yield(ref, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: err.Error()})
		}
	}
}

type msgData struct {
	Role string `json:"role"`
	Time struct {
		Created int64 `json:"created"`
	} `json:"time"`
}

type partData struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Synthetic bool   `json:"synthetic"`
	Tool      string `json:"tool"`
	CallID    string `json:"callID"`
	State     struct {
		Status   string          `json:"status"`
		Input    json.RawMessage `json:"input"`
		Output   string          `json:"output"`
		Metadata struct {
			Exit   *int  `json:"exit"`
			Exists *bool `json:"exists"`
		} `json:"metadata"`
		Time struct {
			Start int64 `json:"start"`
		} `json:"time"`
	} `json:"state"`
}

type toolInput struct {
	Command   string `json:"command"`
	FilePath  string `json:"filePath"`
	PatchText string `json:"patchText"`
}

// Load implements adapters.Adapter.
func (a *Adapter) Load(ctx context.Context, ref model.SessionRef) (*model.RawSession, error) {
	db, err := a.open(ctx)
	if err != nil {
		if adapters.IsSkip(err) {
			return nil, err
		}
		return nil, fmt.Errorf("opencode: open: %w", err)
	}
	defer func() { _ = db.Close() }()

	s := &model.RawSession{Ref: ref, ToolsUsed: map[string]int{}}
	var title, version string
	if err := db.QueryRowContext(ctx, `SELECT title, version FROM session WHERE id = ?`, ref.ID).Scan(&title, &version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &adapters.SkipWarning{Path: a.dbPath, Reason: "session " + ref.ID + " no longer exists"}
		}
		return nil, fmt.Errorf("opencode: session %s: %w", ref.ID, err)
	}
	s.SourceVersion = version
	if !isDefaultTitle(title) {
		s.Title = title
	}
	if version != "" && !tested(version) {
		s.Warnings = append(s.Warnings, fmt.Sprintf("untested OpenCode version %s (tested %s)", version, TestedVersions))
	}

	parts, skipped, err := a.parts(ctx, db, ref.ID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, data FROM message WHERE session_id = ? ORDER BY time_created, id`, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("opencode: messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("opencode: message: %w", err)
		}
		var m msgData
		if json.Unmarshal([]byte(data), &m) != nil {
			skipped["message with unknown shape"]++
			continue
		}
		a.applyMessage(s, &m, parts[id])
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("opencode: messages: %w", err)
	}
	collectFiles(s)
	for reason, n := range skipped {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %d x %s", ref.ID, n, reason))
	}
	return s, nil
}

func (a *Adapter) parts(ctx context.Context, db *sql.DB, session string) (map[string][]partData, map[string]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT message_id, data FROM part WHERE session_id = ? ORDER BY message_id, id`, session)
	if err != nil {
		return nil, nil, fmt.Errorf("opencode: parts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]partData{}
	skipped := map[string]int{}
	for rows.Next() {
		var mid, data string
		if err := rows.Scan(&mid, &data); err != nil {
			return nil, nil, fmt.Errorf("opencode: part: %w", err)
		}
		var p partData
		if json.Unmarshal([]byte(data), &p) != nil {
			skipped["part with unknown shape"]++
			continue
		}
		out[mid] = append(out[mid], p)
	}
	return out, skipped, rows.Err()
}

func isDefaultTitle(t string) bool {
	t = strings.TrimSpace(t)
	return t == "" || strings.HasPrefix(t, "New session") || strings.HasPrefix(t, "Child session")
}

func tested(v string) bool {
	return strings.HasPrefix(v, "1.18.") || strings.HasPrefix(v, "1.19.")
}

func (a *Adapter) applyMessage(s *model.RawSession, m *msgData, parts []partData) {
	at := ms(m.Time.Created)
	var texts []string
	for _, p := range parts {
		if p.Type == "text" && !p.Synthetic && strings.TrimSpace(p.Text) != "" {
			texts = append(texts, p.Text)
		}
	}
	text := strings.TrimSpace(strings.Join(texts, "\n"))
	switch m.Role {
	case "user":
		if text != "" {
			s.Turns = append(s.Turns, model.Turn{Role: "user", Text: jsonl.Clip(text, keepTextBytes), Time: at})
		}
	case "assistant":
		if len(s.Turns) == 0 || s.Turns[len(s.Turns)-1].Role != "assistant" {
			s.Turns = append(s.Turns, model.Turn{Role: "assistant", Time: at})
		}
		t := &s.Turns[len(s.Turns)-1]
		if text != "" {
			if t.Text != "" {
				t.Text += "\n\n"
			}
			t.Text = jsonl.Clip(t.Text+text, keepTextBytes)
			t.LastText = jsonl.Clip(text, keepTextBytes)
		}
		for _, p := range parts {
			if p.Type == "tool" {
				t.Tools = append(t.Tools, a.toolCalls(s, p, at)...)
			}
		}
	}
}

// toolCalls normalizes one tool part. apply_patch yields one call per file.
func (a *Adapter) toolCalls(s *model.RawSession, p partData, at time.Time) []model.ToolCall {
	s.ToolsUsed[p.Tool]++
	if p.State.Time.Start > 0 {
		at = ms(p.State.Time.Start)
	}
	var in toolInput
	_ = json.Unmarshal(p.State.Input, &in)
	done := p.State.Status == "completed"
	failed := p.State.Status == "error"
	base := model.ToolCall{ID: p.CallID, Name: p.Tool, Kind: model.ToolOther, Time: at}

	switch p.Tool {
	case "bash":
		base.Kind, base.Command = model.ToolShell, in.Command
		switch {
		case failed:
			base.Result = model.ToolResult{Known: true, OK: false}
		case done && p.State.Metadata.Exit != nil:
			n := *p.State.Metadata.Exit
			base.Result = model.ToolResult{Known: true, OK: n == 0, ExitCode: &n, Text: jsonl.ClipMiddle(p.State.Output, keepResultBytes)}
		} // completed without an exit code, or still running: the outcome is unknown
	case "write":
		base.Kind, base.Path = model.ToolFileWrite, in.FilePath
		base.Created = p.State.Metadata.Exists != nil && !*p.State.Metadata.Exists
		base.Result = model.ToolResult{Known: done || failed, OK: done}
	case "edit":
		base.Kind, base.Path = model.ToolFileEdit, in.FilePath
		base.Result = model.ToolResult{Known: done || failed, OK: done}
	case "read":
		base.Kind, base.Path = model.ToolFileRead, in.FilePath
	case "apply_patch":
		ok := done && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.State.Output)), "error")
		var out []model.ToolCall
		for i, f := range applypatch.Parse(in.PatchText) {
			c := model.ToolCall{ID: fmt.Sprintf("%s#%d", p.CallID, i), Name: p.Tool, Kind: model.ToolFileEdit, Path: f.Path, Time: at,
				Result: model.ToolResult{Known: done || failed, OK: ok}}
			if f.Op == applypatch.Add {
				c.Kind, c.Created = model.ToolFileWrite, true
			}
			out = append(out, c)
		}
		return out
	default:
		if adapters.IsIssueTool(p.Tool) {
			base.Kind, base.Input = model.ToolIssue, adapters.TrimIssueInput(p.State.Input)
			base.Result = model.ToolResult{Known: done || failed, OK: done, Text: jsonl.Clip(p.State.Output, keepIssueBytes)}
		}
	}
	return []model.ToolCall{base}
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
