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

// pendingEligible is the backoff predicate shared by both legs. A row is
// eligible when it is still pending/failed AND either it has never been
// attempted, it has only been attempted once (the first background retry is
// immediate — a transient blip should recover fast), or its last attempt is
// older than an exponentially growing delay (attempts 2,3,4,... => 4min, 8min,
// 16min, ... capped at 1 hour). This lets a genuinely broken/oversized row
// fall into backoff instead of being reselected every tick, while a good row
// behind it keeps flowing.
const pendingEligible = `embed_status IN ('pending', 'failed')
	AND (embed_last_attempt_at IS NULL
	     OR embed_attempts < 2
	     OR embed_last_attempt_at < now() - LEAST(interval '1 minute' * (2 ^ LEAST(embed_attempts, 6)), interval '1 hour'))`

// ListPendingEmbeds returns rows still awaiting an embedding, across both
// memories and note chunks. Rows in exponential backoff (recent failed
// attempts) are excluded, and the result is ordered so fresh/low-attempt rows
// come first — the ordering is applied across the union so memories can no
// longer structurally starve chunks.
func (s *Store) ListPendingEmbeds(ctx context.Context, limit int) ([]PendingEmbed, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, id, content FROM (
			(SELECT 'memory' AS kind, id::text AS id, content, embed_attempts, embed_last_attempt_at
			 FROM memories
			 WHERE `+pendingEligible+`
			 ORDER BY embed_attempts ASC, embed_last_attempt_at ASC NULLS FIRST
			 LIMIT $1)
			UNION ALL
			(SELECT 'chunk' AS kind, id::text AS id, content, embed_attempts, embed_last_attempt_at
			 FROM note_chunks
			 WHERE `+pendingEligible+`
			 ORDER BY embed_attempts ASC, embed_last_attempt_at ASC NULLS FIRST
			 LIMIT $1)
		) p
		ORDER BY embed_attempts ASC, embed_last_attempt_at ASC NULLS FIRST
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PendingEmbed, error) {
		var p PendingEmbed
		err := row.Scan(&p.Kind, &p.ID, &p.Content)
		return p, err
	})
}

const setChunkEmbeddingSQL = `UPDATE note_chunks
	SET embedding = $2, embed_status = 'ok', embed_attempts = 0, embed_last_attempt_at = NULL
	WHERE id = $1 AND content = $3`

// SetChunkEmbedding stores the embedding only if the chunk's content still
// matches what was embedded. A 0-row result is not an error: the row changed
// or was deleted while the embedding was in flight, and its fresh pending
// state will be re-embedded later.
func (s *Store) SetChunkEmbedding(ctx context.Context, id, content string, vec pgvector.Vector) error {
	_, err := s.pool.Exec(ctx, setChunkEmbeddingSQL, id, vec, content)
	return err
}

// SetChunkEmbeddings applies SetChunkEmbedding to each ref (paired with
// vecs[i]) in a single round trip.
func (s *Store) SetChunkEmbeddings(ctx context.Context, refs []ChunkRef, vecs []pgvector.Vector) error {
	batch := &pgx.Batch{}
	for i, r := range refs {
		batch.Queue(setChunkEmbeddingSQL, r.ID, vecs[i], r.Content)
	}
	return s.pool.SendBatch(ctx, batch).Close()
}

func (s *Store) MarkChunksEmbedFailed(ctx context.Context, ids ...string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE note_chunks
		 SET embed_status = 'failed', embed_attempts = embed_attempts + 1, embed_last_attempt_at = now()
		 WHERE id = ANY($1)`, ids)
	return err
}
