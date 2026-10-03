-- "Forgot password": a 6-digit code sent by email (step 1), exchanged for a
-- one-time reset token (step 2), which allows setting a new password (step 3).
-- At most one pending reset per user: a new request replaces the old one.
CREATE TABLE password_resets (
    id                UUID        PRIMARY KEY DEFAULT uuidv7(),
    user_id           UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- HMAC-SHA256 of the code with a server key. A plain SHA-256 would not
    -- protect a 6-digit code: all 1,000,000 hashes are computed in a second.
    code_hash         BYTEA       NOT NULL,
    -- Wrong guesses so far; at the limit the code is dead (request a new one).
    attempts          INTEGER     NOT NULL DEFAULT 0,
    code_expires_at   TIMESTAMPTZ NOT NULL,

    -- Set when the code is verified (step 2). SHA-256 of the token: unlike
    -- the code, the token is 32 random bytes, so a plain hash is enough.
    verified_at       TIMESTAMPTZ NULL,
    token_hash        BYTEA       NULL UNIQUE,
    token_expires_at  TIMESTAMPTZ NULL,

    -- Set when the password was changed (step 3): single use.
    used_at           TIMESTAMPTZ NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT password_resets_attempts_not_negative CHECK (attempts >= 0),
    CONSTRAINT password_resets_token_complete CHECK (
        (verified_at IS NULL AND token_hash IS NULL AND token_expires_at IS NULL)
        OR (verified_at IS NOT NULL AND token_hash IS NOT NULL AND token_expires_at IS NOT NULL))
);

-- "The user's pending reset" and deleting it when a new one is requested.
CREATE INDEX password_resets_user_id_idx ON password_resets (user_id);
-- The cleanup job deletes old rows.
CREATE INDEX password_resets_created_at_idx ON password_resets (created_at);

-- Limits "send me a code" per email address, so nobody can flood an inbox
-- (the per-IP auth_requests limit also applies). Changeable through the
-- rate-limit API like the other rules.
INSERT INTO rate_limits (name, max_requests, window_seconds, description) VALUES
    ('password_reset_requests', 3, 3600, 'Password reset emails per email address');
