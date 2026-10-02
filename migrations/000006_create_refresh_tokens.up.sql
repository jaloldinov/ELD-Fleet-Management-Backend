-- Long-lived, revocable refresh tokens. Only a SHA-256 hash of each token is
-- stored: a database leak does not give an attacker usable tokens.
CREATE TABLE refresh_tokens (
    id              UUID        PRIMARY KEY DEFAULT uuidv7(),
    user_id         UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- SHA-256 of the token: always exactly 32 bytes.
    token_hash      BYTEA       NOT NULL,

    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ NULL,
    -- Set when this token was rotated: points to the token that replaced it.
    -- A revoked token WITH a successor that is used again = reuse = theft.
    replaced_by_id  UUID        NULL REFERENCES refresh_tokens (id),

    -- Session info, useful for "active sessions" screens and investigations.
    user_agent      TEXT        NOT NULL DEFAULT '',
    ip              TEXT        NOT NULL DEFAULT '',

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT refresh_tokens_hash_length CHECK (octet_length(token_hash) = 32)
);

-- Every refresh/logout looks a token up by its hash.
CREATE UNIQUE INDEX refresh_tokens_token_hash_uniq ON refresh_tokens (token_hash);

-- "Revoke all active tokens of user X" (reuse detection, deactivation).
CREATE INDEX refresh_tokens_user_active_idx
    ON refresh_tokens (user_id)
    WHERE revoked_at IS NULL;
