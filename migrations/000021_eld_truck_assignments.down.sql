-- Back to "current truck only": the open stints become truck_id again;
-- ended stints (history) are lost, as the old schema cannot hold them.
ALTER TABLE eld_devices ADD COLUMN truck_id UUID NULL;

UPDATE eld_devices d
SET truck_id = a.truck_id
FROM eld_truck_assignments a
WHERE a.eld_device_id = d.id AND a.ended_at IS NULL;

ALTER TABLE eld_devices ADD CONSTRAINT eld_devices_truck_company_fkey
    FOREIGN KEY (truck_id, company_id) REFERENCES trucks (id, company_id);
ALTER TABLE eld_devices ADD CONSTRAINT eld_devices_deleted_unassigned
    CHECK (deleted_at IS NULL OR truck_id IS NULL);
CREATE UNIQUE INDEX eld_devices_truck_uniq
    ON eld_devices (truck_id)
    WHERE truck_id IS NOT NULL;

DROP TABLE IF EXISTS eld_truck_assignments;
ALTER TABLE eld_devices DROP CONSTRAINT IF EXISTS eld_devices_id_company_uniq;
