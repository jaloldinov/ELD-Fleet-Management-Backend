DROP TABLE IF EXISTS eld_device_states;
ALTER TABLE eld_devices RENAME COLUMN last_seen_at TO key_last_used_at;
