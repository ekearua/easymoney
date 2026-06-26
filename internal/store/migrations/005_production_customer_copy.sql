-- Refresh customer-visible merchant descriptions so WhatsApp lists read like
-- production merchant profiles instead of prototype/demo fixtures.
UPDATE merchants SET
    description = CASE slug
        WHEN 'lagos-lunchbox' THEN 'Weekday meals, lunch packs, and catering.'
        WHEN 'kora-books' THEN 'Books, stationery, and reading accessories.'
        WHEN 'bright-fix-ng' THEN 'Home repair, maintenance, and installation services.'
        ELSE description
    END,
    updated_at = now()
WHERE slug IN ('lagos-lunchbox', 'kora-books', 'bright-fix-ng');
