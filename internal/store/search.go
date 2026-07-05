package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

type SearchResult struct {
	Type      string    `json:"type"` // "memory" or "note"
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"` // notes only
	Snippet   string    `json:"snippet"`         // memory content or best chunk text
	ChunkID   string    `json:"chunk_id,omitempty"`
	Tags      []string  `json:"tags"`
	Score     float64   `json:"score"`
	CreatedAt time.Time `json:"created_at"`
}

type SearchMode string

const (
	ModeKeyword SearchMode = "keyword"
	ModeVector  SearchMode = "vector"
	ModeHybrid  SearchMode = "hybrid"
)

type SearchParams struct {
	Query    string
	QueryVec *pgvector.Vector // required for vector and hybrid modes
	Mode     SearchMode
	Tag      string
	Limit    int
}

// rrfK is the standard reciprocal-rank-fusion constant. Single-leg modes use
// the same 1/(k+rank) scoring so scores are comparable when merging memory
// and note results.
const rrfK = 60

// candidatePool is how many rows each leg (keyword, vector) contributes
// before fusion.
const candidatePool = 40

// SearchMemories ranks memories by the requested mode: keyword (tsvector),
// vector (cosine), or hybrid (RRF over both legs).
func (s *Store) SearchMemories(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	limit := normalizeLimit(p.Limit)

	// Vector mode never references the query text, so it must not be bound:
	// Postgres cannot infer the type of an unused parameter.
	var args []any
	if p.Mode != ModeVector {
		args = append(args, p.Query)
	}
	tagFilter := ""
	if p.Tag != "" {
		args = append(args, p.Tag)
		tagFilter = fmt.Sprintf(` AND $%d = ANY(tags)`, len(args))
	}

	var query string
	switch p.Mode {
	case ModeKeyword:
		query = fmt.Sprintf(`
			SELECT m.id, m.content, m.tags, m.created_at,
			       1.0/(%d + row_number() OVER (ORDER BY ts_rank_cd(m.search, q) DESC)) AS score
			FROM memories m, websearch_to_tsquery('english', $1) q
			WHERE m.search @@ q%s
			ORDER BY score DESC
			LIMIT %d`, rrfK, tagFilter, limit)
	case ModeVector:
		args = append(args, *p.QueryVec)
		query = fmt.Sprintf(`
			SELECT m.id, m.content, m.tags, m.created_at,
			       1.0/(%d + row_number() OVER (ORDER BY m.embedding <=> $%d)) AS score
			FROM memories m
			WHERE m.embedding IS NOT NULL%s
			ORDER BY m.embedding <=> $%d
			LIMIT %d`, rrfK, len(args), tagFilter, len(args), limit)
	case ModeHybrid:
		args = append(args, *p.QueryVec)
		vecArg := len(args)
		query = fmt.Sprintf(`
			WITH kw AS (
				SELECT m.id, row_number() OVER (ORDER BY ts_rank_cd(m.search, q) DESC) AS r
				FROM memories m, websearch_to_tsquery('english', $1) q
				WHERE m.search @@ q%[1]s
				LIMIT %[2]d
			), vec AS (
				SELECT id, row_number() OVER (ORDER BY embedding <=> $%[3]d) AS r
				FROM memories
				WHERE embedding IS NOT NULL%[1]s
				ORDER BY embedding <=> $%[3]d
				LIMIT %[2]d
			)
			SELECT m.id, m.content, m.tags, m.created_at,
			       COALESCE(1.0/(%[4]d + kw.r), 0) + COALESCE(1.0/(%[4]d + vec.r), 0) AS score
			FROM kw FULL OUTER JOIN vec USING (id)
			JOIN memories m USING (id)
			ORDER BY score DESC
			LIMIT %[5]d`, tagFilter, candidatePool, vecArg, rrfK, limit)
	default:
		return nil, fmt.Errorf("unknown search mode %q", p.Mode)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (SearchResult, error) {
		r := SearchResult{Type: "memory"}
		err := row.Scan(&r.ID, &r.Snippet, &r.Tags, &r.CreatedAt, &r.Score)
		return r, err
	})
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 50 {
		return 50
	}
	return limit
}
