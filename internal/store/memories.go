package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

type Memory struct {
	ID          string    `json:"id"`
	Content     string    `json:"content"`
	Tags        []string  `json:"tags"`
	Source      *string   `json:"source"`
	EmbedStatus string    `json:"embed_status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const memoryColumns = `id, content, tags, source, embed_status, created_at, updated_at`

func scanMemory(row pgx.Row) (Memory, error) {
	var m Memory
	err := row.Scan(&m.ID, &m.Content, &m.Tags, &m.Source, &m.EmbedStatus, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Memory{}, ErrNotFound
	}
	return m, err
}

func (s *Store) CreateMemory(ctx context.Context, content string, tags []string, source *string) (Memory, error) {
	if tags == nil {
		tags = []string{}
	}
	return scanMemory(s.pool.QueryRow(ctx,
		`INSERT INTO memories (content, tags, source) VALUES ($1, $2, $3) RETURNING `+memoryColumns,
		content, tags, source))
}

func (s *Store) GetMemory(ctx context.Context, id string) (Memory, error) {
	return scanMemory(s.pool.QueryRow(ctx,
		`SELECT `+memoryColumns+` FROM memories WHERE id = $1`, id))
}

type ListMemoriesParams struct {
	Tag    string
	Limit  int
	Cursor string
}

// ListMemories returns memories newest-first with keyset pagination on
// (created_at, id). The returned cursor is empty when there are no more rows.
func (s *Store) ListMemories(ctx context.Context, p ListMemoriesParams) ([]Memory, string, error) {
	if p.Limit <= 0 || p.Limit > 100 {
		p.Limit = 50
	}

	query := `SELECT ` + memoryColumns + ` FROM memories WHERE 1=1`
	args := []any{}
	if p.Tag != "" {
		args = append(args, p.Tag)
		query += fmt.Sprintf(` AND $%d = ANY(tags)`, len(args))
	}
	if p.Cursor != "" {
		at, id, err := decodeCursor(p.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, at, id)
		query += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, len(args)-1, len(args))
	}
	args = append(args, p.Limit+1)
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	memories, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Memory, error) {
		return scanMemory(row)
	})
	if err != nil {
		return nil, "", err
	}

	next := ""
	if len(memories) > p.Limit {
		memories = memories[:p.Limit]
		last := memories[len(memories)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return memories, next, nil
}

type UpdateMemoryParams struct {
	Content *string
	Tags    *[]string
	Source  *string
}

// UpdateMemory applies the non-nil fields. A content change resets the
// embedding so the caller can re-embed; contentChanged reports that.
func (s *Store) UpdateMemory(ctx context.Context, id string, p UpdateMemoryParams) (Memory, bool, error) {
	sets := []string{`updated_at = now()`}
	args := []any{id}
	contentChanged := p.Content != nil

	if p.Content != nil {
		args = append(args, *p.Content)
		sets = append(sets, fmt.Sprintf(`content = $%d`, len(args)))
		sets = append(sets, `embedding = NULL`, `embed_status = 'pending'`,
			`embed_attempts = 0`, `embed_last_attempt_at = NULL`)
	}
	if p.Tags != nil {
		args = append(args, *p.Tags)
		sets = append(sets, fmt.Sprintf(`tags = $%d`, len(args)))
	}
	if p.Source != nil {
		args = append(args, *p.Source)
		sets = append(sets, fmt.Sprintf(`source = $%d`, len(args)))
	}

	query := `UPDATE memories SET ` + strings.Join(sets, ", ") + ` WHERE id = $1 RETURNING ` + memoryColumns
	m, err := scanMemory(s.pool.QueryRow(ctx, query, args...))
	return m, contentChanged, err
}

func (s *Store) DeleteMemory(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM memories WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMemoryEmbedding stores the embedding only if the memory's content still
// matches what was embedded. A 0-row result is not an error: the content
// changed (or the row was deleted) while the embedding was in flight, so the
// stale vector is discarded and the fresh pending state is re-embedded later.
func (s *Store) SetMemoryEmbedding(ctx context.Context, id, content string, vec pgvector.Vector) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE memories
		 SET embedding = $2, embed_status = 'ok', embed_attempts = 0, embed_last_attempt_at = NULL
		 WHERE id = $1 AND content = $3`, id, vec, content)
	return err
}

func (s *Store) MarkMemoryEmbedFailed(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE memories
		 SET embed_status = 'failed', embed_attempts = embed_attempts + 1, embed_last_attempt_at = now()
		 WHERE id = $1`, id)
	return err
}

func encodeCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, "", ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	return t, id, nil
}
