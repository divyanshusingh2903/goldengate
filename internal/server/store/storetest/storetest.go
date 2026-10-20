// Package storetest is the contract test suite every store.Store must pass.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/server/store"
)

var t0 = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

func payload(source, id, project, title string, updated time.Time, items ...model.WorkItem) *model.SessionPayload {
	return &model.SessionPayload{
		Source: source, SessionID: id, Project: project, Level: model.LevelSummary,
		StartedAt: updated.Add(-time.Hour), UpdatedAt: updated, ContentHash: strings.Repeat("b", 64),
		Summary:   &model.Summary{Title: title, Goal: "goal of " + id, FilesTouched: []string{"src/a.go"}},
		WorkItems: items,
	}
}

func pr(key, status string, at time.Time) model.WorkItem {
	return model.WorkItem{Kind: model.KindPR, Provider: "github", Key: key, Status: status, ObservedAt: at,
		Evidence: []model.Evidence{{SessionID: "x", Via: model.ViaToolResult, At: at}}}
}

// Run executes the suite against fresh stores from newStore.
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Helper()
	ctx := context.Background()
	for name, fn := range map[string]func(*testing.T, store.Store){
		"sessions":    sessions,
		"quota":       quota,
		"search":      search,
		"work items":  workItems,
		"delete":      deletes,
		"tokens":      tokens,
		"pairing":     pairing,
		"audit":       audit,
		"unicode":     unicode,
		"concurrency": concurrency,
		"oauth":       oauth,
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			t.Cleanup(func() { _ = s.Close() })
			fn(t, s)
		})
	}
	_ = ctx
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func sessions(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.PutSession(ctx, payload("claude-code", "a", "~/p1", "Alpha", t0), t0, store.Limits{}))
	must(t, s.PutSession(ctx, payload("codex", "b", "~/p1", "Beta", t0.Add(time.Hour)), t0, store.Limits{}))
	must(t, s.PutSession(ctx, payload("claude-code", "c", "~/p2", "Gamma", t0.Add(2*time.Hour)), t0, store.Limits{}))

	all, err := s.Sessions(ctx, store.SessionQuery{})
	must(t, err)
	if len(all) != 3 || all[0].Payload.SessionID != "c" || all[2].Payload.SessionID != "a" {
		t.Fatalf("want newest first c,b,a: %+v", ids(all))
	}
	got, _ := s.Sessions(ctx, store.SessionQuery{Project: "~/p1"})
	if len(got) != 2 {
		t.Fatalf("project filter: %v", ids(got))
	}
	got, _ = s.Sessions(ctx, store.SessionQuery{Source: "codex"})
	if len(got) != 1 || got[0].Payload.SessionID != "b" {
		t.Fatalf("source filter: %v", ids(got))
	}
	got, _ = s.Sessions(ctx, store.SessionQuery{Since: t0.Add(30 * time.Minute)})
	if len(got) != 2 {
		t.Fatalf("since filter: %v", ids(got))
	}
	got, _ = s.Sessions(ctx, store.SessionQuery{Limit: 1, Offset: 1})
	if len(got) != 1 || got[0].Payload.SessionID != "b" {
		t.Fatalf("paging: %v", ids(got))
	}

	r, err := s.GetSession(ctx, "codex", "b")
	must(t, err)
	if r.Payload.Summary.Title != "Beta" || r.IngestedAt.IsZero() {
		t.Fatalf("get: %+v", r)
	}
	if _, err := s.GetSession(ctx, "", "b"); err != nil {
		t.Fatalf("any-source get: %v", err)
	}
	if _, err := s.GetSession(ctx, "codex", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// replacing a session keeps one row
	must(t, s.PutSession(ctx, payload("claude-code", "a", "~/p1", "Alpha v2", t0.Add(3*time.Hour)), t0, store.Limits{}))
	r, _ = s.GetSession(ctx, "claude-code", "a")
	all, _ = s.Sessions(ctx, store.SessionQuery{})
	if len(all) != 3 || r.Payload.Summary.Title != "Alpha v2" {
		t.Fatalf("upsert: %d %+v", len(all), r)
	}

	ps, err := s.ListProjects(ctx)
	must(t, err)
	if len(ps) != 2 || ps[0].Project != "~/p1" || ps[0].Sessions != 2 || ps[0].Sources["codex"] != 1 || ps[1].Sessions != 1 {
		t.Fatalf("projects: %+v", ps)
	}
}

