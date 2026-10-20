// Package chatgpt reads the ChatGPT data export (Settings > Data controls >
// Export data).
//
// The layout is not formally documented and has changed over time. This parser
// follows the commonly seen shape: conversations.json (or numbered shards
// conversations-000.json ...) holding an array of {id, title, create_time,
// update_time, current_node, gizmo_id, mapping: {id: {message, parent,
// children}}}, where the visible conversation is the parent chain from
// current_node. It is deliberately tolerant: unknown shapes are skipped with a
// warning. Only the conversation files are opened: user.json (account
// details), chat.html, feedback and shared-conversation files are never read.
package chatgpt

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OWNER/goldengate/internal/adapters/export"
	"github.com/OWNER/goldengate/internal/model"
)

// Name is the adapter's registry name.
const Name = "chatgpt"

const (
	keepTextBytes = 16 << 10
	fallback      = "Chats"
	maxWalk       = 200_000
)

// New builds the adapter over imported export archives.
func New(paths []string) *export.Adapter { return export.New(parser{}, paths, export.DefaultLimits()) }

type parser struct{}

func (parser) Source() string { return Name }

var reShard = regexp.MustCompile(`^conversations-\d{1,6}\.json$`)

func (parser) Files(a *export.Archive) []string {
	var out []string
	for _, n := range a.Names() {
		if n == "conversations.json" || reShard.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (parser) Prepare(*export.Archive) (any, error) { return nil, nil }

// flexTime reads the export's timestamps: Unix seconds as a number, a numeric
// string, or null. Anything else counts as "unknown" instead of failing the conversation.
type flexTime float64

func (f *flexTime) UnmarshalJSON(b []byte) error {
	var n float64
	if json.Unmarshal(b, &n) == nil {
		*f = flexTime(n)
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			*f = flexTime(v)
		}
	}
	return nil
}

type content struct {
	ContentType string            `json:"content_type"`
	Parts       []json.RawMessage `json:"parts"`
	Text        string            `json:"text"`
}

type message struct {
	Author struct {
		Role string `json:"role"`
	} `json:"author"`
	CreateTime flexTime `json:"create_time"`
	Content    content  `json:"content"`
	Recipient  string   `json:"recipient"`
	Metadata   struct {
		Hidden bool `json:"is_visually_hidden_from_conversation"`
	} `json:"metadata"`
}

type node struct {
	Message  *message `json:"message"`
	Parent   string   `json:"parent"`
	Children []string `json:"children"`
}

type conversation struct {
	ID          string          `json:"id"`
	ConvID      string          `json:"conversation_id"`
	Title       string          `json:"title"`
	CreateTime  flexTime        `json:"create_time"`
	UpdateTime  flexTime        `json:"update_time"`
	CurrentNode string          `json:"current_node"`
	GizmoID     string          `json:"gizmo_id"`
	Mapping     map[string]node `json:"mapping"`
}

func decode(raw []byte) (conversation, error) {
	var c conversation
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("unknown conversation shape: %w", err)
	}
	if c.ID == "" {
		c.ID = c.ConvID
	}
	if c.ID == "" {
		return c, errors.New("conversation without an id")
	}
	return c, nil
}

func unix(v flexTime) time.Time {
	f := float64(v)
	if f <= 0 || f > 4e9 {
		return time.Time{}
	}
	return time.Unix(int64(f), int64((f-float64(int64(f)))*1e9)).UTC()
}

func bucket(c conversation) string {
	g := c.GizmoID
	switch {
	case strings.HasPrefix(g, "g-p-"): // a ChatGPT Project: the export carries its id, not its name
		return Name + ":" + export.Bucket("project-"+short(strings.TrimPrefix(g, "g-p-")), fallback)
	case g != "":
		return Name + ":" + export.Bucket("gpt-"+short(strings.TrimPrefix(g, "g-")), fallback)
	}
	return Name + ":" + fallback
}

