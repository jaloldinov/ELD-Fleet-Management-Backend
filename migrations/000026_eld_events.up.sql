-- What ELD devices report, one row per data point. Append-only: rows are
-- never changed or deleted (history for hours of service, inspections).
-- A busy fleet writes millions of rows; partitioning by month comes when
-- the size asks for it (Phase 9 / production).
CREATE TABLE eld_events (
    id             UUID             PRIMARY KEY DEFAULT uuidv7(),
    company_id     UUID             NOT NULL REFERENCES companies (id),
    eld_device_id  UUID             NOT NULL,

    -- The device's own counter, 1, 2, 3...: (device, seq) identifies a data
    -- point, so a batch sent twice (lost reply, offline sync) stores once.
    seq            BIGINT           NOT NULL,

    -- recorded_at: the device's clock, when it happened. received_at: our
    -- clock, when it arrived (later for points buffered offline).
    recorded_at    TIMESTAMPTZ      NOT NULL,
    received_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),

    -- The truck the device was installed in AT recorded_at (from the stint
    -- history), so a point buffered in a tunnel lands on the right truck
    -- even if the device moved since. NULL: installed nowhere then.
    truck_id       UUID             NULL,

    -- GPS can be lost (tunnels, garages): both or neither.
    latitude       DOUBLE PRECISION NULL,
    longitude      DOUBLE PRECISION NULL,

    speed_mph      DOUBLE PRECISION NOT NULL,
    odometer_mi    DOUBLE PRECISION NOT NULL,
    engine_hours   DOUBLE PRECISION NOT NULL,
    engine_on      BOOLEAN          NOT NULL,
    -- Decided by us, not the device: speed > 5 mph (the FMCSA threshold for
    -- automatic driving status). Stored, so later phases query it cheaply.
    moving         BOOLEAN          NOT NULL,

    CONSTRAINT eld_events_device_company_fkey
        FOREIGN KEY (eld_device_id, company_id) REFERENCES eld_devices (id, company_id),
    CONSTRAINT eld_events_truck_company_fkey
        FOREIGN KEY (truck_id, company_id) REFERENCES trucks (id, company_id),
    CONSTRAINT eld_events_seq_positive CHECK (seq >= 1),
    CONSTRAINT eld_events_position_pair CHECK ((latitude IS NULL) = (longitude IS NULL)),
    CONSTRAINT eld_events_latitude  CHECK (latitude IS NULL OR latitude BETWEEN -90 AND 90),
    CONSTRAINT eld_events_longitude CHECK (longitude IS NULL OR longitude BETWEEN -180 AND 180),
    CONSTRAINT eld_events_readings  CHECK (speed_mph >= 0 AND odometer_mi >= 0 AND engine_hours >= 0)
);

-- The duplicate guard, and "events of device X in order".
CREATE UNIQUE INDEX eld_events_device_seq_uniq ON eld_events (eld_device_id, seq);

-- "What did truck X do on day D?" (Phase 5 builds the logbook from this).
CREATE INDEX eld_events_truck_time_idx ON eld_events (truck_id, recorded_at) WHERE truck_id IS NOT NULL;
