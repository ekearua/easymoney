-- Processing consent is the privacy-policy default: every user has granted it
-- from first use (NDPR 3.1 lawful basis). 029 backfilled users that already
-- existed; this trigger keeps the invariant for every user created after that.
CREATE OR REPLACE FUNCTION auto_grant_processing_consent() RETURNS trigger AS $$
BEGIN
    INSERT INTO consent_records(user_id, purpose, source, version)
    VALUES (NEW.id, 'processing', 'chat', '1.0')
    ON CONFLICT (user_id, purpose) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS users_auto_grant_processing_consent ON users;
CREATE TRIGGER users_auto_grant_processing_consent
    AFTER INSERT ON users
    FOR EACH ROW EXECUTE FUNCTION auto_grant_processing_consent();
