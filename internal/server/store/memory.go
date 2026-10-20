package store

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/workitems"
)

// Memory is an in-process Store for tests. It is safe for concurrent use.
type Memory struct {
	mu       sync.Mutex
	sessions map[SessionKey]*SessionRecord
	work     map[workKey]*WorkItemRecord
	tokens   map[string]*Token // by id
	pairings map[string]*pairing
	audit    []AuditEvent

	oclients map[string]*OAuthClient
	otxns    map[string]*memTxn
	ocodes   map[string]*memCode
	orefresh map[string]*memRefresh
}

type memTxn struct {
	t       OAuthTxn
	expires time.Time
}
type memCode struct {
	c    OAuthCode
	used bool
}
type memRefresh struct {
	r    OAuthRefresh
	used bool
}

type workKey struct{ project, kind, provider, key string }

type pairing struct {
	expires  time.Time
	failures int
	burned   bool
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		sessions: map[SessionKey]*SessionRecord{}, work: map[workKey]*WorkItemRecord{},
		tokens: map[string]*Token{}, pairings: map[string]*pairing{},
		oclients: map[string]*OAuthClient{}, otxns: map[string]*memTxn{}, ocodes: map[string]*memCode{}, orefresh: map[string]*memRefresh{},
	}
}

// Close implements Store.
func (m *Memory) Close() error { return nil }

func skey(p *model.SessionPayload) SessionKey { return SessionKey{p.Source, p.SessionID} }

// PutSession implements Store.
func (m *Memory) PutSession(_ context.Context, p *model.SessionPayload, now time.Time, lim Limits) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := skey(p)
	if _, exists := m.sessions[k]; !exists {
		perProject := 0
		for _, r := range m.sessions {
			if r.Payload.Project == p.Project {
				perProject++
			}
		}
		if (lim.MaxSessionsPerProject > 0 && perProject >= lim.MaxSessionsPerProject) ||
			(lim.MaxSessionsTotal > 0 && len(m.sessions) >= lim.MaxSessionsTotal) {
			return ErrQuota
		}
	}
	if old, ok := m.sessions[k]; ok && old.Payload.Project != p.Project {
		m.dropSessionWork(old.Payload.Project, k)
	}
	cp := *p
	m.sessions[k] = &SessionRecord{Payload: cp, IngestedAt: now.UTC()}
	for _, it := range p.WorkItems {
		wk := workKey{p.Project, it.Kind, it.Provider, it.Key}
		if cur, ok := m.work[wk]; ok {
			cur.Item = workitems.Merge(cur.Item, it)
			cur.LastSeen = now.UTC()
			if !hasSession(cur.Sessions, k) {
				cur.Sessions = append(cur.Sessions, k)
			}
			continue
		}
		m.work[wk] = &WorkItemRecord{Project: p.Project, Item: it, FirstSeen: now.UTC(), LastSeen: now.UTC(), Sessions: []SessionKey{k}}
	}
	return nil
}

func hasSession(s []SessionKey, k SessionKey) bool {
	for _, x := range s {
		if x == k {
			return true
		}
	}
	return false
}

// dropSessionWork unlinks a session from work items, deleting orphans.
func (m *Memory) dropSessionWork(project string, k SessionKey) {
	for wk, rec := range m.work {
		if wk.project != project {
			continue
		}
		var keep []SessionKey
		for _, s := range rec.Sessions {
			if s != k {
				keep = append(keep, s)
			}
		}
		if len(keep) == 0 {
			delete(m.work, wk)
		} else {
			rec.Sessions = keep
		}
	}
}

// DeleteProject implements Store.
func (m *Memory) DeleteProject(_ context.Context, project string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, r := range m.sessions {
		if r.Payload.Project == project {
			delete(m.sessions, k)
			n++
		}
	}
	for wk := range m.work {
		if wk.project == project {
			delete(m.work, wk)
		}
	}
	return n, nil
}

// DeleteSession implements Store.
func (m *Memory) DeleteSession(_ context.Context, source, id string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, r := range m.sessions {
		if k.ID == id && (source == "" || k.Source == source) {
			delete(m.sessions, k)
			m.dropSessionWork(r.Payload.Project, k)
			n++
		}
	}
	return n, nil
}

