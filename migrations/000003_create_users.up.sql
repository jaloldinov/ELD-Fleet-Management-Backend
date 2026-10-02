-- A user is anyone who can log in: platform staff, company staff, or a driver.
CREATE TABLE users (
    id             UUID        PRIMARY KEY DEFAULT uuidv7(),

    -- NULL = platform user (works for us, not for a carrier).
    -- No ON DELETE: companies are soft-deleted; a hard delete of a company that
    -- still has users is a bug, so the database refuses it.
    company_id     UUID        NULL REFERENCES companies (id),

    -- Login identifiers. Both optional, but at least one is required
    -- (drivers often log in with a username because they have no email).
    email          TEXT        NULL,
    username       TEXT        NULL,

    -- bcrypt hash. The plain password is never stored.
    password_hash  TEXT        NOT NULL,

    first_name     TEXT        NOT NULL,
    last_name      TEXT        NOT NULL,

    is_active      BOOLEAN     NOT NULL DEFAULT true,
    last_login_at  TIMESTAMPTZ NULL,

    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ NULL,

    CONSTRAINT users_login_required   CHECK (email IS NOT NULL OR username IS NOT NULL),
    -- Light sanity check only; the service does the real validation.
    CONSTRAINT users_email_format     CHECK (email IS NULL OR email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$'),
    CONSTRAINT users_username_format  CHECK (username IS NULL OR username ~ '^[A-Za-z0-9._-]{3,50}$'),
    CONSTRAINT users_first_name_not_blank CHECK (btrim(first_name) <> ''),
    CONSTRAINT users_last_name_not_blank  CHECK (btrim(last_name) <> '')
);

-- Case-insensitive and unique among alive users (globally, because login does
-- not ask for a company). lower(...) makes John@X.com equal to john@x.com.
CREATE UNIQUE INDEX users_email_alive_uniq
    ON users (lower(email))
    WHERE deleted_at IS NULL AND email IS NOT NULL;

CREATE UNIQUE INDEX users_username_alive_uniq
    ON users (lower(username))
    WHERE deleted_at IS NULL AND username IS NOT NULL;

-- "List the users of company X" is a very common query.
-- Postgres does NOT create indexes for foreign keys automatically.
CREATE INDEX users_company_id_idx
    ON users (company_id)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
