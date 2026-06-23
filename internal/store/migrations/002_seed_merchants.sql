INSERT INTO merchants (slug, name, category, description)
VALUES
    ('lagos-lunchbox', 'Lagos Lunchbox', 'Food', 'Fictional weekday meals and catering merchant.'),
    ('kora-books', 'Kora Books', 'Books', 'Fictional independent bookseller for the payment demo.'),
    ('bright-fix-ng', 'BrightFix NG', 'Services', 'Fictional household repair and maintenance merchant.')
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    category = EXCLUDED.category,
    description = EXCLUDED.description,
    active = true,
    updated_at = now();
