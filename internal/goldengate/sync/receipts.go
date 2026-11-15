package sync

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// A receipt is the record of one sync's automatic sends: every session that
// left because a project's trust level and rules sent it, not because the
// user approved it by hand. It lives in the local state database and holds
// nothing the server did not receive: ids, project names and redacted titles.

// Receipt is one sync's automatic sends.
type Receipt struct {
	ID    string
	At    time.Time
	Items []ReceiptItem
}

// ReceiptItem is one session that was sent automatically.
type ReceiptItem struct {
	Source, SessionID string
	Project           string // local project key
	ProjectID         string
	ProjectName       string
	Title             string // redacted, as sent
	RuleID            string // empty when the whole project is automatic
	Why               string // the rule in words, as of the send
	// RetractedAt is set once the session was deleted from the server again.
	RetractedAt time.Time
}

// ProjectLabel is the name to show for the item's project.
func (it ReceiptItem) ProjectLabel() string {
	if it.ProjectName != "" {
		return it.ProjectName
	}
	return it.Project
}

func newReceiptID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic("sync: system randomness unavailable: " + err.Error())
	}
	return "rc_" + hex.EncodeToString(b)
}

// addReceiptItem records one automatic send. Push calls it right after the
// session was sent, so a receipt lists exactly what left.
func (s *State) addReceiptItem(ctx context.Context, a AutoItem, now time.Time) error {
	if a.Receipt == "" {
		return errors.New("state: an automatic send without a receipt")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: receipt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO receipts(id, created_at) VALUES(?,?)`, a.Receipt, now.UnixNano()); err != nil {
		return fmt.Errorf("state: receipt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO receipt_items(receipt_id, source, session_id, project, project_id, project_name, title, rule_id, why, retracted_at)
		VALUES(?,?,?,?,?,?,?,?,?,0)`, a.Receipt, a.Source, a.SessionID, a.Project, a.ProjectID, a.ProjectName, a.Title, a.RuleID, a.Why); err != nil {
		return fmt.Errorf("state: receipt: %w", err)
	}
	return tx.Commit()
}

// Receipts lists receipts, newest first, with their items. limit 0 means all.
func (s *State) Receipts(ctx context.Context, limit int) ([]Receipt, error) {
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at FROM receipts ORDER BY created_at DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("state: receipts: %w", err)
	}
	var out []Receipt
	for rows.Next() {
		var r Receipt
		var at int64
		if err := rows.Scan(&r.ID, &at); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("state: receipts: %w", err)
		}
		r.At = time.Unix(0, at).UTC()
		out = append(out, r)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("state: receipts: %w", err)
	}
	for i := range out {
		items, err := s.receiptItems(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

// Receipt returns one receipt by id.
func (s *State) Receipt(ctx context.Context, id string) (Receipt, bool, error) {
	r := Receipt{ID: id}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT created_at FROM receipts WHERE id=?`, id).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, fmt.Errorf("state: receipt: %w", err)
	}
	r.At = time.Unix(0, at).UTC()
	if r.Items, err = s.receiptItems(ctx, id); err != nil {
		return Receipt{}, false, err
	}
	return r, true, nil
}

func (s *State) receiptItems(ctx context.Context, id string) ([]ReceiptItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, session_id, project, project_id, project_name, title, rule_id, why, retracted_at
		FROM receipt_items WHERE receipt_id=? ORDER BY project_name, project, source, session_id`, id)
	if err != nil {
		return nil, fmt.Errorf("state: receipt items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ReceiptItem
	for rows.Next() {
		var it ReceiptItem
		var ret int64
		if err := rows.Scan(&it.Source, &it.SessionID, &it.Project, &it.ProjectID, &it.ProjectName, &it.Title, &it.RuleID, &it.Why, &ret); err != nil {
			return nil, fmt.Errorf("state: receipt items: %w", err)
		}
		if ret > 0 {
			it.RetractedAt = time.Unix(0, ret).UTC()
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkRetracted notes in every receipt that lists the session that it was
// deleted from the server again.
func (s *State) MarkRetracted(ctx context.Context, source, id string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE receipt_items SET retracted_at=? WHERE source=? AND session_id=? AND retracted_at=0`, now.UnixNano(), source, id); err != nil {
		return fmt.Errorf("state: receipt: %w", err)
	}
	return nil
}
