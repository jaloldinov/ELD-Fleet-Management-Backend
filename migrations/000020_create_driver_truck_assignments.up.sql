-- Which driver drives which truck, and since when. A row is one stint:
--   ended_at IS NULL      -> the driver is on that truck right now
--   ended_at IS NOT NULL  -> history ("Ali drove 101 from Mon to Wed")
-- Rows are never updated except to end them, and never deleted: later
-- phases (logbook, HOS, inspections) ask "who was driving truck X at time T?".

-- Needed for the composite foreign key below, like users and trucks before.
ALTER TABLE drivers ADD CONSTRAINT drivers_id_company_uniq UNIQUE (id, company_id);

CREATE TABLE driver_truck_assignments (
    id          UUID        PRIMARY KEY DEFAULT uuidv7(),
    company_id  UUID        NOT NULL REFERENCES companies (id),
    driver_id   UUID        NOT NULL,
    truck_id    UUID        NOT NULL,

    -- Team driving: a truck can have a primary driver and one co-driver.
    role        TEXT        NOT NULL,

    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ NULL,

    -- Who opened / closed the stint: a dispatcher, or the driver themselves
    -- (self-select). NULL when the system did it (e.g. the truck was deleted).
    started_by  UUID        NULL REFERENCES users (id),
    ended_by    UUID        NULL REFERENCES users (id),

    -- The driver, the truck and the row all belong to the same company.
    CONSTRAINT dta_driver_company_fkey
        FOREIGN KEY (driver_id, company_id) REFERENCES drivers (id, company_id),
    CONSTRAINT dta_truck_company_fkey
        FOREIGN KEY (truck_id, company_id) REFERENCES trucks (id, company_id),
    CONSTRAINT dta_role CHECK (role IN ('primary', 'co_driver')),
    -- >= and not >: an assignment opened and closed in the same transaction
    -- gets the same now() for both.
    CONSTRAINT dta_period CHECK (ended_at IS NULL OR ended_at >= started_at),
    -- Closing a stint without saying when makes no sense.
    CONSTRAINT dta_ended_by_needs_end CHECK (ended_by IS NULL OR ended_at IS NOT NULL)
);

-- A driver is on at most ONE truck at a time.
CREATE UNIQUE INDEX dta_driver_open_uniq
    ON driver_truck_assignments (driver_id)
    WHERE ended_at IS NULL;

-- A truck has at most one open primary and one open co-driver, so at most
-- two drivers. Also serves "who is in truck X now?".
CREATE UNIQUE INDEX dta_truck_role_open_uniq
    ON driver_truck_assignments (truck_id, role)
    WHERE ended_at IS NULL;

-- History, newest first: "trucks driver X drove" / "drivers of truck X".
CREATE INDEX dta_driver_history_idx
    ON driver_truck_assignments (driver_id, started_at DESC);
CREATE INDEX dta_truck_history_idx
    ON driver_truck_assignments (truck_id, started_at DESC);