func ids(rs []store.SessionRecord) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Payload.SessionID)
	}
	return out
}

func quota(t *testing.T, s store.Store) {
	ctx := context.Background()
	lim := store.Limits{MaxSessionsPerProject: 2, MaxSessionsTotal: 3}
	must(t, s.PutSession(ctx, payload("claude-code", "1", "~/q1", "t", t0), t0, lim))
	must(t, s.PutSession(ctx, payload("claude-code", "2", "~/q1", "t", t0), t0, lim))
	if err := s.PutSession(ctx, payload("claude-code", "3", "~/q1", "t", t0), t0, lim); !errors.Is(err, store.ErrQuota) {
		t.Fatalf("per-project quota: %v", err)
	}
	// updating an existing session never hits the quota
	must(t, s.PutSession(ctx, payload("claude-code", "2", "~/q1", "t2", t0), t0, lim))
	must(t, s.PutSession(ctx, payload("claude-code", "4", "~/q2", "t", t0), t0, lim))
	if err := s.PutSession(ctx, payload("claude-code", "5", "~/q3", "t", t0), t0, lim); !errors.Is(err, store.ErrQuota) {
		t.Fatalf("total quota: %v", err)
	}
}

func search(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.PutSession(ctx, payload("claude-code", "s1", "~/a", "Fix OAuth redirect mismatch", t0), t0, store.Limits{}))
	must(t, s.PutSession(ctx, payload("claude-code", "s2", "~/b", "Add gRPC health checks", t0.Add(time.Hour)), t0, store.Limits{}))
	check := func(q, project string, want ...string) {
		t.Helper()
		got, err := s.Search(ctx, q, project, 10)
		must(t, err)
		if fmt.Sprint(ids(got)) != fmt.Sprint(want) {
			t.Errorf("search %q in %q: got %v want %v", q, project, ids(got), want)
		}
	}
	check("oauth", "", "s1")
	check("OAUTH redirect", "", "s1")
	check("redir", "", "s1") // prefix
	check("health grpc", "", "s2")
	check("oauth health", "") // AND semantics
	check("", "")
	check("   ", "")
	check("a", "~/b", "s2") // "a" is a prefix of "add" ...
	check("fix", "~/b")
	check(`"; DROP TABLE sessions; --`, "") // hostile query is just words
	check("NEAR(oauth", "")                 // FTS operators are not interpreted
	check("oauth OR health", "")
	got, _ := s.Search(ctx, "a", "", 1)
	if len(got) != 1 {
		t.Errorf("limit not applied: %v", ids(got))
	}
}

