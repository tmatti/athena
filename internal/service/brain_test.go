package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/embed"
	"github.com/tmatti/athena/internal/store"
	"github.com/tmatti/athena/internal/testdb"
)

func testBrain(t *testing.T, fe *embed.FakeEmbedder) *Brain {
	t.Helper()
	pool := testdb.Pool(t)
	var embedder embed.Embedder
	if fe != nil {
		embedder = fe
	}
	return New(store.New(pool), embedder, slog.New(slog.DiscardHandler))
}

func TestCreateMemoryEmbedsSynchronously(t *testing.T) {
	b := testBrain(t, &embed.FakeEmbedder{Dims: 1536})
	m, err := b.CreateMemory(context.Background(), "the user works in UTC+2", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", m.EmbedStatus)
}

func TestEmbedFailureThenRetryRecovers(t *testing.T) {
	fe := &embed.FakeEmbedder{Dims: 1536, Err: errors.New("provider down")}
	b := testBrain(t, fe)
	ctx := context.Background()

	// Write must succeed even though the provider is down.
	m, err := b.CreateMemory(ctx, "resilient fact", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "failed", m.EmbedStatus)

	// Provider recovers; the retry pass flips the row to ok.
	fe.Err = nil
	b.retryPendingEmbeds(ctx)

	got, err := b.GetMemory(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, "ok", got.EmbedStatus)
}

func TestHybridSearchDegradesToKeywordOnEmbedError(t *testing.T) {
	fe := &embed.FakeEmbedder{Dims: 1536}
	b := testBrain(t, fe)
	ctx := context.Background()

	_, err := b.CreateMemory(ctx, "the sky was full of kites today", nil, nil)
	require.NoError(t, err)

	fe.Err = errors.New("provider down")
	results, err := b.Search(ctx, SearchParams{Query: "kites", Mode: "hybrid"})
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Vector mode has no keyword fallback and must surface the error.
	_, err = b.Search(ctx, SearchParams{Query: "kites", Mode: "vector"})
	require.Error(t, err)
}

func TestSearchWithoutEmbedderIsKeywordOnly(t *testing.T) {
	b := testBrain(t, nil)
	ctx := context.Background()

	_, err := b.CreateMemory(ctx, "keyword only fact about lighthouses", nil, nil)
	require.NoError(t, err)

	results, err := b.Search(ctx, SearchParams{Query: "lighthouses", Mode: "hybrid"})
	require.NoError(t, err)
	require.Len(t, results, 1)

	_, err = b.Search(ctx, SearchParams{Query: "lighthouses", Mode: "vector"})
	require.Error(t, err)
}

func TestSearchValidation(t *testing.T) {
	b := testBrain(t, nil)
	_, err := b.Search(context.Background(), SearchParams{Query: ""})
	require.ErrorIs(t, err, ErrInvalidSearch)
	_, err = b.Search(context.Background(), SearchParams{Query: "x", Mode: "bogus"})
	require.ErrorIs(t, err, ErrInvalidSearch)
}
