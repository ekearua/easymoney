-- Structured corporate documents collected during self-service KYB upgrade
-- requests (CAC, TIN, beneficial owners, business address, SCUML, DNFBP
-- self-certification). These enrich the advisory advancement request so the
-- compliance team can verify the exact documents a merchant supplied.
CREATE TABLE IF NOT EXISTS business_kyb_documents (
    merchant_id       uuid PRIMARY KEY REFERENCES merchants(id) ON DELETE CASCADE,
    cac_reg_number    text NOT NULL DEFAULT '',
    tin               text NOT NULL DEFAULT '',
    beneficial_owners text NOT NULL DEFAULT '',
    business_address  text NOT NULL DEFAULT '',
    scuml_number      text NOT NULL DEFAULT '',
    dnfbp             boolean NOT NULL DEFAULT FALSE,
    recorded_at       timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Individual tier-1 data was re-keyed to the CBN three-tier framework in 073;
-- 074 carries no further individual changes. It exists so environment rebuilds
-- apply the KYB corporate documents table as a discrete step.