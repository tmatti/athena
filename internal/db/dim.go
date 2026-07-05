package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationDimensions is the vector size hard-coded in the SQL migrations.
const migrationDimensions = 1536

// EnsureEmbeddingMeta reconciles the embedding configuration recorded in the
// database with the running config. On first boot it adopts the configured
// dimension (altering the vector columns if it differs from the migration
// default). On later boots any mismatch is a hard error: silently mixing
// embeddings from different models or dimensions would corrupt search.
func EnsureEmbeddingMeta(ctx context.Context, pool *pgxpool.Pool, provider, model string, dimensions int) error {
	if provider == "none" {
		return nil
	}

	var storedProvider, storedModel string
	var storedDims int
	err := pool.QueryRow(ctx,
		`SELECT provider, model, dimensions FROM embedding_meta`,
	).Scan(&storedProvider, &storedModel, &storedDims)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if dimensions != migrationDimensions {
			if err := alterVectorColumns(ctx, pool, dimensions); err != nil {
				return err
			}
		}
		_, err := pool.Exec(ctx,
			`INSERT INTO embedding_meta (provider, model, dimensions) VALUES ($1, $2, $3)`,
			provider, model, dimensions)
		return err
	case err != nil:
		return fmt.Errorf("read embedding_meta: %w", err)
	}

	if storedDims != dimensions || storedModel != model || storedProvider != provider {
		return fmt.Errorf(
			"embedding config mismatch: database has %s/%s (%d dims), config wants %s/%s (%d dims); "+
				"either revert the config, or wipe stored embeddings and update embedding_meta: "+
				"UPDATE memories SET embedding = NULL, embed_status = 'pending'; "+
				"UPDATE note_chunks SET embedding = NULL, embed_status = 'pending'; "+
				"UPDATE embedding_meta SET provider = '%s', model = '%s', dimensions = %d; "+
				"(a dimension change also needs ALTER TABLE ... ALTER COLUMN embedding TYPE vector(%d) on both tables)",
			storedProvider, storedModel, storedDims,
			provider, model, dimensions,
			provider, model, dimensions, dimensions,
		)
	}
	return nil
}

func alterVectorColumns(ctx context.Context, pool *pgxpool.Pool, dimensions int) error {
	stmts := []string{
		`DROP INDEX IF EXISTS memories_embedding_idx`,
		`DROP INDEX IF EXISTS note_chunks_embedding_idx`,
		fmt.Sprintf(`ALTER TABLE memories ALTER COLUMN embedding TYPE vector(%d)`, dimensions),
		fmt.Sprintf(`ALTER TABLE note_chunks ALTER COLUMN embedding TYPE vector(%d)`, dimensions),
		`CREATE INDEX memories_embedding_idx ON memories USING hnsw (embedding vector_cosine_ops)`,
		`CREATE INDEX note_chunks_embedding_idx ON note_chunks USING hnsw (embedding vector_cosine_ops)`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("adjust embedding dimension to %d: %w", dimensions, err)
		}
	}
	return nil
}
