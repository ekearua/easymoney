-- Cross-channel account linking: a user proves ownership of a WhatsApp
-- number with a one-time code, then their channel row is merged into the
-- WhatsApp account. The channel row survives as a tombstone pointing at
-- the surviving account via merged_into_id so receipts, KYC history, and
-- wallet balances follow the customer across channels.

ALTER TABLE users
	ADD COLUMN merged_into_id uuid NULL REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX idx_users_merged_into_id ON users(merged_into_id)
	WHERE merged_into_id IS NOT NULL;

CREATE TABLE user_link_requests (
	id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	channel      text NOT NULL,
	phone_digest text NOT NULL,
	code_hash    bytea NOT NULL,
	attempts     integer NOT NULL DEFAULT 0,
	consumed_at  timestamptz,
	expires_at   timestamptz NOT NULL,
	created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_user_link_requests_active
	ON user_link_requests(user_id, phone_digest, consumed_at);