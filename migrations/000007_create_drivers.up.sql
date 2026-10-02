-- A driver is a user (login, roles, active status) with a driver profile
-- (license data). One profile per user.

-- Needed for the composite foreign key below: (id, company_id) must be unique
-- to be referenced. id alone is already unique, so this adds no real limit.
ALTER TABLE users ADD CONSTRAINT users_id_company_uniq UNIQUE (id, company_id);

CREATE TABLE drivers (
    id                  UUID        PRIMARY KEY DEFAULT uuidv7(),
    user_id             UUID        NOT NULL,
    -- Copied from the user so every tenant table can be scoped by company_id
    -- without a join. The composite FK below guarantees it always equals
    -- the user's company: the copy can never drift.
    company_id          UUID        NOT NULL REFERENCES companies (id),

    license_number      TEXT        NOT NULL,
    -- US state that issued the license (two-letter code, e.g. TX).
    license_state       TEXT        NOT NULL,
    -- A calendar day, not a moment: DATE has no time zone.
    license_expires_at  DATE        NOT NULL,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ NULL,

    CONSTRAINT drivers_user_company_fkey
        FOREIGN KEY (user_id, company_id) REFERENCES users (id, company_id),
    CONSTRAINT drivers_license_state_format  CHECK (license_state ~ '^[A-Z]{2}$'),
    CONSTRAINT drivers_license_number_format CHECK (license_number ~ '^[A-Za-z0-9-]{1,25}$')
);

-- One alive driver profile per user.
CREATE UNIQUE INDEX drivers_user_alive_uniq
    ON drivers (user_id)
    WHERE deleted_at IS NULL;

-- License numbers are issued by states: unique per state, not nationally.
CREATE UNIQUE INDEX drivers_license_alive_uniq
    ON drivers (license_state, upper(license_number))
    WHERE deleted_at IS NULL;

-- "Drivers of company X": the most common query.
CREATE INDEX drivers_company_id_idx
    ON drivers (company_id)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_drivers_updated_at
    BEFORE UPDATE ON drivers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
