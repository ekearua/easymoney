-- W1: wallet top-ups ("fund wallet") are a platform collection. The payer's
-- payment is recorded against this inactive system merchant and routed through
-- the gateway like any other checkout, so the money-in allowance, ledger
-- money-in posting, and gateway verification all apply. The wallet credit is
-- applied by the wallet_topup post-success hook.
INSERT INTO merchants(slug,name,category,description,active,search_keywords,sort_order)
VALUES(
    'xego-wallet-topup',
    'Xego Wallet Top-up',
    'Wallet',
    'System recipient for Xego wallet funding payments.',
    false,
    'xego wallet top up fund add money deposit',
    9999
)
ON CONFLICT(slug) DO UPDATE
SET name=EXCLUDED.name,
    category=EXCLUDED.category,
    description=EXCLUDED.description,
    active=EXCLUDED.active,
    search_keywords=EXCLUDED.search_keywords,
    sort_order=EXCLUDED.sort_order;