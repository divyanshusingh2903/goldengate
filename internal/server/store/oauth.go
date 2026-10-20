package store

import (
	"context"
	"time"
)

// OAuthClient is a registered OAuth client (dynamic or created by the operator).
type OAuthClient struct {
	ID           string
	Name         string
	SecretHash   string // empty for public clients
	RedirectURIs []string
	CreatedAt    time.Time
	LastUsedAt   time.Time
}

// OAuthTxn is an authorization request waiting for the owner's consent. The
// browser only ever carries the transaction id, never these parameters.
type OAuthTxn struct {
	ClientID    string
	RedirectURI string
	State       string
	Challenge   string
	Resource    string
	Scope       string
}

// OAuthCode is an issued authorization code.
type OAuthCode struct {
	ClientID    string
	RedirectURI string
	Challenge   string
	Resource    string
	Scope       string
	Family      string
	Expires     time.Time
}

// OAuthRefresh is a stored refresh token (by hash).
type OAuthRefresh struct {
	Hash     string
	ClientID string
	Family   string
	Resource string
	Scope    string
	Expires  time.Time
}

// OAuthStore holds the state of the built-in OAuth 2.1 authorization server.
type OAuthStore interface {
	CreateOAuthClient(ctx context.Context, c OAuthClient) error
	OAuthClient(ctx context.Context, id string) (*OAuthClient, error)
	ListOAuthClients(ctx context.Context) ([]OAuthClient, error)
	CountOAuthClients(ctx context.Context) (int, error)
	TouchOAuthClient(ctx context.Context, id string, at time.Time) error
	// DeleteOAuthClient removes a client and everything it holds: its
	// access tokens are revoked at 'at', its codes, refresh tokens and
	// pending requests are deleted. It returns how many tokens it revoked.
	DeleteOAuthClient(ctx context.Context, id string, at time.Time) (int, error)

	PutOAuthTxn(ctx context.Context, hash string, t OAuthTxn, expires time.Time) error
	// TakeOAuthTxn returns and deletes a pending request (single use).
	TakeOAuthTxn(ctx context.Context, hash string, now time.Time) (*OAuthTxn, error)

	PutOAuthCode(ctx context.Context, hash string, c OAuthCode) error
	// UseOAuthCode marks a code used. replay is true when it had been used
	// before (the caller must then revoke the grant's family). Unknown or
	// expired-and-unused codes yield ErrNotFound.
	UseOAuthCode(ctx context.Context, hash string, now time.Time) (c *OAuthCode, replay bool, err error)

	PutOAuthRefresh(ctx context.Context, r OAuthRefresh) error
	// UseOAuthRefresh marks a refresh token used (rotation). replay is true
	// when it had been used before.
	UseOAuthRefresh(ctx context.Context, hash string, now time.Time) (r *OAuthRefresh, replay bool, err error)
	// RevokeFamily revokes every access token of a grant family at 'at' and
	// deletes its refresh tokens.
	RevokeFamily(ctx context.Context, family string, at time.Time) error

	// PruneOAuth deletes expired transactions, codes and refresh tokens, and
	// clients that registered but were never used before createdBefore.
	PruneOAuth(ctx context.Context, now, unusedClientsBefore time.Time) (int, error)
}