// ListProjects implements Store.
func (m *Memory) ListProjects(context.Context) ([]ProjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	by := map[string]*ProjectInfo{}
	for _, r := range m.sessions {
		p := r.Payload
		pi, ok := by[p.Project]
		if !ok {
			pi = &ProjectInfo{Project: p.Project, Sources: map[string]int{}}
			by[p.Project] = pi
		}
		pi.Sessions++
		pi.Sources[p.Source]++
		if p.UpdatedAt.After(pi.LastActivity) {
			pi.LastActivity = p.UpdatedAt
		}
	}
	out := make([]ProjectInfo, 0, len(by))
	for _, pi := range by {
		out = append(out, *pi)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastActivity.Equal(out[j].LastActivity) {
			return out[i].LastActivity.After(out[j].LastActivity)
		}
		return out[i].Project < out[j].Project
	})
	return out, nil
}

func newestFirst(rs []SessionRecord) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i].Payload, rs[j].Payload
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.Source+a.SessionID < b.Source+b.SessionID
	})
}

func page[T any](s []T, offset, limit int) []T {
	if offset > len(s) {
		return nil
	}
	s = s[offset:]
	if limit > 0 && len(s) > limit {
		s = s[:limit]
	}
	return s
}

// Sessions implements Store.
func (m *Memory) Sessions(_ context.Context, q SessionQuery) ([]SessionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SessionRecord
	for _, r := range m.sessions {
		p := r.Payload
		if (q.Project != "" && p.Project != q.Project) || (q.Source != "" && p.Source != q.Source) || (!q.Since.IsZero() && p.UpdatedAt.Before(q.Since)) {
			continue
		}
		out = append(out, *r)
	}
	newestFirst(out)
	return page(out, q.Offset, q.Limit), nil
}

