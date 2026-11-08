-- "Key last used" was always "the device last contacted us": every device
-- request is authenticated by its key. Named for what it means now that
-- the office reads it as online/offline.
ALTER TABLE eld_devices RENAME COLUMN key_last_used_at TO last_seen_at;

-- The last known state of each device: one row, overwritten as newer
-- readings arrive. The full history stays in eld_events; this row only
-- makes "where is truck 101 right now?" one indexed read.
CREATE TABLE eld_device_states (
    eld_device_id  UUID             PRIMARY KEY,
    company_id     UUID             NOT NULL REFERENCES companies (id),

    -- The reading this state comes from. Only a NEWER recorded_at may
    -- replace it: points buffered offline arrive late and must not move
    -- the truck back to where it was an hour ago.
    seq            BIGINT           NOT NULL,
    recorded_at    TIMESTAMPTZ      NOT NULL,
    received_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),
    truck_id       UUID             NULL,

    latitude       DOUBLE PRECISION NULL,
    longitude      DOUBLE PRECISION NULL,
    speed_mph      DOUBLE PRECISION NOT NULL,
    odometer_mi    DOUBLE PRECISION NOT NULL,
    engine_hours   DOUBLE PRECISION NOT NULL,
    engine_on      BOOLEAN          NOT NULL,
    moving         BOOLEAN          NOT NULL,

    CONSTRAINT eld_device_states_device_company_fkey
        FOREIGN KEY (eld_device_id, company_id) REFERENCES eld_devices (id, company_id),
    CONSTRAINT eld_device_states_truck_company_fkey
        FOREIGN KEY (truck_id, company_id) REFERENCES trucks (id, company_id),
    CONSTRAINT eld_device_states_position_pair CHECK ((latitude IS NULL) = (longitude IS NULL))
);
