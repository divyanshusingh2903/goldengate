// Package antigravity reads Google Antigravity's agent conversations, from
// the IDE and from its agy command-line tool.
//
// Verified on Linux (DESIGN.md: verify on disk). Both keep the same layout,
// the IDE under ~/.gemini/antigravity and agy under ~/.gemini/antigravity-cli:
//
//   - conversation_summaries.db, table conversation_summaries, has one row
//     per conversation: its id, title, workspace folders (workspace_uris, a
//     JSON list of file:// URIs) and, for a subagent, its parent
//     (parent_conversation_id, nesting_depth). The IDE's table also lists
//     agy's conversations, whose files are in agy's directory.
//   - brain/<id>/.system_generated/logs/transcript_full.jsonl is the
//     transcript Antigravity writes itself: one JSON object per step, with
//     type, status, created_at and content. A PLANNER_RESPONSE step lists
//     its tool calls (name and JSON args); each call is followed, in order
//     and within the same turn, by a GENERIC step holding its result, whose
//     status is DONE, ERROR, INVALID or RUNNING. A shell result starts with
//     "Command exited with code N.".
//   - conversations/<id>.db is Antigravity's own record (protobuf blobs).
//     Only trajectory_metadata_blob is read from it, for the git remote and
//     branch the conversation ran on.
//   - Older conversations are conversations/<id>.pb, which are encrypted and
//     have no transcript. They are not read; Detect counts them.
//
// The macOS and Windows locations follow the same home-relative path and were
// not verified. The adapter is read-only: it opens the databases in read-only
// mode and reads nothing else in those directories (which also hold sign-in
// tokens).
package antigravity

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite" // the database driver

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/jsonl"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Name is the source name of Antigravity sessions.
const Name = "antigravity"

// NoFolder is the project key of conversations started without a folder.
const NoFolder = Name + ":no-folder"

const (
	indexFile       = "conversation_summaries.db"
	maxTranscript   = 32 << 20 // a transcript larger than this is not read
	keepResultBytes = 4 << 10
)

// Options configure where Antigravity's data is. Empty fields use the
// defaults.
type Options struct {
	// Dirs are the data directories to read, in order: the IDE's and agy's.
	Dirs []string
}

// Adapter reads Antigravity conversations.
type Adapter struct {
	dirs []string
}

// New returns an adapter for the given locations.
func New(o Options) *Adapter {
	if len(o.Dirs) == 0 {
		home := pathutil.Home()
		o.Dirs = []string{filepath.Join(home, ".gemini", "antigravity"), filepath.Join(home, ".gemini", "antigravity-cli")}
	}
	return &Adapter{dirs: o.Dirs}
}

// Name implements adapters.Adapter.
func (a *Adapter) Name() string { return Name }

