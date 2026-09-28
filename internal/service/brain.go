// Package service holds the shared application logic consumed by both the
// REST handlers and the MCP tools.
package service

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/tmatti/athena/internal/embed"
	"github.com/tmatti/athena/internal/store"
)

var ErrNotFound = store.ErrNotFound

// ErrInvalidInput matches every error caused by bad caller input. Such errors
// carry a message that is safe to return to the client verbatim.
var ErrInvalidInput = errors.New("invalid input")

// inputError is a client-safe validation message that matches ErrInvalidInput.
type inputError string

func (e inputError) Error() string        { return string(e) }
func (e inputError) Is(target error) bool { return target == ErrInvalidInput }

// ErrInvalidSearch reports a missing query or an unknown mode or type.
var ErrInvalidSearch error = inputError("invalid search parameters")

func blank(s string) bool { return strings.TrimSpace(s) == "" }

const embedTimeout = 10 * time.Second

type Brain struct {
	store    *store.Store
	embedder embed.Embedder // nil => keyword-only mode
	log      *slog.Logger
}

func New(st *store.Store, embedder embed.Embedder, log *slog.Logger) *Brain {
	return &Brain{store: st, embedder: embedder, log: log}
}

func (b *Brain) CreateMemory(ctx context.Context, content string, tags []string, source *string) (store.Memory, error) {
	if blank(content) {
		return store.Memory{}, inputError("content is required")
	}
	m, err := b.store.CreateMemory(ctx, content, tags, source)
	if err != nil {
		return store.Memory{}, err
	}
	return b.embedMemory(ctx, m)
}

func (b *Brain) GetMemory(ctx context.Context, id string) (store.Memory, error) {
	return b.store.GetMemory(ctx, id)
}

func (b *Brain) ListMemories(ctx context.Context, p store.ListMemoriesParams) ([]store.Memory, string, error) {
	return b.store.ListMemories(ctx, p)
}

func (b *Brain) UpdateMemory(ctx context.Context, id string, p store.UpdateMemoryParams) (store.Memory, error) {
	switch {
	case p.Content == nil && p.Tags == nil && p.Source == nil:
		return store.Memory{}, inputError("nothing to update: provide content, tags, and/or source")
	case p.Content != nil && blank(*p.Content):
		return store.Memory{}, inputError("content cannot be empty")
	}
	m, contentChanged, err := b.store.UpdateMemory(ctx, id, p)
	if err != nil {
		return store.Memory{}, err
	}
	if !contentChanged {
		return m, nil
	}
	return b.embedMemory(ctx, m)
}

func (b *Brain) DeleteMemory(ctx context.Context, id string) error {
	return b.store.DeleteMemory(ctx, id)
}

func (b *Brain) ListTags(ctx context.Context) ([]store.TagCount, error) {
	return b.store.ListTags(ctx)
}

// embedMemory embeds synchronously; on failure the memory is kept with
// embed_status='failed' and the retry loop picks it up later.
func (b *Brain) embedMemory(ctx context.Context, m store.Memory) (store.Memory, error) {
	if b.embedder == nil {
		return m, nil
	}
	ectx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()

	vecs, err := b.embedder.Embed(ectx, []string{m.Content})
	if err != nil {
		b.log.Warn("embed memory failed; will retry in background", "id", m.ID, "error", err)
		if err := b.store.MarkMemoryEmbedFailed(ctx, m.ID); err != nil {
			return store.Memory{}, err
		}
		m.EmbedStatus = "failed"
		return m, nil
	}
	if err := b.store.SetMemoryEmbedding(ctx, m.ID, m.Content, pgvector.NewVector(vecs[0])); err != nil {
		return store.Memory{}, err
	}
	m.EmbedStatus = "ok"
	return m, nil
}

type SearchParams struct {
	Query string
	Mode  string // hybrid | vector | keyword
	Type  string // all | memory | note
	Tag   string
	Limit int
}

func (b *Brain) Search(ctx context.Context, p SearchParams) ([]store.SearchResult, error) {
	if p.Query == "" {
		return nil, ErrInvalidSearch
	}
	if p.Mode == "" {
		p.Mode = "hybrid"
	}
	if p.Type == "" {
		p.Type = "all"
	}
	switch p.Mode {
	case "hybrid", "vector", "keyword":
	default:
		return nil, ErrInvalidSearch
	}
	switch p.Type {
	case "all", "memory", "note":
	default:
		return nil, ErrInvalidSearch
	}

	// Resolve the query embedding for vector/hybrid modes. Hybrid degrades to
	// keyword-only when the embedding cannot be produced; vector mode errors.
	mode := store.SearchMode(p.Mode)
	var queryVec *pgvector.Vector
	if mode != store.ModeKeyword {
		if b.embedder == nil {
			if mode == store.ModeVector {
				return nil, errors.New("vector search unavailable: no embedding provider configured")
			}
			mode = store.ModeKeyword
		} else {
			ectx, cancel := context.WithTimeout(ctx, embedTimeout)
			vecs, err := b.embedder.Embed(ectx, []string{p.Query})
			cancel()
			switch {
			case err != nil && mode == store.ModeVector:
				return nil, err
			case err != nil:
				b.log.Warn("query embedding failed; degrading to keyword search", "error", err)
				mode = store.ModeKeyword
			default:
				v := pgvector.NewVector(vecs[0])
				queryVec = &v
			}
		}
	}

	storeParams := store.SearchParams{Query: p.Query, QueryVec: queryVec, Mode: mode, Tag: p.Tag, Limit: p.Limit}
	var results []store.SearchResult

	if p.Type == "all" || p.Type == "memory" {
		ms, err := b.store.SearchMemories(ctx, storeParams)
		if err != nil {
			return nil, err
		}
		results = append(results, ms...)
	}
	if p.Type == "all" || p.Type == "note" {
		ns, err := b.store.SearchNotes(ctx, storeParams)
		if err != nil {
			return nil, err
		}
		results = append(results, ns...)
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if limit := store.NormalizeSearchLimit(p.Limit); len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
