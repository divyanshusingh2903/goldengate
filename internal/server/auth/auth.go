// Package auth implements the bridge's three-scope bearer-token model:
//
//	admin   mints, lists and revokes tokens; nothing else
//	ingest  writes and deletes work data; nothing else
//	read    the MCP endpoint only
//
// Minted tokens are 256-bit random values with a recognizable prefix
// (ggb_read_..., ggb_ingest_...) so secret scanners can find leaks. Only their
// SHA-256 hash is stored. The admin secret comes from the environment, is
// never stored, and is compared in constant time.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OWNER/goldengate/internal/server/store"
)

// Scopes.
const (
	ScopeAdmin  = "admin"
	ScopeIngest = "ingest"
	ScopeRead   = "read"
)

// Errors returned by Authenticate.
var (
	ErrUnauthorized = errors.New("auth: missing, invalid, revoked or expired token")
	ErrTooMany      = errors.New("auth: too many failed attempts")
)

// Principal is an authenticated caller.
type Principal struct {
	TokenID string
	Scope   string
	Label   string
}

// Authenticator verifies bearer tokens.
type Authenticator struct {
	st       store.AuthStore
	admin    [sha256.Size]byte
	hasAdmin bool
	now      func() time.Time
	fails    *failTracker
	touchMu  sync.Mutex
	touched  map[string]time.Time
}

// Options configures an Authenticator.
type Options struct {
	AdminSecret string           // optional; validated when set
	Now         func() time.Time // default time.Now
}

// New builds an Authenticator.
func New(st store.AuthStore, o Options) (*Authenticator, error) {
	a := &Authenticator{st: st, now: o.Now, fails: newFailTracker(10, 5*time.Minute), touched: map[string]time.Time{}}
	if a.now == nil {
		a.now = time.Now
	}
	if o.AdminSecret != "" {
		if err := ValidateAdminSecret(o.AdminSecret); err != nil {
			return nil, err
		}
		a.admin, a.hasAdmin = sha256.Sum256([]byte(o.AdminSecret)), true
	}
	return a, nil
}

// ValidateAdminSecret rejects weak admin secrets. `openssl rand -base64 32`
// (44 chars) and 64 hex characters both pass.
func ValidateAdminSecret(s string) error {
	if len(s) < 43 {
		return errors.New("auth: admin secret too short: generate one with `openssl rand -base64 32` (at least 32 random bytes)")
	}
	distinct := map[rune]bool{}
	for _, r := range s {
		distinct[r] = true
	}
	if len(distinct) < 12 {
		return errors.New("auth: admin secret is not random enough: generate one with `openssl rand -base64 32`")
	}
	return nil
}

// HasAdmin reports whether an admin secret is configured.
func (a *Authenticator) HasAdmin() bool { return a.hasAdmin }

// HashToken returns the stored form of a token secret.
func HashToken(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("auth: system randomness unavailable: %v", err)) // never continue with weak tokens
	}
	return b
}

// Mint creates and stores a token. The returned secret is shown once and is
// not recoverable. expires <= 0 means no expiry.
func (a *Authenticator) Mint(ctx context.Context, scope, label string, expires time.Duration) (string, store.Token, error) {
	if scope != ScopeIngest && scope != ScopeRead {
		return "", store.Token{}, fmt.Errorf("auth: cannot mint a %q token (want ingest or read)", scope)
	}
	if len(label) > 80 || strings.ContainsAny(label, "\x00\n\r") {
		return "", store.Token{}, errors.New("auth: invalid label")
	}
	secret := "ggb_" + scope + "_" + base64.RawURLEncoding.EncodeToString(randomBytes(32))
	now := a.now().UTC()
	t := store.Token{ID: "tok_" + hex.EncodeToString(randomBytes(4)), Hash: HashToken(secret), Scope: scope, Label: label, CreatedAt: now}
	if expires > 0 {
		t.ExpiresAt = now.Add(expires)
	}
	if err := a.st.CreateToken(ctx, t); err != nil {
		return "", store.Token{}, fmt.Errorf("auth: store token: %w", err)
	}
	return secret, t, nil
}

// Authenticate verifies an Authorization header value. ip is used only for
// failure backoff.
func (a *Authenticator) Authenticate(ctx context.Context, header, ip string) (Principal, error) {
	if a.fails.blocked(ip, a.now()) {
		return Principal{}, ErrTooMany
	}
	secret, ok := bearer(header)
	if !ok {
		a.fails.record(ip, a.now())
		return Principal{}, ErrUnauthorized
	}
	if a.hasAdmin {
		h := sha256.Sum256([]byte(secret))
		if subtle.ConstantTimeCompare(h[:], a.admin[:]) == 1 {
			return Principal{TokenID: "admin", Scope: ScopeAdmin, Label: "admin secret"}, nil
		}
	}
	tok, err := a.st.TokenByHash(ctx, HashToken(secret))
	if err != nil {
		a.fails.record(ip, a.now())
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, ErrUnauthorized
		}
		return Principal{}, fmt.Errorf("auth: lookup: %w", err)
	}
	now := a.now()
	if !tok.Active(now) || !strings.HasPrefix(secret, "ggb_"+tok.Scope+"_") {
		a.fails.record(ip, now)
		return Principal{}, ErrUnauthorized
	}
	a.touch(ctx, tok.ID, now)
	return Principal{TokenID: tok.ID, Scope: tok.Scope, Label: tok.Label}, nil
}

