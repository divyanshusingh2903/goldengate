package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Every source gets a short tag so that a row, a session or a setting says at
// a glance where it came from, and whether it is a coding session (a tool that
// ran in a folder) or a chat (an export from a chat app). The tag is text, so
// it survives NO_COLOR; color only adds the kind.

type sourceKind int

const (
	kindCode sourceKind = iota
	kindChat
)

func (k sourceKind) String() string { return [...]string{"code", "chat"}[k] }

type sourceMeta struct {
	tag  string
	kind sourceKind
}

var sourceMetas = map[string]sourceMeta{
	"claude-code": {"CC", kindCode},
	"codex":       {"CX", kindCode},
	"opencode":    {"OC", kindCode},
	"claude-ai":   {"AI", kindChat},
	"chatgpt":     {"GPT", kindChat},
}

func metaOf(source string) sourceMeta {
	if m, ok := sourceMetas[source]; ok {
		return m
	}
	tag := strings.ToUpper(source)
	if len(tag) > 3 {
		tag = tag[:3]
	}
	return sourceMeta{tag, kindCode}
}

func (m *Model) kindStyle(k sourceKind) lipgloss.Style {
	if k == kindChat {
		return m.th.ChatTag
	}
	return m.th.CodeTag
}

// tag renders a source's tag, e.g. "CC".
func (m *Model) tag(source string) string {
	md := metaOf(source)
	return m.kindStyle(md.kind).Render(md.tag)
}

// tags renders the tags of a row's sources, e.g. "CC CX".
func (m *Model) tags(r *Row) string {
	var parts []string
	for _, s := range r.Sources {
		parts = append(parts, m.tag(s.Name))
	}
	return strings.Join(parts, " ")
}

// rowKind says whether a project holds coding sessions or chats.
func rowKind(r *Row) sourceKind {
	if r.Virtual {
		return kindChat
	}
	for _, s := range r.Sources {
		if metaOf(s.Name).kind == kindCode {
			return kindCode
		}
	}
	if len(r.Sources) > 0 {
		return kindChat
	}
	return kindCode
}

// kindLine describes a kind for the card and settings.
func (m *Model) kindLabel(k sourceKind) string {
	if k == kindChat {
		return m.th.ChatTag.Render("chat") + m.th.Dim.Render(" · chat-app export")
	}
	return m.th.CodeTag.Render("code") + m.th.Dim.Render(" · coding-tool sessions")
}

// legend lists every tag, for help.
func (m *Model) legend() []string {
	var code, chat []string
	for _, s := range []string{"claude-code", "codex", "opencode", "claude-ai", "chatgpt"} {
		e := m.tag(s) + " " + pretty(s)
		if metaOf(s).kind == kindChat {
			chat = append(chat, e)
		} else {
			code = append(code, e)
		}
	}
	return []string{
		m.th.CodeTag.Render("code") + "  " + strings.Join(code, "  "),
		m.th.ChatTag.Render("chat") + "  " + strings.Join(chat, "  "),
	}
}
