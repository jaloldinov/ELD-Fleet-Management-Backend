-- Indexes for deleting expired refresh tokens (the cleanup job).

-- Finds the next batch of expired tokens, oldest first.
CREATE INDEX refresh_tokens_expires_at_idx ON refresh_tokens (expires_at);

-- Postgres does NOT index foreign-key columns automatically. Without this,
-- deleting a token makes Postgres scan the whole table to check that no
-- other token's replaced_by_id still points at it: once per deleted row.
-- Partial: only rotated tokens have a successor.
CREATE INDEX refresh_tokens_replaced_by_idx
    ON refresh_tokens (replaced_by_id)
    WHERE replaced_by_id IS NOT NULL;

-- Note: on a large production table, create these with
-- CREATE INDEX CONCURRENTLY (outside a transaction) to avoid blocking writes.
