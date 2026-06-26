INSERT INTO merchants (slug, name, category, description)
VALUES
    ('lagos-lunchbox', 'Lagos Lunchbox', 'Food', 'Weekday meals, lunch packs, and catering.'),
    ('kora-books', 'Kora Books', 'Books', 'Books, stationery, and reading accessories.'),
    ('bright-fix-ng', 'BrightFix NG', 'Services', 'Home repair, maintenance, and installation services.')
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    category = EXCLUDED.category,
    description = EXCLUDED.description,
    active = true,
    updated_at = now();
