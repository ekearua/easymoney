-- Bank directory resolved from bank name to NUBAN code. Seeded from the
-- published CBN / NIBSS bank list so chat and web flows can accept a bank
-- name (e.g. "GTBank") and persist the canonical code alongside the name.
-- normalized holds the case/punctuation-compacted form of "name short_name"
-- (e.g. "guarantytrustbank") for exact matching; search_terms carries the
-- lowercase, space-separated words and aliases used for substring/trigram
-- search (e.g. "gtb gtbank"). Both are derived values used for lookup only.
CREATE TABLE IF NOT EXISTS bank_directory (
    code text PRIMARY KEY,
    name text NOT NULL,
    short_name text NOT NULL DEFAULT '',
    normalized text NOT NULL UNIQUE,
    search_terms text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    sort_order int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_bank_directory_search ON bank_directory USING gin (
    (search_terms) gin_trgm_ops
);

INSERT INTO bank_directory (code, name, short_name, normalized, search_terms, sort_order) VALUES
    ('044', 'Access Bank', 'Access', 'accessbank', 'access bank accessbank access diamond', 1),
    ('058', 'Guaranty Trust Bank', 'GTBank', 'guarantytrustbankgtbank', 'guaranty trust bank gtb gtbank gtco guarantytrustbank', 2),
    ('057', 'Zenith Bank', 'Zenith', 'zenithbank', 'zenith bank zenithbank', 3),
    ('011', 'First Bank of Nigeria', 'First Bank', 'firstbankofnigeriafirstbank', 'first bank of nigeria firstbank first bank fbn', 4),
    ('033', 'United Bank for Africa', 'UBA', 'unitedbankforafricauba', 'united bank for africa uba unitedbankforafrica', 5),
    ('032', 'Union Bank of Nigeria', 'Union Bank', 'unionbankofnigeriaunionbank', 'union bank of nigeria unionbank union ubn', 6),
    ('070', 'Fidelity Bank', 'Fidelity', 'fidelitybank', 'fidelity bank fidelitybank', 7),
    ('214', 'First City Monument Bank', 'FCMB', 'firstcitymonumentbankfcmb', 'first city monument bank fcmb firstcitymonumentbank', 8),
    ('221', 'Stanbic IBTC Bank', 'Stanbic IBTC', 'stanbicibtcbankstanbicibtc', 'stanbic ibtc bank stanbic ibtc stanbicibtc ibtc', 9),
    ('232', 'Sterling Bank', 'Sterling', 'sterlingbank', 'sterling bank sterlingbank', 10),
    ('035', 'Wema Bank', 'Wema', 'wemabank', 'wema bank wemabank alat', 11),
    ('076', 'Polaris Bank', 'Polaris', 'polarisbank', 'polaris bank polarisbank', 12),
    ('082', 'Keystone Bank', 'Keystone', 'keystonebank', 'keystone bank keystonebank', 13),
    ('030', 'Heritage Bank', 'Heritage', 'heritagebank', 'heritage bank heritagebank', 14),
    ('063', 'Diamond Bank', 'Diamond', 'diamondbank', 'diamond bank diamondbank', 15),
    ('050', 'Ecobank Nigeria', 'Ecobank', 'ecobanknigeriaecobank', 'ecobank nigeria ecobank ecobanknigeria', 16),
    ('023', 'Citibank Nigeria', 'Citibank', 'citibanknigeriacitibank', 'citibank nigeria citibank', 17),
    ('068', 'Standard Chartered Bank', 'Standard Chartered', 'standardcharteredbankstandardchartered', 'standard chartered bank standard chartered standardchartered', 18),
    ('215', 'Unity Bank', 'Unity', 'unitybank', 'unity bank unitybank', 19),
    ('301', 'Jaiz Bank', 'Jaiz', 'jaizbank', 'jaiz bank jaizbank', 20),
    ('302', 'TAJ Bank', 'TAJ', 'tajbank', 'taj bank tajbank', 21),
    ('303', 'Lotus Bank', 'Lotus', 'lotusbank', 'lotus bank lotusbank', 22),
    ('100', 'SunTrust Bank Nigeria', 'SunTrust', 'suntrustbanknigeriasuntrust', 'suntrust bank nigeria suntrust suntrustbank', 23),
    ('101', 'Providus Bank', 'Providus', 'providusbank', 'providus bank providusbank', 24),
    ('105', 'Premium Trust Bank', 'Premium Trust', 'premiumtrustbankpremiumtrust', 'premium trust bank premium trust premiumtrustbank', 25),
    ('107', 'Optimus Bank', 'Optimus', 'optimusbank', 'optimus bank optimusbank', 26),
    ('523', 'Globus Bank', 'Globus', 'globusbank', 'globus bank globusbank', 27),
    ('526', 'Parallex Bank', 'Parallex', 'parallexbank', 'parallex bank parallexbank', 28),
    ('522', '9 Payment Service Bank', '9PSB', '9paymentservicebank9psb', '9 payment service bank 9psb nine payment service bank', 29),
    ('502', 'Kuda Microfinance Bank', 'Kuda', 'kudamicrofinancebankkuda', 'kuda microfinance bank kuda kudabank', 30),
    ('501', 'Sparkle Microfinance Bank', 'Sparkle', 'sparklemicrofinancebanksparkle', 'sparkle microfinance bank sparkle sparklebank', 31),
    ('503', 'VFD Microfinance Bank', 'VFD', 'vfdmicrofinancebankvfd', 'vfd microfinance bank vfd vfdbank', 32),
    ('504', 'Moniepoint Microfinance Bank', 'Moniepoint', 'moniepointmicrofinancebankmoniepoint', 'moniepoint microfinance bank moniepoint moniepointbank', 33),
    ('505', 'PalmPay', 'PalmPay', 'palmpay', 'palmpay palm pay', 34),
    ('506', 'OPay Digital Services', 'OPay', 'opaydigitalservicesopay', 'opay digital services opay opaybank', 35),
    ('509', 'Paga', 'Paga', 'paga', 'paga', 36),
    ('515', 'Eyowo', 'Eyowo', 'eyowo', 'eyowo', 37),
    ('566', 'VFD Microfinance Bank', 'VFD', 'vfdmicrofinancebank', 'vfd microfinance bank vfd', 38),
    ('553', 'Rubies Microfinance Bank', 'Rubies', 'rubiesmicrofinancebankrubies', 'rubies microfinance bank rubies rubiesbank', 39),
    ('552', 'Fairmoney Microfinance Bank', 'FairMoney', 'fairmoneymicrofinancebankfairmoney', 'fairmoney microfinance bank fairmoney fair money', 40)
ON CONFLICT (code) DO UPDATE SET
    name = EXCLUDED.name,
    short_name = EXCLUDED.short_name,
    normalized = EXCLUDED.normalized,
    search_terms = EXCLUDED.search_terms,
    active = true,
    sort_order = EXCLUDED.sort_order;