// touch records last use at most once a minute per token.
func (a *Authenticator) touch(ctx context.Context, id string, now time.Time) {
	a.touchMu.Lock()
	last := a.touched[id]
	due := now.Sub(last) >= time.Minute
	if due {
		a.touched[id] = now
	}
	a.touchMu.Unlock()
	if due {
		_ = a.st.TouchToken(ctx, id, now)
	}
}

func bearer(h string) (string, bool) {
	const p = "bearer "
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", false
	}
	s := strings.TrimSpace(h[len(p):])
	return s, s != ""
}

// Revoke revokes a token now, or at now+grace when grace > 0 (rotation).
func (a *Authenticator) Revoke(ctx context.Context, id string, grace time.Duration) error {
	return a.st.RevokeToken(ctx, id, a.now().UTC().Add(grace))
}

// --- pairing ---

const pairAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// MaxPairFailures burns live pairing codes after this many failed exchanges.
const MaxPairFailures = 5

// NormalizeCode uppercases a pairing code and drops separators.
func NormalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if strings.ContainsRune(pairAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func pairHash(norm string) string { return HashToken("pair:" + norm) }

// CreatePairing stores a new single-use code and returns it for display.
func (a *Authenticator) CreatePairing(ctx context.Context, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		return "", time.Time{}, errors.New("auth: pairing ttl must be positive")
	}
	raw := randomBytes(12)
	var code strings.Builder
	for i, b := range raw {
		if i > 0 && i%4 == 0 {
			code.WriteByte('-')
		}
		code.WriteByte(pairAlphabet[int(b)%len(pairAlphabet)]) // 32 symbols: no modulo bias
	}
	exp := a.now().UTC().Add(ttl)
	if err := a.st.CreatePairing(ctx, pairHash(NormalizeCode(code.String())), exp); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: store pairing: %w", err)
	}
	return code.String(), exp, nil
}

// ExchangePairing burns a valid code and mints an ingest token. Every failure
// counts against all live codes.
func (a *Authenticator) ExchangePairing(ctx context.Context, code, ip string) (string, store.Token, error) {
	if a.fails.blocked(ip, a.now()) {
		return "", store.Token{}, ErrTooMany
	}
	ok, err := a.st.ConsumePairing(ctx, pairHash(NormalizeCode(code)), a.now())
	if err != nil {
		return "", store.Token{}, fmt.Errorf("auth: pairing: %w", err)
	}
	if !ok {
		a.fails.record(ip, a.now())
		_ = a.st.PairingFailure(ctx, a.now(), MaxPairFailures)
		return "", store.Token{}, ErrUnauthorized
	}
	return a.Mint(ctx, ScopeIngest, "paired", 0)
}

// --- failure backoff and rate limiting ---

type failTracker struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	by     map[string][]time.Time
}

func newFailTracker(max int, window time.Duration) *failTracker {
	return &failTracker{max: max, window: window, by: map[string][]time.Time{}}
}

func (f *failTracker) prune(ip string, now time.Time) []time.Time {
	cut := now.Add(-f.window)
	ts := f.by[ip]
	i := 0
	for i < len(ts) && ts[i].Before(cut) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(f.by, ip)
	} else {
		f.by[ip] = ts
	}
	return ts
}

func (f *failTracker) record(ip string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.by) > 10000 { // bound memory under a spray from many addresses
		f.by = map[string][]time.Time{}
	}
	f.by[ip] = append(f.prune(ip, now), now)
}

func (f *failTracker) blocked(ip string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prune(ip, now)) >= f.max
}

// Limiter is a token-bucket rate limiter keyed by string (token id or IP).
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter allows rate events per second with the given burst.
func NewLimiter(rate float64, burst int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{rate: rate, burst: float64(burst), buckets: map[string]*bucket{}, now: now}
}

// Allow consumes one token for key.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.buckets) > 10000 {
		l.buckets = map[string]*bucket{}
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Blocked reports whether ip is currently locked out after repeated failures.
func (a *Authenticator) Blocked(ip string) bool { return a.fails.blocked(ip, a.now()) }

// --- OAuth-issued credentials ---

// MintOAuthAccess creates a short-lived read token for an OAuth grant. It is
// an ordinary read token (same prefix, same checks) tied to a client and grant
// family so that deleting the client or revoking the family kills it at once.
func (a *Authenticator) MintOAuthAccess(ctx context.Context, clientID, family, label string, ttl time.Duration) (string, store.Token, error) {
	if ttl <= 0 {
		return "", store.Token{}, errors.New("auth: oauth access token needs a positive ttl")
	}
	secret := "ggb_" + ScopeRead + "_" + base64.RawURLEncoding.EncodeToString(randomBytes(32))
	now := a.now().UTC()
	t := store.Token{ID: "tok_" + hex.EncodeToString(randomBytes(4)), Hash: HashToken(secret), Scope: ScopeRead, Label: label,
		CreatedAt: now, ExpiresAt: now.Add(ttl), ClientID: clientID, Family: family}
	if err := a.st.CreateToken(ctx, t); err != nil {
		return "", store.Token{}, fmt.Errorf("auth: store oauth token: %w", err)
	}
	return secret, t, nil
}

// RandomSecret returns n random bytes, base64url encoded, with a prefix.
func RandomSecret(prefix string, n int) string {
	return prefix + base64.RawURLEncoding.EncodeToString(randomBytes(n))
}
