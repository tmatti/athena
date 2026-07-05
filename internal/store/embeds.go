package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

type PendingEmbed struct {
	Kind    string // "memory" or "chunk"
	ID      string
	Content string
}

// ListPendingEmbeds returns rows still awaiting an embedding, across both
// memories and note chunks.
func (s *Store) ListPendingEmbeds(ctx context.Context, limit int) ([]PendingEmbed, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT * FROM (
			(SELECT 'memory' AS kind, id::text, content FROM memories
			 WHERE embed_status IN ('pending', 'failed') LIMIT $1)
			UNION ALL
			(SELECT 'chunk' AS kind, id::text, content FROM note_chunks
			 WHERE embed_status IN ('pending', 'failed') LIMIT $1)
		) p LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PendingEmbed, error) {
		var p PendingEmbed
		err := row.Scan(&p.Kind, &p.ID, &p.Content)
		return p, err
	})
}

func (s *Store) SetChunkEmbedding(ctx context.Context, id string, vec pgvector.Vector) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE note_chunks SET embedding = $2, embed_status = 'ok' WHERE id = $1`, id, vec)
	return err
}

func (s *Store) MarkChunkEmbedFailed(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE note_chunks SET embed_status = 'failed' WHERE id = $1`, id)
	return err
}
