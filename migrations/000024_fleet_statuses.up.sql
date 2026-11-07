-- A richer state than "active or not", for trucks and ELD devices.
--
-- trucks.status:
--   active          on the road: drivers may be assigned
--   in_maintenance  in the shop: no drivers, but its ELD stays installed
--                   (and one may be installed there); still part of the
--                   fleet, so still billed
--   out_of_service  left the fleet (sold, scrapped, long-term parked)
--
-- eld_devices.status:
--   active   works: may be installed
--   faulty   broken: may stay in its truck until replaced, cannot be
--            installed anywhere
--   retired  end of life: must be taken out of its truck first
--
-- is_active stays, as a column GENERATED from status: old API clients and
-- queries keep working, and since nobody can write it, it can never
-- disagree with status. For trucks it means "part of the fleet" (what
-- per-truck billing counts), so a truck in the shop is still active.

ALTER TABLE trucks ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
UPDATE trucks SET status = 'out_of_service' WHERE NOT is_active;
ALTER TABLE trucks ADD CONSTRAINT trucks_status
    CHECK (status IN ('active', 'in_maintenance', 'out_of_service'));
ALTER TABLE trucks DROP COLUMN is_active;
ALTER TABLE trucks ADD COLUMN is_active BOOLEAN
    GENERATED ALWAYS AS (status <> 'out_of_service') STORED;

ALTER TABLE eld_devices ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
UPDATE eld_devices SET status = 'retired' WHERE NOT is_active;
ALTER TABLE eld_devices ADD CONSTRAINT eld_devices_status
    CHECK (status IN ('active', 'faulty', 'retired'));
ALTER TABLE eld_devices DROP COLUMN is_active;
ALTER TABLE eld_devices ADD COLUMN is_active BOOLEAN
    GENERATED ALWAYS AS (status = 'active') STORED;
