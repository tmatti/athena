-- +goose Up
-- Keyset pagination orders by (created_at, id) newest-first; a backward scan
-- of these indexes serves each page without sorting the whole table.
CREATE INDEX memories_created_idx ON memories (created_at, id);
CREATE INDEX notes_created_idx ON notes (created_at, id);

-- The embed retry loop polls for unembedded rows every minute. Once embedding
-- is healthy these partial indexes are near-empty, so the poll stops scanning
-- every row.
CREATE INDEX memories_embed_pending_idx ON memories (embed_attempts, embed_last_attempt_at)
    WHERE embed_status IN ('pending', 'failed');
CREATE INDEX note_chunks_embed_pending_idx ON note_chunks (embed_attempts, embed_last_attempt_at)
    WHERE embed_status IN ('pending', 'failed');

-- +goose Down
DROP INDEX note_chunks_embed_pending_idx;
DROP INDEX memories_embed_pending_idx;
DROP INDEX notes_created_idx;
DROP INDEX memories_created_idx;
