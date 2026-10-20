package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CreateOAuthClient implements OAuthStore.
func (s *SQLite) CreateOAuthClient(ctx context.Context, c OAuthClient) error {
	uris, _ := json.Marshal(c.RedirectURIs)
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_clients(id, name, secret_hash, redirect_uris, created_at, last_used_at) VALUES(?,?,?,?,?,?)`,
		c.ID, c.Name, c.SecretHash, string(uris), nano(c.CreatedAt), nano(c.LastUsedAt))
	if isUnique(err) {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("sqlite store: create oauth client: %w", err)
	}
	return nil
}

func scanClient(r rowScanner) (OAuthClient, error) {
	var c OAuthClient
	var uris string
	var created, used int64
	if err := r.Scan(&c.ID, &c.Name, &c.SecretHash, &uris, &created, &used); err != nil {
		return OAuthClient{}, err
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return OAuthClient{}, fmt.Errorf("decode redirect uris: %w", err)
	}
	c.CreatedAt, c.LastUsedAt = fromNano(created), fromNano(used)
	return c, nil
}

const clientCols = `id, name, secret_hash, redirect_uris, created_at, last_used_at`

// OAuthClient implements OAuthStore.
func (s *SQLite) OAuthClient(ctx context.Context, id string) (*OAuthClient, error) {
	c, err := scanClient(s.db.QueryRowContext(ctx, `SELECT `+clientCols+` FROM oauth_clients WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite store: oauth client: %w", err)
	}
	return &c, nil
}

// ListOAuthClients implements OAuthStore.
func (s *SQLite) ListOAuthClients(ctx context.Context) ([]OAuthClient, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+clientCols+` FROM oauth_clients ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite store: list oauth clients: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []OAuthClient
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite store: list oauth clients: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountOAuthClients implements OAuthStore.
func (s *SQLite) CountOAuthClients(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_clients`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite store: count oauth clients: %w", err)
	}
	return n, nil
}

// TouchOAuthClient implements OAuthStore.
func (s *SQLite) TouchOAuthClient(ctx context.Context, id string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE oauth_clients SET last_used_at=? WHERE id=?`, nano(at), id); err != nil {
		return fmt.Errorf("sqlite store: touch oauth client: %w", err)
	}
	return nil
}

// DeleteOAuthClient implements OAuthStore.
func (s *SQLite) DeleteOAuthClient(ctx context.Context, id string, at time.Time) (int, error) {
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM oauth_clients WHERE id=?`, id)
		if err != nil {
			return fmt.Errorf("sqlite store: delete oauth client: %w", err)
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return ErrNotFound
		}
		res, err = tx.ExecContext(ctx, `UPDATE tokens SET revoked_at=? WHERE client_id=? AND (revoked_at=0 OR revoked_at>?)`, nano(at), id, nano(at))
		if err != nil {
			return fmt.Errorf("sqlite store: revoke client tokens: %w", err)
		}
		c, _ := res.RowsAffected()
		n = int(c)
		for _, q := range []string{`DELETE FROM oauth_codes WHERE client_id=?`, `DELETE FROM oauth_refresh WHERE client_id=?`, `DELETE FROM oauth_txns WHERE client_id=?`} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return fmt.Errorf("sqlite store: delete client state: %w", err)
			}
		}
		return nil
	})
	return n, err
}

