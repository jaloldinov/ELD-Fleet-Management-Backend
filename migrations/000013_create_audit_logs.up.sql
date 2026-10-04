-- Audit log: who changed what, when, and from which value to which.
-- Written by the services in the SAME transaction as the change itself, so
-- a change without its audit row (or the reverse) cannot exist.
CREATE TABLE audit_logs (
    id           UUID        PRIMARY KEY DEFAULT uuidv7(),
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Who: NULL = the system itself (e.g. the createadmin CLI). No foreign
    -- key: the history must outlive users (and is never rewritten).
    actor_id     UUID        NULL,
    -- Tenant of the changed record, so company admins see only their own
    -- history. NULL = platform-level (platform users, settings).
    company_id   UUID        NULL,

    action       TEXT        NOT NULL,   -- 'truck.update', 'user.set_password'
    entity       TEXT        NOT NULL,   -- 'truck'
    entity_id    TEXT        NOT NULL,   -- a UUID, or a name (rate limits)
    -- {"field": {"old": ..., "new": ...}}, only fields that changed. Never
    -- secrets: password changes are recorded by action alone.
    changes      JSONB       NULL,
    -- Joins this row with the request log (IP, user agent, latency).
    request_id   TEXT        NULL,

    CONSTRAINT audit_logs_action_format CHECK (action ~ '^[a-z_]+\.[a-z_]+$')
);

-- "History of this truck" (the timeline UI), newest first.
CREATE INDEX audit_logs_entity_idx ON audit_logs (entity, entity_id, occurred_at DESC);
-- "Everything in my company", newest first.
CREATE INDEX audit_logs_company_idx ON audit_logs (company_id, occurred_at DESC);
-- "What did this person do?"
CREATE INDEX audit_logs_actor_idx ON audit_logs (actor_id, occurred_at DESC);

-- Append-only: history can be added to, never rewritten or erased, not even
-- by a bug or by someone with the application's database password.
-- (TRUNCATE is not a row operation and stays possible for an operator;
-- retention, e.g. dropping old months, is a deliberate admin task.)
CREATE FUNCTION audit_logs_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only: % is not allowed', TG_OP;
END;
$$;

CREATE TRIGGER trg_audit_logs_append_only
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION audit_logs_append_only();
