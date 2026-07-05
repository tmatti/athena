package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Note struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content,omitempty"` // omitted in list responses
	Tags      []string  `json:"tags"`
	// EmbedStatus aggregates the note's chunks: failed if any chunk failed,
	// pending if any is pending, else ok.
	EmbedStatus string    `json:"embed_status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ChunkRef identifies a chunk that still needs an embedding.
type ChunkRef struct {
	ID      string
	Content string
}

const noteStatusExpr = `(
	SELECT COALESCE(
		CASE
			WHEN bool_or(c.embed_status = 'failed') THEN 'failed'
			WHEN bool_or(c.embed_status = 'pending') THEN 'pending'
			ELSE 'ok'
		END, 'ok')
	FROM note_chunks c WHERE c.note_id = n.id
)`

func scanNote(row pgx.Row, withContent bool) (Note, error) {
	var n Note
	var err error
	if withContent {
		err = row.Scan(&n.ID, &n.Title, &n.Content, &n.Tags, &n.EmbedStatus, &n.CreatedAt, &n.UpdatedAt)
	} else {
		err = row.Scan(&n.ID, &n.Title, &n.Tags, &n.EmbedStatus, &n.CreatedAt, &n.UpdatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Note{}, ErrNotFound
	}
	return n, err
}

// CreateNote inserts the note and its chunks (embed_status pending) in one
// transaction and returns the chunk refs so the caller can embed them.
func (s *Store) CreateNote(ctx context.Context, title, content string, tags []string, chunks []string) (Note, []ChunkRef, error) {
	if tags == nil {
		tags = []string{}
	}
	var note Note
	var refs []ChunkRef
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		note, err = scanNote(tx.QueryRow(ctx,
			`INSERT INTO notes (title, content, tags) VALUES ($1, $2, $3)
			 RETURNING id, title, content, tags, 'pending', created_at, updated_at`,
			title, content, tags), true)
		if err != nil {
			return err
		}
		refs, err = insertChunks(ctx, tx, note.ID, chunks)
		return err
	})
	if err != nil {
		return Note{}, nil, err
	}
	if len(chunks) == 0 {
		note.EmbedStatus = "ok"
	}
	return note, refs, nil
}

func insertChunks(ctx context.Context, tx pgx.Tx, noteID string, chunks []string) ([]ChunkRef, error) {
	refs := make([]ChunkRef, 0, len(chunks))
	for i, c := range chunks {
		var id string
		if err := tx.QueryRow(ctx,
			`INSERT INTO note_chunks (note_id, idx, content) VALUES ($1, $2, $3) RETURNING id`,
			noteID, i, c).Scan(&id); err != nil {
			return nil, err
		}
		refs = append(refs, ChunkRef{ID: id, Content: c})
	}
	return refs, nil
}

func (s *Store) GetNote(ctx context.Context, id string) (Note, error) {
	return scanNote(s.pool.QueryRow(ctx,
		`SELECT n.id, n.title, n.content, n.tags, `+noteStatusExpr+`, n.created_at, n.updated_at
		 FROM notes n WHERE n.id = $1`, id), true)
}

type ListNotesParams struct {
	Tag    string
	Limit  int
	Cursor string
}

// ListNotes returns note metadata (no content) newest-first with keyset
// pagination, mirroring ListMemories.
func (s *Store) ListNotes(ctx context.Context, p ListNotesParams) ([]Note, string, error) {
	if p.Limit <= 0 || p.Limit > 100 {
		p.Limit = 50
	}
	query := `SELECT n.id, n.title, n.tags, ` + noteStatusExpr + `, n.created_at, n.updated_at
		FROM notes n WHERE 1=1`
	args := []any{}
	if p.Tag != "" {
		args = append(args, p.Tag)
		query += fmt.Sprintf(` AND $%d = ANY(n.tags)`, len(args))
	}
	if p.Cursor != "" {
		at, id, err := decodeCursor(p.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, at, id)
		query += fmt.Sprintf(` AND (n.created_at, n.id) < ($%d, $%d)`, len(args)-1, len(args))
	}
	args = append(args, p.Limit+1)
	query += fmt.Sprintf(` ORDER BY n.created_at DESC, n.id DESC LIMIT $%d`, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	notes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Note, error) {
		return scanNote(row, false)
	})
	if err != nil {
		return nil, "", err
	}

	next := ""
	if len(notes) > p.Limit {
		notes = notes[:p.Limit]
		last := notes[len(notes)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return notes, next, nil
}

type UpdateNoteParams struct {
	Title   *string
	Content *string
	Tags    *[]string
	// Chunks replaces the note's chunks when Content is set.
	Chunks []string
}

// UpdateNote applies the non-nil fields. When Content changes, the existing
// chunks are replaced (embed_status pending) in the same transaction and the
// new chunk refs are returned for embedding.
func (s *Store) UpdateNote(ctx context.Context, id string, p UpdateNoteParams) (Note, []ChunkRef, error) {
	sets := []string{`updated_at = now()`}
	args := []any{id}
	if p.Title != nil {
		args = append(args, *p.Title)
		sets = append(sets, fmt.Sprintf(`title = $%d`, len(args)))
	}
	if p.Content != nil {
		args = append(args, *p.Content)
		sets = append(sets, fmt.Sprintf(`content = $%d`, len(args)))
	}
	if p.Tags != nil {
		args = append(args, *p.Tags)
		sets = append(sets, fmt.Sprintf(`tags = $%d`, len(args)))
	}

	var note Note
	var refs []ChunkRef
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		note, err = scanNote(tx.QueryRow(ctx,
			`UPDATE notes n SET `+strings.Join(sets, ", ")+` WHERE n.id = $1
			 RETURNING n.id, n.title, n.content, n.tags, 'pending', n.created_at, n.updated_at`,
			args...), true)
		if err != nil {
			return err
		}
		if p.Content == nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM note_chunks WHERE note_id = $1`, id); err != nil {
			return err
		}
		refs, err = insertChunks(ctx, tx, id, p.Chunks)
		return err
	})
	if err != nil {
		return Note{}, nil, err
	}
	if p.Content == nil {
		// Status was not recomputed inside the UPDATE; fetch the aggregate.
		fresh, err := s.GetNote(ctx, id)
		if err != nil {
			return Note{}, nil, err
		}
		note.EmbedStatus = fresh.EmbedStatus
	}
	return note, refs, nil
}

