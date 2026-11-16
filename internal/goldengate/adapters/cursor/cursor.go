// Package cursor reads Cursor's agent conversations ("composers").
//
// Verified against Cursor 3.30 on Linux (DESIGN.md: verify on disk):
//
//   - The global state database, <user dir>/globalStorage/state.vscdb, has a
//     table composerHeaders with one row per conversation: its id, the
//     workspace it belongs to, creation and update times, and a JSON value
//     with the conversation's name and the workspace folder
//     (workspaceIdentifier.uri.fsPath).
//   - The messages are rows of the key-value table cursorDiskKV under
//     "bubbleId:<conversation>:<bubble>", in insertion order: type 1 is the
//     user, type 2 the assistant. An assistant bubble holds text, thinking,
//     or one tool call (toolFormerData: name, status, params and result as
//     JSON strings).
//   - A conversation started from the agent sidebar may have no user bubble.
//     Its prompts are then only in the transcript Cursor writes to
//     ~/.cursor/projects/<slug>/agent-transcripts/<id>/<id>.jsonl.
//
// What the format does not record, and what follows from it:
//
//   - A shell call has a status and its output but no exit code. Whether a
//     command that exits non-zero is stored differently from one that
//     succeeds was not established, so a Cursor shell result is never taken
//     as proof (ToolResult.Known is false). Commits and pull requests from
//     Cursor sessions are therefore "mentioned" until that is verified.
//   - The git branch is not recorded.
//   - File-editing tools were not present in the inspected conversations, so
//     they are not mapped: FilesTouched stays empty.
//
// The adapter is read-only and opens the database in read-only mode. It
// reads the composerHeaders table, the bubbleId rows, and one version entry;
// nothing else in the database (which also holds sign-in state) is touched.
package cursor

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the database driver

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Name is the source name of Cursor sessions.
const Name = "cursor"

// FormatVersion is the storage layout this adapter was written against: the
// bubble record version. Bubbles of another version are not parsed.
const FormatVersion = 3

// NoFolder is the project key of conversations started without a folder.
const NoFolder = Name + ":no-folder"

const (
	maxBubble     = 8 << 20 // a single message record larger than this is skipped
	maxTranscript = 32 << 20
	versionKey    = "cursor/localLastActivityClientVersion"
)

// Options configure where Cursor's data is. Empty fields use the defaults.
type Options struct {
	// UserDir is Cursor's "User" directory, which holds globalStorage.
	UserDir string
	// ProjectsDir is where Cursor writes agent transcripts (~/.cursor/projects).
	ProjectsDir string
}

// Adapter reads Cursor conversations.
type Adapter struct {
	dbPath, projects string
}

