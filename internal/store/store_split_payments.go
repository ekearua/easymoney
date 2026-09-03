package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// SplitSpec describes one leg of a payment split to record.
type SplitSpec struct {
	SplitType  string
	Account    string
	AmountKobo int64
	Currency   string
	Description string
}

// ApplyPaymentSplits records split rows and posts double-entry ledger pairs
// for each leg. It is idempotent: if splits already exist for the payment,
// it returns immediately.
func (s *Store) ApplyPaymentSplits(ctx context.Context, paymentID uuid.UUID, merchantID uuid.UUID, splits []SplitSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM payment_splits WHERE payment_id=$1`, paymentID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return tx.Commit(ctx)
	}

	for _, sp := range splits {
		if sp.AmountKobo <= 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_splits(payment_id, split_type, account, amount_kobo, currency, description)
			VALUES($1, $2, $3, $4, $5, $6)`,
			paymentID, sp.SplitType, sp.Account, sp.AmountKobo, sp.Currency, sp.Description); err != nil {
			return err
		}
		// MerchantPayable is the only merchant-scoped account in the split.
		var tag *uuid.UUID
		if sp.Account == LedgerAccountMerchantPayable {
			tag = &merchantID
		}
		if err := s.postLedgerPair(ctx, tx, paymentID.String(), "payment_split", paymentID.String(),
			LedgerAccountCustomerFloat, sp.Account, sp.Currency,
			sp.Description, "system", sp.AmountKobo, tag); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// IsInvoiceThriftOrData reports whether the payment is linked to an invoice,
// thrift contribution, or data order. These payments have different ledger
// flows and skip collection splits.
func (s *Store) IsInvoiceThriftOrData(ctx context.Context, paymentID uuid.UUID) (bool, error) {
	var isInvoice, isThrift, isData bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM invoice_payments WHERE payment_id=$1),
		       EXISTS(SELECT 1 FROM thrift_contributions WHERE payment_id=$1),
		       EXISTS(SELECT 1 FROM data_orders WHERE payment_id=$1)`, paymentID).
		Scan(&isInvoice, &isThrift, &isData)
	return isInvoice || isThrift || isData, err
}

// RecordIndividualPaySplits books the money-in side of an individual bank
// transfer so the legs reconcile with the total the sender paid:
// totalPay = collection fee + NIP fee + payout to the recipient. It debits the
// customer float and credits each split leg, keyed by the provided reference.
func (s *Store) RecordIndividualPaySplits(ctx context.Context, ref string, splits []SplitSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, sp := range splits {
		if sp.AmountKobo <= 0 {
			continue
		}
		if err := s.postLedgerPair(ctx, tx, ref, "individual_pay", ref,
			LedgerAccountCustomerFloat, sp.Account, sp.Currency,
			sp.Description, "system", sp.AmountKobo, nil); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UserPayoutDestination is a saved bank account for sending money to a user.
type UserPayoutDestination struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	BankCode      string
	BankName      string
	AccountNumber string
	AccountName   string
	IsDefault     bool
}

// GetOrCreateUserPayoutDestination returns the user's default payout
// destination or creates one if none exist.
func (s *Store) GetOrCreateUserPayoutDestination(ctx context.Context, userID uuid.UUID, bankCode, bankName, accountNumber, accountName string) (UserPayoutDestination, error) {
	var dest UserPayoutDestination
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, bank_code, bank_name, account_number, account_name, is_default
		FROM user_payout_destinations WHERE user_id=$1 AND bank_code=$2 AND account_number=$3`,
		userID, bankCode, accountNumber).Scan(
		&dest.ID, &dest.UserID, &dest.BankCode, &dest.BankName, &dest.AccountNumber, &dest.AccountName, &dest.IsDefault)
	if err == nil {
		return dest, nil
	}
	if err != nil {
		// pgx.ErrNoRows → create
		var insertErr error
		insertErr = s.pool.QueryRow(ctx, `
			INSERT INTO user_payout_destinations(user_id, bank_code, bank_name, account_number, account_name)
			VALUES($1,$2,$3,$4,$5)
			RETURNING id, user_id, bank_code, bank_name, account_number, account_name, is_default`,
			userID, bankCode, bankName, accountNumber, accountName).Scan(
			&dest.ID, &dest.UserID, &dest.BankCode, &dest.BankName, &dest.AccountNumber, &dest.AccountName, &dest.IsDefault)
		return dest, insertErr
	}
	return dest, err
}

// UserPayoutDestinations returns all saved payout destinations for a user.
func (s *Store) UserPayoutDestinations(ctx context.Context, userID uuid.UUID) ([]UserPayoutDestination, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, bank_code, bank_name, account_number, account_name, is_default
		FROM user_payout_destinations WHERE user_id=$1 ORDER BY is_default DESC, created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dests []UserPayoutDestination
	for rows.Next() {
		var d UserPayoutDestination
		if err := rows.Scan(&d.ID, &d.UserID, &d.BankCode, &d.BankName, &d.AccountNumber, &d.AccountName, &d.IsDefault); err != nil {
			return nil, err
		}
		dests = append(dests, d)
	}
	return dests, rows.Err()
}

// RecordPayout records an individual payout ledger entry (OperatingBank → UserPayable)
// and returns a reference for tracking.
func (s *Store) RecordPayout(ctx context.Context, userID uuid.UUID, amountKobo int64, dest UserPayoutDestination, description string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	ref := fmt.Sprintf("payout-%s", uuid.New().String())
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries(journal_ref, source_type, source_id, entry_type, account,
			amount_kobo, currency, description, posted_by, merchant_id, prev_hash, hash)
		VALUES($1,'payout',$2,'debit',$3,$4,'NGN',$5,'system',NULL,'','')`,
		ref, ref, LedgerAccountUserPayable, amountKobo, description); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries(journal_ref, source_type, source_id, entry_type, account,
			amount_kobo, currency, description, posted_by, merchant_id, prev_hash, hash)
		VALUES($1,'payout',$2,'credit',$3,$4,'NGN',$5,'system',NULL,'','')`,
		ref, ref, LedgerAccountOperatingBank, amountKobo, description); err != nil {
		return "", err
	}

	return ref, tx.Commit(ctx)
}
