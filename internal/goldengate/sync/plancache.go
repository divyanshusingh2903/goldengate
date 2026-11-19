package sync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// planCacheVersion changes when the code that builds an offline payload
// changes in a way the fingerprint cannot see.
const planCacheVersion = 1

// planCacheSchema keeps what an offline plan built for a session that was
// never sent (a sent session already has its row in sessions), so opening the
// inbox does not read and rebuild every such transcript again. A row is used
// only while the file's size and modified time and the fingerprint of
// everything that shapes the payload are the same; a send never reads it.
const planCacheSchema = `
CREATE TABLE IF NOT EXISTS plan_cache(
  source TEXT NOT NULL, session_id TEXT NOT NULL, project TEXT NOT NULL,
  size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, fingerprint TEXT NOT NULL,
  remote TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL,
  PRIMARY KEY(source, session_id));
CREATE INDEX IF NOT EXISTS plan_cache_project ON plan_cache(project);
`

// cachedPlan is a stored offline build of one session.
type cachedPlan struct {
	Payload        *model.SessionPayload
	Remote, Branch string // what rules read from the transcript
}

type planRow struct {
	source, id, project, fingerprint, remote, branch string
	size, mtime                                      int64
	payload                                          []byte
}

// cachedPlan returns the stored build for a session whose file and
// fingerprint are unchanged. A cache that cannot be read is a miss.
func (s *State) cachedPlan(ctx context.Context, source, id string, size, mtime int64, fp string) (cachedPlan, bool) {
	var c cachedPlan
	var payload string
	err := s.db.QueryRowContext(ctx,
		`SELECT remote, branch, payload FROM plan_cache WHERE source=? AND session_id=? AND size=? AND mtime_ns=? AND fingerprint=?`,
		source, id, size, mtime, fp).Scan(&c.Remote, &c.Branch, &payload)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return c, false
	}
	var p model.SessionPayload
	if json.Unmarshal([]byte(payload), &p) != nil {
		return c, false
	}
	c.Payload = &p
	return c, true
}

func (s *State) storePlans(ctx context.Context, rows []planRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: plan cache: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO plan_cache(source, session_id, project, size, mtime_ns, fingerprint, remote, branch, payload)
VALUES(?,?,?,?,?,?,?,?,?)`, r.source, r.id, r.project, r.size, r.mtime, r.fingerprint, r.remote, r.branch, string(r.payload)); err != nil {
			return fmt.Errorf("state: plan cache: %w", err)
		}
	}
	return tx.Commit()
}

// prunePlans removes the rows of sessions a full plan no longer lists (the
// file is gone, or its project is no longer shared).
func (s *State) prunePlans(ctx context.Context, keep map[[2]string]bool) error {
	rows, err := s.db.QueryContext(ctx, `SELECT source, session_id FROM plan_cache`)
	if err != nil {
		return fmt.Errorf("state: plan cache: %w", err)
	}
	var drop [][2]string
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			_ = rows.Close()
			return fmt.Errorf("state: plan cache: %w", err)
		}
		if !keep[k] {
			drop = append(drop, k)
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("state: plan cache: %w", err)
	}
	for _, k := range drop {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM plan_cache WHERE source=? AND session_id=?`, k[0], k[1]); err != nil {
			return fmt.Errorf("state: plan cache: %w", err)
		}
	}
	return nil
}

// modelStamp changes whenever a model answer is cached: an offline build
// uses cached answers, so a new one can change a payload.
func (s *State) modelStamp(ctx context.Context) string {
	var n, last int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(created_at),0) FROM model_summaries`).Scan(&n, &last); err != nil {
		return "?"
	}
	return fmt.Sprintf("%d/%d", n, last)
}

// planFingerprint hashes everything, apart from the session's own file, that
// shapes an offline payload: the share level, redaction, work-item and
// summarizer settings, the cached model answers, and this code's version.
func (s *Syncer) planFingerprint(ctx context.Context) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	stamp := ""
	name := ""
	if s.Summarizer != nil {
		stamp, name = s.State.modelStamp(ctx), s.Summarizer.Name()
	}
	_ = enc.Encode([]any{planCacheVersion, s.Home, s.Config.Share.Level, s.Config.Redact, s.Config.Issues, s.Config.Summarize, name, stamp})
	return hex.EncodeToString(h.Sum(nil))
}

// sessionFingerprint adds what depends on the single session: the project
// it is filed under, which the config and the resolver decide.
func (s *Syncer) sessionFingerprint(base string, ref model.SessionRef) string {
	id, name := s.projectOf(ref)
	return base + "|" + id + "|" + name + "|" + s.serverKey(ref)
}

// useCachedPlan returns the stored build of a session when it may be used:
// its file and fingerprint are unchanged, and the user has no edit of it (an
// edit changes the payload, and is read from the state each time).
func (s *Syncer) useCachedPlan(ctx context.Context, ref model.SessionRef, mtime int64, fp string) (cachedPlan, bool) {
	if _, edited, err := s.State.Edit(ctx, ref.Source, ref.ID); err != nil || edited {
		return cachedPlan{}, false
	}
	return s.State.cachedPlan(ctx, ref.Source, ref.ID, ref.SizeBytes, mtime, s.sessionFingerprint(fp, ref))
}

// rememberPlans stores what an offline plan built, so the next one can skip
// reading those sessions. A failure only costs the next plan its speed. After
// a plan of everything it also drops the rows of sessions no longer listed.
func (s *Syncer) rememberPlans(ctx context.Context, jobs []job, fp string, listed map[[2]string]bool, full bool) {
	var rows []planRow
	for _, j := range jobs {
		p := j.payload
		if j.cached || p == nil || p.EditConflict || (p.Summary != nil && p.Summary.Edited) {
			continue
		}
		body, err := json.Marshal(p)
		if err != nil {
			continue
		}
		rows = append(rows, planRow{source: j.ref.Source, id: j.ref.ID, project: j.ref.ProjectKey, size: j.ref.SizeBytes, mtime: j.mtime,
			fingerprint: s.sessionFingerprint(fp, j.ref), remote: j.remote, branch: j.branch, payload: body})
	}
	if err := s.State.storePlans(ctx, rows); err != nil {
		s.log().Warn("plan cache not saved", "err", err)
	}
	if full {
		if err := s.State.prunePlans(ctx, listed); err != nil {
			s.log().Warn("plan cache not pruned", "err", err)
		}
	}
}
