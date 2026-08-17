package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MerchantAPIKeyView is a stored Partner API credential joined with its
// merchant so the auth middleware can enforce merchant-level status.
type MerchantAPIKeyView struct {
	ID             uuid.UUID
	MerchantID     uuid.UUID
	MerchantName   string
	MerchantActive bool
	Name           string
	Prefix         string
	Enabled        bool
	LastUsedAt     *time.Time
	CreatedAt      time.Time
	RevokedAt      *time.Time
}

// CreateMerchantAPIKey persists a hashed Partner API credential. The plaintext
// key is shown to the merchant exactly once at creation time.
func (s *Store) CreateMerchantAPIKey(ctx context.Context, merchantID uuid.UUID, name string, keyHash []byte, prefix string) (MerchantAPIKeyView, error) {
	var view MerchantAPIKeyView
	err := s.pool.QueryRow(ctx, `
		INSERT INTO merchant_api_keys(merchant_id,name,key_hash,prefix)
		VALUES($1,$2,$3,$4)
		RETURNING id,merchant_id,name,prefix,enabled,last_used_at,created_at,revoked_at`,
		merchantID, name, keyHash, prefix).Scan(
		&view.ID, &view.MerchantID, &view.Name, &view.Prefix, &view.Enabled, &view.LastUsedAt, &view.CreatedAt, &view.RevokedAt)
	return view, err
}

// MerchantAPIKeyByKeyHash resolves a credential by its SHA-256 hash, joining the
// owning merchant for active-status enforcement.
func (s *Store) MerchantAPIKeyByKeyHash(ctx context.Context, keyHash []byte) (MerchantAPIKeyView, error) {
	var view MerchantAPIKeyView
	err := s.pool.QueryRow(ctx, `
		SELECT k.id,k.merchant_id,m.name,m.active,k.name,k.prefix,k.enabled,k.last_used_at,k.created_at,k.revoked_at
		FROM merchant_api_keys k
		JOIN merchants m ON m.id=k.merchant_id
		WHERE k.key_hash=$1`, keyHash).Scan(
		&view.ID, &view.MerchantID, &view.MerchantName, &view.MerchantActive,
		&view.Name, &view.Prefix, &view.Enabled, &view.LastUsedAt, &view.CreatedAt, &view.RevokedAt)
	return view, err
}

// ListMerchantAPIKeys returns the merchant's credentials, newest first. The
// hash is never returned.
func (s *Store) ListMerchantAPIKeys(ctx context.Context, merchantID uuid.UUID) ([]MerchantAPIKeyView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,merchant_id,name,prefix,enabled,last_used_at,created_at,revoked_at
		FROM merchant_api_keys
		WHERE merchant_id=$1
		ORDER BY created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []MerchantAPIKeyView
	for rows.Next() {
		var view MerchantAPIKeyView
		if err := rows.Scan(
			&view.ID, &view.MerchantID, &view.Name, &view.Prefix, &view.Enabled, &view.LastUsedAt, &view.CreatedAt, &view.RevokedAt); err != nil {
			return nil, err
		}
		keys = append(keys, view)
	}
	return keys, rows.Err()
}

// RevokeMerchantAPIKey disables a credential owned by the merchant.
func (s *Store) RevokeMerchantAPIKey(ctx context.Context, keyID, merchantID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE merchant_api_keys
		SET enabled=false,revoked_at=now()
		WHERE id=$1 AND merchant_id=$2 AND revoked_at IS NULL`, keyID, merchantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("api key not found")
	}
	return nil
}

// TouchMerchantAPIKey records the credential's most recent successful use.
func (s *Store) TouchMerchantAPIKey(ctx context.Context, keyID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE merchant_api_keys SET last_used_at=now() WHERE id=$1`, keyID)
	return err
}

// PaymentByMerchantReference resolves a payment by the merchant's own
// idempotency reference, scoped to the merchant for multi-tenant safety.
func (s *Store) PaymentByMerchantReference(ctx context.Context, merchantID uuid.UUID, reference string) (PaymentView, error) {
	return s.paymentBy(ctx, "p.merchant_id=$1 AND p.merchant_reference=$2", merchantID, reference)
}

// PaymentByIdempotencyKey returns an existing payment for a merchant+idempotency key pair.
func (s *Store) PaymentByIdempotencyKey(ctx context.Context, merchantID uuid.UUID, idempotencyKey string) (PaymentView, error) {
	if idempotencyKey == "" {
		return PaymentView{}, pgx.ErrNoRows
	}
	return s.paymentBy(ctx, "p.merchant_id=$1 AND p.idempotency_key=$2", merchantID, idempotencyKey)
}

// SetPaymentInitiationMeta stamps the merchant idempotency reference and the
// echoed request metadata onto an already-created payment. Both columns are
// NOT NULL, so a nil/empty metadata input is stored as an empty object.
func (s *Store) SetPaymentInitiationMeta(ctx context.Context, paymentID uuid.UUID, reference string, idempotencyKey string, metadata []byte) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE payments
		SET merchant_reference=$2,idempotency_key=COALESCE(NULLIF($3,''),merchant_reference),metadata=$4::jsonb,updated_at=now()
		WHERE id=$1`, paymentID, reference, idempotencyKey, metadataValue(metadata))
	if err != nil {
		return fmt.Errorf("set payment initiation metadata: %w", err)
	}
	return nil
}
