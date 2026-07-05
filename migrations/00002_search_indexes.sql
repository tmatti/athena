-- +goose Up
CREATE INDEX memories_search_idx ON memories USING gin (search);
CREATE INDEX memories_tags_idx ON memories USING gin (tags);
CREATE INDEX notes_tags_idx ON notes USING gin (tags);
CREATE INDEX note_chunks_search_idx ON note_chunks USING gin (search);
CREATE INDEX note_chunks_note_id_idx ON note_chunks (note_id);

CREATE INDEX memories_embedding_idx ON memories USING hnsw (embedding vector_cosine_ops);
CREATE INDEX note_chunks_embedding_idx ON note_chunks USING hnsw (embedding vector_cosine_ops);

-- +goose Down
DROP INDEX memories_embedding_idx;
DROP INDEX note_chunks_embedding_idx;
DROP INDEX note_chunks_note_id_idx;
DROP INDEX note_chunks_search_idx;
DROP INDEX notes_tags_idx;
DROP INDEX memories_tags_idx;
DROP INDEX memories_search_idx;
