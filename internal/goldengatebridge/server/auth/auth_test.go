package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/auth"
	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/store"
)

// strong is a valid admin secret built at test time.
func strong() string { return "Zx9Qm2Lp7Vb4Nc8Rt1Yw5Ke3Hd6Jf0GaBq+Ux/OwLk2Mn7P=" }

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func setup(t *testing.T) (*auth.Authenticator, *store.Memory, *clock) {
	t.Helper()
	st := store.NewMemory()
	ck := &clock{t: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)}
	a, err := auth.New(st, auth.Options{AdminSecret: strong(), Now: ck.now})
	if err != nil {
		t.Fatal(err)
	}
	return a, st, ck
}

func TestAdminSecretValidation(t *testing.T) {
	for _, bad := range []string{"", "short", strings.Repeat("a", 64), strings.Repeat("abcd", 20), "changeme-changeme-changeme-changeme-changeme"} {
		if err := auth.ValidateAdminSecret(bad); err == nil && bad != "" {
			t.Errorf("weak secret %q accepted", bad)
		}
	}
	for _, good := range []string{strong(), "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"} {
		if err := auth.ValidateAdminSecret(good); err != nil {
			t.Errorf("%q rejected: %v", good, err)
		}
	}
	if _, err := auth.New(store.NewMemory(), auth.Options{AdminSecret: "weak"}); err == nil {
		t.Error("New must reject a weak admin secret")
	}
}