func workItems(t *testing.T, s store.Store) {
	ctx := context.Background()
	p := "~/w"
	must(t, s.PutSession(ctx, payload("claude-code", "s1", p, "one", t0, pr("o/r#7", model.StatusMentioned, t0)), t0, store.Limits{}))
	must(t, s.PutSession(ctx, payload("claude-code", "s2", p, "two", t0.Add(time.Hour), pr("o/r#7", model.StatusMerged, t0.Add(time.Hour))), t0.Add(time.Hour), store.Limits{}))
	must(t, s.PutSession(ctx, payload("codex", "s3", p, "three", t0.Add(2*time.Hour), pr("o/r#7", model.StatusMentioned, t0.Add(2*time.Hour)),
		model.WorkItem{Kind: model.KindCommit, Key: "r@abc1234", Status: model.StatusCreated, ObservedAt: t0.Add(2 * time.Hour), Evidence: []model.Evidence{{SessionID: "s3", Via: model.ViaToolResult}}}), t0.Add(2*time.Hour), store.Limits{}))

	items, err := s.WorkItems(ctx, store.WorkItemQuery{Project: p, Kind: model.KindPR})
	must(t, err)
	if len(items) != 1 {
		t.Fatalf("one PR across three sessions must be one record, got %d", len(items))
	}
	it := items[0]
	if it.Item.Status != model.StatusMerged {
		t.Errorf("later mention must not downgrade merged, got %s", it.Item.Status)
	}
	if len(it.Sessions) != 3 {
		t.Errorf("linked sessions: %v", it.Sessions)
	}
	if !it.FirstSeen.Equal(t0) || !it.LastSeen.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("first/last seen: %v %v", it.FirstSeen, it.LastSeen)
	}
	all, _ := s.WorkItems(ctx, store.WorkItemQuery{})
	if len(all) != 2 {
		t.Fatalf("all: %d", len(all))
	}
	if got, _ := s.WorkItems(ctx, store.WorkItemQuery{Status: model.StatusCreated}); len(got) != 1 || got[0].Item.Kind != model.KindCommit {
		t.Errorf("status filter: %+v", got)
	}
	if got, _ := s.WorkItems(ctx, store.WorkItemQuery{Since: t0.Add(90 * time.Minute)}); len(got) != 1 {
		t.Errorf("since filter on observed_at: %d", len(got))
	}
	if got, _ := s.WorkItems(ctx, store.WorkItemQuery{Provider: "github"}); len(got) != 1 {
		t.Errorf("provider filter: %d", len(got))
	}
	if got, _ := s.WorkItems(ctx, store.WorkItemQuery{Limit: 1}); len(got) != 1 {
		t.Errorf("limit: %d", len(got))
	}

	// removing sessions unlinks; the item disappears with its last session
	_, err = s.DeleteSession(ctx, "claude-code", "s1")
	must(t, err)
	_, _ = s.DeleteSession(ctx, "claude-code", "s2")
	items, _ = s.WorkItems(ctx, store.WorkItemQuery{Kind: model.KindPR})
	if len(items) != 1 || len(items[0].Sessions) != 1 {
		t.Fatalf("PR must survive via s3: %+v", items)
	}
	_, _ = s.DeleteSession(ctx, "codex", "s3")
	if items, _ = s.WorkItems(ctx, store.WorkItemQuery{}); len(items) != 0 {
		t.Fatalf("orphaned work items must be removed: %+v", items)
	}
}

func deletes(t *testing.T, s store.Store) {
	ctx := context.Background()
	it := pr("o/r#1", model.StatusCreated, t0)
	must(t, s.PutSession(ctx, payload("claude-code", "d1", "~/keep", "k", t0, it), t0, store.Limits{}))
	must(t, s.PutSession(ctx, payload("claude-code", "d2", "~/gone", "g", t0, it), t0, store.Limits{}))
	must(t, s.PutSession(ctx, payload("codex", "d3", "~/gone", "g", t0), t0, store.Limits{}))
	n, err := s.DeleteProject(ctx, "~/gone")
	must(t, err)
	if n != 2 {
		t.Fatalf("deleted %d", n)
	}
	if got, _ := s.Search(ctx, "gone", "", 10); len(got) != 0 {
		t.Errorf("search index kept deleted sessions: %v", ids(got))
	}
	ps, _ := s.ListProjects(ctx)
	if len(ps) != 1 || ps[0].Project != "~/keep" {
		t.Fatalf("projects: %+v", ps)
	}
	items, _ := s.WorkItems(ctx, store.WorkItemQuery{})
	if len(items) != 1 || items[0].Project != "~/keep" {
		t.Fatalf("work items of the deleted project must go, the other project's must stay: %+v", items)
	}
	if n, _ := s.DeleteProject(ctx, "~/gone"); n != 0 {
		t.Errorf("second delete: %d", n)
	}
	// delete by id without source removes every match
	must(t, s.PutSession(ctx, payload("codex", "same", "~/x", "t", t0), t0, store.Limits{}))
	must(t, s.PutSession(ctx, payload("claude-code", "same", "~/x", "t", t0), t0, store.Limits{}))
	if n, _ := s.DeleteSession(ctx, "", "same"); n != 2 {
		t.Errorf("any-source delete: %d", n)
	}
}

