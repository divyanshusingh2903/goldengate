package sync_test

import (
	"bytes"
	"context"
	"iter"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/config"
	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/selection"
	gsync "github.com/OWNER/goldengate/internal/sync"
)

var t0 = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

// fake is an in-memory adapter that counts Load calls per project.
type fake struct {
	sessions []model.RawSession
	loads    map[string]int
	mtime    time.Time
}

func (f *fake) Name() string { return "claude-code" }
func (f *fake) Detect(context.Context) (adapters.Detection, error) {
	return adapters.Detection{Installed: true}, nil
}
func (f *fake) ListSessions(_ context.Context, _ adapters.ListOpts) iter.Seq2[model.SessionRef, error] {
	return func(yield func(model.SessionRef, error) bool) {
		for _, s := range f.sessions {
			r := s.Ref
			if r.UpdatedAt.IsZero() {
				r.UpdatedAt = f.mtime
			}
			if !yield(r, nil) {
				return
			}
		}
	}
}
func (f *fake) Load(_ context.Context, ref model.SessionRef) (*model.RawSession, error) {
	f.loads[ref.ProjectKey]++
	for _, s := range f.sessions {
		if s.Ref.ID == ref.ID {
			c := s
			c.Ref = ref
			return &c, nil
		}
	}
	return nil, nil
}

func sess(id, project, prompt string) model.RawSession {
	return model.RawSession{
		Ref:   model.SessionRef{Source: "claude-code", ID: id, ProjectKey: project, StartedAt: t0, SizeBytes: 10},
		Turns: []model.Turn{{Role: "user", Text: prompt, Time: t0}, {Role: "assistant", LastText: "ok", Text: "ok", Time: t0.Add(time.Minute)}},
	}
}

type capture struct{ got []*model.SessionPayload }

func (c *capture) Send(_ context.Context, ps []*model.SessionPayload) error {
	c.got = append(c.got, ps...)
	return nil
}

type env struct {
	f   *fake
	st  *gsync.State
	s   *gsync.Syncer
	log *bytes.Buffer
}

func setup(t *testing.T, pol selection.Policy, sessions ...model.RawSession) *env {
	t.Helper()
	f := &fake{sessions: sessions, loads: map[string]int{}, mtime: t0}
	var reg adapters.Registry
	if err := reg.Register(f); err != nil {
		t.Fatal(err)
	}
	st, err := gsync.OpenState(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m, err := selection.New(pol, selection.Options{Home: "/home/u"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Share.Since = ""
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &env{f: f, st: st, log: &buf, s: &gsync.Syncer{Registry: &reg, Config: cfg, Matcher: m, State: st, Home: "/home/u", Log: log, Now: func() time.Time { return t0.Add(time.Hour) }}}
}

func TestUnselectedProjectNeverReachesPayloadOrLog(t *testing.T) {
	secretProject := "/home/u/work/client-x-confidential"
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}},
		sess("a", "/home/u/code/app", "build the widget"),
		sess("b", secretProject, "TOPSECRET-PROMPT about client-x "+"AKIA"+"IOSFODNN7EXAMPLE"),
		sess("c", "/home/u/code/app/denied-child", "denied child prompt"),
	)
	// deny beats allow: rebuild with a deny rule
	m, _ := selection.New(selection.Policy{Allow: []string{"~/code/app"}, Deny: []string{"~/code/app/denied-child"}}, selection.Options{Home: "/home/u"})
	e.s.Matcher = m

	sink := &capture{}
	rep, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 1 || rep.NotSelected != 2 {
		t.Fatalf("payloads=%d report=%+v", len(sink.got), rep)
	}
	if e.f.loads[secretProject] != 0 || e.f.loads["/home/u/code/app/denied-child"] != 0 {
		t.Fatalf("unselected sessions were even loaded: %v", e.f.loads)
	}
	all := e.log.String()
	for _, p := range sink.got {
		all += fmtJSON(t, p)
	}
	for _, leak := range []string{"client-x", "confidential", "TOPSECRET", "IOSFODNN7", "denied-child", "denied child"} {
		if strings.Contains(all, leak) {
			t.Errorf("%q leaked into payload or log:\n%s", leak, all)
		}
	}
}

