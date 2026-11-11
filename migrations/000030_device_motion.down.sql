DROP INDEX IF EXISTS eld_device_states_stopped_idx;
ALTER TABLE eld_device_states DROP COLUMN IF EXISTS motion_since;