func tokens(t *testing.T, s store.Store) {
	ctx := context.Background()
	now := t0
	tok := store.Token{ID: "id1", Hash: "h1", Scope: "read", Label: "gemini", CreatedAt: now}
	must(t, s.CreateToken(ctx, tok))
	if err := s.CreateToken(ctx, store.Token{ID: "id2", Hash: "h1", Scope: "read", CreatedAt: now}); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("duplicate hash: %v", err)
	}
	if err := s.CreateToken(ctx, store.Token{ID: "id1", Hash: "h9", Scope: "read", CreatedAt: now}); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("duplicate id: %v", err)
	}
	got, err := s.TokenByHash(ctx, "h1")
	must(t, err)
	if got.ID != "id1" || got.Scope != "read" || got.Label != "gemini" || !got.Active(now) {
		t.Fatalf("%+v", got)
	}
	if _, err := s.TokenByHash(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	must(t, s.TouchToken(ctx, "id1", now.Add(time.Minute)))
	got, _ = s.TokenByHash(ctx, "h1")
	if !got.LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Errorf("last used: %v", got.LastUsedAt)
	}

	must(t, s.CreateToken(ctx, store.Token{ID: "id2", Hash: "h2", Scope: "ingest", CreatedAt: now.Add(time.Second), ExpiresAt: now.Add(time.Hour)}))
	if n, _ := s.CountActiveTokens(ctx, now); n != 2 {
		t.Errorf("active: %d", n)
	}
	if n, _ := s.CountActiveTokens(ctx, now.Add(2*time.Hour)); n != 1 {
		t.Errorf("expired token counted active: %d", n)
	}
	must(t, s.RevokeToken(ctx, "id1", now.Add(time.Minute)))
	got, _ = s.TokenByHash(ctx, "h1")
	if got.Active(now.Add(2*time.Minute)) || got.RevokedAt.IsZero() {
		t.Errorf("revoked token still active: %+v", got)
	}
	if !got.Active(now) {
		t.Errorf("revocation at a future instant must not affect earlier checks")
	}
	if err := s.RevokeToken(ctx, "ghost", now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%v", err)
	}
	must(t, s.SetTokenExpiry(ctx, "id2", now.Add(10*time.Minute)))
	got, _ = s.TokenByHash(ctx, "h2")
	if got.Active(now.Add(11 * time.Minute)) {
		t.Errorf("expiry not applied")
	}
	list, _ := s.ListTokens(ctx)
	if len(list) != 2 || list[0].ID != "id1" {
		t.Errorf("list order: %+v", list)
	}
}

func pairing(t *testing.T, s store.Store) {
	ctx := context.Background()
	now := t0
	must(t, s.CreatePairing(ctx, "p1", now.Add(10*time.Minute)))
	if err := s.CreatePairing(ctx, "p1", now.Add(time.Hour)); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("%v", err)
	}
	if ok, _ := s.ConsumePairing(ctx, "wrong", now); ok {
		t.Fatal("unknown code accepted")
	}
	if ok, _ := s.ConsumePairing(ctx, "p1", now); !ok {
		t.Fatal("valid code rejected")
	}
	if ok, _ := s.ConsumePairing(ctx, "p1", now); ok {
		t.Fatal("code must be single use")
	}

	must(t, s.CreatePairing(ctx, "p2", now.Add(time.Minute)))
	if ok, _ := s.ConsumePairing(ctx, "p2", now.Add(2*time.Minute)); ok {
		t.Fatal("expired code accepted")
	}

	must(t, s.CreatePairing(ctx, "p3", now.Add(time.Hour)))
	for i := 0; i < 5; i++ {
		must(t, s.PairingFailure(ctx, now, 5))
	}
	if ok, _ := s.ConsumePairing(ctx, "p3", now); ok {
		t.Fatal("code must be burned after 5 failures")
	}
	must(t, s.CreatePairing(ctx, "p4", now.Add(time.Hour)))
	for i := 0; i < 4; i++ {
		must(t, s.PairingFailure(ctx, now, 5))
	}
	if ok, _ := s.ConsumePairing(ctx, "p4", now); !ok {
		t.Fatal("four failures must not burn a code")
	}
}

