package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Token kinds stored in oauth_tokens.kind.
const (
	TokenKindAccess  = "access"
	TokenKindRefresh = "refresh"
)

type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

func (s *Store) CreateOAuthClient(ctx context.Context, name string, redirectURIs []string) (OAuthClient, error) {
	var c OAuthClient
	err := s.pool.QueryRow(ctx,
		`INSERT INTO oauth_clients (name, redirect_uris) VALUES ($1, $2)
		 RETURNING id, name, redirect_uris, created_at`,
		name, redirectURIs).Scan(&c.ID, &c.Name, &c.RedirectURIs, &c.CreatedAt)
	return c, err
}

func (s *Store) GetOAuthClient(ctx context.Context, id string) (OAuthClient, error) {
	var c OAuthClient
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, redirect_uris, created_at FROM oauth_clients WHERE id = $1`,
		id).Scan(&c.ID, &c.Name, &c.RedirectURIs, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthClient{}, ErrNotFound
	}
	return c, err
}

type AuthCodeParams struct {
	CodeHash      []byte
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	Resource      string
	Subject       string
	ExpiresAt     time.Time
}

func (s *Store) CreateAuthCode(ctx context.Context, p AuthCodeParams) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_auth_codes (code_hash, client_id, redirect_uri, code_challenge, scope, resource, subject, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		p.CodeHash, p.ClientID, p.RedirectURI, p.CodeChallenge, p.Scope, p.Resource, p.Subject, p.ExpiresAt)
	return err
}

type AuthCode struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	Resource      string
	Subject       string
	ExpiresAt     time.Time
}

// ConsumeAuthCode atomically deletes and returns the code so it can only be
// redeemed once. Expired codes are treated as not found.
func (s *Store) ConsumeAuthCode(ctx context.Context, codeHash []byte) (AuthCode, error) {
	var c AuthCode
	err := s.pool.QueryRow(ctx,
		`DELETE FROM oauth_auth_codes WHERE code_hash = $1
		 RETURNING client_id, redirect_uri, code_challenge, scope, resource, subject, expires_at`,
		codeHash).Scan(&c.ClientID, &c.RedirectURI, &c.CodeChallenge, &c.Scope, &c.Resource, &c.Subject, &c.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthCode{}, ErrNotFound
	}
	if err != nil {
		return AuthCode{}, err
	}
	if time.Now().After(c.ExpiresAt) {
		return AuthCode{}, ErrNotFound
	}
	return c, nil
}

type OAuthTokenParams struct {
	Hash      []byte
	Kind      string
	ClientID  string
	Subject   string
	Scope     string
	ExpiresAt time.Time
}

// InsertOAuthTokens stores a set of tokens (typically one access + one
// refresh) in a single transaction.
func (s *Store) InsertOAuthTokens(ctx context.Context, tokens ...OAuthTokenParams) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, t := range tokens {
			if _, err := tx.Exec(ctx,
				`INSERT INTO oauth_tokens (token_hash, kind, client_id, subject, scope, expires_at)
				 VALUES ($1, $2, $3, $4, $5, $6)`,
				t.Hash, t.Kind, t.ClientID, t.Subject, t.Scope, t.ExpiresAt); err != nil {
				return err
			}
		}
		return nil
	})
}

type OAuthToken struct {
	Kind      string
	ClientID  string
	Subject   string
	Scope     string
	ExpiresAt time.Time
}

// GetAccessToken returns a live access token by hash, or ErrNotFound.
func (s *Store) GetAccessToken(ctx context.Context, tokenHash []byte) (OAuthToken, error) {
	var t OAuthToken
	err := s.pool.QueryRow(ctx,
		`SELECT kind, client_id, subject, scope, expires_at FROM oauth_tokens
		 WHERE token_hash = $1 AND kind = 'access' AND expires_at > now()`,
		tokenHash).Scan(&t.Kind, &t.ClientID, &t.Subject, &t.Scope, &t.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthToken{}, ErrNotFound
	}
	return t, err
}

// ConsumeRefreshToken atomically deletes and returns a live refresh token so
// each refresh token can only be used once (rotation).
func (s *Store) ConsumeRefreshToken(ctx context.Context, tokenHash []byte) (OAuthToken, error) {
	var t OAuthToken
	err := s.pool.QueryRow(ctx,
		`DELETE FROM oauth_tokens WHERE token_hash = $1 AND kind = 'refresh'
		 RETURNING kind, client_id, subject, scope, expires_at`,
		tokenHash).Scan(&t.Kind, &t.ClientID, &t.Subject, &t.Scope, &t.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthToken{}, ErrNotFound
	}
	if err != nil {
		return OAuthToken{}, err
	}
	if time.Now().After(t.ExpiresAt) {
		return OAuthToken{}, ErrNotFound
	}
	return t, nil
}

// DeleteExpiredOAuth sweeps expired codes and tokens, plus registered clients
// that are old and hold no grant at all — registration is open, so abandoned
// rows would otherwise accumulate forever. Called opportunistically when new
// tokens are issued; the tables are tiny for a single-user server.
func (s *Store) DeleteExpiredOAuth(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM oauth_auth_codes WHERE expires_at <= now()`); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM oauth_tokens WHERE expires_at <= now()`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM oauth_clients c
		WHERE c.created_at < now() - interval '30 days'
		  AND NOT EXISTS (SELECT 1 FROM oauth_tokens t WHERE t.client_id = c.id)
		  AND NOT EXISTS (SELECT 1 FROM oauth_auth_codes a WHERE a.client_id = c.id)`)
	return err
}
