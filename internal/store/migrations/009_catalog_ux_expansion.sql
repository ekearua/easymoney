-- Expand customer-facing catalogs so merchant and bank pickers can be tested
-- with realistic browsing, pagination, and search. Xego Data remains in the
-- merchants table only as an internal payment recipient for data orders.
UPDATE merchants
SET active = false,
    category = 'Service',
    description = 'Internal payment recipient for Xego data orders.',
    search_keywords = 'internal data mtn airtel glo 9mobile telecoms',
    sort_order = 900,
    updated_at = now()
WHERE slug = 'xego-data';

INSERT INTO merchants (slug, name, category, description, active, search_keywords, sort_order)
VALUES
    ('lagos-lunchbox', 'Lagos Lunchbox', 'Food', 'Weekday meals, lunch packs, and catering.', true, 'food lunch meals catering lagos', 10),
    ('kora-books', 'Kora Books', 'Books', 'Books, stationery, and reading accessories.', true, 'books stationery education reading', 20),
    ('bright-fix-ng', 'BrightFix NG', 'Services', 'Home repair, maintenance, and installation services.', true, 'repairs maintenance electrician plumber installation', 30),
    ('campusmart-ng', 'CampusMart NG', 'Retail', 'Student essentials, electronics accessories, and daily supplies.', true, 'retail campus student supplies accessories', 40),
    ('greencart-grocers', 'GreenCart Grocers', 'Groceries', 'Fresh groceries, pantry staples, and household provisions.', true, 'groceries food market provisions fresh', 50),
    ('swiftride-logistics', 'SwiftRide Logistics', 'Logistics', 'Local delivery, dispatch, and small-business courier services.', true, 'delivery logistics courier dispatch transport', 60),
    ('lekki-laundry-co', 'Lekki Laundry Co.', 'Laundry', 'Laundry, dry cleaning, ironing, and garment care.', true, 'laundry dry cleaning ironing clothes', 70),
    ('terahome-decor', 'TeraHome Decor', 'Home', 'Home decor, bedding, curtains, and household accents.', true, 'home decor bedding curtains furniture', 80),
    ('naija-gadgets-hub', 'Naija Gadgets Hub', 'Electronics', 'Phones, chargers, accessories, and device support.', true, 'electronics phones gadgets chargers accessories', 90),
    ('fitlife-studio', 'FitLife Studio', 'Fitness', 'Gym sessions, wellness classes, and fitness subscriptions.', true, 'fitness gym wellness health classes', 100),
    ('abeokuta-agro-market', 'Abeokuta Agro Market', 'Agriculture', 'Farm produce, grains, feeds, and agro supplies.', true, 'agriculture farm produce grains feeds', 110),
    ('ph-power-tools', 'PH Power Tools', 'Hardware', 'Tools, safety gear, repair parts, and workshop supplies.', true, 'hardware tools workshop safety equipment', 120),
    ('enugu-event-rentals', 'Enugu Event Rentals', 'Events', 'Canopies, chairs, tables, and event equipment rental.', true, 'events rentals canopies chairs tables', 130),
    ('kaduna-tutors-network', 'Kaduna Tutors Network', 'Education', 'Private tutoring, lesson packages, and exam preparation.', true, 'education tutors lessons exams school', 140),
    ('owambe-fabrics', 'Owambe Fabrics', 'Fashion', 'Fabrics, aso-ebi coordination, and tailoring deposits.', true, 'fashion fabrics aso ebi tailoring clothing', 150),
    ('mainland-diagnostics', 'Mainland Diagnostics', 'Health', 'Diagnostics bookings, wellness checks, and lab services.', true, 'health diagnostics laboratory wellness medical', 160),
    ('cloudkiosk-digital', 'CloudKiosk Digital', 'Software', 'Digital tools, website care, and business software support.', true, 'software digital website business tools', 170),
    ('northstar-travel-desk', 'Northstar Travel Desk', 'Travel', 'Local travel bookings, tour deposits, and itinerary support.', true, 'travel tours bookings itinerary transport', 180),
    ('eko-beauty-bar', 'Eko Beauty Bar', 'Beauty', 'Beauty appointments, skincare services, and salon deposits.', true, 'beauty salon skincare makeup hair', 190),
    ('surulere-auto-care', 'Surulere Auto Care', 'Auto', 'Vehicle diagnostics, servicing, wash, and repair bookings.', true, 'auto car vehicle repairs service wash', 200),
    ('ibadan-furniture-works', 'Ibadan Furniture Works', 'Furniture', 'Custom furniture, repairs, and installation deposits.', true, 'furniture carpentry chairs tables home', 210)
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    category = EXCLUDED.category,
    description = EXCLUDED.description,
    active = EXCLUDED.active,
    search_keywords = EXCLUDED.search_keywords,
    sort_order = EXCLUDED.sort_order,
    updated_at = now();

INSERT INTO bank_transfer_accounts (bank_name, account_name, account_number, active, search_keywords, sort_order)
VALUES
    ('Access Bank', 'Xego Collections', '9000000001', true, 'access diamond access bank', 10),
    ('Guaranty Trust Bank', 'Xego Collections', '9000000002', true, 'gtbank gtb guaranty trust gtco', 20),
    ('Zenith Bank', 'Xego Collections', '9000000003', true, 'zenith zenithbank', 30),
    ('First Bank of Nigeria', 'Xego Collections', '9000000004', true, 'firstbank first bank fbn', 40),
    ('United Bank for Africa', 'Xego Collections', '9000000005', true, 'uba united bank africa', 50),
    ('Fidelity Bank', 'Xego Collections', '9000000006', true, 'fidelity fidelitybank', 60),
    ('First City Monument Bank', 'Xego Collections', '9000000007', true, 'fcmb first city monument', 70),
    ('Sterling Bank', 'Xego Collections', '9000000008', true, 'sterling onebank', 80),
    ('Stanbic IBTC Bank', 'Xego Collections', '9000000009', true, 'stanbic ibtc standard bank', 90),
    ('Wema Bank', 'Xego Collections', '9000000010', true, 'wema alat', 100),
    ('Polaris Bank', 'Xego Collections', '9000000011', true, 'polaris skye', 110),
    ('Union Bank of Nigeria', 'Xego Collections', '9000000012', true, 'union bank unionbank', 120),
    ('Keystone Bank', 'Xego Collections', '9000000013', true, 'keystone', 130),
    ('Providus Bank', 'Xego Collections', '9000000014', true, 'providus providusbank', 140),
    ('Ecobank Nigeria', 'Xego Collections', '9000000015', true, 'ecobank eco bank', 150),
    ('Jaiz Bank', 'Xego Collections', '9000000016', true, 'jaiz non interest islamic', 160),
    ('StanChart Nigeria', 'Xego Collections', '9000000017', true, 'standard chartered stanchart', 170),
    ('Titan Trust Bank', 'Xego Collections', '9000000018', true, 'titan trust', 180)
ON CONFLICT (bank_name) DO UPDATE SET
    account_name = EXCLUDED.account_name,
    account_number = EXCLUDED.account_number,
    active = EXCLUDED.active,
    search_keywords = EXCLUDED.search_keywords,
    sort_order = EXCLUDED.sort_order,
    updated_at = now();
