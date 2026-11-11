-- Edits. A record is never changed or deleted: an edit writes a NEW active
-- record (replaces -> the old one) and makes the old one inactive. The
-- log is built from active records; inactive ones are its history.
ALTER TABLE duty_status_events
    ADD COLUMN record_status  TEXT        NOT NULL DEFAULT 'active',
    ADD COLUMN replaces       UUID        NULL REFERENCES duty_status_events (id),
    ADD COLUMN edit_reason    TEXT        NULL,
    ADD COLUMN inactivated_at TIMESTAMPTZ NULL,
    ADD COLUMN inactivated_by UUID        NULL REFERENCES users (id),
    ADD CONSTRAINT dse_record_status CHECK (record_status IN ('active', 'inactive')),
    -- Inactive exactly when it was taken out, and only then by someone.
    ADD CONSTRAINT dse_inactivated CHECK ((record_status = 'inactive') = (inactivated_at IS NOT NULL)),
    ADD CONSTRAINT dse_inactivated_by CHECK (inactivated_by IS NULL OR inactivated_at IS NOT NULL),
    -- Every edit says why (FMCSA annotation: 4 to 60 characters).
    ADD CONSTRAINT dse_edit_reason CHECK (edit_reason IS NULL OR length(edit_reason) BETWEEN 4 AND 60),
    ADD CONSTRAINT dse_replaces_has_reason CHECK (replaces IS NULL OR edit_reason IS NOT NULL);

-- One ACTIVE status per instant. A replaced record keeps its time, and its
-- replacement may start at the same instant.
DROP INDEX dse_driver_started_uniq;
CREATE UNIQUE INDEX dse_driver_started_uniq ON duty_status_events (driver_id, started_at)
    WHERE record_status = 'active';

-- A record is replaced at most once: two edits of the same version would
-- fork the history.
CREATE UNIQUE INDEX dse_replaces_uniq ON duty_status_events (replaces) WHERE replaces IS NOT NULL;

-- The day's history: "what did edits replace?".
CREATE INDEX dse_driver_inactive_idx ON duty_status_events (driver_id, started_at)
    WHERE record_status = 'inactive';

-- The driver's signature of a day. Append-only: a day changed after it was
-- signed is signed again, with a new row.
CREATE TABLE duty_log_certifications (
    id            UUID        PRIMARY KEY DEFAULT uuidv7(),
    company_id    UUID        NOT NULL REFERENCES companies (id),
    driver_id     UUID        NOT NULL,
    -- The day, in the company's time zone.
    log_date      DATE        NOT NULL,
    certified_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    certified_by  UUID        NOT NULL REFERENCES users (id),

    CONSTRAINT dlc_driver_company_fkey
        FOREIGN KEY (driver_id, company_id) REFERENCES drivers (id, company_id)
);

CREATE INDEX dlc_driver_date_idx ON duty_log_certifications (driver_id, log_date, certified_at DESC);
