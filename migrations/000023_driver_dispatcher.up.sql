-- The dispatcher who looks after a driver ("my drivers"). Informational:
-- it does not limit who may see or assign the driver. NULL = nobody.
ALTER TABLE drivers ADD COLUMN dispatcher_id UUID NULL;

-- The dispatcher is a user of the SAME company as the driver. With MATCH
-- SIMPLE (the default) the check is skipped while dispatcher_id IS NULL.
ALTER TABLE drivers ADD CONSTRAINT drivers_dispatcher_company_fkey
    FOREIGN KEY (dispatcher_id, company_id) REFERENCES users (id, company_id);

-- A driver is not their own dispatcher.
ALTER TABLE drivers ADD CONSTRAINT drivers_dispatcher_not_self
    CHECK (dispatcher_id IS NULL OR dispatcher_id <> user_id);

-- "Drivers of dispatcher X" (the list filter), and clearing a leaving
-- dispatcher's links.
CREATE INDEX drivers_dispatcher_idx
    ON drivers (dispatcher_id)
    WHERE dispatcher_id IS NOT NULL AND deleted_at IS NULL;
