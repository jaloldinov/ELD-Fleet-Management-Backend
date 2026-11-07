-- History lists filtered only by company ("everything that happened in my
-- fleet, newest first"). The existing history indexes start with driver_id,
-- truck_id or eld_device_id, so they cannot serve this: without these,
-- Postgres would read every company's stints and sort them.
CREATE INDEX dta_company_history_idx
    ON driver_truck_assignments (company_id, started_at DESC);
CREATE INDEX eta_company_history_idx
    ON eld_truck_assignments (company_id, started_at DESC);
