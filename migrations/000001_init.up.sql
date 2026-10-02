-- Shared trigger function: sets updated_at to the current time on every UPDATE.
-- Each table that has an updated_at column attaches it like this:
--
--   CREATE TRIGGER trg_<table>_updated_at
--       BEFORE UPDATE ON <table>
--       FOR EACH ROW EXECUTE FUNCTION set_updated_at();
--
-- Doing this in the database means updated_at is always correct, even for
-- updates made outside the Go application (manual SQL, scripts, other services).
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
