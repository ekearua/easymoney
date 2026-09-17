-- Outbound SMS delivery tracking: the outbox worker records the provider's
-- message identifier after a successful send so the /webhooks/sms/status
-- delivery-status callback can reconcile the outbound message to delivered or
-- failed without correlating by recipient/body/text.

ALTER TABLE message_outbox
	ADD COLUMN IF NOT EXISTS provider_ref text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_message_outbox_provider_ref
	ON message_outbox(provider_ref)
	WHERE provider_ref <> '';