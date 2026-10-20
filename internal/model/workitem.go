package model

import "time"

// Work item kinds.
const (
	KindCommit = "commit"
	KindPR     = "pull_request"
	KindIssue  = "issue"
	KindFile   = "file"
)

// Work item statuses. Anything above StatusMentioned needs evidence from a
// successful tool result or a harness event.
const (
	StatusMentioned = "mentioned"
	StatusCreated   = "created"
	StatusUpdated   = "updated"
	StatusMerged    = "merged"
	StatusClosed    = "closed"
	StatusCompleted = "completed"
	StatusCanceled  = "canceled"
	ViaToolResult   = "tool_result"
	ViaHarnessEvent = "harness_event"
	ViaText         = "text"
)

// Evidence records why a work item has its status.
type Evidence struct {
	SessionID string    `json:"session_id"`
	Turn      int       `json:"turn"`
	Via       string    `json:"via"`
	At        time.Time `json:"at"`
}

// WorkItem is a structured record of something that happened in a session.
// Only metadata is carried: never file or artifact contents, issue bodies or
// comments.
type WorkItem struct {
	Kind       string     `json:"kind"`
	Provider   string     `json:"provider,omitempty"`
	Key        string     `json:"key"`
	Title      string     `json:"title,omitempty"`
	URL        string     `json:"url,omitempty"`
	Status     string     `json:"status"`
	RawState   string     `json:"raw_state,omitempty"`
	Evidence   []Evidence `json:"evidence,omitempty"`
	ObservedAt time.Time  `json:"observed_at"`
}

// IsEvidenceBacked reports whether a status may only be set from evidence.
func IsEvidenceBacked(status string) bool { return status != StatusMentioned && status != "" }
