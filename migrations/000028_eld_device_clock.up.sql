-- How far the device's clock is from ours, measured on each batch that
-- carries sent_at (the device's clock when it sent): sent_at − our clock.
-- Positive = the device is fast. Readings are never corrected (an ELD
-- record is kept exactly as recorded); the office sees the drift before it
-- reaches FMCSA's 10-minute limit, where the device is malfunctioning.
ALTER TABLE eld_devices
    ADD COLUMN clock_offset_ms  BIGINT      NULL,
    ADD COLUMN clock_checked_at TIMESTAMPTZ NULL,
    ADD CONSTRAINT eld_devices_clock_pair CHECK ((clock_offset_ms IS NULL) = (clock_checked_at IS NULL));
