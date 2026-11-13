package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/redact"
)

const (
	InterswitchWalletOperationDebit   = "debit"
	InterswitchWalletOperationReverse = "reversal"
	InterswitchWalletOperationDispatching = "dispatching"
	InterswitchWalletOperationSucceeded   = "succeeded"
	InterswitchWalletOperationFailed      = "failed"
	InterswitchWalletOperationUnknown     = "unknown"
)

// InterswitchWalletOperation is a durable record of an external wallet debit
// or reversal. Dispatching/unknown outcomes must be reconciled, never resent.
type InterswitchWalletOperation struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	WalletID          string
	OperationType     string
	Reference         string
	OriginalReference string
	AmountKobo        int64
	CurrencyCode      string
	Status            string
	ResponseCode      string
	LastError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
}

// BeginInterswitchWalletOperation inserts one write-ahead operation record.
// started is true only for the caller that created the reference; all retries
// must reconcile the existing reference instead of dispatching it again.
func (s *Store) BeginInterswitchWalletOperation(ctx context.Context, operation InterswitchWalletOperation) (InterswitchWalletOperation, bool, error) {
	if operation.UserID == uuid.Nil || strings.TrimSpace(operation.WalletID) == "" ||
		strings.TrimSpace(operation.Reference) == "" || operation.AmountKobo <= 0 {
		return InterswitchWalletOperation{}, false, errors.New("store: invalid Interswitch wallet operation")
	}
	if operation.OperationType != InterswitchWalletOperationDebit && operation.OperationType != InterswitchWalletOperationReverse {
		return InterswitchWalletOperation{}, false, errors.New("store: unsupported Interswitch wallet operation type")
	}
	if operation.OperationType == InterswitchWalletOperationReverse && strings.TrimSpace(operation.OriginalReference) == "" {
		return InterswitchWalletOperation{}, false, errors.New("store: wallet reversal requires the original reference")
	}
	if operation.CurrencyCode == "" {
		operation.CurrencyCode = "566"
	}
	if operation.CurrencyCode != "566" {
		return InterswitchWalletOperation{}, false, errors.New("store: Interswitch customer wallets must use NGN")
	}
	operation.WalletID = strings.TrimSpace(operation.WalletID)
	operation.Reference = strings.TrimSpace(operation.Reference)
	operation.OriginalReference = strings.TrimSpace(operation.OriginalReference)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InterswitchWalletOperation{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO interswitch_wallet_operations
			(user_id,wallet_id,operation_type,reference,original_reference,amount_kobo,currency_code,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,'dispatching')
		ON CONFLICT(reference) DO NOTHING`,
		operation.UserID, operation.WalletID, operation.OperationType, operation.Reference,
		operation.OriginalReference, operation.AmountKobo, operation.CurrencyCode)
	if err != nil {
		return InterswitchWalletOperation{}, false, err
	}
	started := tag.RowsAffected() == 1
	stored, err := scanInterswitchWalletOperation(tx.QueryRow(ctx, `
		SELECT id,user_id,wallet_id,operation_type,reference,original_reference,amount_kobo,currency_code,
		       status,response_code,last_error,created_at,updated_at,completed_at
		FROM interswitch_wallet_operations WHERE reference=$1 FOR UPDATE`, operation.Reference))
	if err != nil {
		return InterswitchWalletOperation{}, false, err
	}
	if stored.UserID != operation.UserID || stored.WalletID != operation.WalletID ||
		stored.OperationType != operation.OperationType || stored.OriginalReference != operation.OriginalReference ||
		stored.AmountKobo != operation.AmountKobo || stored.CurrencyCode != operation.CurrencyCode {
		return InterswitchWalletOperation{}, false, errors.New("store: Interswitch wallet reference is already bound to a different operation")
	}
	if err := tx.Commit(ctx); err != nil {
		return InterswitchWalletOperation{}, false, err
	}
	return stored, started, nil
}

// InterswitchWalletOperationByReference returns the durable operation keyed by
// the exact reference sent to Interswitch.
func (s *Store) InterswitchWalletOperationByReference(ctx context.Context, reference string) (InterswitchWalletOperation, error) {
	return scanInterswitchWalletOperation(s.pool.QueryRow(ctx, `
		SELECT id,user_id,wallet_id,operation_type,reference,original_reference,amount_kobo,currency_code,
		       status,response_code,last_error,created_at,updated_at,completed_at
		FROM interswitch_wallet_operations WHERE reference=$1`, strings.TrimSpace(reference)))
}

// PendingInterswitchWalletOperations returns the oldest unresolved external
// wallet operations for reconciliation. Callers must confirm outcomes with the
// provider before changing state or mirroring ledger effects.
func (s *Store) PendingInterswitchWalletOperations(ctx context.Context, limit int) ([]InterswitchWalletOperation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,user_id,wallet_id,operation_type,reference,original_reference,amount_kobo,currency_code,
		       status,response_code,last_error,created_at,updated_at,completed_at
		FROM interswitch_wallet_operations
		WHERE status IN ('dispatching','unknown')
		ORDER BY updated_at,id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var operations []InterswitchWalletOperation
	for rows.Next() {
		operation, err := scanInterswitchWalletOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

// SetInterswitchWalletOperationOutcome records a provider-confirmed terminal
// outcome or a sanitized reconciliation error without storing provider bodies.
func (s *Store) SetInterswitchWalletOperationOutcome(ctx context.Context, reference, status, responseCode, message string) error {
	if status != InterswitchWalletOperationSucceeded && status != InterswitchWalletOperationFailed && status != InterswitchWalletOperationUnknown {
		return errors.New("store: invalid Interswitch wallet operation outcome")
	}
	completed := status != InterswitchWalletOperationUnknown
	tag, err := s.pool.Exec(ctx, `
		UPDATE interswitch_wallet_operations
		SET status=$2,response_code=$3,last_error=$4,updated_at=now(),
		    completed_at=CASE WHEN $5 THEN now() ELSE NULL END
		WHERE reference=$1 AND status IN ('dispatching','unknown')`,
		strings.TrimSpace(reference), status, strings.TrimSpace(responseCode), redact.Error(message, redact.DefaultMaxLen), completed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		operation, err := s.InterswitchWalletOperationByReference(ctx, reference)
		if err != nil {
			return err
		}
		if operation.Status == status {
			return nil
		}
		return errors.New("store: Interswitch wallet operation is no longer open")
	}
	return nil
}

func scanInterswitchWalletOperation(row pgx.Row) (InterswitchWalletOperation, error) {
	var operation InterswitchWalletOperation
	err := row.Scan(
		&operation.ID, &operation.UserID, &operation.WalletID, &operation.OperationType,
		&operation.Reference, &operation.OriginalReference, &operation.AmountKobo, &operation.CurrencyCode,
		&operation.Status, &operation.ResponseCode, &operation.LastError,
		&operation.CreatedAt, &operation.UpdatedAt, &operation.CompletedAt,
	)
	return operation, err
}
