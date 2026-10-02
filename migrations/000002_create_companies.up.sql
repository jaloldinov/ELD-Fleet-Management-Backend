-- A company is a motor carrier: the tenant that owns users, drivers, trucks and ELDs.
CREATE TABLE companies (
    -- uuidv7() is time-ordered: new rows go to the end of the index (fast inserts)
    -- and ids sort roughly by creation time. Native in PostgreSQL 18+.
    id          UUID        PRIMARY KEY DEFAULT uuidv7(),

    name        TEXT        NOT NULL,
    -- USDOT number: the carrier's federal ID, required on ELD records.
    -- TEXT, not INT: it is an identifier, never used in arithmetic.
    dot_number  TEXT        NOT NULL,
    -- Home-terminal IANA time zone (e.g. America/Chicago). The driver's log day
    -- is calculated in this zone. Validated in the service (time.LoadLocation).
    timezone    TEXT        NOT NULL,

    -- Inactive = suspended (can be re-activated). Different from deleted.
    is_active   BOOLEAN     NOT NULL DEFAULT true,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Soft delete: NULL = alive.
    deleted_at  TIMESTAMPTZ NULL,

    CONSTRAINT companies_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT companies_dot_number_format CHECK (dot_number ~ '^[0-9]{1,8}$')
);

-- Unique only among alive companies, so a DOT number can be registered again
-- after the previous company was soft-deleted.
CREATE UNIQUE INDEX companies_dot_number_alive_uniq
    ON companies (dot_number)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_companies_updated_at
    BEFORE UPDATE ON companies
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
