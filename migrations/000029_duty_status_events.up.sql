-- A driver's logbook: one row per duty status CHANGE. A row says only when
-- a status started; it lasts until the driver's next row starts. So the
-- log has no gaps and no overlaps by construction, and every day adds up
-- to 24 hours (the FMCSA record format works the same way).
--
--   06:00 on_duty   -> 1 h  (07:00 - 06:00)
--   07:00 driving   -> 4 h
--   11:00 off_duty  -> until the next row
CREATE TABLE duty_status_events (
    id            UUID             PRIMARY KEY DEFAULT uuidv7(),
    company_id    UUID             NOT NULL REFERENCES companies (id),
    driver_id     UUID             NOT NULL,

    status        TEXT             NOT NULL,
    -- Special driving categories, a flag on top of the status:
    -- personal_conveyance (counts as off duty), yard_moves (counts as on duty).
    special       TEXT             NULL,
    started_at    TIMESTAMPTZ      NOT NULL,

    -- Who wrote it: 'auto' = the ELD (driving from truck movement),
    -- 'driver' = the driver pressed a button.
    origin        TEXT             NOT NULL,

    -- Where and in which truck it happened, copied from the truck's ELD at
    -- started_at (FMCSA requires them on every record). NULL when the driver
    -- was in no truck (e.g. off duty at home) or the truck had no reading.
    truck_id      UUID             NULL,
    latitude      DOUBLE PRECISION NULL,
    longitude     DOUBLE PRECISION NULL,
    odometer_mi   DOUBLE PRECISION NULL,
    engine_hours  DOUBLE PRECISION NULL,

    note          TEXT             NULL,
    -- The user who entered it; NULL for 'auto' rows.
    created_by    UUID             NULL REFERENCES users (id),
    created_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),

    CONSTRAINT dse_driver_company_fkey
        FOREIGN KEY (driver_id, company_id) REFERENCES drivers (id, company_id),
    CONSTRAINT dse_truck_company_fkey
        FOREIGN KEY (truck_id, company_id) REFERENCES trucks (id, company_id),
    CONSTRAINT dse_status CHECK (status IN ('off_duty', 'sleeper_berth', 'driving', 'on_duty')),
    CONSTRAINT dse_origin CHECK (origin IN ('auto', 'driver')),
    -- Personal conveyance is off-duty driving; yard moves are on-duty driving.
    CONSTRAINT dse_special CHECK (
        special IS NULL
        OR (special = 'personal_conveyance' AND status = 'off_duty')
        OR (special = 'yard_moves' AND status = 'on_duty')),
    -- Driving is never typed in: only the ELD records it.
    CONSTRAINT dse_driving_is_auto CHECK (status <> 'driving' OR origin = 'auto'),
    CONSTRAINT dse_auto_has_no_author CHECK (origin <> 'auto' OR created_by IS NULL),
    CONSTRAINT dse_position_pair CHECK ((latitude IS NULL) = (longitude IS NULL)),
    CONSTRAINT dse_note_length CHECK (note IS NULL OR length(note) BETWEEN 1 AND 500)
);

-- One status per instant: two rows starting at the same moment would make
-- "what was the driver doing then?" unanswerable. Also serves the reads:
-- "status at time T" and "rows of driver X between A and B".
CREATE UNIQUE INDEX dse_driver_started_uniq ON duty_status_events (driver_id, started_at);