// New returns an adapter for the given locations.
func New(o Options) *Adapter {
	home := pathutil.Home()
	if o.UserDir == "" {
		// Electron's per-user data directory. Only the Linux path was
		// verified on disk; the others follow the platform convention.
		switch runtime.GOOS {
		case "darwin":
			o.UserDir = filepath.Join(home, "Library", "Application Support", "Cursor", "User")
		case "windows":
			o.UserDir = filepath.Join(os.Getenv("APPDATA"), "Cursor", "User")
		default:
			base := os.Getenv("XDG_CONFIG_HOME")
			if base == "" {
				base = filepath.Join(home, ".config")
			}
			o.UserDir = filepath.Join(base, "Cursor", "User")
		}
	}
	if o.ProjectsDir == "" {
		o.ProjectsDir = filepath.Join(home, ".cursor", "projects")
	}
	return &Adapter{dbPath: filepath.Join(o.UserDir, "globalStorage", "state.vscdb"), projects: o.ProjectsDir}
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
		return adapters.Detection{Root: a.dbPath, Notes: []string{"Cursor's state.vscdb not found"}}, nil
	}
	db, err := a.open(ctx)
	if err != nil {
		return adapters.Detection{Root: a.dbPath, Notes: []string{"Cursor's state.vscdb cannot be opened: " + err.Error()}}, nil
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('composerHeaders','cursorDiskKV')`).Scan(&n); err != nil || n != 2 {
		return adapters.Detection{Root: a.dbPath, Notes: []string{"this Cursor version stores conversations in a layout that is not supported (no composerHeaders table)"}}, nil
	}
	return adapters.Detection{Installed: true, Root: a.dbPath}, nil
}

// open opens the database read-only. A busy timeout keeps a running Cursor
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

func ms(n sql.NullInt64) time.Time {
	if !n.Valid || n.Int64 <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(n.Int64).UTC()
}

// header is the part of a composerHeaders value the adapter reads.
type header struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	IsDraft   bool   `json:"isDraft"`
	Workspace struct {
		URI struct {
			FsPath string `json:"fsPath"`
			Scheme string `json:"scheme"`
		} `json:"uri"`
	} `json:"workspaceIdentifier"`
}

// bubbleRange is the key range holding one conversation's messages.
func bubbleRange(id string) (lo, hi string) {
	return "bubbleId:" + id + ":", "bubbleId:" + id + ";" // ';' follows ':' in ASCII
}

var reID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// ListSessions implements adapters.Adapter. Drafts, sub-agent conversations
// and conversations without any message are not listed.
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
		rows, err := db.QueryContext(ctx, `SELECT composerId, createdAt, lastUpdatedAt, checkpointAt, value FROM composerHeaders
			WHERE COALESCE(isSubagent, 0) = 0 ORDER BY createdAt, composerId`)
		if err != nil {
			yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: "cannot list conversations: " + err.Error()})
			return
		}
		type row struct {
			id                        string
			created, updated, checked sql.NullInt64
			value                     sql.NullString
		}
		var all []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.created, &r.updated, &r.checked, &r.value); err != nil {
				_ = rows.Close()
				yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: "cannot read a conversation header: " + err.Error()})
				return
			}
			all = append(all, r)
		}
		if err := rows.Close(); err != nil {
			yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: err.Error()})
			return
		}
		for _, r := range all {
			if ctx.Err() != nil {
				return
			}
			var h header
			if !r.value.Valid || json.Unmarshal([]byte(r.value.String), &h) != nil || h.Type != "head" {
				if !yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: "conversation header of an unknown shape"}) {
					return
				}
				continue
			}
			if h.IsDraft {
				continue
			}
			if !reID.MatchString(r.id) {
				if !yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: "conversation with an unusable id"}) {
					return
				}
				continue
			}
			lo, hi := bubbleRange(r.id)
			var n, size sql.NullInt64
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(LENGTH(value)) FROM cursorDiskKV WHERE key >= ? AND key < ?`, lo, hi).Scan(&n, &size); err != nil {
				if !yield(model.SessionRef{}, &adapters.SkipWarning{Path: a.dbPath, Reason: "cannot count messages: " + err.Error()}) {
					return
				}
				continue
			}
			if n.Int64 == 0 {
				continue // opened and never used
			}
			ref := model.SessionRef{Source: Name, ID: r.id, ProjectKey: projectKey(h), Path: a.dbPath, StartedAt: ms(r.created), SizeBytes: size.Int64}
			ref.UpdatedAt = ref.StartedAt
			for _, t := range []time.Time{ms(r.updated), ms(r.checked)} {
				if t.After(ref.UpdatedAt) {
					ref.UpdatedAt = t
				}
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

// projectKey is the conversation's folder, or NoFolder.
func projectKey(h header) string {
	p := h.Workspace.URI.FsPath
	if p == "" || (h.Workspace.URI.Scheme != "" && h.Workspace.URI.Scheme != "file") || !filepath.IsAbs(p) {
		return NoFolder
	}
	return pathutil.Normalize(p)
}

// bubble is the part of a message record the adapter reads.
type bubble struct {
	V         int    `json:"_v"`
	Type      int    `json:"type"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
	Tool      *struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Params string `json:"params"`
		Result string `json:"result"`
	} `json:"toolFormerData"`
}

// Load implements adapters.Adapter.
func (a *Adapter) Load(ctx context.Context, ref model.SessionRef) (*model.RawSession, error) {
	db, err := a.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	s := &model.RawSession{Ref: ref, ToolsUsed: map[string]int{}}
	var value sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT value FROM composerHeaders WHERE composerId = ?`, ref.ID).Scan(&value); err != nil {
		return nil, &adapters.SkipWarning{Path: a.dbPath, Reason: "conversation is gone"}
	}
	var h header
	if json.Unmarshal([]byte(value.String), &h) != nil || h.Type != "head" {
		return nil, &adapters.SkipWarning{Path: a.dbPath, Reason: "conversation header of an unknown shape"}
	}
	s.Title = h.Name
	var ver sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT value FROM ItemTable WHERE key = ?`, versionKey).Scan(&ver); err == nil && len(ver.String) <= 40 {
		s.SourceVersion = ver.String
	}

	lo, hi := bubbleRange(ref.ID)
	rows, err := db.QueryContext(ctx, `SELECT value FROM cursorDiskKV WHERE key >= ? AND key < ? ORDER BY rowid`, lo, hi)
	if err != nil {
		return nil, fmt.Errorf("cursor: read messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	users, unknown, oversized := 0, 0, 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("cursor: read message: %w", err)
		}
		if len(raw) > maxBubble {
			oversized++
			continue
		}
		var b bubble
		if json.Unmarshal(raw, &b) != nil || b.V != FormatVersion {
			unknown++
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, b.CreatedAt)
		at = at.UTC()
		switch {
		case b.Type == 1:
			if strings.TrimSpace(b.Text) != "" {
				users++
				s.Turns = append(s.Turns, model.Turn{Role: "user", Text: b.Text, Time: at})
			}
		case b.Type == 2 && b.Tool != nil:
			t := assistantTurn(s, at)
			t.Tools = append(t.Tools, toolCall(b, at))
			s.ToolsUsed[b.Tool.Name]++
		case b.Type == 2 && strings.TrimSpace(b.Text) != "":
			t := assistantTurn(s, at)
			if t.Text != "" {
				t.Text += "\n\n"
			}
			t.Text += b.Text
			t.LastText = b.Text
		case b.Type == 1 || b.Type == 2:
			// thinking, or an empty placeholder: nothing to keep
		default:
			unknown++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cursor: read messages: %w", err)
	}
	if len(s.Turns) == 0 && unknown > 0 {
		return nil, &adapters.SkipWarning{Path: a.dbPath, Reason: fmt.Sprintf("conversation in an unknown message format (expected version %d)", FormatVersion)}
	}
	if unknown > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%d message(s) of an unknown shape were skipped", unknown))
	}
	if oversized > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%d oversized message(s) were skipped", oversized))
	}
	// A conversation started from the agent sidebar has no user message in
	// the database: its prompts are in the transcript file only. Their place
	// among the assistant's messages is not recorded there, so they go
	// first, in order.
	if users == 0 {
		prompts := a.transcriptPrompts(ref.ID)
		if len(prompts) > 0 {
			front := make([]model.Turn, 0, len(prompts)+len(s.Turns))
			for _, p := range prompts {
				front = append(front, model.Turn{Role: "user", Text: p, Time: ref.StartedAt})
			}
			s.Turns = append(front, s.Turns...)
		} else if len(s.Turns) > 0 {
			s.Warnings = append(s.Warnings, "the prompt of this conversation was not found")
		}
	}
	return s, nil
}

// assistantTurn returns the assistant turn that new content belongs to: the
// last turn if it is the assistant's, else a new one.
func assistantTurn(s *model.RawSession, at time.Time) *model.Turn {
	if n := len(s.Turns); n > 0 && s.Turns[n-1].Role == "assistant" {
		return &s.Turns[n-1]
	}
	s.Turns = append(s.Turns, model.Turn{Role: "assistant", Time: at})
	return &s.Turns[len(s.Turns)-1]
}

// toolCall maps a tool bubble. Only what was seen on disk is mapped: the
// shell and file-read tools. A shell result is never marked as known,
// because the record has no exit code (see the package comment).
func toolCall(b bubble, at time.Time) model.ToolCall {
	c := model.ToolCall{Name: b.Tool.Name, Kind: model.ToolOther, Time: at}
	switch b.Tool.Name {
	case "run_terminal_command_v2":
		var p struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(b.Tool.Params), &p)
		var r struct {
			Output string `json:"output"`
		}
		_ = json.Unmarshal([]byte(b.Tool.Result), &r)
		c.Kind, c.Command, c.Result.Text = model.ToolShell, p.Command, r.Output
	case "read_file_v2":
		var p struct {
			TargetFile string `json:"targetFile"`
		}
		_ = json.Unmarshal([]byte(b.Tool.Params), &p)
		c.Kind, c.Path = model.ToolFileRead, p.TargetFile
	}
	return c
}

var reUserQuery = regexp.MustCompile(`(?s)<user_query>\s*(.*?)\s*</user_query>`)

// transcriptPrompts reads the user's prompts from the transcript file
// Cursor writes for a conversation, if there is one.
func (a *Adapter) transcriptPrompts(id string) []string {
	if a.projects == "" || !reID.MatchString(id) {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(a.projects, "*", "agent-transcripts", id, id+".jsonl"))
	for _, p := range matches {
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() || st.Size() > maxTranscript {
			continue
		}
		f, err := os.Open(p) //nolint:gosec // G304: a transcript under Cursor's own directory, named by a validated id
		if err != nil {
			continue
		}
		var out []string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), maxBubble)
		for sc.Scan() {
			var rec struct {
				Role    string `json:"role"`
				Message struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Role != "user" {
				continue
			}
			for _, c := range rec.Message.Content {
				if c.Type != "text" {
					continue
				}
				text := c.Text
				if m := reUserQuery.FindStringSubmatch(text); m != nil {
					text = m[1] // the prompt itself, without the wrapper Cursor adds
				}
				if text = strings.TrimSpace(text); text != "" {
					out = append(out, text)
				}
			}
		}
		_ = f.Close()
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
