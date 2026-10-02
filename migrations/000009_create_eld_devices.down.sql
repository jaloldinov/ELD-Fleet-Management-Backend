-- Reverses 000009_create_eld_devices.up.sql.
DROP TABLE IF EXISTS eld_devices;
ALTER TABLE trucks DROP CONSTRAINT IF EXISTS trucks_id_company_uniq;
