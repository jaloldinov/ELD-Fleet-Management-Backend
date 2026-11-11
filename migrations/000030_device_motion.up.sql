-- When the device's current run began: moving since, or standing since.
-- The logbook needs it to tell "the truck has stood still for 5 minutes"
-- (the driver's automatic driving ends) without rereading the history.
-- Set by the same "newer recorded_at wins" upsert as the rest of the row.
ALTER TABLE eld_device_states ADD COLUMN motion_since TIMESTAMPTZ NULL;
-- Existing rows: we only know the run lasted at least since their reading.
UPDATE eld_device_states SET motion_since = recorded_at;
ALTER TABLE eld_device_states ALTER COLUMN motion_since SET NOT NULL;
ALTER TABLE eld_device_states
    ADD CONSTRAINT eld_device_states_motion_since CHECK (motion_since <= recorded_at);

-- The stop sweeper's question: "trucks standing still since when?".
CREATE INDEX eld_device_states_stopped_idx ON eld_device_states (motion_since)
    WHERE NOT moving AND truck_id IS NOT NULL;
