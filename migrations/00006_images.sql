-- +goose Up
-- Image attachments. Every image belongs to exactly one memory or note and is
-- deleted with it. Bytes live in Postgres (bytea) so backups stay a single
-- pg_dump; metadata queries never select the data column.
CREATE TABLE images (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    memory_id     uuid REFERENCES memories(id) ON DELETE CASCADE,
    note_id       uuid REFERENCES notes(id) ON DELETE CASCADE,
    filename      text NOT NULL,
    content_type  text NOT NULL,
    size_bytes    bigint NOT NULL,
    sha256        text NOT NULL,
    -- Optional description; reserved for a future vision-captioning step.
    caption       text,
    data          bytea NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT images_exactly_one_owner CHECK (num_nonnulls(memory_id, note_id) = 1)
);

-- Image formats are already compressed; skip pglz and store out-of-line.
ALTER TABLE images ALTER COLUMN data SET STORAGE EXTERNAL;

CREATE INDEX images_memory_idx ON images (memory_id, created_at);
CREATE INDEX images_note_idx ON images (note_id, created_at);

-- +goose Down
DROP TABLE images;
