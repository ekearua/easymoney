-- The device-share signal reads trusted_devices by fingerprint alone: every
-- device sighting looks up whether anyone else uses the same browser, and the
-- network edge query joins the table to itself on it. The only existing
-- constraint is UNIQUE (user_id, device_fingerprint), which serves neither.
CREATE INDEX IF NOT EXISTS trusted_devices_fingerprint_idx
    ON trusted_devices(device_fingerprint);
