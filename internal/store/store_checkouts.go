// General "request money" payment links for individuals. A checkout is minted
// with no customer bound; the payer is resolved when someone opens the hosted
// link page, which then creates a payment against the payee merchant.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
)

// CheckoutSpec contains validated checkout details collected from the Partner API.
type CheckoutSpec struct {
	PayeeMerchantID uuid.UUID
	Reference       string
	Note            string
	AmountKobo      int64
	Currency        string
	RedirectURL     string
	ExpiresAt       *time.Time
}

// CheckoutView joins a checkout with the payee merchant display fields.
type CheckoutView struct {
	ID              uuid.UUID
	PayeeMerchantID uuid.UUID
	Token           string
	Reference       string
	Note            string
	AmountKobo      int64
	Currency        string
	Status          string
	PaymentID       uuid.NullUUID
	RedirectURL     string
	ExpiresAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	PayeeName       string
	PayeeSlug       string
	PayeeCategory   string
	PaymentStatus   string
}

// CreateCheckout mints a request-money link. When a reference is supplied the
// (payee, reference) pair is unique; replaying it returns the existing checkout.
func (s *Store) CreateCheckout(ctx context.Context, spec CheckoutSpec) (CheckoutView, error) {
	if spec.Currency == "" {
		spec.Currency = "NGN"
	}
	if spec.Reference != "" {
		if view, err := s.CheckoutByPayeeAndReference(ctx, spec.PayeeMerchantID, spec.Reference); err == nil {
			return view, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return CheckoutView{}, err
		}
	}
	token, err := domain.NewCheckoutToken()
	if err != nil {
		return CheckoutView{}, err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO checkouts (payee_merchant_id, token, reference, note, amount_kobo, currency, redirect_url, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		spec.PayeeMerchantID, token, spec.Reference, spec.Note,
		spec.AmountKobo, spec.Currency, spec.RedirectURL, spec.ExpiresAt)
	if err != nil {
		return CheckoutView{}, err
	}
	if spec.Reference != "" {
		return s.CheckoutByPayeeAndReference(ctx, spec.PayeeMerchantID, spec.Reference)
	}
	return s.checkoutByToken(ctx, token)
}

func (s *Store) checkoutByToken(ctx context.Context, token string) (CheckoutView, error) {
	return s.checkoutWhere(ctx, `c.token = $1`, token)
}

// CheckoutByToken resolves a checkout capability by its opaque token.
func (s *Store) CheckoutByToken(ctx context.Context, token string) (CheckoutView, error) {
	return s.checkoutByToken(ctx, token)
}

// CheckoutByPayeeAndReference resolves a checkout by the payee's reference.
func (s *Store) CheckoutByPayeeAndReference(ctx context.Context, payeeID uuid.UUID, reference string) (CheckoutView, error) {
	return s.checkoutWhere(ctx, `c.payee_merchant_id = $1 AND c.reference = $2`, payeeID, reference)
}

func (s *Store) checkoutWhere(ctx context.Context, where string, args ...any) (CheckoutView, error) {
	var view CheckoutView
	var paymentID *uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT c.id, c.payee_merchant_id, c.token, c.reference, c.note, c.amount_kobo, c.currency,
		       c.status, c.payment_id, c.redirect_url, c.expires_at, c.created_at, c.updated_at,
		       m.name, m.slug, m.category, COALESCE(p.status, '')
		FROM checkouts c
		JOIN merchants m ON m.id = c.payee_merchant_id
		LEFT JOIN payments p ON p.id = c.payment_id
		WHERE `+where, args...).Scan(
		&view.ID, &view.PayeeMerchantID, &view.Token, &view.Reference, &view.Note, &view.AmountKobo, &view.Currency,
		&view.Status, &paymentID, &view.RedirectURL, &view.ExpiresAt, &view.CreatedAt, &view.UpdatedAt,
		&view.PayeeName, &view.PayeeSlug, &view.PayeeCategory, &view.PaymentStatus)
	if err != nil {
		return CheckoutView{}, err
	}
	if paymentID != nil {
		view.PaymentID = uuid.NullUUID{UUID: *paymentID, Valid: true}
	}
	return view, nil
}

// LinkCheckoutPayment attaches the payment created when a payer resolved the link.
func (s *Store) LinkCheckoutPayment(ctx context.Context, checkoutID, paymentID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE checkouts SET payment_id = $2, updated_at = now() WHERE id = $1`, checkoutID, paymentID)
	return err
}

// MarkCheckoutsPaidByPayment marks every checkout attached to a succeeded payment.
func (s *Store) MarkCheckoutsPaidByPayment(ctx context.Context, paymentID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE checkouts SET status = 'paid', updated_at = now()
		WHERE payment_id = $1 AND status <> 'paid'`, paymentID)
	return err
}

// ExpireCheckouts moves due, unpaid checkouts to 'expired'.
func (s *Store) ExpireCheckouts(ctx context.Context, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE checkouts SET status = 'expired', updated_at = now()
		WHERE status = 'open' AND expires_at IS NOT NULL AND expires_at <= $1`, now)
	return err
}