// Detect implements adapters.Adapter.
func (a *Adapter) Detect(ctx context.Context) (adapters.Detection, error) {
	if err := ctx.Err(); err != nil {
		return adapters.Detection{}, err
	}
	d := adapters.Detection{}
	for _, dir := range a.dirs {
		db, err := open(ctx, filepath.Join(dir, indexFile))
		if err != nil {
			continue
		}
		var n int
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='conversation_summaries'`).Scan(&n)
		_ = db.Close()
		if err != nil || n != 1 {
			d.Notes = append(d.Notes, "this Antigravity version stores conversations in a layout that is not supported (no conversation_summaries table)")
			continue
		}
		if !d.Installed {
			d.Installed, d.Root = true, dir
		}
	}
	if !d.Installed {
		if len(a.dirs) > 0 && d.Root == "" {
			d.Root = a.dirs[0]
		}
		if len(d.Notes) == 0 {
			d.Notes = []string{"Antigravity's conversation_summaries.db not found"}
		}
		return d, nil
	}
	if n := a.oldConversations(); n > 0 {
		d.Notes = append(d.Notes, fmt.Sprintf("%d older conversation(s) in an encrypted format are not read", n))
	}
	return d, nil
}

// oldConversations counts conversations kept in the older, encrypted format.
func (a *Adapter) oldConversations() int {
	n := 0
	for _, dir := range a.dirs {
		m, _ := filepath.Glob(filepath.Join(dir, "conversations", "*.pb"))
		n += len(m)
	}
	return n
}

// open opens a database read-only. A busy timeout keeps a running
// Antigravity from making reads fail.
func open(ctx context.Context, path string) (*sql.DB, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, &adapters.SkipWarning{Path: path, Reason: "not a regular file"}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(3000)")
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

// reID is a conversation id that is safe to join into a path: it cannot be
// "." or "..", nor hold a separator.
var reID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func transcriptPath(dir, id string) string {
	return filepath.Join(dir, "brain", id, ".system_generated", "logs", "transcript_full.jsonl")
}

// ListSessions implements adapters.Adapter. It reads the index and stats
// each transcript; it never opens one, so the folders a user does not share
// are not read. Subagent conversations and conversations without a
// transcript (the older format) are not listed.
func (a *Adapter) ListSessions(ctx context.Context, opt adapters.ListOpts) iter.Seq2[model.SessionRef, error] {
	return func(yield func(model.SessionRef, error) bool) {
		seen := map[string]bool{}
		for _, dir := range a.dirs {
			if ctx.Err() != nil {
				return
			}
			if !a.listDir(ctx, dir, opt, seen, yield) {
				return
			}
		}
	}
}

type indexRow struct {
	id, workspaces string
}

// listDir lists one data directory; false stops the listing.
func (a *Adapter) listDir(ctx context.Context, dir string, opt adapters.ListOpts, seen map[string]bool, yield func(model.SessionRef, error) bool) bool {
	path := filepath.Join(dir, indexFile)
	db, err := open(ctx, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		return yield(model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: err.Error()})
	}
	rows, err := db.QueryContext(ctx, `SELECT conversation_id, workspace_uris FROM conversation_summaries
		WHERE parent_conversation_id = '' AND nesting_depth = 0 ORDER BY last_modified_time, conversation_id`)
	if err != nil {
		_ = db.Close()
		return yield(model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: "cannot list conversations: " + err.Error()})
	}
	var all []indexRow
	for rows.Next() {
		var id, ws sql.NullString
		if err := rows.Scan(&id, &ws); err != nil {
			_ = rows.Close()
			_ = db.Close()
			return yield(model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: "cannot read a conversation row: " + err.Error()})
		}
		all = append(all, indexRow{id: id.String, workspaces: ws.String})
	}
	err = rows.Close()
	_ = db.Close()
	if err != nil {
		return yield(model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: err.Error()})
	}
	for _, r := range all {
		if ctx.Err() != nil {
			return false
		}
		if !reID.MatchString(r.id) || !model.ValidSessionID(r.id) {
			if !yield(model.SessionRef{}, &adapters.SkipWarning{Path: path, Reason: "conversation with an unusable id"}) {
				return false
			}
			continue
		}
		if seen[r.id] {
			continue
		}
		// The transcript decides where a conversation lives: the IDE's
		// index also lists agy's conversations, whose files are elsewhere.
		tp := transcriptPath(dir, r.id)
		st, err := os.Lstat(tp)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		seen[r.id] = true
		ref := model.SessionRef{Source: Name, ID: r.id, ProjectKey: projectKey(r.workspaces), Path: tp,
			UpdatedAt: st.ModTime().UTC(), SizeBytes: st.Size()}
		if !opt.Since.IsZero() && ref.UpdatedAt.Before(opt.Since) {
			continue
		}
		if !yield(ref, nil) {
			return false
		}
	}
	return true
}

// projectKey is the conversation's first workspace folder, or NoFolder.
func projectKey(workspaces string) string {
	var uris []string
	if json.Unmarshal([]byte(workspaces), &uris) != nil || len(uris) == 0 {
		return NoFolder
	}
	if p := fileURIPath(uris[0]); p != "" {
		return pathutil.Normalize(p)
	}
	return NoFolder
}

// fileURIPath is the local path of a file:// URI, or "" for anything else.
func fileURIPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return ""
	}
	p := u.Path
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' { // /C:/Users/... on Windows
		p = p[1:]
	}
	p = filepath.FromSlash(p)
	if !filepath.IsAbs(p) {
		return ""
	}
	return p
}

// step is one line of a transcript.
type step struct {
	Type      string `json:"type"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Content   string `json:"content"`
	ToolCalls []struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
}

// dirOf is the data directory a listed transcript belongs to.
func (a *Adapter) dirOf(ref model.SessionRef) (string, bool) {
	if !reID.MatchString(ref.ID) {
		return "", false
	}
	for _, d := range a.dirs {
		if transcriptPath(d, ref.ID) == ref.Path {
			return d, true
		}
	}
	return "", false
}

// Load implements adapters.Adapter.
func (a *Adapter) Load(ctx context.Context, ref model.SessionRef) (*model.RawSession, error) {
	dir, ok := a.dirOf(ref)
	if !ok {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "not an Antigravity transcript"}
	}
	st, err := os.Lstat(ref.Path)
	if err != nil {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "conversation is gone"}
	}
	if !st.Mode().IsRegular() {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "not a regular file"}
	}
	if st.Size() > maxTranscript {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "transcript too large"}
	}
	f, err := os.Open(ref.Path) //nolint:gosec // G304: a transcript under Antigravity's own directory, named by a validated id
	if err != nil {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: err.Error()}
	}
	defer func() { _ = f.Close() }()

	s := &model.RawSession{Ref: ref, ToolsUsed: map[string]int{}}
	var p parser
	p.s = s
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256<<10), maxTranscript) // a single step can be hundreds of kilobytes
	first := true
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var stp step
		if err := json.Unmarshal(line, &stp); err != nil || stp.Type == "" {
			return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "transcript line of an unknown shape"}
		}
		at, _ := time.Parse(time.RFC3339Nano, stp.CreatedAt)
		at = at.UTC()
		if first && !at.IsZero() {
			s.Ref.StartedAt = at
			first = false
		}
		p.add(stp, at)
	}
	if err := sc.Err(); err != nil {
		return nil, &adapters.SkipWarning{Path: ref.Path, Reason: "cannot read the transcript: " + err.Error()}
	}
	if p.unknown > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%d step(s) of an unknown type were skipped", p.unknown))
	}
	collectFiles(s)
	s.Title = a.title(ctx, dir, ref.ID)
	s.GitRemote, s.GitBranch = gitInfo(ctx, filepath.Join(dir, "conversations", ref.ID+".db"))
	return s, nil
}

