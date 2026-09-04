-- C52: individual pay ("transfer money to an account") is a platform
-- collection. The sender's payment is recorded against this inactive system
-- merchant and routed through the Interswitch gateway like any other checkout,
-- so the money-in allowance, ledger money-in posting, and gateway verification
-- all apply. The recipient payout is settled by the post-success hook.
INSERT INTO merchants(slug,name,category,description,active,search_keywords,sort_order)
VALUES(
    'xego-individual-pay',
    'Xego Individual Pay',
    'Transfers',
    'System recipient for Xego individual-to-individual transfer payments.',
    false,
    'xego individual pay transfer send money',
    9999
)
ON CONFLICT(slug) DO UPDATE
SET name=EXCLUDED.name,
    category=EXCLUDED.category,
    description=EXCLUDED.description,
    active=EXCLUDED.active,
    search_keywords=EXCLUDED.search_keywords,
    sort_order=EXCLUDED.sort_order;
