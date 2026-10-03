-- Rate limits that a platform admin can change at runtime (through the API),
-- instead of numbers hard-coded in main.go. One row per rule; the code looks
-- rules up by name (see model.RateLimit*).
--
-- Each instance keeps the values in memory and re-reads this table every
-- minute, so a login never queries it.
CREATE TABLE rate_limits (
    name            TEXT        PRIMARY KEY,
    -- At most max_requests events per window_seconds, per key.
    max_requests    INTEGER     NOT NULL,
    window_seconds  INTEGER     NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Who changed it last (NULL = the seed below). Security settings deserve
    -- an answer to "who loosened this?".
    updated_by      UUID        NULL REFERENCES users (id) ON DELETE SET NULL,

    -- Same ranges as the service validation: the database is the last line
    -- of defence against a zero limit (which would block everyone).
    CONSTRAINT rate_limits_max_requests_range CHECK (max_requests BETWEEN 1 AND 10000),
    CONSTRAINT rate_limits_window_range CHECK (window_seconds BETWEEN 1 AND 86400)
);

CREATE TRIGGER trg_rate_limits_updated_at
    BEFORE UPDATE ON rate_limits
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The values the code used to hard-code.
INSERT INTO rate_limits (name, max_requests, window_seconds, description) VALUES
    ('auth_requests',  20,  60, 'Requests per client IP to /api/v1/auth/* (login, refresh, logout)'),
    ('login_attempts',  5, 900, 'Login attempts per login name + client IP; a successful login resets it');

-- Platform setting: only platform_admin gets these permissions.
-- Keep in sync with internal/model/permission.go (a test checks this).
INSERT INTO permissions (code, description) VALUES
    ('ratelimit:read',   'View rate-limit settings'),
    ('ratelimit:update', 'Change rate-limit settings');

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
CROSS JOIN permissions p
WHERE r.company_id IS NULL AND r.name = 'platform_admin'
  AND p.code IN ('ratelimit:read', 'ratelimit:update');
