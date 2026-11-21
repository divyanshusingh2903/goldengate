package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
)

// Fingerprint summarizes which shared sessions exist and when each last
// changed: one hash over (source, id, size, last change) of every session in
// a shared, un-blocked project inside the since window. It lists sessions
// only; none is read or built, so a background process can call it every few
// minutes and start a real send only when the answer changed. A session in a
// folder that is not shared never contributes (and is never named).
func (s *Syncer) Fingerprint(ctx context.Context) (string, error) {
	since, err := s.sinceTime(RunOpts{})
	if err != nil {
		return "", err
	}
	var lines []string
	for _, a := range s.Registry.All() {
		det, err := a.Detect(ctx)
		if err != nil {
			return "", fmt.Errorf("sync: detect %s: %w", a.Name(), err)
		}
		if !det.Installed {
			continue
		}
		for ref, lerr := range a.ListSessions(ctx, adapters.ListOpts{Since: since}) {
			if lerr != nil {
				if adapters.IsSkip(lerr) {
					continue
				}
				return "", fmt.Errorf("sync: list %s: %w", a.Name(), lerr)
			}
			if !s.Shared(ref) {
				continue
			}
			if dead, err := s.State.Tombstoned(ctx, ref.ProjectKey); err != nil {
				return "", err
			} else if dead {
				continue
			}
			lines = append(lines, strings.Join([]string{ref.Source, ref.ID, strconv.FormatInt(ref.SizeBytes, 10),
				strconv.FormatInt(lastChange(ref).UnixNano(), 10)}, "\x00"))
		}
	}
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:]), nil
}

// LastDecision is the time of the newest inbox decision (approve, exclude,
// retract), as unix nanoseconds, or 0 when there is none. It changes when the
// user decides something, which can make a waiting session sendable.
func (s *State) LastDecision(ctx context.Context) (int64, error) {
	var at int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(decided_at), 0) FROM decisions`).Scan(&at); err != nil {
		return 0, fmt.Errorf("state: last decision: %w", err)
	}
	return at, nil
}