func audit(t *testing.T, s store.Store) {
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		must(t, s.AppendAudit(ctx, store.AuditEvent{At: t0.Add(time.Duration(i) * time.Hour), TokenID: "id", Scope: "ingest", Action: fmt.Sprintf("a%d", i), Project: "~/p", IP: "203.0.113.9"}))
	}
	got, err := s.ListAudit(ctx, 3)
	must(t, err)
	if len(got) != 3 || got[0].Action != "a4" || got[2].Action != "a2" || got[0].IP != "203.0.113.9" {
		t.Fatalf("%+v", got)
	}
	n, err := s.PruneAudit(ctx, t0.Add(150*time.Minute))
	must(t, err)
	if n != 3 {
		t.Fatalf("pruned %d", n)
	}
	got, _ = s.ListAudit(ctx, 10)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
}

func unicode(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.PutSession(ctx, payload("claude-code", "u1", "~/ü", "Größe ändern 日本語 テスト", t0), t0, store.Limits{}))
	r, err := s.GetSession(ctx, "claude-code", "u1")
	must(t, err)
	if r.Payload.Summary.Title != "Größe ändern 日本語 テスト" || r.Payload.Project != "~/ü" {
		t.Fatalf("unicode mangled: %+v", r.Payload)
	}
	got, _ := s.Search(ctx, "größe", "", 5)
	if len(got) != 1 {
		t.Errorf("unicode search: %v", ids(got))
	}
}

func concurrency(t *testing.T, s store.Store) {
	ctx := context.Background()
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			done <- s.PutSession(ctx, payload("claude-code", fmt.Sprintf("c%d", i), "~/c", "t", t0.Add(time.Duration(i)*time.Second),
				pr("o/r#1", model.StatusMentioned, t0)), t0, store.Limits{})
		}(i)
	}
	for i := 0; i < 20; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	items, _ := s.WorkItems(ctx, store.WorkItemQuery{})
	if len(items) != 1 || len(items[0].Sessions) != 20 {
		t.Fatalf("concurrent merge lost links: %d items, %d sessions", len(items), len(items[0].Sessions))
	}
}

