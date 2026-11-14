package sync_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
)

// inboxEnv has one project that waits for approval ("Review me") and one
// that is auto-approved ("Auto"), each with sessions.
func inboxEnv(t *testing.T) *env {
	t.Helper()
	pol := selection.Policy{Allow: []string{"~/code/review", "~/code/auto"}, Projects: map[string]string{"~/code/review": "Review me", "~/code/auto": "Auto"}}
	e := setup(t, pol,
		sess("r1", "/home/u/code/review", "PENDING-ONE build the widget"),
		sess("r2", "/home/u/code/review", "PENDING-TWO fix the gadget"),
		sess("a1", "/home/u/code/auto", "auto project work"),
	)
	e.s.Config.Projects = []config.Project{
		{ID: "p_review1", Name: "Review me", Members: []config.Member{{Path: "~/code/review"}}},
		{ID: "p_auto01", Name: "Auto", Members: []config.Member{{Path: "~/code/auto"}}, AutoApprove: true},
	}
	return e
}

func ids(c *capture) string {
	var out []string
	for _, p := range c.got {
		out = append(out, p.SessionID)
	}
	return strings.Join(out, ",")
}

func TestAPendingSessionNeverAppearsInAPayload(t *testing.T) {
	e := inboxEnv(t)
	ctx := context.Background()
	for _, dry := range []bool{true, false} {
		sink := &capture{}
		rep, err := e.s.Run(ctx, gsync.RunOpts{}, sink, dry)
		if err != nil {
			t.Fatal(err)
		}
		if dry && (ids(sink) != "a1" || rep.Pending != 2 || rep.Planned != 1 || len(rep.PendingItems) != 2) {
			t.Fatalf("only the auto-approved project is planned: %s %+v", ids(sink), rep)
		}
		for _, p := range sink.got {
			if strings.Contains(p.Summary.FirstMessage+p.Summary.Title, "PENDING") {
				t.Fatalf("a pending session reached a payload: %+v", p.Summary)
			}
		}
	}
	if strings.Contains(e.log.String(), "PENDING") {
		t.Error("a pending session's text reached the log")
	}
	// the listing says what waits, with the redacted title and the content it is for
	_, rep, _ := e.s.Plan(ctx, gsync.RunOpts{})
	it := rep.PendingItems[0]
	if it.SessionID != "r1" || it.ProjectName != "Review me" || !strings.Contains(it.Title, "PENDING-ONE") || len(it.ContentHash) != 64 || it.Changed || it.Project != "/home/u/code/review" {
		t.Fatalf("pending item: %+v", it)
	}
	if n, _ := e.st.Count(ctx); n != 1 {
		t.Fatalf("only the sent session is remembered as sent: %d", n)
	}
}

func TestApprovingSendsExactlyThatContentAndAChangeReturnsItToPending(t *testing.T) {
	e := inboxEnv(t)
	ctx := context.Background()
	now := t0.Add(time.Hour)
	_, rep, err := e.s.Plan(ctx, gsync.RunOpts{})
	if err != nil {
		t.Fatal(err)
	}
	r1 := rep.PendingItems[0]
	if err := e.st.Decide(ctx, gsync.Decision{Source: r1.Source, SessionID: r1.SessionID, State: gsync.DecisionApproved, ContentHash: r1.ContentHash, At: now, Title: r1.Title}); err != nil {
		t.Fatal(err)
	}
	sink := &capture{}
	rep, err = e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if err != nil || ids(sink) != "r1,a1" && ids(sink) != "a1,r1" || rep.Pending != 1 {
		t.Fatalf("the approved session is sent, the other still waits: %s %+v %v", ids(sink), rep, err)
	}
	// nothing changed: nothing more is sent and r1 does not come back
	sink = &capture{}
	rep, _ = e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if len(sink.got) != 0 || rep.Pending != 1 || rep.PendingItems[0].SessionID != "r2" {
		t.Fatalf("an unchanged sent session is left alone: %s %+v", ids(sink), rep)
	}

	// the approved session continues: its content differs from what was approved
	e.f.sessions[0].Turns[0].Text = "PENDING-ONE build the widget, and now also CHANGED-PART"
	e.f.sessions[0].Ref.SizeBytes = 99
	sink = &capture{}
	rep, err = e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if err != nil || len(sink.got) != 0 || rep.Pending != 2 {
		t.Fatalf("changed content is not sent on the old approval: %s %+v %v", ids(sink), rep, err)
	}
	var back gsync.PendingItem
	for _, it := range rep.PendingItems {
		if it.SessionID == "r1" {
			back = it
		}
	}
	if !back.Changed || back.ContentHash == r1.ContentHash {
		t.Fatalf("it is back in the inbox as changed, for its new content: %+v", back)
	}
	// approving the new content sends it
	if err := e.st.Decide(ctx, gsync.Decision{Source: back.Source, SessionID: back.SessionID, State: gsync.DecisionApproved, ContentHash: back.ContentHash, At: now}); err != nil {
		t.Fatal(err)
	}
	sink = &capture{}
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false); err != nil || ids(sink) != "r1" || !strings.Contains(sink.got[0].Summary.FirstMessage, "CHANGED-PART") {
		t.Fatalf("the newly approved content is sent: %s %v", ids(sink), err)
	}
}

