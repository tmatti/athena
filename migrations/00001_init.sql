-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE memories (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    content       text NOT NULL,
    tags          text[] NOT NULL DEFAULT '{}',
    source        text,
    embedding     vector(1536),
    embed_status  text NOT NULL DEFAULT 'pending'
                  CHECK (embed_status IN ('pending', 'ok', 'failed')),
    search        tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title         text NOT NULL,
    content       text NOT NULL,
    tags          text[] NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE note_chunks (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    note_id       uuid NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
    idx           int  NOT NULL,
    content       text NOT NULL,
    embedding     vector(1536),
    embed_status  text NOT NULL DEFAULT 'pending'
                  CHECK (embed_status IN ('pending', 'ok', 'failed')),
    search        tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
    UNIQUE (note_id, idx)
);

-- Single row recording the embedding configuration the stored vectors were
-- produced with. Guarded at startup against the running config.
CREATE TABLE embedding_meta (
    id         bool PRIMARY KEY DEFAULT true CHECK (id),
    provider   text NOT NULL,
    model      text NOT NULL,
    dimensions int  NOT NULL
);

-- +goose Down
DROP TABLE embedding_meta;
DROP TABLE note_chunks;
DROP TABLE notes;
DROP TABLE memories;
