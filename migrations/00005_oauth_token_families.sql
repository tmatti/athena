-- +goose Up
-- Track the grant "family" a token belongs to so that reuse of a rotated
-- refresh token can revoke every token descended from the same grant, and so
-- a grant's total lifetime can be capped. Rotated refresh tokens are kept as
-- consumed tombstones (consumed_at set) until they expire, to detect reuse.
ALTER TABLE oauth_tokens
    ADD COLUMN family_id uuid,
    ADD COLUMN family_created_at timestamptz,
    ADD COLUMN consumed_at timestamptz;

-- Pre-existing tokens each get their own family; their access/refresh
-- linkage is unknown, so reuse detection starts fresh with the next grant.
UPDATE oauth_tokens SET family_id = gen_random_uuid(), family_created_at = created_at;

ALTER TABLE oauth_tokens
    ALTER COLUMN family_id SET NOT NULL,
    ALTER COLUMN family_created_at SET NOT NULL;

CREATE INDEX oauth_tokens_family_idx ON oauth_tokens (family_id);

-- +goose Down
DROP INDEX oauth_tokens_family_idx;
ALTER TABLE oauth_tokens
    DROP COLUMN family_id,
    DROP COLUMN family_created_at,
    DROP COLUMN consumed_at;
