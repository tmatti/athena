package service

import (
	"context"

	"github.com/pgvector/pgvector-go"

	"github.com/tmatti/athena/internal/chunk"
	"github.com/tmatti/athena/internal/store"
)

func (b *Brain) CreateNote(ctx context.Context, title, content string, tags []string) (store.Note, error) {
	if blank(title) || blank(content) {
		return store.Note{}, inputError("title and content are required")
	}
	note, refs, err := b.store.CreateNote(ctx, title, content, tags, chunk.Split(content))
	if err != nil {
		return store.Note{}, err
	}
	return b.embedChunks(ctx, note, refs)
}

func (b *Brain) GetNote(ctx context.Context, id string) (store.Note, error) {
	return b.store.GetNote(ctx, id)
}

func (b *Brain) ListNotes(ctx context.Context, p store.ListNotesParams) ([]store.Note, string, error) {
	return b.store.ListNotes(ctx, p)
}

type UpdateNoteParams struct {
	Title   *string
	Content *string
	Tags    *[]string
}

func (b *Brain) UpdateNote(ctx context.Context, id string, p UpdateNoteParams) (store.Note, error) {
	switch {
	case p.Title == nil && p.Content == nil && p.Tags == nil:
		return store.Note{}, inputError("nothing to update: provide title, content, and/or tags")
	case p.Title != nil && blank(*p.Title):
		return store.Note{}, inputError("title cannot be empty")
	case p.Content != nil && blank(*p.Content):
		return store.Note{}, inputError("content cannot be empty")
	}
	storeParams := store.UpdateNoteParams{Title: p.Title, Content: p.Content, Tags: p.Tags}
	if p.Content != nil {
		storeParams.Chunks = chunk.Split(*p.Content)
	}
	note, refs, err := b.store.UpdateNote(ctx, id, storeParams)
	if err != nil {
		return store.Note{}, err
	}
	if p.Content == nil {
		return note, nil
	}
	return b.embedChunks(ctx, note, refs)
}

func (b *Brain) DeleteNote(ctx context.Context, id string) error {
	return b.store.DeleteNote(ctx, id)
}

// embedChunks embeds a note's fresh chunks in one batched call. Failures
// leave the chunks marked failed for the retry loop; the note write itself
// never fails on embedding problems.
func (b *Brain) embedChunks(ctx context.Context, note store.Note, refs []store.ChunkRef) (store.Note, error) {
	if b.embedder == nil || len(refs) == 0 {
		return note, nil
	}
	texts := make([]string, len(refs))
	for i, r := range refs {
		texts[i] = r.Content
	}

	ectx, cancel := context.WithTimeout(ctx, embedTimeout)
	vecs, err := b.embedder.Embed(ectx, texts)
	cancel()
	if err != nil {
		b.log.Warn("embed note chunks failed; will retry in background", "note_id", note.ID, "error", err)
		ids := make([]string, len(refs))
		for i, r := range refs {
			ids[i] = r.ID
		}
		if err := b.store.MarkChunksEmbedFailed(ctx, ids...); err != nil {
			return store.Note{}, err
		}
		note.EmbedStatus = "failed"
		return note, nil
	}
	pgvecs := make([]pgvector.Vector, len(vecs))
	for i, v := range vecs {
		pgvecs[i] = pgvector.NewVector(v)
	}
	if err := b.store.SetChunkEmbeddings(ctx, refs, pgvecs); err != nil {
		return store.Note{}, err
	}
	note.EmbedStatus = "ok"
	return note, nil
}