func fmtJSON(t *testing.T, p *model.SessionPayload) string {
	t.Helper()
	var b bytes.Buffer
	if err := (gsync.DryRunSink{W: &b}).Send(context.Background(), []*model.SessionPayload{p}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestSecondRunSendsNothing(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "build"))
	sink := &capture{}
	if _, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false); err != nil || len(sink.got) != 1 {
		t.Fatalf("first run: %v %d", err, len(sink.got))
	}
	loadsAfterFirst := e.f.loads["/home/u/code/app"]
	rep, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false)
	if err != nil || len(sink.got) != 1 || rep.Unchanged != 1 {
		t.Fatalf("second run sent data: %v %d %+v", err, len(sink.got), rep)
	}
	if e.f.loads["/home/u/code/app"] != loadsAfterFirst {
		t.Error("unchanged session was re-parsed")
	}
}

func TestChangedStatSameContentSendsNothing(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "build"))
	ctx := context.Background()
	sink := &capture{}
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false); err != nil || len(sink.got) != 1 {
		t.Fatalf("first run: %v %d", err, len(sink.got))
	}
	// A bookkeeping event bumped the file mtime; the conversation is identical.
	e.f.mtime = t0.Add(time.Minute)
	rep, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if err != nil || len(sink.got) != 1 || rep.Unchanged != 1 {
		t.Fatalf("same content was re-sent: %v %d %+v", err, len(sink.got), rep)
	}
	loads := e.f.loads["/home/u/code/app"]
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false); err != nil {
		t.Fatal(err)
	}
	if e.f.loads["/home/u/code/app"] != loads {
		t.Error("the refreshed stat should let the next run skip parsing")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "build"))
	sink := &capture{}
	if _, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, true); err != nil || len(sink.got) != 1 {
		t.Fatalf("dry run: %v %d", err, len(sink.got))
	}
	if n, _ := e.st.Count(context.Background()); n != 0 {
		t.Fatalf("dry run wrote state: %d", n)
	}
	if _, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, false); err != nil || len(sink.got) != 2 {
		t.Fatalf("real run after dry run must still send: %v %d", err, len(sink.got))
	}
}

func TestTombstoneBlocksUntilCleared(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "build"))
	ctx := context.Background()
	sink := &capture{}
	_, _ = e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if err := e.st.Tombstone(ctx, "/home/u/code/app", t0); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.st.Count(ctx); n != 0 {
		t.Errorf("tombstone must forget pushed sessions, have %d", n)
	}
	rep, _ := e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if rep.Tombstoned != 1 || len(sink.got) != 1 {
		t.Fatalf("tombstoned project re-pushed: %+v %d", rep, len(sink.got))
	}
	_ = e.st.ClearTombstone(ctx, "/home/u/code/app")
	_, _ = e.s.Run(ctx, gsync.RunOpts{}, sink, false)
	if len(sink.got) != 2 {
		t.Fatalf("re-shared project must push again: %d", len(sink.got))
	}
}

func TestProjectAndSourceFilters(t *testing.T) {
	e := setup(t, selection.Policy{Default: "allow"},
		sess("a", "/home/u/code/app", "one"), sess("b", "/home/u/code/other", "two"))
	sink := &capture{}
	if _, err := e.s.Run(context.Background(), gsync.RunOpts{Project: "~/code/app"}, sink, true); err != nil || len(sink.got) != 1 || sink.got[0].SessionID != "a" {
		t.Fatalf("project filter: %v %+v", err, sink.got)
	}
	sink = &capture{}
	if _, err := e.s.Run(context.Background(), gsync.RunOpts{Source: "nope"}, sink, true); err != nil || len(sink.got) != 0 {
		t.Fatalf("source filter: %v %d", err, len(sink.got))
	}
}

