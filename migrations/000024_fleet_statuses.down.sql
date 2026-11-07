-- Back to a plain boolean, filled from status (the generated column is
-- dropped first: it cannot be turned into a normal one in place).
ALTER TABLE eld_devices DROP COLUMN is_active;
ALTER TABLE eld_devices ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT true;
UPDATE eld_devices SET is_active = (status = 'active');
ALTER TABLE eld_devices DROP CONSTRAINT IF EXISTS eld_devices_status;
ALTER TABLE eld_devices DROP COLUMN status;

ALTER TABLE trucks DROP COLUMN is_active;
ALTER TABLE trucks ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT true;
UPDATE trucks SET is_active = (status <> 'out_of_service');
ALTER TABLE trucks DROP CONSTRAINT IF EXISTS trucks_status;
ALTER TABLE trucks DROP COLUMN status;
