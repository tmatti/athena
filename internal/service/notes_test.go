package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/embed"
)

func TestNoteRoundTripAndHybridSearch(t *testing.T) {
	b := testBrain(t, &embed.FakeEmbedder{Dims: 1536})
	ctx := context.Background()

	// Long enough to produce multiple chunks.
	content := "Athena deployment guide.\n\n" +
		strings.Repeat("Filler paragraph about general operations and maintenance tasks. ", 30) +
		"\n\nTo deploy to production, build the docker image and set DATABASE_URL." +
		"\n\n" + strings.Repeat("More filler about unrelated admin chores. ", 30)

	note, err := b.CreateNote(ctx, "Deployment", content, []string{"ops"})
	require.NoError(t, err)
	require.Equal(t, "ok", note.EmbedStatus)

	results, err := b.Search(ctx, SearchParams{Query: "deploy production docker", Mode: "hybrid"})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Equal(t, "note", results[0].Type)
	require.Equal(t, note.ID, results[0].ID)
	require.Equal(t, "Deployment", results[0].Title)
	require.Contains(t, results[0].Snippet, "deploy to production")
	require.NotEmpty(t, results[0].ChunkID)

	// Dedup: one result per note even though multiple chunks matched terms.
	var count int
	for _, r := range results {
		if r.ID == note.ID {
			count++
		}
	}
	require.Equal(t, 1, count)

	// Update content: re-chunk + re-embed; old chunk no longer findable.
	newContent := "Rewritten note about gardening tomatoes."
	updated, err := b.UpdateNote(ctx, note.ID, UpdateNoteParams{Content: &newContent})
	require.NoError(t, err)
	require.Equal(t, "ok", updated.EmbedStatus)

	results, err = b.Search(ctx, SearchParams{Query: "gardening tomatoes", Mode: "hybrid"})
	require.NoError(t, err)
	require.Len(t, results, 1)

	results, err = b.Search(ctx, SearchParams{Query: "deploy production docker", Mode: "keyword"})
	require.NoError(t, err)
	require.Empty(t, results)

	// Delete cascades chunks.
	require.NoError(t, b.DeleteNote(ctx, note.ID))
	results, err = b.Search(ctx, SearchParams{Query: "gardening tomatoes", Mode: "hybrid"})
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestSearchTypeFilter(t *testing.T) {
	b := testBrain(t, &embed.FakeEmbedder{Dims: 1536})
	ctx := context.Background()

	_, err := b.CreateMemory(ctx, "shared topic zeppelin facts", nil, nil)
	require.NoError(t, err)
	_, err = b.CreateNote(ctx, "Zeppelins", "A note about zeppelin history.", nil)
	require.NoError(t, err)

	all, err := b.Search(ctx, SearchParams{Query: "zeppelin", Mode: "hybrid"})
	require.NoError(t, err)
	require.Len(t, all, 2)

	memories, err := b.Search(ctx, SearchParams{Query: "zeppelin", Mode: "hybrid", Type: "memory"})
	require.NoError(t, err)
	require.Len(t, memories, 1)
	require.Equal(t, "memory", memories[0].Type)

	notes, err := b.Search(ctx, SearchParams{Query: "zeppelin", Mode: "hybrid", Type: "note"})
	require.NoError(t, err)
	require.Len(t, notes, 1)
	require.Equal(t, "note", notes[0].Type)
}