func TestPushRefusesIncompletePayload(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "build"))
	b, _, err := e.s.Plan(context.Background(), gsync.RunOpts{})
	if err != nil || b.Len() != 1 {
		t.Fatal(err)
	}
	b.Payloads()[0].ContentHash = "" // payloads are shared pointers: simulate corruption
	if err := gsync.Push(context.Background(), &capture{}, nil, b, t0); err == nil {
		t.Fatal("Push must refuse a payload without a content hash")
	}
}

func TestPlantedSecretNeverInPayload(t *testing.T) {
	secret := "AKIA" + "IOSFODNN7EXAMPLE"
	s := sess("a", "/home/u/code/app", "use key "+secret)
	s.Turns[1].LastText = "I saw " + secret
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, s)
	sink := &capture{}
	if _, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmtJSON(t, sink.got[0]), "IOSFODNN7") {
		t.Fatal("planted secret reached the payload")
	}
}

func TestWorktreesAreFiledUnderTheirRepositoryOnlyWhenItIsSelected(t *testing.T) {
	run := func(allow []string, sessions ...model.RawSession) []*model.SessionPayload {
		e := setup(t, selection.Policy{Allow: allow}, sessions...)
		sink := &capture{}
		if _, err := e.s.Run(context.Background(), gsync.RunOpts{}, sink, true); err != nil {
			t.Fatal(err)
		}
		return sink.got
	}
	wt := sess("w1", "/home/u/code/app/.claude/worktrees/fox", "work in a worktree")
	main := sess("m1", "/home/u/code/app", "work in the main checkout")

	// the repository is selected: both file under it
	got := run([]string{"~/code/app"}, wt, main)
	for _, p := range got {
		if p.Project != "~/code/app" {
			t.Errorf("want ~/code/app, got %s", p.Project)
		}
	}
	if len(got) != 2 {
		t.Fatalf("payloads: %d", len(got))
	}
	// only the worktree is selected: the repository's name must never be sent
	got = run([]string{"~/code/app/.claude/worktrees/fox"}, wt, main)
	if len(got) != 1 || got[0].Project != "~/code/app/.claude/worktrees/fox" {
		t.Fatalf("a worktree-only selection must keep the worktree key: %+v", got)
	}
	// a tool-recorded root (OpenCode) behaves the same
	oc := sess("o1", "/home/u/code/app-feature", "opencode worktree")
	oc.Ref.RepoRoot = "/home/u/code/app"
	got = run([]string{"~/code/app-feature", "~/code/app"}, oc)
	if len(got) != 1 || got[0].Project != "~/code/app" {
		t.Errorf("RepoRoot with both selected: %+v", got)
	}
	got = run([]string{"~/code/app-feature"}, oc)
	if len(got) != 1 || got[0].Project != "~/code/app-feature" {
		t.Errorf("RepoRoot not selected must stay hidden: %+v", got)
	}
	// a denied repository root is never used either
	e := setup(t, selection.Policy{Allow: []string{"~/code/app-feature", "~/code/app"}, Deny: []string{"/home/u/code/app"}}, oc)
	sink := &capture{}
	_, _ = e.s.Run(context.Background(), gsync.RunOpts{}, sink, true)
	if len(sink.got) != 1 || sink.got[0].Project != "~/code/app-feature" {
		t.Errorf("deny on the root: %+v", sink.got)
	}
}

