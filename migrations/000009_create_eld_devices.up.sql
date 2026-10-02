-- ELD devices: the hardware installed in a truck that records driving time.

-- Needed for the composite foreign key below: (id, company_id) must be unique
-- to be referenced. id alone is already unique, so this costs nothing logically.
ALTER TABLE trucks ADD CONSTRAINT trucks_id_company_uniq UNIQUE (id, company_id);

CREATE TABLE eld_devices (
    id               UUID        PRIMARY KEY DEFAULT uuidv7(),
    company_id       UUID        NOT NULL REFERENCES companies (id),

    -- Manufacturer's hardware serial: identifies the physical device.
    -- Stored upper-case; immutable after creation.
    serial_number    TEXT        NOT NULL,
    model            TEXT        NOT NULL DEFAULT '',
    firmware_version TEXT        NOT NULL DEFAULT '',

    -- The truck the device is installed in, if any.
    truck_id         UUID        NULL,

    is_active        BOOLEAN     NOT NULL DEFAULT true,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ NULL,

    CONSTRAINT eld_devices_serial_format CHECK (serial_number ~ '^[A-Z0-9-]{4,32}$'),
    -- The device and its truck belong to the same company. With MATCH SIMPLE
    -- (the default) the check is skipped while truck_id IS NULL.
    CONSTRAINT eld_devices_truck_company_fkey
        FOREIGN KEY (truck_id, company_id) REFERENCES trucks (id, company_id),
    -- A deleted device is never "installed" anywhere.
    CONSTRAINT eld_devices_deleted_unassigned CHECK (deleted_at IS NULL OR truck_id IS NULL)
);

-- A serial is one physical device: it can be in only one fleet at a time.
CREATE UNIQUE INDEX eld_devices_serial_alive_uniq
    ON eld_devices (serial_number)
    WHERE deleted_at IS NULL;

-- One ELD per truck. (One truck per ELD is automatic: truck_id is one column.)
-- Also serves "which device is in truck X?" lookups.
CREATE UNIQUE INDEX eld_devices_truck_uniq
    ON eld_devices (truck_id)
    WHERE truck_id IS NOT NULL;

CREATE INDEX eld_devices_company_idx
    ON eld_devices (company_id)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_eld_devices_updated_at
    BEFORE UPDATE ON eld_devices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
