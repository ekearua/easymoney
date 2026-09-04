package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// SplitSpec describes one leg of a payment split to record.
type SplitSpec struct {
	SplitType   string
	Account     string
	AmountKobo  int64
	Currency    string
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

// IndividualPaySystemMerchant returns the inactive internal merchant used only
// for individual-pay sender payment records (mirrors ThriftSystemMerchant).
func (s *Store) IndividualPaySystemMerchant(ctx context.Context) (Merchant, error) {
	var merchant Merchant
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at,
		       password_hash, allow_partial_payments, min_invoice_amount_kobo, upfront_percent,
		       min_installment_percent, max_installments, allow_full_pay_always
		FROM merchants WHERE slug='xego-individual-pay'`).Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords,
		&merchant.SortOrder, &merchant.CreatedAt,
		&merchant.PasswordHash, &merchant.AllowPartialPayments,
		&merchant.MinInvoiceAmountKobo, &merchant.UpfrontPercent,
		&merchant.MinInstallmentPercent, &merchant.MaxInstallments,
		&merchant.AllowFullPayAlways,
	)
	return merchant, err
}

// RecordIndividualPaySettlement books the money-in splits and the recipient
// payout for an individual pay in one transaction, keyed by the payment id so
// a retried post-success hook can never double-post. The sender's money-in is
// posted by the payment transition (OperatingBank → CustomerFloat); this
// allocates the customer float to the split legs (collection fee, NIP fee,
// payout liability) and discharges the payout liability to the operating bank.
// The splits must reconcile with the sender's payment amount:
// totalPay = collection fee + NIP fee + recipientGets.
func (s *Store) RecordIndividualPaySettlement(ctx context.Context, paymentID uuid.UUID, recipientGets int64, dest UserPayoutDestination, splits []SplitSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ref := fmt.Sprintf("individual-pay:%s", paymentID.String())
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ledger_entries WHERE journal_ref=$1)`, ref).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	for _, sp := range splits {
		if sp.AmountKobo <= 0 {
			continue
		}
		if err := s.postLedgerPair(ctx, tx, ref, "individual_pay", paymentID.String(),
			LedgerAccountCustomerFloat, sp.Account, sp.Currency,
			sp.Description, "system", sp.AmountKobo, nil); err != nil {
			return err
		}
	}
	if recipientGets > 0 {
		description := "Individual payout"
		if dest.AccountNumber != "" {
			description = fmt.Sprintf("Individual payout to account %s (%s)", dest.AccountNumber, dest.BankName)
		}
		// W1: the recipient's share lands in their wallet (dr 2300_user_payable
		// / cr 2301_user_wallet:<recipient>) instead of a direct external bank
		// credit; the recipient withdraws from the wallet through the payout
		// rail, which is where the money-out allowance is enforced. The wallet
		// is ensured (created pending) inside this transaction. The wallet leg
		// uses its own journal ref ("<settlement>:wallet") so its idempotency
		// check does not collide with the split legs posted under the
		// settlement ref above.
		if err := s.creditWalletTx(ctx, tx, dest.UserID, LedgerAccountUserPayable, recipientGets, ref+":wallet", description); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