func TestPreviewWorksBeforeSharingAndTracksChanges(t *testing.T) {
	secret := "AKIA" + "IOSFODNN7EXAMPLE"
	a := sess("a", "/home/u/code/app", "use key "+secret)
	b := sess("b", "/home/u/code/app", "second session")
	b.Ref.UpdatedAt = t0.Add(time.Hour)
	e := setup(t, selection.Policy{}, a, b, sess("x", "/home/u/code/other", "unrelated"))
	ctx := context.Background()

	// nothing is shared: preview still shows exactly what sharing would send
	pr, err := e.s.Preview(ctx, "/home/u/code/app", 10)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Total != 2 || len(pr.Items) != 2 || pr.Items[0].Ref.ID != "b" {
		t.Fatalf("newest first, only this project: %+v", pr)
	}
	for _, it := range pr.Items {
		if it.Status != gsync.PreviewNew || it.Prev != nil {
			t.Errorf("never pushed: %+v", it)
		}
		if strings.Contains(fmtJSON(t, it.Payload), "IOSFODNN7") {
			t.Error("the preview must show the redacted payload")
		}
	}
	if pr.Redactions["aws-access-key"] == 0 {
		t.Errorf("redaction counts: %v", pr.Redactions)
	}
	if got, _ := e.s.Preview(ctx, "/home/u/code/app", 1); len(got.Items) != 1 || got.Total != 2 {
		t.Errorf("limit: %+v", got)
	}
	if e.f.loads["/home/u/code/other"] != 0 {
		t.Error("other projects must not be parsed")
	}
	if n, _ := e.st.Count(ctx); n != 0 {
		t.Error("preview must write nothing")
	}

	// after a real sync the same preview says "unchanged"; edit a session and it says "changed" with the old payload
	m, _ := selection.New(selection.Policy{Allow: []string{"~/code/app"}}, selection.Options{Home: "/home/u"})
	e.s.Matcher = m
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, &capture{}, false); err != nil {
		t.Fatal(err)
	}
	pr, _ = e.s.Preview(ctx, "/home/u/code/app", 10)
	for _, it := range pr.Items {
		if it.Status != gsync.PreviewUnchanged || it.Prev == nil || it.Prev.ContentHash != it.Payload.ContentHash {
			t.Errorf("after sync: %+v", it)
		}
	}
	e.f.sessions[1].Turns[0].Text = "second session, now different"
	pr, _ = e.s.Preview(ctx, "/home/u/code/app", 10)
	var changed *gsync.PreviewItem
	for i := range pr.Items {
		if pr.Items[i].Ref.ID == "b" {
			changed = &pr.Items[i]
		}
	}
	if changed == nil || changed.Status != gsync.PreviewChanged || changed.Prev == nil || changed.Prev.Summary.Title != "second session" {
		t.Errorf("changed session must carry the previously pushed payload: %+v", changed)
	}
}

func TestPendingCounts(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "one"), sess("b", "/home/u/code/app", "two"), sess("x", "/home/u/code/other", "three"))
	ctx := context.Background()
	got, err := e.s.PendingCounts(ctx)
	if err != nil || got["/home/u/code/app"] != 2 || len(got) != 1 {
		t.Fatalf("unselected projects have no pending count: %v %v", got, err)
	}
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, &capture{}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = e.s.PendingCounts(ctx); len(got) != 0 {
		t.Errorf("nothing pending after a sync: %v", got)
	}
	e.f.mtime = t0.Add(time.Hour) // files changed
	if got, _ = e.s.PendingCounts(ctx); got["/home/u/code/app"] != 2 {
		t.Errorf("changed files are pending again: %v", got)
	}
}

func TestBatchByProject(t *testing.T) {
	e := setup(t, selection.Policy{Default: "allow"}, sess("a", "/home/u/code/a", "1"), sess("b", "/home/u/code/b", "2"), sess("c", "/home/u/code/a", "3"))
	b, _, err := e.s.Plan(context.Background(), gsync.RunOpts{})
	if err != nil {
		t.Fatal(err)
	}
	parts := b.ByProject()
	if len(parts) != 2 || parts[0].Len()+parts[1].Len() != 3 {
		t.Fatalf("parts: %d", len(parts))
	}
	for _, p := range parts {
		seen := map[string]bool{}
		for _, pl := range p.Payloads() {
			seen[pl.Project] = true
		}
		if len(seen) != 1 {
			t.Errorf("a part mixes projects: %v", seen)
		}
	}
	// pushing part by part records state exactly like pushing the whole
	sink := &capture{}
	for _, p := range parts {
		if err := gsync.Push(context.Background(), sink, e.st, p, t0); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := e.st.Count(context.Background()); n != 3 || len(sink.got) != 3 {
		t.Errorf("state=%d sent=%d", n, len(sink.got))
	}
}
