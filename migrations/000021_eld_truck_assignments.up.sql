-- Which ELD device is installed in which truck, and since when: the same
-- stint model as driver_truck_assignments. Replaces eld_devices.truck_id,
-- which only knew "now" and forgot every earlier truck. Later phases need
-- "which device recorded truck X's driving on day D?".

-- Needed for the composite foreign key below.
ALTER TABLE eld_devices ADD CONSTRAINT eld_devices_id_company_uniq UNIQUE (id, company_id);

CREATE TABLE eld_truck_assignments (
    id             UUID        PRIMARY KEY DEFAULT uuidv7(),
    company_id     UUID        NOT NULL REFERENCES companies (id),
    eld_device_id  UUID        NOT NULL,
    truck_id       UUID        NOT NULL,

    started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at       TIMESTAMPTZ NULL,

    -- Who installed / removed it. NULL = the system (e.g. this migration).
    started_by     UUID        NULL REFERENCES users (id),
    ended_by       UUID        NULL REFERENCES users (id),

    CONSTRAINT eta_device_company_fkey
        FOREIGN KEY (eld_device_id, company_id) REFERENCES eld_devices (id, company_id),
    CONSTRAINT eta_truck_company_fkey
        FOREIGN KEY (truck_id, company_id) REFERENCES trucks (id, company_id),
    CONSTRAINT eta_period CHECK (ended_at IS NULL OR ended_at >= started_at),
    CONSTRAINT eta_ended_by_needs_end CHECK (ended_by IS NULL OR ended_at IS NOT NULL)
);

-- A device is in at most ONE truck at a time...
CREATE UNIQUE INDEX eta_device_open_uniq
    ON eld_truck_assignments (eld_device_id)
    WHERE ended_at IS NULL;

-- ...and a truck has at most ONE device. Also serves "which device is in
-- truck X now?".
CREATE UNIQUE INDEX eta_truck_open_uniq
    ON eld_truck_assignments (truck_id)
    WHERE ended_at IS NULL;

-- History, newest first.
CREATE INDEX eta_device_history_idx
    ON eld_truck_assignments (eld_device_id, started_at DESC);
CREATE INDEX eta_truck_history_idx
    ON eld_truck_assignments (truck_id, started_at DESC);

-- Move today's installations over as open stints. When each device was
-- really installed is unknown (the old column kept no date), so history
-- honestly starts now, instead of guessing from updated_at (which also
-- changes when the firmware version is edited).
INSERT INTO eld_truck_assignments (company_id, eld_device_id, truck_id)
SELECT company_id, id, truck_id
FROM eld_devices
WHERE truck_id IS NOT NULL;

-- The old column and everything that guarded it.
ALTER TABLE eld_devices DROP CONSTRAINT eld_devices_deleted_unassigned;
ALTER TABLE eld_devices DROP CONSTRAINT eld_devices_truck_company_fkey;
DROP INDEX eld_devices_truck_uniq;
ALTER TABLE eld_devices DROP COLUMN truck_id;