// GetSession implements Store.
func (m *Memory) GetSession(_ context.Context, source, id string) (*SessionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, r := range m.sessions {
		if k.ID == id && (source == "" || k.Source == source) {
			cp := *r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// Tokenize splits text into lowercase alphanumeric words (shared by backends).
func Tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// SearchText is the text indexed for a payload.
func SearchText(p *model.SessionPayload) string {
	var b strings.Builder
	add := func(s string) { b.WriteString(s); b.WriteByte(' ') }
	add(p.Project)
	add(p.TitleOnly)
	if s := p.Summary; s != nil {
		add(s.Title)
		add(s.Goal)
		add(s.LastAssistant)
		add(s.Outcome)
		add(strings.Join(s.FilesTouched, " "))
	}
	for _, w := range p.WorkItems {
		add(w.Key)
		add(w.Title)
	}
	return b.String()
}

// Search implements Store: every query word must appear (prefix match on words).
func (m *Memory) Search(_ context.Context, query, project string, limit int) ([]SessionRecord, error) {
	words := Tokenize(query)
	if len(words) == 0 {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SessionRecord
	for _, r := range m.sessions {
		if project != "" && r.Payload.Project != project {
			continue
		}
		have := Tokenize(SearchText(&r.Payload))
		ok := true
		for _, w := range words {
			found := false
			for _, h := range have {
				if strings.HasPrefix(h, w) {
					found = true
					break
				}
			}
			if !found {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, *r)
		}
	}
	newestFirst(out)
	return page(out, 0, limit), nil
}

// WorkItems implements Store.
func (m *Memory) WorkItems(_ context.Context, q WorkItemQuery) ([]WorkItemRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []WorkItemRecord
	for _, w := range m.work {
		it := w.Item
		if (q.Project != "" && w.Project != q.Project) || (q.Kind != "" && it.Kind != q.Kind) ||
			(q.Provider != "" && it.Provider != q.Provider) || (q.Status != "" && it.Status != q.Status) ||
			(!q.Since.IsZero() && it.ObservedAt.Before(q.Since)) {
			continue
		}
		cp := *w
		cp.Sessions = append([]SessionKey(nil), w.Sessions...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.Item.ObservedAt.Equal(b.Item.ObservedAt) {
			return a.Item.ObservedAt.After(b.Item.ObservedAt)
		}
		return a.Project+a.Item.Key < b.Project+b.Item.Key
	})
	return page(out, q.Offset, q.Limit), nil
}

// --- auth ---

// CreateToken implements AuthStore.
func (m *Memory) CreateToken(_ context.Context, t Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.tokens {
		if x.Hash == t.Hash || x.ID == t.ID {
			return errDuplicate
		}
	}
	m.tokens[t.ID] = &t
	return nil
}

// TokenByHash implements AuthStore.
func (m *Memory) TokenByHash(_ context.Context, hash string) (*Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.Hash == hash {
			cp := *t
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// ListTokens implements AuthStore.
func (m *Memory) ListTokens(context.Context) ([]Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Token, 0, len(m.tokens))
	for _, t := range m.tokens {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// RevokeToken implements AuthStore.
func (m *Memory) RevokeToken(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return ErrNotFound
	}
	if t.RevokedAt.IsZero() || at.Before(t.RevokedAt) {
		t.RevokedAt = at.UTC()
	}
	return nil
}

// SetTokenExpiry implements AuthStore.
func (m *Memory) SetTokenExpiry(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return ErrNotFound
	}
	t.ExpiresAt = at.UTC()
	return nil
}

// TouchToken implements AuthStore.
func (m *Memory) TouchToken(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tokens[id]; ok {
		t.LastUsedAt = at.UTC()
	}
	return nil
}

// CountActiveTokens implements AuthStore.
func (m *Memory) CountActiveTokens(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.tokens {
		if t.Active(now) {
			n++
		}
	}
	return n, nil
}

// CreatePairing implements AuthStore.
func (m *Memory) CreatePairing(_ context.Context, hash string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pairings[hash]; ok {
		return errDuplicate
	}
	m.pairings[hash] = &pairing{expires: expires.UTC()}
	return nil
}

// ConsumePairing implements AuthStore.
func (m *Memory) ConsumePairing(_ context.Context, hash string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pairings[hash]
	if !ok || p.burned || !now.Before(p.expires) {
		return false, nil
	}
	p.burned = true
	return true, nil
}

// PairingFailure implements AuthStore.
func (m *Memory) PairingFailure(_ context.Context, now time.Time, max int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pairings {
		if p.burned || !now.Before(p.expires) {
			continue
		}
		p.failures++
		if p.failures >= max {
			p.burned = true
		}
	}
	return nil
}

// AppendAudit implements AuthStore.
func (m *Memory) AppendAudit(_ context.Context, e AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.At = e.At.UTC()
	m.audit = append(m.audit, e)
	return nil
}

// ListAudit implements AuthStore (newest first).
func (m *Memory) ListAudit(_ context.Context, limit int) ([]AuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEvent, 0, len(m.audit))
	for i := len(m.audit) - 1; i >= 0; i-- {
		out = append(out, m.audit[i])
	}
	return page(out, 0, limit), nil
}

// PruneAudit implements AuthStore.
func (m *Memory) PruneAudit(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keep []AuditEvent
	for _, e := range m.audit {
		if !e.At.Before(before) {
			keep = append(keep, e)
		}
	}
	n := len(m.audit) - len(keep)
	m.audit = keep
	return n, nil
}

// --- OAuth ---

// CreateOAuthClient implements OAuthStore.
func (m *Memory) CreateOAuthClient(_ context.Context, c OAuthClient) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.oclients[c.ID]; ok {
		return errDuplicate
	}
	c.RedirectURIs = append([]string(nil), c.RedirectURIs...)
	m.oclients[c.ID] = &c
	return nil
}

// OAuthClient implements OAuthStore.
func (m *Memory) OAuthClient(_ context.Context, id string) (*OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.oclients[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *c
	cp.RedirectURIs = append([]string(nil), c.RedirectURIs...)
	return &cp, nil
}

// ListOAuthClients implements OAuthStore.
func (m *Memory) ListOAuthClients(context.Context) ([]OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]OAuthClient, 0, len(m.oclients))
	for _, c := range m.oclients {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// CountOAuthClients implements OAuthStore.
func (m *Memory) CountOAuthClients(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.oclients), nil
}

// TouchOAuthClient implements OAuthStore.
func (m *Memory) TouchOAuthClient(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.oclients[id]; ok {
		c.LastUsedAt = at.UTC()
	}
	return nil
}

// DeleteOAuthClient implements OAuthStore.
func (m *Memory) DeleteOAuthClient(_ context.Context, id string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.oclients[id]; !ok {
		return 0, ErrNotFound
	}
	delete(m.oclients, id)
	n := 0
	for _, t := range m.tokens {
		if t.ClientID == id && (t.RevokedAt.IsZero() || at.Before(t.RevokedAt)) {
			t.RevokedAt = at.UTC()
			n++
		}
	}
	for h, c := range m.ocodes {
		if c.c.ClientID == id {
			delete(m.ocodes, h)
		}
	}
	for h, r := range m.orefresh {
		if r.r.ClientID == id {
			delete(m.orefresh, h)
		}
	}
	for h, t := range m.otxns {
		if t.t.ClientID == id {
			delete(m.otxns, h)
		}
	}
	return n, nil
}

// PutOAuthTxn implements OAuthStore.
func (m *Memory) PutOAuthTxn(_ context.Context, hash string, t OAuthTxn, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.otxns[hash] = &memTxn{t: t, expires: expires.UTC()}
	return nil
}

// TakeOAuthTxn implements OAuthStore.
func (m *Memory) TakeOAuthTxn(_ context.Context, hash string, now time.Time) (*OAuthTxn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.otxns[hash]
	if !ok {
		return nil, ErrNotFound
	}
	delete(m.otxns, hash)
	if !now.Before(t.expires) {
		return nil, ErrNotFound
	}
	cp := t.t
	return &cp, nil
}

// PutOAuthCode implements OAuthStore.
func (m *Memory) PutOAuthCode(_ context.Context, hash string, c OAuthCode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ocodes[hash] = &memCode{c: c}
	return nil
}

// UseOAuthCode implements OAuthStore.
func (m *Memory) UseOAuthCode(_ context.Context, hash string, now time.Time) (*OAuthCode, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.ocodes[hash]
	if !ok {
		return nil, false, ErrNotFound
	}
	cp := c.c
	if c.used {
		return &cp, true, nil
	}
	if !now.Before(c.c.Expires) {
		return nil, false, ErrNotFound
	}
	c.used = true
	return &cp, false, nil
}

// PutOAuthRefresh implements OAuthStore.
func (m *Memory) PutOAuthRefresh(_ context.Context, r OAuthRefresh) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orefresh[r.Hash] = &memRefresh{r: r}
	return nil
}

// UseOAuthRefresh implements OAuthStore.
func (m *Memory) UseOAuthRefresh(_ context.Context, hash string, now time.Time) (*OAuthRefresh, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.orefresh[hash]
	if !ok {
		return nil, false, ErrNotFound
	}
	cp := r.r
	if r.used {
		return &cp, true, nil
	}
	if !now.Before(r.r.Expires) {
		return nil, false, ErrNotFound
	}
	r.used = true
	return &cp, false, nil
}

// RevokeFamily implements OAuthStore.
func (m *Memory) RevokeFamily(_ context.Context, family string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.Family == family && (t.RevokedAt.IsZero() || at.Before(t.RevokedAt)) {
			t.RevokedAt = at.UTC()
		}
	}
	for h, r := range m.orefresh {
		if r.r.Family == family {
			delete(m.orefresh, h)
		}
	}
	return nil
}

// PruneOAuth implements OAuthStore.
func (m *Memory) PruneOAuth(_ context.Context, now, unusedClientsBefore time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for h, t := range m.otxns {
		if !now.Before(t.expires) {
			delete(m.otxns, h)
			n++
		}
	}
	for h, c := range m.ocodes {
		if now.After(c.c.Expires.Add(24 * time.Hour)) {
			delete(m.ocodes, h)
			n++
		}
	}
	for h, r := range m.orefresh {
		if now.After(r.r.Expires.Add(24 * time.Hour)) {
			delete(m.orefresh, h)
			n++
		}
	}
	for id, c := range m.oclients {
		if c.LastUsedAt.IsZero() && c.CreatedAt.Before(unusedClientsBefore) {
			delete(m.oclients, id)
			n++
		}
	}
	return n, nil
}