func (s *Store) DeleteNote(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notes WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchNotes ranks note chunks by the requested mode and dedupes to the
// parent note, keeping the best-scoring chunk as the snippet.
func (s *Store) SearchNotes(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	limit := normalizeLimit(p.Limit)

	var args []any
	if p.Mode != ModeVector {
		args = append(args, p.Query)
	}
	tagFilter := ""
	if p.Tag != "" {
		args = append(args, p.Tag)
		tagFilter = fmt.Sprintf(` AND $%d = ANY(n.tags)`, len(args))
	}

	var scored string
	switch p.Mode {
	case ModeKeyword:
		scored = fmt.Sprintf(`
			SELECT c.note_id, c.id AS chunk_id, c.content,
			       1.0/(%d + row_number() OVER (ORDER BY ts_rank_cd(c.search, q) DESC)) AS score
			FROM note_chunks c JOIN notes n ON n.id = c.note_id,
			     websearch_to_tsquery('english', $1) q
			WHERE c.search @@ q%s
			LIMIT %d`, rrfK, tagFilter, candidatePool)
	case ModeVector:
		args = append(args, *p.QueryVec)
		scored = fmt.Sprintf(`
			SELECT c.note_id, c.id AS chunk_id, c.content,
			       1.0/(%d + row_number() OVER (ORDER BY c.embedding <=> $%d)) AS score
			FROM note_chunks c JOIN notes n ON n.id = c.note_id
			WHERE c.embedding IS NOT NULL%s
			ORDER BY c.embedding <=> $%d
			LIMIT %d`, rrfK, len(args), tagFilter, len(args), candidatePool)
	case ModeHybrid:
		args = append(args, *p.QueryVec)
		vecArg := len(args)
		scored = fmt.Sprintf(`
			WITH kw AS (
				SELECT c.id, row_number() OVER (ORDER BY ts_rank_cd(c.search, q) DESC) AS r
				FROM note_chunks c JOIN notes n ON n.id = c.note_id,
				     websearch_to_tsquery('english', $1) q
				WHERE c.search @@ q%[1]s
				LIMIT %[2]d
			), vec AS (
				SELECT c.id, row_number() OVER (ORDER BY c.embedding <=> $%[3]d) AS r
				FROM note_chunks c JOIN notes n ON n.id = c.note_id
				WHERE c.embedding IS NOT NULL%[1]s
				ORDER BY c.embedding <=> $%[3]d
				LIMIT %[2]d
			)
			SELECT c.note_id, c.id AS chunk_id, c.content,
			       COALESCE(1.0/(%[4]d + kw.r), 0) + COALESCE(1.0/(%[4]d + vec.r), 0) AS score
			FROM kw FULL OUTER JOIN vec USING (id)
			JOIN note_chunks c USING (id)`, tagFilter, candidatePool, vecArg, rrfK)
	default:
		return nil, fmt.Errorf("unknown search mode %q", p.Mode)
	}

	query := fmt.Sprintf(`
		WITH scored AS (%s),
		best AS (
			SELECT DISTINCT ON (s.note_id) s.note_id, s.chunk_id, s.content, s.score
			FROM scored s
			ORDER BY s.note_id, s.score DESC
		)
		SELECT n.id, n.title, b.content, b.chunk_id, n.tags, b.score, n.created_at
		FROM best b JOIN notes n ON n.id = b.note_id
		ORDER BY b.score DESC
		LIMIT %d`, scored, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (SearchResult, error) {
		r := SearchResult{Type: "note"}
		err := row.Scan(&r.ID, &r.Title, &r.Snippet, &r.ChunkID, &r.Tags, &r.Score, &r.CreatedAt)
		return r, err
	})
}
