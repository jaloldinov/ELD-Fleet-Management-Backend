-- Going back loses the edit history: the old schema has no place for it.
DROP TABLE IF EXISTS duty_log_certifications;
DROP INDEX IF EXISTS dse_driver_inactive_idx;
DROP INDEX IF EXISTS dse_replaces_uniq;
ALTER TABLE duty_status_events
    DROP COLUMN IF EXISTS replaces,
    DROP COLUMN IF EXISTS edit_reason,
    DROP COLUMN IF EXISTS inactivated_by;
DELETE FROM duty_status_events WHERE record_status = 'inactive';
ALTER TABLE duty_status_events
    DROP COLUMN IF EXISTS inactivated_at,
    DROP COLUMN IF EXISTS record_status;
DROP INDEX IF EXISTS dse_driver_started_uniq;
CREATE UNIQUE INDEX dse_driver_started_uniq ON duty_status_events (driver_id, started_at);
