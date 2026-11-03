-- Remove the last non-financial blocker to deleting a merchant.
--
-- registered_services is the merchant's receipt-scanning configuration. Every
-- merchant created through the admin form gets one by default, so its
-- un-cascaded foreign key made Remove fail with a foreign-key error for 28 of
-- 32 merchants -- including brand-new rows with no payments, invoices or
-- ledger entries at all.
--
-- Cascading it keeps the admin's Remove meaningful. Child rows follow the
-- service: service_readers and receipt_scan_tokens cascade, while
-- receipt_scan_attempts keeps its history with service_id set to NULL.
--
-- Payments, invoices, ledger_entries and checkouts deliberately still have no
-- ON DELETE CASCADE: those reference an actual money movement for a merchant
-- and must remain immutable (MLPA). The dashboard now reports exactly which
-- of them blocks a removal and tells the admin to disable the merchant
-- instead.

ALTER TABLE registered_services
	DROP CONSTRAINT IF EXISTS registered_services_merchant_id_fkey;

ALTER TABLE registered_services
	ADD CONSTRAINT registered_services_merchant_id_fkey
	FOREIGN KEY (merchant_id) REFERENCES merchants(id) ON DELETE CASCADE;
