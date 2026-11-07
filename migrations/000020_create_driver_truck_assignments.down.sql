DROP TABLE IF EXISTS driver_truck_assignments;
ALTER TABLE drivers DROP CONSTRAINT IF EXISTS drivers_id_company_uniq;