func short(id string) string {
	id = strings.Map(func(r rune) rune {
		if r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return -1
	}, id)
	return id[:min(8, len(id))]
}

func (parser) Head(raw []byte, _ any) (export.Head, error) {
	c, err := decode(raw)
	if err != nil {
		return export.Head{}, err
	}
	created, updated := unix(c.CreateTime), unix(c.UpdateTime)
	if updated.IsZero() {
		updated = created
	}
	return export.Head{ID: c.ID, Title: c.Title, Created: created, Updated: updated, Messages: len(c.Mapping), Project: bucket(c)}, nil
}

// visibleChain returns the nodes of the active branch, oldest first: the
// parent chain from current_node, or from the newest leaf when current_node is
// missing. Cycles and absurd depths are cut off.
func visibleChain(c conversation) []node {
	cur := c.CurrentNode
	if _, ok := c.Mapping[cur]; !ok {
		cur = newestLeaf(c.Mapping)
	}
	var chain []node
	seen := map[string]bool{}
	for cur != "" && !seen[cur] && len(chain) < maxWalk {
		seen[cur] = true
		n, ok := c.Mapping[cur]
		if !ok {
			break
		}
		chain = append(chain, n)
		cur = n.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func newestLeaf(m map[string]node) string {
	best, bestT := "", flexTime(-1)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic on ties
	for _, k := range keys {
		n := m[k]
		if len(n.Children) != 0 {
			continue
		}
		t := flexTime(0)
		if n.Message != nil {
			t = n.Message.CreateTime
		}
		if t > bestT {
			best, bestT = k, t
		}
	}
	return best
}

// text returns the human-visible text of a message, or "" when it is not conversation.
func (m *message) text() string {
	if m.Metadata.Hidden {
		return ""
	}
	switch m.Content.ContentType {
	case "text", "multimodal_text", "":
	default:
		return "" // code, execution_output, browsing, thoughts, reasoning_recap, user_editable_context, ...
	}
	var parts []string
	for _, p := range m.Content.Parts {
		var s string
		if json.Unmarshal(p, &s) == nil && strings.TrimSpace(s) != "" { // image and file parts are objects: skipped
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 && m.Content.Text != "" {
		parts = append(parts, m.Content.Text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (parser) Load(raw []byte, _ any, _ export.Head) (*model.RawSession, error) {
	c, err := decode(raw)
	if err != nil {
		return nil, err
	}
	if len(c.Mapping) == 0 {
		return nil, errors.New("conversation has no messages (unknown shape)")
	}
	s := &model.RawSession{Title: strings.TrimSpace(c.Title), SourceVersion: "export", ToolsUsed: map[string]int{}}
	for _, n := range visibleChain(c) {
		m := n.Message
		if m == nil {
			continue
		}
		at := unix(m.CreateTime)
		if m.Author.Role == "assistant" && m.Recipient != "" && m.Recipient != "all" {
			s.ToolsUsed[m.Recipient]++ // a call to a tool (python, browser...), not prose
			continue
		}
		text := m.text()
		if text == "" {
			continue
		}
		switch m.Author.Role {
		case "user":
			s.Turns = append(s.Turns, model.Turn{Role: "user", Text: clip(text), Time: at})
		case "assistant":
			if len(s.Turns) == 0 || s.Turns[len(s.Turns)-1].Role != "assistant" {
				s.Turns = append(s.Turns, model.Turn{Role: "assistant", Time: at})
			}
			t := &s.Turns[len(s.Turns)-1]
			if t.Text != "" {
				t.Text += "\n\n"
			}
			t.Text = clip(t.Text + text)
			t.LastText = clip(text)
		}
	}
	return s, nil
}

func clip(s string) string {
	if len(s) <= keepTextBytes {
		return s
	}
	return strings.ToValidUTF8(s[:keepTextBytes], "") + "…"
}
