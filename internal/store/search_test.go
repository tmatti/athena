package store

import (
	"context"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/embed"
	"github.com/tmatti/athena/internal/testdb"
)

const testDims = 1536

func seedMemory(t *testing.T, s *Store, fe *embed.FakeEmbedder, content string, tags []string) Memory {
	t.Helper()
	ctx := context.Background()
	m, err := s.CreateMemory(ctx, content, tags, nil)
	require.NoError(t, err)
	vecs, err := fe.Embed(ctx, []string{content})
	require.NoError(t, err)
	require.NoError(t, s.SetMemoryEmbedding(ctx, m.ID, pgvector.NewVector(vecs[0])))
	return m
}

func queryVec(t *testing.T, fe *embed.FakeEmbedder, q string) *pgvector.Vector {
	t.Helper()
	vecs, err := fe.Embed(context.Background(), []string{q})
	require.NoError(t, err)
	v := pgvector.NewVector(vecs[0])
	return &v
}

func TestSearchMemoriesKeyword(t *testing.T) {
	s := New(testdb.Pool(t))
	fe := &embed.FakeEmbedder{Dims: testDims}
	ctx := context.Background()

	coffee := seedMemory(t, s, fe, "buy coffee beans and oat milk", []string{"errands"})
	seedMemory(t, s, fe, "the user prefers Go for backend work", []string{"preferences"})

	results, err := s.SearchMemories(ctx, SearchParams{Query: "coffee", Mode: ModeKeyword, Limit: 10})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, coffee.ID, results[0].ID)
	require.Equal(t, "memory", results[0].Type)
	require.Greater(t, results[0].Score, 0.0)
}

func TestSearchMemoriesHybridOrdering(t *testing.T) {
	s := New(testdb.Pool(t))
	fe := &embed.FakeEmbedder{Dims: testDims}
	ctx := context.Background()

	both := seedMemory(t, s, fe, "alpha beta gamma delta", nil)
	partial := seedMemory(t, s, fe, "alpha epsilon zeta unrelated trailing words", nil)
	seedMemory(t, s, fe, "cooking pasta with tomato sauce", nil)

	results, err := s.SearchMemories(ctx, SearchParams{
		Query:    "alpha beta",
		QueryVec: queryVec(t, fe, "alpha beta"),
		Mode:     ModeHybrid,
		Limit:    10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	// The memory matching both query words must outrank the partial match.
	require.Equal(t, both.ID, results[0].ID)
	var partialScore float64
	for _, r := range results {
		if r.ID == partial.ID {
			partialScore = r.Score
		}
	}
	require.Greater(t, results[0].Score, partialScore)
}

func TestSearchMemoriesVectorLeg(t *testing.T) {
	s := New(testdb.Pool(t))
	fe := &embed.FakeEmbedder{Dims: testDims}
	ctx := context.Background()

	// No keyword overlap with the query, but we plant an embedding identical
	// to the query's: only the vector leg can find this.
	m, err := s.CreateMemory(ctx, "zzz qqq nothing in common", nil, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetMemoryEmbedding(ctx, m.ID, *queryVec(t, fe, "alpha beta")))

	keyword, err := s.SearchMemories(ctx, SearchParams{Query: "alpha beta", Mode: ModeKeyword, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, keyword)

	vector, err := s.SearchMemories(ctx, SearchParams{
		Query:    "alpha beta",
		QueryVec: queryVec(t, fe, "alpha beta"),
		Mode:     ModeVector,
		Limit:    10,
	})
	require.NoError(t, err)
	require.Len(t, vector, 1)
	require.Equal(t, m.ID, vector[0].ID)

	hybrid, err := s.SearchMemories(ctx, SearchParams{
		Query:    "alpha beta",
		QueryVec: queryVec(t, fe, "alpha beta"),
		Mode:     ModeHybrid,
		Limit:    10,
	})
	require.NoError(t, err)
	require.Len(t, hybrid, 1)
	require.Equal(t, m.ID, hybrid[0].ID)
}

func TestSearchMemoriesTagFilter(t *testing.T) {
	s := New(testdb.Pool(t))
	fe := &embed.FakeEmbedder{Dims: testDims}
	ctx := context.Background()

	tagged := seedMemory(t, s, fe, "quarterly report numbers look strong", []string{"work"})
	seedMemory(t, s, fe, "quarterly budget planning at home", []string{"personal"})

	results, err := s.SearchMemories(ctx, SearchParams{
		Query:    "quarterly",
		QueryVec: queryVec(t, fe, "quarterly"),
		Mode:     ModeHybrid,
		Tag:      "work",
		Limit:    10,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, tagged.ID, results[0].ID)
}

func TestListMemoriesKeysetPagination(t *testing.T) {
	s := New(testdb.Pool(t))
	ctx := context.Background()

	var ids []string
	for _, c := range []string{"first", "second", "third"} {
		m, err := s.CreateMemory(ctx, c, nil, nil)
		require.NoError(t, err)
		ids = append(ids, m.ID)
	}

	page1, cursor, err := s.ListMemories(ctx, ListMemoriesParams{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.NotEmpty(t, cursor)

	page2, cursor2, err := s.ListMemories(ctx, ListMemoriesParams{Limit: 2, Cursor: cursor})
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.Empty(t, cursor2)

	seen := map[string]bool{}
	for _, m := range append(page1, page2...) {
		seen[m.ID] = true
	}
	for _, id := range ids {
		require.True(t, seen[id])
	}
}
