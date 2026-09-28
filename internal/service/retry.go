package service

import (
	"context"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/tmatti/athena/internal/store"
)

const retryBatchSize = 32

// RunEmbedRetryLoop periodically re-embeds rows whose embedding is pending or
// failed. It returns when ctx is cancelled. No-op without an embedder.
func (b *Brain) RunEmbedRetryLoop(ctx context.Context, interval time.Duration) {
	if b.embedder == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.retryPendingEmbeds(ctx)
		}
	}
}

func (b *Brain) retryPendingEmbeds(ctx context.Context) {
	pending, err := b.store.ListPendingEmbeds(ctx, retryBatchSize)
	if err != nil {
		b.log.Error("list pending embeds", "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	texts := make([]string, len(pending))
	for i, p := range pending {
		texts[i] = p.Content
	}

	ectx, cancel := context.WithTimeout(ctx, embedTimeout)
	vecs, err := b.embedder.Embed(ectx, texts)
	cancel()
	if err != nil {
		// The batch call failed. A single oversized/poison row must not block
		// the rest, so fall back to embedding each item individually.
		b.log.Warn("retry embedding batch failed; falling back to per-item", "count", len(pending), "error", err)
		b.retryPerItem(ctx, pending)
		return
	}

	var done int
	for i, p := range pending {
		if b.storeEmbedding(ctx, p, vecs[i]) {
			done++
		}
	}
	b.log.Info("re-embedded pending rows", "count", done)
}

// retryPerItem embeds each pending row on its own, isolating failures so one
// permanently failing row cannot starve the batch. Items that fail are marked
// failed (incrementing attempts, pushing them into backoff).
func (b *Brain) retryPerItem(ctx context.Context, pending []store.PendingEmbed) {
	var done, failed int
	for _, p := range pending {
		if ctx.Err() != nil {
			return
		}
		ectx, cancel := context.WithTimeout(ctx, embedTimeout)
		vecs, err := b.embedder.Embed(ectx, []string{p.Content})
		cancel()
		if err != nil {
			b.log.Warn("retry embedding item failed", "kind", p.Kind, "id", p.ID, "error", err)
			if merr := b.markEmbedFailed(ctx, p); merr != nil {
				b.log.Error("mark embed failed", "kind", p.Kind, "id", p.ID, "error", merr)
			}
			failed++
			continue
		}
		if b.storeEmbedding(ctx, p, vecs[0]) {
			done++
		}
	}
	b.log.Info("re-embedded pending rows individually", "ok", done, "failed", failed)
}

// storeEmbedding writes a successful embedding via the content-guarded setters
// and reports whether it was stored without error.
func (b *Brain) storeEmbedding(ctx context.Context, p store.PendingEmbed, vec []float32) bool {
	v := pgvector.NewVector(vec)
	var err error
	if p.Kind == "memory" {
		err = b.store.SetMemoryEmbedding(ctx, p.ID, p.Content, v)
	} else {
		err = b.store.SetChunkEmbedding(ctx, p.ID, p.Content, v)
	}
	if err != nil {
		b.log.Error("store retried embedding", "kind", p.Kind, "id", p.ID, "error", err)
		return false
	}
	return true
}

func (b *Brain) markEmbedFailed(ctx context.Context, p store.PendingEmbed) error {
	if p.Kind == "memory" {
		return b.store.MarkMemoryEmbedFailed(ctx, p.ID)
	}
	return b.store.MarkChunksEmbedFailed(ctx, p.ID)
}
