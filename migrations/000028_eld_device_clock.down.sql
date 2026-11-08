ALTER TABLE eld_devices
    DROP CONSTRAINT IF EXISTS eld_devices_clock_pair,
    DROP COLUMN IF EXISTS clock_checked_at,
    DROP COLUMN IF EXISTS clock_offset_ms;
