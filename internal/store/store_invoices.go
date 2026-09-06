package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// InvoiceItem is one line on a merchant-generated invoice.
type InvoiceItem struct {
	ID            int64
	Description   string
	Quantity      int
	UnitPriceKobo int64
	LineTotalKobo int64
	SortOrder     int
}

// InvoiceView joins invoice data with merchant and creator display fields.
type InvoiceView struct {
	ID                     uuid.UUID
	MerchantID             uuid.UUID
	CreatedByUserID        uuid.UUID
	CustomerWhatsAppNumber string
	CustomerEmail          string
	Reference              string
	Status                 string
	DeliveryFeeKobo        int64
	SubtotalKobo           int64
	TotalKobo              int64
	AmountPaidKobo         int64
	DueAt                  *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
	PaidAt                 *time.Time
	MerchantName           string
	MerchantSlug           string
	MerchantCategory       string
	CreatorName            string
	CreatorEmail           string
	Items                  []InvoiceItem
}

// InvoicePaymentView links one payment receipt to one invoice contribution.
type InvoicePaymentView struct {
	ID          uuid.UUID
	InvoiceID   uuid.UUID
	PaymentID   uuid.UUID
	PayerUserID uuid.UUID
	AmountKobo  int64
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// InvoiceSpec contains validated invoice details collected in chat.
type InvoiceSpec struct {
	MerchantID             uuid.UUID
	CreatedByUserID        uuid.UUID
	CustomerWhatsAppNumber string
	CustomerEmail          string
	DeliveryFeeKobo        int64
	DueAt                  *time.Time
	Reference              string
	Items                  []InvoiceItem
}

// CreateInvoice stores a merchant invoice and its line items atomically.
func (s *Store) CreateInvoice(ctx context.Context, spec InvoiceSpec) (InvoiceView, error) {
	if len(spec.Items) == 0 {
		return InvoiceView{}, errors.New("invoice requires at least one line item")
	}
	subtotal := int64(0)
	for i := range spec.Items {
		item := &spec.Items[i]
		if item.Quantity <= 0 || item.UnitPriceKobo <= 0 || strings.TrimSpace(item.Description) == "" {
			return InvoiceView{}, errors.New("invoice item is invalid")
		}
		item.LineTotalKobo = int64(item.Quantity) * item.UnitPriceKobo
		item.SortOrder = i + 1
		subtotal += item.LineTotalKobo
	}
	total := subtotal + spec.DeliveryFeeKobo
	if total <= 0 {
		return InvoiceView{}, errors.New("invoice total must be greater than zero")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InvoiceView{}, err
	}
	defer tx.Rollback(ctx)
	reference := strings.ToUpper(strings.TrimSpace(spec.Reference))
	if reference == "" {
		reference = newInvoiceReference()
	}
	var invoice InvoiceView
	err = tx.QueryRow(ctx, `
		INSERT INTO invoices
			(id,merchant_id,created_by_user_id,customer_whatsapp_number,customer_email,reference,status,delivery_fee_kobo,subtotal_kobo,total_kobo,due_at)
		VALUES($1,$2,$3,$4,lower($5),$6,'sent',$7,$8,$9,$10)
		RETURNING id,merchant_id,created_by_user_id,customer_whatsapp_number,customer_email,reference,status,
		          delivery_fee_kobo,subtotal_kobo,total_kobo,amount_paid_kobo,due_at,created_at,updated_at,paid_at`,
		uuid.New(), spec.MerchantID, spec.CreatedByUserID, spec.CustomerWhatsAppNumber, spec.CustomerEmail,
		reference, spec.DeliveryFeeKobo, subtotal, total, spec.DueAt,
	).Scan(&invoice.ID, &invoice.MerchantID, &invoice.CreatedByUserID, &invoice.CustomerWhatsAppNumber,
		&invoice.CustomerEmail, &invoice.Reference, &invoice.Status, &invoice.DeliveryFeeKobo,
		&invoice.SubtotalKobo, &invoice.TotalKobo, &invoice.AmountPaidKobo, &invoice.DueAt,
		&invoice.CreatedAt, &invoice.UpdatedAt, &invoice.PaidAt)
	if err != nil {
		return InvoiceView{}, err
	}
	for _, item := range spec.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO invoice_items(invoice_id,description,quantity,unit_price_kobo,line_total_kobo,sort_order)
			VALUES($1,$2,$3,$4,$5,$6)`, invoice.ID, item.Description, item.Quantity, item.UnitPriceKobo, item.LineTotalKobo, item.SortOrder); err != nil {
			return InvoiceView{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return InvoiceView{}, err
	}
	return s.InvoiceByReference(ctx, reference)
}

func newInvoiceReference() string {
	return "XG-INV-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
}

// InvoiceByReference returns an invoice with merchant details and line items.
func (s *Store) InvoiceByReference(ctx context.Context, reference string) (InvoiceView, error) {
	var invoice InvoiceView
	err := s.pool.QueryRow(ctx, `
		SELECT i.id,i.merchant_id,i.created_by_user_id,i.customer_whatsapp_number,i.customer_email,
		       i.reference,i.status,i.delivery_fee_kobo,i.subtotal_kobo,i.total_kobo,i.amount_paid_kobo,
		       i.due_at,i.created_at,i.updated_at,i.paid_at,
		       m.name,m.slug,m.category,u.display_name,u.email
		FROM invoices i
		JOIN merchants m ON m.id=i.merchant_id
		JOIN users u ON u.id=i.created_by_user_id
		WHERE i.reference=$1`, strings.ToUpper(strings.TrimSpace(reference))).Scan(
		&invoice.ID, &invoice.MerchantID, &invoice.CreatedByUserID, &invoice.CustomerWhatsAppNumber,
		&invoice.CustomerEmail, &invoice.Reference, &invoice.Status, &invoice.DeliveryFeeKobo,
		&invoice.SubtotalKobo, &invoice.TotalKobo, &invoice.AmountPaidKobo, &invoice.DueAt,
		&invoice.CreatedAt, &invoice.UpdatedAt, &invoice.PaidAt, &invoice.MerchantName,
		&invoice.MerchantSlug, &invoice.MerchantCategory, &invoice.CreatorName, &invoice.CreatorEmail,
	)
	if err != nil {
		return InvoiceView{}, err
	}
	items, err := s.invoiceItems(ctx, invoice.ID)
	if err != nil {
		return InvoiceView{}, err
	}
	invoice.Items = items
	return invoice, nil
}

func (s *Store) invoiceItems(ctx context.Context, invoiceID uuid.UUID) ([]InvoiceItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,description,quantity,unit_price_kobo,line_total_kobo,sort_order
		FROM invoice_items
		WHERE invoice_id=$1
		ORDER BY sort_order,id`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []InvoiceItem
	for rows.Next() {
		var item InvoiceItem
		if err := rows.Scan(&item.ID, &item.Description, &item.Quantity, &item.UnitPriceKobo, &item.LineTotalKobo, &item.SortOrder); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RecentInvoicesForMerchantOwner returns a chat-sized dashboard for an approved merchant.
func (s *Store) RecentInvoicesForMerchantOwner(ctx context.Context, userID uuid.UUID, limit int) ([]InvoiceView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.id,i.merchant_id,i.created_by_user_id,i.customer_whatsapp_number,i.customer_email,
		       i.reference,i.status,i.delivery_fee_kobo,i.subtotal_kobo,i.total_kobo,i.amount_paid_kobo,
		       i.due_at,i.created_at,i.updated_at,i.paid_at,
		       m.name,m.slug,m.category,u.display_name,u.email
		FROM invoices i
		JOIN merchants m ON m.id=i.merchant_id
		JOIN merchant_owners mo ON mo.merchant_id=m.id
		JOIN users u ON u.id=i.created_by_user_id
		WHERE mo.user_id=$1
		ORDER BY i.created_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invoices []InvoiceView
	for rows.Next() {
		var invoice InvoiceView
		if err := rows.Scan(&invoice.ID, &invoice.MerchantID, &invoice.CreatedByUserID, &invoice.CustomerWhatsAppNumber,
			&invoice.CustomerEmail, &invoice.Reference, &invoice.Status, &invoice.DeliveryFeeKobo,
			&invoice.SubtotalKobo, &invoice.TotalKobo, &invoice.AmountPaidKobo, &invoice.DueAt,
			&invoice.CreatedAt, &invoice.UpdatedAt, &invoice.PaidAt, &invoice.MerchantName,
			&invoice.MerchantSlug, &invoice.MerchantCategory, &invoice.CreatorName, &invoice.CreatorEmail); err != nil {
			return nil, err
		}
		invoices = append(invoices, invoice)
	}
	return invoices, rows.Err()
}

// MerchantOwnerByRegistrationID returns the user who submitted a merchant registration.
func (s *Store) MerchantOwnerByRegistrationID(ctx context.Context, registrationID uuid.UUID) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id,u.whatsapp_number,u.display_name,u.email
		FROM merchant_registrations mr
		JOIN users u ON u.id=mr.user_id
		WHERE mr.id=$1`, registrationID).Scan(&u.ID, &u.WhatsAppNumber, &u.DisplayName, &u.Email)
	return u, err
}

// MerchantOwnerByInvoiceID returns the merchant owner who owns the invoice's merchant.
func (s *Store) MerchantOwnerByInvoiceID(ctx context.Context, invoiceID uuid.UUID) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id,u.whatsapp_number,u.display_name,u.email
		FROM invoices i
		JOIN merchant_owners mo ON mo.merchant_id=i.merchant_id
		JOIN users u ON u.id=mo.user_id
		WHERE i.id=$1
		ORDER BY mo.created_at ASC
		LIMIT 1`, invoiceID).Scan(&u.ID, &u.WhatsAppNumber, &u.DisplayName, &u.Email)
	return u, err
}

// CreateInvoicePayment links a newly created payment attempt to an invoice.
func (s *Store) CreateInvoicePayment(ctx context.Context, invoiceID, paymentID, payerUserID uuid.UUID, amountKobo int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO invoice_payments(invoice_id,payment_id,payer_user_id,amount_kobo,status)
		VALUES($1,$2,$3,$4,'pending')
		ON CONFLICT(payment_id) DO NOTHING`, invoiceID, paymentID, payerUserID, amountKobo)
	return err
}

// CountInvoicePayments returns the number of payment attempts linked to an invoice.
func (s *Store) CountInvoicePayments(ctx context.Context, invoiceID uuid.UUID) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM invoice_payments WHERE invoice_id=$1`, invoiceID).Scan(&count)
	return count, err
}