func TestMintAndAuthenticate(t *testing.T) {
	a, st, _ := setup(t)
	ctx := context.Background()
	secret, tok, err := a.Mint(ctx, auth.ScopeRead, "gemini", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "ggb_read_") || len(secret) < 9+43 {
		t.Errorf("token format: %q", secret)
	}
	stored, _ := st.TokenByHash(ctx, auth.HashToken(secret))
	if stored.ID != tok.ID || strings.Contains(stored.Hash, secret) {
		t.Errorf("only the hash may be stored: %+v", stored)
	}
	p, err := a.Authenticate(ctx, "Bearer "+secret, "1.2.3.4")
	if err != nil || p.Scope != auth.ScopeRead || p.TokenID != tok.ID {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := a.Authenticate(ctx, "bearer "+secret, "1.2.3.4"); err != nil {
		t.Errorf("scheme is case-insensitive: %v", err)
	}
	for _, h := range []string{"", "Bearer", "Bearer ", "Basic " + secret, secret, "Bearer " + secret + "x", "Bearer ggb_read_" + strings.Repeat("A", 43)} {
		if _, err := a.Authenticate(ctx, h, "9.9.9.9"); !errors.Is(err, auth.ErrUnauthorized) {
			t.Errorf("%q: %v", h, err)
		}
	}
	if _, _, err := a.Mint(ctx, auth.ScopeAdmin, "", 0); err == nil {
		t.Error("admin tokens must not be mintable")
	}
	s1, _, _ := a.Mint(ctx, auth.ScopeRead, "", 0)
	s2, _, _ := a.Mint(ctx, auth.ScopeRead, "", 0)
	if s1 == s2 {
		t.Error("tokens must be unique")
	}
}

func TestAdminSecretAuthenticates(t *testing.T) {
	a, st, _ := setup(t)
	p, err := a.Authenticate(context.Background(), "Bearer "+strong(), "1.1.1.1")
	if err != nil || p.Scope != auth.ScopeAdmin {
		t.Fatalf("%+v %v", p, err)
	}
	toks, _ := st.ListTokens(context.Background())
	if len(toks) != 0 {
		t.Error("the admin secret must never be stored")
	}
	noAdmin, _ := auth.New(st, auth.Options{})
	if _, err := noAdmin.Authenticate(context.Background(), "Bearer "+strong(), "1.1.1.1"); err == nil {
		t.Error("without a configured admin secret nothing is admin")
	}
}

func TestRevokedExpiredAndGrace(t *testing.T) {
	a, _, ck := setup(t)
	ctx := context.Background()
	s, tok, _ := a.Mint(ctx, auth.ScopeIngest, "", time.Hour)
	if _, err := a.Authenticate(ctx, "Bearer "+s, "ip"); err != nil {
		t.Fatal(err)
	}
	ck.t = ck.t.Add(2 * time.Hour)
	if _, err := a.Authenticate(ctx, "Bearer "+s, "ip"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("expired token accepted: %v", err)
	}
	ck.t = ck.t.Add(-2 * time.Hour)

	s2, tok2, _ := a.Mint(ctx, auth.ScopeRead, "", 0)
	if err := a.Revoke(ctx, tok2.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "Bearer "+s2, "ip"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("revoked token accepted immediately? %v", err)
	}

	s3, tok3, _ := a.Mint(ctx, auth.ScopeRead, "", 0)
	_ = a.Revoke(ctx, tok3.ID, 10*time.Minute) // rotation grace period
	if _, err := a.Authenticate(ctx, "Bearer "+s3, "ip"); err != nil {
		t.Errorf("token within grace must work: %v", err)
	}
	ck.t = ck.t.Add(11 * time.Minute)
	if _, err := a.Authenticate(ctx, "Bearer "+s3, "ip"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("token after grace must fail: %v", err)
	}
	_ = tok
}

func TestPrefixMustMatchScope(t *testing.T) {
	a, st, ck := setup(t)
	ctx := context.Background()
	// a stored read token presented with an ingest prefix must not authenticate
	forged := "ggb_ingest_" + strings.Repeat("Q", 43)
	_ = st.CreateToken(ctx, store.Token{ID: "x", Hash: auth.HashToken(forged), Scope: auth.ScopeRead, CreatedAt: ck.t})
	if _, err := a.Authenticate(ctx, "Bearer "+forged, "ip"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("scope/prefix mismatch accepted: %v", err)
	}
}

func TestFailureBackoffPerIP(t *testing.T) {
	a, _, ck := setup(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_, _ = a.Authenticate(ctx, "Bearer nope", "6.6.6.6")
	}
	if _, err := a.Authenticate(ctx, "Bearer "+strong(), "6.6.6.6"); !errors.Is(err, auth.ErrTooMany) {
		t.Errorf("blocked IP got through: %v", err)
	}
	if _, err := a.Authenticate(ctx, "Bearer "+strong(), "7.7.7.7"); err != nil {
		t.Errorf("other IPs unaffected: %v", err)
	}
	ck.t = ck.t.Add(6 * time.Minute)
	if _, err := a.Authenticate(ctx, "Bearer "+strong(), "6.6.6.6"); err != nil {
		t.Errorf("block must expire: %v", err)
	}
}

func TestPairing(t *testing.T) {
	a, _, ck := setup(t)
	ctx := context.Background()
	code, exp, err := a.CreatePairing(ctx, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 14 || code[4] != '-' || code[9] != '-' || !exp.Equal(ck.t.Add(10*time.Minute)) {
		t.Errorf("code format %q exp %v", code, exp)
	}
	secret, _, err := a.ExchangePairing(ctx, strings.ToLower(strings.ReplaceAll(code, "-", " ")), "ip")
	if err != nil || !strings.HasPrefix(secret, "ggb_ingest_") {
		t.Fatalf("%q %v", secret, err)
	}
	if _, _, err := a.ExchangePairing(ctx, code, "ip"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("code reused: %v", err)
	}

	code2, _, _ := a.CreatePairing(ctx, 10*time.Minute)
	for i := 0; i < auth.MaxPairFailures; i++ {
		_, _, _ = a.ExchangePairing(ctx, "AAAA-BBBB-CCCC", "ip"+string(rune('a'+i)))
	}
	if _, _, err := a.ExchangePairing(ctx, code2, "fresh"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("code must be burned after %d failures: %v", auth.MaxPairFailures, err)
	}

	code3, _, _ := a.CreatePairing(ctx, time.Minute)
	ck.t = ck.t.Add(2 * time.Minute)
	if _, _, err := a.ExchangePairing(ctx, code3, "ip"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("expired code accepted: %v", err)
	}
	if _, _, err := a.CreatePairing(ctx, 0); err == nil {
		t.Error("zero ttl must fail")
	}
	if auth.NormalizeCode("abcd-efgh-jklm") != "ABCDEFGHJKLM" {
		t.Error("normalize")
	}
}

func TestLimiter(t *testing.T) {
	ck := &clock{t: time.Unix(1000, 0)}
	l := auth.NewLimiter(1, 3, ck.now)
	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatalf("burst %d denied", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("over burst allowed")
	}
	if !l.Allow("other") {
		t.Fatal("keys are independent")
	}
	ck.t = ck.t.Add(2 * time.Second)
	for i := 0; i < 2; i++ {
		if !l.Allow("k") {
			t.Fatalf("refill at 1/s: call %d denied", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("refill at 1/s: third call allowed")
	}
}
