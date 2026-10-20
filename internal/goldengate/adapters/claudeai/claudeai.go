// Package claudeai reads the claude.ai data export (Settings > Privacy >
// Export data).
//
// The layout is not formally documented. This parser follows the commonly
// seen shape (conversations.json: an array of {uuid, name, created_at,
// updated_at, chat_messages: [{sender, text, content: [...], created_at,
// attachments, files}]}; projects.json: [{uuid, name}]) and is deliberately
// tolerant: anything it does not recognize is skipped with a warning. Only
// conversations.json and projects.json are ever opened: users.json (account
// details) is never read, and project documents are never decoded.
package claudeai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/export"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/jsonl"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// Name is the adapter's registry name.
const Name = "claude-ai"

const (
	keepTextBytes = 16 << 10
	fallback      = "Chats"
)

// New builds the adapter over imported export archives.
func New(paths []string) *export.Adapter { return export.New(parser{}, paths, export.DefaultLimits()) }

type parser struct{}

func (parser) Source() string { return Name }

func (parser) Files(a *export.Archive) []string {
	if a.Has("conversations.json") {
		return []string{"conversations.json"}
	}
	return nil
}

type project struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// Prepare maps project ids to names. Missing or odd projects.json is fine.
func (parser) Prepare(a *export.Archive) (any, error) {
	names := map[string]string{}
	if !a.Has("projects.json") {
		return names, nil
	}
	b, err := a.Read("projects.json")
	if err != nil {
		return names, nil // a damaged project list only costs us the names
	}
	var ps []project
	if json.Unmarshal(b, &ps) == nil {
		for _, p := range ps {
			if p.UUID != "" {
				names[p.UUID] = p.Name
			}
		}
	}
	return names, nil
}

type attachment struct {
	FileName string `json:"file_name"`
}

type block struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type message struct {
	Sender      string       `json:"sender"`
	Text        string       `json:"text"`
	CreatedAt   string       `json:"created_at"`
	Content     []block      `json:"content"`
	Attachments []attachment `json:"attachments"`
	Files       []attachment `json:"files"`
}

type conversation struct {
	UUID        string    `json:"uuid"`
	Name        string    `json:"name"`
	CreatedAt   string    `json:"created_at"`
	UpdatedAt   string    `json:"updated_at"`
	ProjectUUID string    `json:"project_uuid"`
	Project     *project  `json:"project"`
	Messages    []message `json:"chat_messages"`
}

func decode(raw []byte) (conversation, error) {
	var c conversation
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("unknown conversation shape: %w", err)
	}
	if c.UUID == "" {
		return c, errors.New("conversation without a uuid")
	}
	return c, nil
}

func (parser) Head(raw []byte, ctx any) (export.Head, error) {
	c, err := decode(raw)
	if err != nil {
		return export.Head{}, err
	}
	created, updated := jsonl.ParseTime(c.CreatedAt), jsonl.ParseTime(c.UpdatedAt)
	if updated.IsZero() {
		updated = created
		for _, m := range c.Messages {
			if t := jsonl.ParseTime(m.CreatedAt); t.After(updated) {
				updated = t
			}
		}
	}
	return export.Head{ID: c.UUID, Title: c.Name, Created: created, Updated: updated, Messages: len(c.Messages), Project: bucket(c, ctx)}, nil
}

func bucket(c conversation, ctx any) string {
	names, _ := ctx.(map[string]string)
	id, name := c.ProjectUUID, ""
	if c.Project != nil {
		id, name = jsonl.FirstNonEmpty(c.Project.UUID, id), c.Project.Name
	}
	if name == "" && id != "" {
		name = names[id]
		if name == "" {
			t := strings.TrimPrefix(id, "-")
			name = "project-" + t[:min(8, len(t))]
		}
	}
	return Name + ":" + export.Bucket(name, fallback)
}

func (parser) Load(raw []byte, ctx any, h export.Head) (*model.RawSession, error) {
	c, err := decode(raw)
	if err != nil {
		return nil, err
	}
	s := &model.RawSession{Title: strings.TrimSpace(c.Name), SourceVersion: "export", ToolsUsed: map[string]int{}}
	for _, m := range c.Messages {
		at := jsonl.ParseTime(m.CreatedAt)
		text := messageText(m)
		switch m.Sender {
		case "human":
			if text != "" {
				s.Turns = append(s.Turns, model.Turn{Role: "user", Text: jsonl.Clip(text, keepTextBytes), Time: at})
			}
			// uploads are recorded by file name only; their contents are never read
			if len(m.Attachments)+len(m.Files) > 0 {
				addUploads(s, at, m)
			}
		case "assistant":
			t := assistantTurn(s, at)
			if text != "" {
				if t.Text != "" {
					t.Text += "\n\n"
				}
				t.Text = jsonl.Clip(t.Text+text, keepTextBytes)
				t.LastText = jsonl.Clip(text, keepTextBytes)
			}
			for _, b := range m.Content {
				if b.Type == "tool_use" {
					s.ToolsUsed[b.Name]++
					if b.Name == "artifacts" {
						t.Tools = append(t.Tools, model.ToolCall{Name: b.Name, Kind: model.ToolArtifact, Time: at,
							Input: artifactInput(b.Input), Result: model.ToolResult{Known: true, OK: true}})
					}
				}
			}
		}
	}
	return s, nil
}

func assistantTurn(s *model.RawSession, at time.Time) *model.Turn {
	if len(s.Turns) == 0 || s.Turns[len(s.Turns)-1].Role != "assistant" {
		s.Turns = append(s.Turns, model.Turn{Role: "assistant", Time: at})
	}
	return &s.Turns[len(s.Turns)-1]
}

func messageText(m message) string {
	var parts []string
	for _, b := range m.Content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(m.Text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// artifactInput keeps an artifact's identity (id, title, command, type) and
// drops its content.
func artifactInput(raw json.RawMessage) []byte {
	var in struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Command string `json:"command"`
		Type    string `json:"type"`
	}
	_ = json.Unmarshal(raw, &in)
	b, _ := json.Marshal(in)
	return b
}

func addUploads(s *model.RawSession, at time.Time, m message) {
	seen := map[string]bool{}
	for _, f := range append(append([]attachment{}, m.Attachments...), m.Files...) {
		name := strings.TrimSpace(f.FileName)
		if name == "" || len(name) > 100 || seen[name] {
			continue
		}
		seen[name] = true
		in, _ := json.Marshal(map[string]string{"id": "upload:" + name, "title": name, "command": "create"})
		t := assistantTurnOrUser(s, at)
		t.Tools = append(t.Tools, model.ToolCall{Name: "upload", Kind: model.ToolArtifact, Time: at, Input: in, Result: model.ToolResult{Known: true, OK: true}})
		s.ToolsUsed["upload"]++
	}
}

// assistantTurnOrUser attaches an upload to the user turn that carried it, or to a new one.
func assistantTurnOrUser(s *model.RawSession, at time.Time) *model.Turn {
	if n := len(s.Turns); n > 0 && s.Turns[n-1].Role == "user" {
		return &s.Turns[n-1]
	}
	s.Turns = append(s.Turns, model.Turn{Role: "user", Time: at})
	return &s.Turns[len(s.Turns)-1]
}
