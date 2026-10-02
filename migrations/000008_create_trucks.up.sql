-- Trucks (power units) operated by a carrier.
CREATE TABLE trucks (
    id             UUID        PRIMARY KEY DEFAULT uuidv7(),
    company_id     UUID        NOT NULL REFERENCES companies (id),

    -- The carrier's own name for the truck, e.g. "101". Unique per company.
    unit_number    TEXT        NOT NULL,
    -- Vehicle Identification Number: identifies the physical vehicle.
    -- Format here; the check digit is validated by the service.
    vin            TEXT        NOT NULL,

    make           TEXT        NOT NULL DEFAULT '',
    model          TEXT        NOT NULL DEFAULT '',
    year           SMALLINT    NULL,

    license_plate  TEXT        NULL,
    plate_state    TEXT        NULL,

    -- Out of service (repairs, sold...). Can be re-activated.
    is_active      BOOLEAN     NOT NULL DEFAULT true,

    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ NULL,

    CONSTRAINT trucks_unit_number_format CHECK (unit_number ~ '^[A-Za-z0-9-]{1,20}$'),
    -- 17 characters, no I, O or Q.
    CONSTRAINT trucks_vin_format CHECK (vin ~ '^[A-HJ-NPR-Z0-9]{17}$'),
    CONSTRAINT trucks_year_range CHECK (year IS NULL OR year BETWEEN 1980 AND 2100),
    -- A plate is meaningless without the state that issued it: both or neither.
    CONSTRAINT trucks_plate_pair CHECK ((license_plate IS NULL) = (plate_state IS NULL)),
    CONSTRAINT trucks_plate_state_format CHECK (plate_state IS NULL OR plate_state ~ '^[A-Z]{2}$')
);

-- Unit numbers are the carrier's own naming: unique WITHIN a company.
CREATE UNIQUE INDEX trucks_unit_number_alive_uniq
    ON trucks (company_id, lower(unit_number))
    WHERE deleted_at IS NULL;

-- A VIN is one physical vehicle: it can be in only one fleet at a time.
CREATE UNIQUE INDEX trucks_vin_alive_uniq
    ON trucks (vin)
    WHERE deleted_at IS NULL;

-- No separate company_id index: the unit-number index starts with
-- company_id, so Postgres can use it for "trucks of company X" too.

CREATE TRIGGER trg_trucks_updated_at
    BEFORE UPDATE ON trucks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
