package service

import (
	"context"
	"time"

	"github.com/pgvector/pgvector-go"
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
		b.log.Warn("retry embedding batch failed", "count", len(pending), "error", err)
		return
	}

	var done int
	for i, p := range pending {
		vec := pgvector.NewVector(vecs[i])
		var err error
		if p.Kind == "memory" {
			err = b.store.SetMemoryEmbedding(ctx, p.ID, vec)
		} else {
			err = b.store.SetChunkEmbedding(ctx, p.ID, vec)
		}
		if err != nil {
			b.log.Error("store retried embedding", "kind", p.Kind, "id", p.ID, "error", err)
			continue
		}
		done++
	}
	b.log.Info("re-embedded pending rows", "count", done)
}
