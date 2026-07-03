-- Catalog picker metadata lets Xego present short, searchable customer lists
-- while keeping stable merchant and bank identifiers behind the scenes.
ALTER TABLE merchants
    ADD COLUMN IF NOT EXISTS search_keywords text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sort_order integer NOT NULL DEFAULT 1000;

ALTER TABLE bank_transfer_accounts
    ADD COLUMN IF NOT EXISTS search_keywords text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sort_order integer NOT NULL DEFAULT 1000;

UPDATE merchants SET
    search_keywords = 'food meals lunch catering lagos',
    sort_order = 10
WHERE slug = 'lagos-lunchbox';

UPDATE merchants SET
    search_keywords = 'books stationery reading education',
    sort_order = 20
WHERE slug = 'kora-books';

UPDATE merchants SET
    search_keywords = 'repairs home maintenance installation services',
    sort_order = 30
WHERE slug = 'bright-fix-ng';

UPDATE bank_transfer_accounts SET
    search_keywords = 'access diamond',
    sort_order = 10
WHERE bank_name = 'Access Bank';

UPDATE bank_transfer_accounts SET
    search_keywords = 'gtbank gtb guaranty trust',
    sort_order = 20
WHERE bank_name = 'Guaranty Trust Bank';

UPDATE bank_transfer_accounts SET
    search_keywords = 'zenith',
    sort_order = 30
WHERE bank_name = 'Zenith Bank';

UPDATE bank_transfer_accounts SET
    search_keywords = 'firstbank first bank fbn',
    sort_order = 40
WHERE bank_name = 'First Bank of Nigeria';

UPDATE bank_transfer_accounts SET
    search_keywords = 'uba united bank africa',
    sort_order = 50
WHERE bank_name = 'United Bank for Africa';

CREATE TABLE IF NOT EXISTS user_merchant_recents (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    last_selected_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, merchant_id)
);

CREATE INDEX IF NOT EXISTS user_merchant_recents_user_idx
    ON user_merchant_recents(user_id, last_selected_at DESC);