func TestAutoApprovedProjectsSendChangesWithoutAsking(t *testing.T) {
	e := inboxEnv(t)
	ctx := context.Background()
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, &capture{}, false); err != nil {
		t.Fatal(err)
	}
	e.f.sessions[2].Turns[0].Text = "auto project work, continued"
	e.f.sessions[2].Ref.SizeBytes = 77
	sink := &capture{}
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false); err != nil || ids(sink) != "a1" {
		t.Fatalf("an auto-approved project sends changes: %s %v", ids(sink), err)
	}
	// a folder shared outside any named project behaves as before
	e2 := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("x", "/home/u/code/app", "plain rule"))
	sink = &capture{}
	if rep, err := e2.s.Run(ctx, gsync.RunOpts{}, sink, false); err != nil || ids(sink) != "x" || rep.Pending != 0 {
		t.Fatalf("no project, no inbox: %s %+v %v", ids(sink), rep, err)
	}
}

func TestApproveOptionSendsWhatWaitsAndCountsIt(t *testing.T) {
	e := inboxEnv(t)
	ctx := context.Background()
	sink := &capture{}
	rep, err := e.s.Run(ctx, gsync.RunOpts{Approve: true}, sink, true)
	if err != nil || len(sink.got) != 3 || rep.Approving != 2 || rep.Pending != 0 {
		t.Fatalf("a reviewed plan holds everything and says how much it approves: %s %+v %v", ids(sink), rep, err)
	}
	if _, rep, _ := e.s.Plan(ctx, gsync.RunOpts{}); rep.Pending != 2 {
		t.Fatal("a dry run approves nothing")
	}
	sink = &capture{}
	if _, err := e.s.Run(ctx, gsync.RunOpts{Approve: true}, sink, false); err != nil || len(sink.got) != 3 {
		t.Fatalf("sending the reviewed plan: %s %v", ids(sink), err)
	}
	if _, rep, _ := e.s.Plan(ctx, gsync.RunOpts{}); rep.Pending != 0 || rep.Planned != 0 {
		t.Fatalf("once sent, nothing waits: %+v", rep)
	}
}

func TestExcludedSessionsAreNeverReadOrSent(t *testing.T) {
	e := inboxEnv(t)
	ctx := context.Background()
	now := t0.Add(time.Hour)
	for _, id := range []string{"r1", "a1"} { // one in each kind of project
		if err := e.st.Decide(ctx, gsync.Decision{Source: "claude-code", SessionID: id, State: gsync.DecisionExcluded, ContentHash: strings.Repeat("0", 64), At: now, Project: "p", Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	sink := &capture{}
	rep, err := e.s.Run(ctx, gsync.RunOpts{Approve: true}, sink, false)
	if err != nil || ids(sink) != "r2" || rep.Excluded != 2 {
		t.Fatalf("excluded sessions are in no plan, even an approving one or an auto project: %s %+v %v", ids(sink), rep, err)
	}
	if e.f.loads["/home/u/code/auto"] != 0 {
		t.Error("an excluded session is not even read")
	}
	ex, err := e.st.Decisions(ctx, gsync.DecisionExcluded)
	if err != nil || len(ex) != 2 || !ex[0].Blocks() {
		t.Fatalf("decisions list: %+v %v", ex, err)
	}
	// resetting brings it back: pending in the review project, sent in the auto one
	for _, id := range []string{"r1", "a1"} {
		if ok, err := e.st.Undecide(ctx, "claude-code", id); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	sink = &capture{}
	rep, _ = e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if ids(sink) != "a1" || rep.Pending != 1 || rep.PendingItems[0].SessionID != "r1" {
		t.Fatalf("after a reset: %s %+v", ids(sink), rep)
	}
	if err := e.st.Decide(ctx, gsync.Decision{Source: "claude-code", SessionID: "r1", State: "maybe", At: now}); err == nil {
		t.Error("unknown decisions are refused")
	}
	if _, ok, _ := e.st.Decision(ctx, "claude-code", "nope"); ok {
		t.Error("no decision for an unknown session")
	}
}
