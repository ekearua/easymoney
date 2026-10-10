-- AML vendor record §8(1)/§8(5) + evidence pack Gate 4: record which vendor
-- list version produced each screening decision, so a disposition is
-- defensible later and the rescreen worker can re-evaluate open holds when
-- the list moves (adverse_media_screenings already carries the column from
-- migration 097; this brings the customer and payment lanes to parity).

ALTER TABLE screening_results ADD COLUMN IF NOT EXISTS list_version text NOT NULL DEFAULT '';

ALTER TABLE payment_screenings ADD COLUMN IF NOT EXISTS list_version text NOT NULL DEFAULT '';