// pendingCall is a tool call still waiting for its result.
type pendingCall struct{ turn, tool int }

type parser struct {
	s       *model.RawSession
	pending []pendingCall
	unknown int
}

func (p *parser) add(stp step, at time.Time) {
	s := p.s
	switch stp.Type {
	case "USER_INPUT":
		p.pending = nil
		if text := userText(stp.Content); text != "" {
			s.Turns = append(s.Turns, model.Turn{Role: "user", Text: text, Time: at})
		}
	case "PLANNER_RESPONSE":
		// A new response starts a new round of tool calls: results are
		// paired within it, so a call that never got one cannot shift the
		// next round's results.
		p.pending = nil
		t := assistantTurn(s, at)
		if text := strings.TrimSpace(stp.Content); text != "" {
			if t.Text != "" {
				t.Text += "\n\n"
			}
			t.Text += text
			t.LastText = text
		}
		for _, c := range stp.ToolCalls {
			if c.Name == "" {
				continue
			}
			t.Tools = append(t.Tools, toolCall(c.Name, c.Args, at))
			s.ToolsUsed[c.Name]++
			p.pending = append(p.pending, pendingCall{turn: len(s.Turns) - 1, tool: len(t.Tools) - 1})
		}
	case "GENERIC":
		if len(p.pending) == 0 {
			return
		}
		pc := p.pending[0]
		p.pending = p.pending[1:]
		applyResult(&s.Turns[pc.turn].Tools[pc.tool], stp.Status, stp.Content)
	case "SYSTEM_MESSAGE", "ERROR_MESSAGE", "CHECKPOINT":
		// harness notices, and a summary the model wrote of the
		// conversation so far: not part of what was said or done
	default:
		p.unknown++
	}
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

const (
	openRequest  = "<USER_REQUEST>"
	closeRequest = "</USER_REQUEST>"
	metaStart    = "<ADDITIONAL_METADATA>"
)

// userText is what the user typed. Antigravity wraps it in <USER_REQUEST>
// and appends harness metadata (time, settings changes) after it.
func userText(content string) string {
	i := strings.Index(content, openRequest)
	if i < 0 {
		if j := strings.Index(content, metaStart); j >= 0 {
			content = content[:j]
		}
		return strings.TrimSpace(content)
	}
	rest := content[i+len(openRequest):]
	if j := strings.Index(rest, closeRequest); j >= 0 {
		return strings.TrimSpace(rest[:j])
	}
	if j := strings.Index(rest, metaStart); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// toolCall maps a call by what was seen on disk: the shell, and reading,
// editing and writing files.
func toolCall(name string, raw json.RawMessage, at time.Time) model.ToolCall {
	c := model.ToolCall{Name: name, Kind: model.ToolOther, Time: at}
	var args struct {
		CommandLine  string `json:"CommandLine"`
		Path         string `json:"Path"`
		AbsolutePath string `json:"AbsolutePath"`
	}
	_ = json.Unmarshal(raw, &args)
	switch name {
	case "run_command":
		c.Kind, c.Command = model.ToolShell, args.CommandLine
	case "read_file":
		c.Kind, c.Path = model.ToolFileRead, args.Path
	case "view_file":
		c.Kind, c.Path = model.ToolFileRead, args.AbsolutePath
	case "edit_file":
		c.Kind, c.Path = model.ToolFileEdit, args.Path
	case "write_file":
		c.Kind, c.Path = model.ToolFileWrite, args.Path
	default:
		if adapters.IsIssueTool(name) {
			c.Kind, c.Input = model.ToolIssue, adapters.TrimIssueInput(raw)
		}
	}
	return c
}

var reExit = regexp.MustCompile(`^Command exited with code (-?\d+)\.`)

// applyResult records a call's result from its GENERIC step.
func applyResult(c *model.ToolCall, status, content string) {
	body := resultBody(content)
	text := jsonl.ClipMiddle(body, keepResultBytes)
	switch status {
	case "ERROR", "INVALID":
		c.Result = model.ToolResult{Known: true, OK: false, Text: text}
	case "DONE":
		switch c.Kind {
		case model.ToolShell:
			if m := reExit.FindStringSubmatch(body); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					c.Result = model.ToolResult{Known: true, OK: n == 0, ExitCode: &n, Text: text}
					return
				}
			}
			// cut short or stopped: what happened is not known
			c.Result = model.ToolResult{Text: text}
		case model.ToolFileWrite:
			c.Created = strings.HasPrefix(body, "Created file.")
			c.Result = model.ToolResult{Known: true, OK: true, Text: text}
		default:
			c.Result = model.ToolResult{Known: true, OK: true, Text: text}
		}
	default: // RUNNING (a background command), or a status not seen yet
		c.Result = model.ToolResult{Text: text}
	}
}