func oauth(t *testing.T, s store.Store) {
	ctx := context.Background()
	now := t0
	c := store.OAuthClient{ID: "c1", Name: "Gemini", SecretHash: "", RedirectURIs: []string{"https://a.example/cb", "http://localhost:1/cb"}, CreatedAt: now}
	must(t, s.CreateOAuthClient(ctx, c))
	if err := s.CreateOAuthClient(ctx, c); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("duplicate client: %v", err)
	}
	got, err := s.OAuthClient(ctx, "c1")
	must(t, err)
	if got.Name != "Gemini" || len(got.RedirectURIs) != 2 || got.RedirectURIs[1] != "http://localhost:1/cb" {
		t.Fatalf("%+v", got)
	}
	if _, err := s.OAuthClient(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	must(t, s.CreateOAuthClient(ctx, store.OAuthClient{ID: "c2", Name: "unused", RedirectURIs: []string{"https://b.example/cb"}, CreatedAt: now.Add(-48 * time.Hour)}))
	if n, _ := s.CountOAuthClients(ctx); n != 2 {
		t.Fatalf("count %d", n)
	}
	must(t, s.TouchOAuthClient(ctx, "c1", now.Add(time.Minute)))
	if l, _ := s.ListOAuthClients(ctx); len(l) != 2 || l[0].ID != "c2" || !l[1].LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("list/touch: %+v", l)
	}

	// pending consent: single use, expires
	txn := store.OAuthTxn{ClientID: "c1", RedirectURI: "https://a.example/cb", State: "st", Challenge: "ch", Resource: "https://h/mcp", Scope: "read"}
	must(t, s.PutOAuthTxn(ctx, "t1", txn, now.Add(10*time.Minute)))
	back, err := s.TakeOAuthTxn(ctx, "t1", now)
	must(t, err)
	if *back != txn {
		t.Fatalf("txn round trip: %+v", back)
	}
	if _, err := s.TakeOAuthTxn(ctx, "t1", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a pending request must be single use")
	}
	must(t, s.PutOAuthTxn(ctx, "t2", txn, now.Add(time.Minute)))
	if _, err := s.TakeOAuthTxn(ctx, "t2", now.Add(2*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired pending request accepted")
	}

	// codes: use once, replay is reported, expiry
	code := store.OAuthCode{ClientID: "c1", RedirectURI: "https://a.example/cb", Challenge: "ch", Resource: "https://h/mcp", Scope: "read", Family: "fam1", Expires: now.Add(5 * time.Minute)}
	must(t, s.PutOAuthCode(ctx, "code1", code))
	cc, replay, err := s.UseOAuthCode(ctx, "code1", now)
	if err != nil || replay || *cc != code {
		t.Fatalf("first use: %+v %v %v", cc, replay, err)
	}
	if cc, replay, err = s.UseOAuthCode(ctx, "code1", now.Add(time.Second)); err != nil || !replay || cc.Family != "fam1" {
		t.Fatalf("replay must be reported with the family: %+v %v %v", cc, replay, err)
	}
	if _, _, err := s.UseOAuthCode(ctx, "ghost", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	must(t, s.PutOAuthCode(ctx, "code2", code))
	if _, _, err := s.UseOAuthCode(ctx, "code2", now.Add(6*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired code accepted")
	}

	// refresh tokens rotate; replay reported
	rt := store.OAuthRefresh{Hash: "r1", ClientID: "c1", Family: "fam1", Resource: "https://h/mcp", Scope: "read", Expires: now.Add(time.Hour)}
	must(t, s.PutOAuthRefresh(ctx, rt))
	rr, replay, err := s.UseOAuthRefresh(ctx, "r1", now)
	if err != nil || replay || *rr != rt {
		t.Fatalf("%+v %v %v", rr, replay, err)
	}
	if _, replay, _ = s.UseOAuthRefresh(ctx, "r1", now); !replay {
		t.Fatal("refresh replay not detected")
	}
	if _, _, err := s.UseOAuthRefresh(ctx, "ghost", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}

	// families: revoking one revokes its access tokens and refresh tokens, nothing else
	must(t, s.CreateToken(ctx, store.Token{ID: "a1", Hash: "ha1", Scope: "read", CreatedAt: now, ClientID: "c1", Family: "fam1"}))
	must(t, s.CreateToken(ctx, store.Token{ID: "a2", Hash: "ha2", Scope: "read", CreatedAt: now, ClientID: "c1", Family: "fam2"}))
	must(t, s.PutOAuthRefresh(ctx, store.OAuthRefresh{Hash: "r2", ClientID: "c1", Family: "fam1", Expires: now.Add(time.Hour)}))
	must(t, s.RevokeFamily(ctx, "fam1", now.Add(time.Minute)))
	t1, _ := s.TokenByHash(ctx, "ha1")
	t2, _ := s.TokenByHash(ctx, "ha2")
	if t1.Active(now.Add(2*time.Minute)) || !t2.Active(now.Add(2*time.Minute)) || t1.ClientID != "c1" || t1.Family != "fam1" {
		t.Fatalf("family revocation: %+v / %+v", t1, t2)
	}
	if _, _, err := s.UseOAuthRefresh(ctx, "r2", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a revoked family must lose its refresh tokens")
	}

	// deleting a client revokes its tokens and removes its state
	must(t, s.PutOAuthCode(ctx, "code3", code))
	n, err := s.DeleteOAuthClient(ctx, "c1", now.Add(3*time.Minute))
	must(t, err)
	if n != 1 { // a1 was already revoked, a2 is revoked now
		t.Fatalf("revoked %d tokens", n)
	}
	if t2, _ = s.TokenByHash(ctx, "ha2"); t2.Active(now.Add(4 * time.Minute)) {
		t.Fatal("client deletion left a token active")
	}
	if _, _, err := s.UseOAuthCode(ctx, "code3", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("client deletion left codes behind")
	}
	if _, err := s.OAuthClient(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("client still exists")
	}
	if _, err := s.DeleteOAuthClient(ctx, "c1", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}

	// pruning removes never-used clients older than the cutoff only
	must(t, s.CreateOAuthClient(ctx, store.OAuthClient{ID: "c3", Name: "fresh", RedirectURIs: []string{"https://c.example/cb"}, CreatedAt: now}))
	if _, err := s.PruneOAuth(ctx, now, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OAuthClient(ctx, "c2"); !errors.Is(err, store.ErrNotFound) {
		t.Error("stale unused client survived pruning")
	}
	if _, err := s.OAuthClient(ctx, "c3"); err != nil {
		t.Errorf("fresh client was pruned: %v", err)
	}
}