// PutOAuthTxn implements OAuthStore.
func (s *SQLite) PutOAuthTxn(ctx context.Context, hash string, t OAuthTxn, expires time.Time) error {
	b, _ := json.Marshal(t)
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO oauth_txns(hash, data, client_id, expires_at) VALUES(?,?,?,?)`, hash, string(b), t.ClientID, nano(expires)); err != nil {
		return fmt.Errorf("sqlite store: put oauth txn: %w", err)
	}
	return nil
}

// TakeOAuthTxn implements OAuthStore.
func (s *SQLite) TakeOAuthTxn(ctx context.Context, hash string, now time.Time) (*OAuthTxn, error) {
	var t OAuthTxn
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var data string
		var exp int64
		if err := tx.QueryRowContext(ctx, `SELECT data, expires_at FROM oauth_txns WHERE hash=?`, hash).Scan(&data, &exp); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("sqlite store: take oauth txn: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_txns WHERE hash=?`, hash); err != nil {
			return fmt.Errorf("sqlite store: take oauth txn: %w", err)
		}
		if nano(now) >= exp {
			return ErrNotFound
		}
		return json.Unmarshal([]byte(data), &t)
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// PutOAuthCode implements OAuthStore.
func (s *SQLite) PutOAuthCode(ctx context.Context, hash string, c OAuthCode) error {
	b, _ := json.Marshal(c)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO oauth_codes(hash, data, client_id, family, expires_at) VALUES(?,?,?,?,?)`, hash, string(b), c.ClientID, c.Family, nano(c.Expires)); err != nil {
		return fmt.Errorf("sqlite store: put oauth code: %w", err)
	}
	return nil
}

// UseOAuthCode implements OAuthStore.
func (s *SQLite) UseOAuthCode(ctx context.Context, hash string, now time.Time) (*OAuthCode, bool, error) {
	var c OAuthCode
	replay := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var data string
		var exp int64
		var used int
		if err := tx.QueryRowContext(ctx, `SELECT data, expires_at, used FROM oauth_codes WHERE hash=?`, hash).Scan(&data, &exp, &used); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("sqlite store: use oauth code: %w", err)
		}
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return fmt.Errorf("sqlite store: decode oauth code: %w", err)
		}
		if used == 1 {
			replay = true
			return nil
		}
		if nano(now) >= exp {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_codes SET used=1 WHERE hash=?`, hash); err != nil {
			return fmt.Errorf("sqlite store: use oauth code: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &c, replay, nil
}

// PutOAuthRefresh implements OAuthStore.
func (s *SQLite) PutOAuthRefresh(ctx context.Context, r OAuthRefresh) error {
	b, _ := json.Marshal(r)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO oauth_refresh(hash, data, client_id, family, expires_at) VALUES(?,?,?,?,?)`, r.Hash, string(b), r.ClientID, r.Family, nano(r.Expires)); err != nil {
		return fmt.Errorf("sqlite store: put oauth refresh: %w", err)
	}
	return nil
}

// UseOAuthRefresh implements OAuthStore.
func (s *SQLite) UseOAuthRefresh(ctx context.Context, hash string, now time.Time) (*OAuthRefresh, bool, error) {
	var r OAuthRefresh
	replay := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var data string
		var exp int64
		var used int
		if err := tx.QueryRowContext(ctx, `SELECT data, expires_at, used FROM oauth_refresh WHERE hash=?`, hash).Scan(&data, &exp, &used); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("sqlite store: use oauth refresh: %w", err)
		}
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return fmt.Errorf("sqlite store: decode oauth refresh: %w", err)
		}
		if used == 1 {
			replay = true
			return nil
		}
		if nano(now) >= exp {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_refresh SET used=1 WHERE hash=?`, hash); err != nil {
			return fmt.Errorf("sqlite store: use oauth refresh: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &r, replay, nil
}

// RevokeFamily implements OAuthStore.
func (s *SQLite) RevokeFamily(ctx context.Context, family string, at time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE tokens SET revoked_at=? WHERE family=? AND (revoked_at=0 OR revoked_at>?)`, nano(at), family, nano(at)); err != nil {
			return fmt.Errorf("sqlite store: revoke family: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_refresh WHERE family=?`, family); err != nil {
			return fmt.Errorf("sqlite store: revoke family: %w", err)
		}
		return nil
	})
}

// PruneOAuth implements OAuthStore.
func (s *SQLite) PruneOAuth(ctx context.Context, now, unusedClientsBefore time.Time) (int, error) {
	total := 0
	day := (24 * time.Hour).Nanoseconds()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM oauth_txns WHERE expires_at<=?`, []any{nano(now)}},
		{`DELETE FROM oauth_codes WHERE expires_at+?<?`, []any{day, nano(now)}},
		{`DELETE FROM oauth_refresh WHERE expires_at+?<?`, []any{day, nano(now)}},
		{`DELETE FROM oauth_clients WHERE last_used_at=0 AND created_at<?`, []any{nano(unusedClientsBefore)}},
	} {
		res, err := s.db.ExecContext(ctx, q.sql, q.args...)
		if err != nil {
			return total, fmt.Errorf("sqlite store: prune oauth: %w", err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}
