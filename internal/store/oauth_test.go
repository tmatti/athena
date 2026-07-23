package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/testdb"
)

func TestDeleteExpiredOAuthSweepsStaleClients(t *testing.T) {
	s := New(testdb.Pool(t))
	ctx := context.Background()

	backdate := func(id string) {
		_, err := s.pool.Exec(ctx,
			`UPDATE oauth_clients SET created_at = now() - interval '31 days' WHERE id = $1`, id)
		require.NoError(t, err)
	}

	// An old client with no codes or tokens is swept.
	stale, err := s.CreateOAuthClient(ctx, "stale", []string{"https://a.example/cb"})
	require.NoError(t, err)
	backdate(stale.ID)

	// An old client that still holds a live token survives.
	active, err := s.CreateOAuthClient(ctx, "active", []string{"https://b.example/cb"})
	require.NoError(t, err)
	backdate(active.ID)
	tok := OAuthTokenParams{Hash: []byte("live-token-hash"), Kind: TokenKindRefresh,
		ClientID: active.ID, Subject: SubjectOwner, ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, s.InsertOAuthTokens(ctx, tok))

	// A recent client with no grants also survives.
	fresh, err := s.CreateOAuthClient(ctx, "fresh", []string{"https://c.example/cb"})
	require.NoError(t, err)

	require.NoError(t, s.DeleteExpiredOAuth(ctx))

	_, err = s.GetOAuthClient(ctx, stale.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetOAuthClient(ctx, active.ID)
	require.NoError(t, err)
	_, err = s.GetOAuthClient(ctx, fresh.ID)
	require.NoError(t, err)
}
