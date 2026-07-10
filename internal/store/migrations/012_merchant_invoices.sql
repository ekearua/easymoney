-- Merchant approval, ownership, and chat-generated invoice support.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS account_level text NOT NULL DEFAULT 'customer';

UPDATE users
SET account_level = 'customer'
WHERE account_level = '';

UPDATE merchant_registrations
SET status = 'awaiting_approval'
WHERE status = 'pending_review';

CREATE TABLE IF NOT EXISTS merchant_owners (
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (merchant_id, user_id)
);

CREATE INDEX IF NOT EXISTS merchant_owners_user_idx
    ON merchant_owners(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS invoices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id),
    created_by_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    customer_whatsapp_number text NOT NULL,
    customer_email text NOT NULL DEFAULT '',
    reference text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'sent',
    delivery_fee_kobo bigint NOT NULL DEFAULT 0 CHECK (delivery_fee_kobo >= 0),
    subtotal_kobo bigint NOT NULL CHECK (subtotal_kobo >= 0),
    total_kobo bigint NOT NULL CHECK (total_kobo > 0),
    amount_paid_kobo bigint NOT NULL DEFAULT 0 CHECK (amount_paid_kobo >= 0),
    due_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    paid_at timestamptz
);

CREATE INDEX IF NOT EXISTS invoices_reference_idx ON invoices(reference);
CREATE INDEX IF NOT EXISTS invoices_merchant_created_idx ON invoices(merchant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS invoices_customer_idx ON invoices(customer_whatsapp_number, created_at DESC);
CREATE INDEX IF NOT EXISTS invoices_status_idx ON invoices(status, updated_at);

CREATE TABLE IF NOT EXISTS invoice_items (
    id bigserial PRIMARY KEY,
    invoice_id uuid NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    description text NOT NULL,
    quantity integer NOT NULL CHECK (quantity > 0),
    unit_price_kobo bigint NOT NULL CHECK (unit_price_kobo > 0),
    line_total_kobo bigint NOT NULL CHECK (line_total_kobo > 0),
    sort_order integer NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS invoice_payments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id uuid NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    payment_id uuid NOT NULL UNIQUE REFERENCES payments(id) ON DELETE CASCADE,
    payer_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount_kobo bigint NOT NULL CHECK (amount_kobo > 0),
    status text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS invoice_payments_invoice_idx
    ON invoice_payments(invoice_id, created_at DESC);
