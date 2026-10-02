-- Reverses 000010_refresh_tokens_cleanup_indexes.up.sql.
DROP INDEX IF EXISTS refresh_tokens_replaced_by_idx;
DROP INDEX IF EXISTS refresh_tokens_expires_at_idx;
