-- The secret key an ELD device uses to call the API ("Authorization:
-- Device eldk_..."). Only its SHA-256 is stored: a leaked database does not
-- leak working keys. One key per device: issuing a new one replaces
-- (rotates) the old one, which stops working at once.
ALTER TABLE eld_devices
    ADD COLUMN key_hash         BYTEA       NULL,
    ADD COLUMN key_created_at   TIMESTAMPTZ NULL,
    -- Updated at most once a minute, not on every request: a device sends
    -- data every few seconds, and a write per request would be wasteful.
    ADD COLUMN key_last_used_at TIMESTAMPTZ NULL;

ALTER TABLE eld_devices
    ADD CONSTRAINT eld_devices_key_hash_length CHECK (key_hash IS NULL OR octet_length(key_hash) = 32),
    -- A key and its creation time go together.
    ADD CONSTRAINT eld_devices_key_pair CHECK ((key_hash IS NULL) = (key_created_at IS NULL)),
    -- A deleted device can never authenticate: its key is removed with it.
    ADD CONSTRAINT eld_devices_deleted_no_key CHECK (deleted_at IS NULL OR key_hash IS NULL);

-- The lookup on every device request; unique, so a hash finds one device.
CREATE UNIQUE INDEX eld_devices_key_hash_uniq
    ON eld_devices (key_hash)
    WHERE key_hash IS NOT NULL;
