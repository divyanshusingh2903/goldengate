package model

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)

func good() *SessionPayload {
	return &SessionPayload{
		Source: "claude-code", SessionID: "abc-123", Project: "~/code/app", Level: LevelSummary,
		StartedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute), ContentHash: strings.Repeat("a", 64),
		Summary: &Summary{Title: "Fix login", Goal: "g", FilesTouched: []string{"a.go"}, ToolsUsed: map[string]int{"Bash": 2}},
		WorkItems: []WorkItem{{Kind: KindPR, Provider: "github", Key: "o/r#1", Status: StatusMerged, URL: "https://github.com/o/r/pull/1",
			ObservedAt: now, Evidence: []Evidence{{SessionID: "abc-123", Via: ViaToolResult}}}},
	}
}

func TestValidateAcceptsGood(t *testing.T) {
	if err := good().Validate(now); err != nil {
		t.Fatal(err)
	}
	p := good()
	p.Level, p.Summary, p.WorkItems, p.TitleOnly = LevelTitlesOnly, nil, nil, "just a title"
	if err := p.Validate(now); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*SessionPayload){
		"unknown source":     func(p *SessionPayload) { p.Source = "evil" },
		"bad session id":     func(p *SessionPayload) { p.SessionID = "../x" },
		"empty project":      func(p *SessionPayload) { p.Project = "" },
		"control char":       func(p *SessionPayload) { p.Summary.Title = "a\x1b[31mb" },
		"newline in title":   func(p *SessionPayload) { p.Summary.Title = "a\nb" },
		"bidi override":      func(p *SessionPayload) { p.Summary.Goal = "a\u202eb" },
		"zero width":         func(p *SessionPayload) { p.Summary.Goal = "a\u200bb" },
		"bad utf8":           func(p *SessionPayload) { p.Summary.Goal = "a\xffb" },
		"too long title":     func(p *SessionPayload) { p.Summary.Title = strings.Repeat("x", MaxTitleLen+1) },
		"future time":        func(p *SessionPayload) { p.UpdatedAt = now.Add(48 * time.Hour) },
		"ancient time":       func(p *SessionPayload) { p.StartedAt = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC) },
		"bad hash":           func(p *SessionPayload) { p.ContentHash = "xyz" },
		"unknown level":      func(p *SessionPayload) { p.Level = "full" },
		"unknown origin":     func(p *SessionPayload) { p.Origin = "robot" },
		"summary missing":    func(p *SessionPayload) { p.Summary = nil },
		"too many items":     func(p *SessionPayload) { p.WorkItems = make([]WorkItem, MaxWorkItemCount+1) },
		"bad kind":           func(p *SessionPayload) { p.WorkItems[0].Kind = "script" },
		"bad status":         func(p *SessionPayload) { p.WorkItems[0].Status = "pwned" },
		"http url":           func(p *SessionPayload) { p.WorkItems[0].URL = "http://github.com/x" },
		"javascript url":     func(p *SessionPayload) { p.WorkItems[0].URL = "javascript:alert(1)" },
		"credential url":     func(p *SessionPayload) { p.WorkItems[0].URL = "https://user:pw@github.com/x" },
		"too much evidence":  func(p *SessionPayload) { p.WorkItems[0].Evidence = make([]Evidence, 4) },
		"title only + items": func(p *SessionPayload) { p.Level = LevelTitlesOnly },
		"too many files":     func(p *SessionPayload) { p.Summary.FilesTouched = make([]string, MaxFiles+1) },
	}
	for name, mutate := range cases {
		p := good()
		mutate(p)
		err := p.Validate(now)
		if err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}
