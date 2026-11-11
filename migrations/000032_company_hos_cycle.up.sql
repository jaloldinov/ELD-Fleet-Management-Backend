-- The carrier's weekly hours-of-service limit (49 CFR 395.3(b)): 70 hours
-- in 8 days (the usual choice, the default) or 60 hours in 7 days (for
-- carriers that do not run every day of the week).
ALTER TABLE companies
    ADD COLUMN hos_cycle TEXT NOT NULL DEFAULT 'us_70_8',
    ADD CONSTRAINT companies_hos_cycle CHECK (hos_cycle IN ('us_70_8', 'us_60_7'));