// resultBody drops the "Created At:" and "Completed At:" lines a result
// starts with.
func resultBody(content string) string {
	for {
		line, rest, ok := strings.Cut(content, "\n")
		if !ok || (!strings.HasPrefix(line, "Created At:") && !strings.HasPrefix(line, "Completed At:")) {
			return strings.TrimLeft(content, "\n")
		}
		content = rest
	}
}

// collectFiles lists project-relative files whose edit or write succeeded.
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

// title is the conversation's title from the index of its directory, or of
// another one.
func (a *Adapter) title(ctx context.Context, dir, id string) string {
	for _, d := range append([]string{dir}, a.dirs...) {
		db, err := open(ctx, filepath.Join(d, indexFile))
		if err != nil {
			continue
		}
		var t sql.NullString
		err = db.QueryRowContext(ctx, `SELECT title FROM conversation_summaries WHERE conversation_id = ?`, id).Scan(&t)
		_ = db.Close()
		if err == nil && strings.TrimSpace(t.String) != "" {
			return strings.TrimSpace(t.String)
		}
	}
	return ""
}

// gitInfo reads the remote and branch Antigravity recorded for the
// conversation: in trajectory_metadata_blob, field 1 (the workspace) holds
// field 3 (the repository: 1 its slug, 2 its remote) and field 4 (the
// branch). Anything unreadable leaves both empty.
func gitInfo(ctx context.Context, path string) (remote, branch string) {
	db, err := open(ctx, path)
	if err != nil {
		return "", ""
	}
	defer func() { _ = db.Close() }()
	var blob []byte
	if err := db.QueryRowContext(ctx, `SELECT data FROM trajectory_metadata_blob ORDER BY id = 'main' DESC LIMIT 1`).Scan(&blob); err != nil {
		return "", ""
	}
	for _, ws := range fieldBytes(blob, 1) {
		for _, repo := range fieldBytes(ws, 3) {
			if r := fieldBytes(repo, 2); len(r) > 0 && remote == "" {
				remote = string(r[0])
			}
		}
		if b := fieldBytes(ws, 4); len(b) > 0 && branch == "" {
			branch = string(b[0])
		}
		if remote != "" || branch != "" {
			break
		}
	}
	if !printable(remote) || len(remote) > 512 {
		remote = ""
	}
	if !printable(branch) || len(branch) > 256 {
		branch = ""
	}
	return stripCredentials(remote), branch
}

// fieldBytes returns the values of a length-delimited field of a protobuf
// message; nil when the message cannot be parsed.
func fieldBytes(b []byte, want protowire.Number) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil
		}
		b = b[n:]
		if typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil
			}
			if num == want {
				out = append(out, v)
			}
			b = b[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return nil
		}
		b = b[m:]
	}
	return out
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\uFFFD' {
			return false
		}
	}
	return true
}

// stripCredentials removes a user and password from a remote URL. An
// scp-style git@host:path remote carries no secret and is kept.
func stripCredentials(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Scheme != "file" {
		u.User = nil
		return u.String()
	}
	return raw
}
