DROP INDEX IF EXISTS eld_devices_key_hash_uniq;
ALTER TABLE eld_devices
    DROP CONSTRAINT IF EXISTS eld_devices_deleted_no_key,
    DROP CONSTRAINT IF EXISTS eld_devices_key_pair,
    DROP CONSTRAINT IF EXISTS eld_devices_key_hash_length,
    DROP COLUMN IF EXISTS key_last_used_at,
    DROP COLUMN IF EXISTS key_created_at,
    DROP COLUMN IF EXISTS key_hash;