// ApplyInvoicePaymentSuccess records a successful contribution and marks the
// invoice paid only when cumulative successful payments cover the invoice total.
func (s *Store) ApplyInvoicePaymentSuccess(ctx context.Context, paymentID uuid.UUID) (InvoiceView, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InvoiceView{}, false, err
	}
	defer tx.Rollback(ctx)
	var invoiceID uuid.UUID
	var paidKobo int64
	var currency string
	var invoiceMerchantID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT ip.invoice_id, ip.amount_kobo, COALESCE(p.currency,'NGN'), i.merchant_id
		FROM invoice_payments ip
		JOIN payments p ON p.id=ip.payment_id
		JOIN invoices i ON i.id=ip.invoice_id
		WHERE ip.payment_id=$1
		FOR UPDATE OF ip`, paymentID).Scan(&invoiceID, &paidKobo, &currency, &invoiceMerchantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvoiceView{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return InvoiceView{}, false, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE invoice_payments
		SET status='succeeded',updated_at=now()
		WHERE payment_id=$1 AND status <> 'succeeded'`, paymentID)
	if err != nil {
		return InvoiceView{}, false, err
	}
	// C16: allocate the customer float to the merchant payable once a payment
	// is confirmed against an invoice. Gated on the status change so a retried
	// hook cannot post the allocation twice. Reversing the payment journal
	// reference unwinds both the money-in and this allocation.
	if tag.RowsAffected() == 1 {
		if err := s.postLedgerPair(ctx, tx, paymentID.String(), "invoice_payment", paymentID.String(),
			LedgerAccountCustomerFloat, LedgerAccountMerchantPayable, currency, "Invoice payment allocation", "system", paidKobo, &invoiceMerchantID); err != nil {
			return InvoiceView{}, false, err
		}
	}
	var total, paid int64
	if err := tx.QueryRow(ctx, `
		SELECT i.total_kobo,
		       COALESCE((SELECT SUM(ip.amount_kobo) FROM invoice_payments ip WHERE ip.invoice_id=i.id AND ip.status='succeeded'),0)
		FROM invoices i
		WHERE i.id=$1
		FOR UPDATE`, invoiceID).Scan(&total, &paid); err != nil {
		return InvoiceView{}, false, err
	}
	status := "partially_paid"
	paidClause := ""
	if paid >= total {
		status = "paid"
		paidClause = ", paid_at=COALESCE(paid_at, now())"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE invoices
		SET amount_paid_kobo=$2,status=$3,updated_at=now()`+paidClause+`
		WHERE id=$1`, invoiceID, paid, status); err != nil {
		return InvoiceView{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvoiceView{}, false, err
	}
	invoice, err := s.InvoiceByID(ctx, invoiceID)
	return invoice, true, err
}

// InvoiceByID returns one invoice by primary key.
func (s *Store) InvoiceByID(ctx context.Context, id uuid.UUID) (InvoiceView, error) {
	var reference string
	if err := s.pool.QueryRow(ctx, `SELECT reference FROM invoices WHERE id=$1`, id).Scan(&reference); err != nil {
		return InvoiceView{}, err
	}
	return s.InvoiceByReference(ctx, reference)
}

// LatestSuccessPaymentForInvoice returns the most recent succeeded payment for
// an invoice, so a fully paid public page can link straight to its receipt.
func (s *Store) LatestSuccessPaymentForInvoice(ctx context.Context, invoiceID uuid.UUID) (PaymentView, error) {
	var paymentID uuid.UUID
	if err := s.pool.QueryRow(ctx, `
		SELECT ip.payment_id
		FROM invoice_payments ip
		JOIN payments p ON p.id=ip.payment_id
		WHERE ip.invoice_id=$1 AND p.status='succeeded'
		ORDER BY p.updated_at DESC, p.id DESC
		LIMIT 1`, invoiceID).Scan(&paymentID); err != nil {
		return PaymentView{}, err
	}
	return s.PaymentByID(ctx, paymentID)
}

// InvoiceByPaymentID resolves invoice context for a receipt contribution.
func (s *Store) InvoiceByPaymentID(ctx context.Context, paymentID uuid.UUID) (InvoiceView, error) {
	var reference string
	err := s.pool.QueryRow(ctx, `
		SELECT i.reference
		FROM invoice_payments ip
		JOIN invoices i ON i.id=ip.invoice_id
		WHERE ip.payment_id=$1`, paymentID).Scan(&reference)
	if err != nil {
		return InvoiceView{}, err
	}
	return s.InvoiceByReference(ctx, reference)
}

// InvoicesByMerchantID returns all invoices for a merchant, most recent first.
func (s *Store) InvoicesByMerchantID(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]InvoiceView, error) {
	offset, limit = normalizePageBounds(offset, limit)
	rows, err := s.pool.Query(ctx, `
		SELECT i.id,i.merchant_id,i.created_by_user_id,i.customer_whatsapp_number,i.customer_email,
		       i.reference,i.status,i.delivery_fee_kobo,i.subtotal_kobo,i.total_kobo,i.amount_paid_kobo,
		       i.due_at,i.created_at,i.updated_at,i.paid_at,
		       m.name,m.slug,m.category,u.display_name,u.email
		FROM invoices i
		JOIN merchants m ON m.id=i.merchant_id
		JOIN users u ON u.id=i.created_by_user_id
		WHERE i.merchant_id=$1
		ORDER BY i.created_at DESC
		LIMIT $2 OFFSET $3`, merchantID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invoices []InvoiceView
	for rows.Next() {
		var invoice InvoiceView
		if err := rows.Scan(&invoice.ID, &invoice.MerchantID, &invoice.CreatedByUserID, &invoice.CustomerWhatsAppNumber,
			&invoice.CustomerEmail, &invoice.Reference, &invoice.Status, &invoice.DeliveryFeeKobo,
			&invoice.SubtotalKobo, &invoice.TotalKobo, &invoice.AmountPaidKobo, &invoice.DueAt,
			&invoice.CreatedAt, &invoice.UpdatedAt, &invoice.PaidAt, &invoice.MerchantName,
			&invoice.MerchantSlug, &invoice.MerchantCategory, &invoice.CreatorName, &invoice.CreatorEmail); err != nil {
			return nil, err
		}
		invoices = append(invoices, invoice)
	}
	return invoices, rows.Err()
}
