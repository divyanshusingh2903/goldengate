package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// metaSchema is a small key/value table for state that belongs to no
// session: today, how unattended runs are backing off from an unreachable
// server.
const metaSchema = `
CREATE TABLE IF NOT EXISTS sync_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

const offlineKey = "offline"

// Offline is what unattended runs remember about an unreachable server:
// how many runs in a row could not reach it, and when the last one tried.
type Offline struct {
	Failures int       `json:"failures"`
	Last     time.Time `json:"last"`
}

// maxOfflineWait caps how long unattended runs leave an unreachable server
// alone.
const maxOfflineWait = time.Hour

// Next is the earliest time an unattended run should try the server again:
// a minute after the first failure, doubling up to an hour. Zero when the
// last run reached it.
func (o Offline) Next() time.Time {
	if o.Failures <= 0 {
		return time.Time{}
	}
	wait := maxOfflineWait
	if o.Failures <= 7 { // 2^6 minutes is past the cap
		wait = min(time.Duration(1<<(o.Failures-1))*time.Minute, maxOfflineWait)
	}
	return o.Last.Add(wait)
}

// Offline reads the backoff record; a server never found unreachable gives
// the zero value.
func (s *State) Offline(ctx context.Context) (Offline, error) {
	var o Offline
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM sync_meta WHERE key = ?`, offlineKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return o, nil
	}
	if err != nil {
		return o, fmt.Errorf("state: read offline: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		return Offline{}, nil // an unreadable record only means trying the server now
	}
	return o, nil
}

// SetOffline stores the backoff record; the zero value clears it.
func (s *State) SetOffline(ctx context.Context, o Offline) error {
	if o.Failures <= 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sync_meta WHERE key = ?`, offlineKey); err != nil {
			return fmt.Errorf("state: clear offline: %w", err)
		}
		return nil
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sync_meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, offlineKey, string(raw)); err != nil {
		return fmt.Errorf("state: write offline: %w", err)
	}
	return nil
}
