package app

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func (a *App) customerWalletOpsEnabled() bool {
	return a != nil && a.cfg.InterswitchWalletsEnabled && a.interswitch != nil && strings.TrimSpace(a.cfg.InterswitchMerchantCode) != ""
}

func (a *App) reconcilePendingWalletOperations(ctx context.Context) {
	if !a.customerWalletOpsEnabled() || a.store == nil {
		return
	}
	operations, err := a.store.PendingInterswitchWalletOperations(ctx, 100)
	if err != nil {
		a.logger.WarnContext(ctx, "interswitch wallet operation reconciliation list failed", "error", err)
		return
	}
	for _, operation := range operations {
		if err := a.reconcileWalletOperation(ctx, operation); err != nil {
			a.logger.WarnContext(ctx, "interswitch wallet operation reconciliation failed", "reference", operation.Reference, "type", operation.OperationType, "error", err)
		}
	}
}

func (a *App) reconcileWalletOperation(ctx context.Context, operation store.InterswitchWalletOperation) error {
	tx, err := a.interswitch.CustomerWalletTransaction(ctx, a.cfg.InterswitchMerchantCode, operation.Reference)
	if err != nil {
		// Not visible yet or lookup unavailable: keep pending/unknown for a later
		// conservative reconciliation pass.
		return nil
	}
	switch operation.OperationType {
	case store.InterswitchWalletOperationDebit:
		return a.reconcileWalletDebit(ctx, operation, tx)
	case store.InterswitchWalletOperationReverse:
		return a.reconcileWalletReversal(ctx, operation, tx)
	default:
		return nil
	}
}

func providerWalletTxConfirmed(tx ports.CustomerWalletTransaction, operation store.InterswitchWalletOperation, direction string) bool {
	if tx.Reversed {
		return false
	}
	if !strings.EqualFold(tx.Direction, direction) {
		return false
	}
	if tx.AmountKobo != operation.AmountKobo {
		return false
	}
	if tx.CurrencyCode != "" && tx.CurrencyCode != "566" {
		return false
	}
	if tx.ResponseCode != "" && tx.ResponseCode != "00" {
		return false
	}
	if tx.WalletRecordID != 0 {
		// Best-effort binding when the provider returns the wallet record id.
		// We don't have the expected record id on every operation yet, so only
		// reject impossible mismatches when the caller has set one via metadata in
		// future; for now this is a placeholder for stricter binding.
		_ = tx.WalletRecordID
	}
	return true
}

func (a *App) reconcileWalletDebit(ctx context.Context, operation store.InterswitchWalletOperation, tx ports.CustomerWalletTransaction) error {
	payment, err := a.store.PaymentByReference(ctx, operation.Reference)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	// Conservative: only act on explicit, matching confirmations.
	if providerWalletTxConfirmed(tx, operation, "DEBIT") {
		// Mark the external debit as confirmed, then finalize the payment mirror.
		if err := a.store.SetInterswitchWalletOperationOutcome(ctx, operation.Reference, store.InterswitchWalletOperationSucceeded, tx.ResponseCode, ""); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "no longer open") {
				return err
			}
		}
		if err != nil || payment.ID == (store.PaymentView{}).ID || payment.Provider != "wallet" {
			return nil
		}
		// Ensure allowance reservation exists for the wallet portion when applicable.
		walletPortion := payment.AmountKobo - payment.PurseAmountKobo
		if walletPortion < 0 {
			walletPortion = payment.AmountKobo
		}
		if walletPortion > 0 {
			profile, profileErr := a.store.KYCProfileByUser(ctx, payment.UserID)
			if errors.Is(profileErr, pgx.ErrNoRows) {
				profile, profileErr = a.store.EnsureKYCProfile(ctx, payment.UserID)
			}
			if profileErr == nil {
				_ = a.store.ReserveAllowance(ctx, store.AllowanceReservation{
					AccountType: store.AccountIndividual, SubjectID: payment.UserID, Direction: kyc.DirOut,
					Tier: profile.Tier, AmountKobo: walletPortion, Ref: operation.Reference,
					RiskBand: profile.RiskBand, RiskHighSingleLimitKobo: a.cfg.RiskHighSingleLimitKobo,
				})
			}
		}
		changed, terr := a.store.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "wallet_reconciled",
			map[string]any{"wallet_payment": true, "provider_wallet_debit_confirmed": true, "reconciled_reference": operation.Reference}, a.payments.ResultOutbox(payment, domain.StatusSucceeded))
		if terr != nil {
			return terr
		}
		updated, uerr := a.store.PaymentByID(ctx, payment.ID)
		if uerr != nil {
			return uerr
		}
		if changed && a.payments != nil {
			a.payments.OnPaymentSucceeded(ctx, updated)
		}
		return nil
	}

	// Definitive failure is only accepted when the provider lookup returns a
	// matching, non-reversed record that explicitly indicates non-success.
	if tx.Reversed || !strings.EqualFold(tx.Direction, "DEBIT") || tx.AmountKobo != operation.AmountKobo {
		return nil
	}
	if tx.CurrencyCode != "" && tx.CurrencyCode != "566" {
		return nil
	}
	if tx.ResponseCode == "" || tx.ResponseCode == "00" {
		return nil
	}
	if err := a.store.SetInterswitchWalletOperationOutcome(ctx, operation.Reference, store.InterswitchWalletOperationFailed, tx.ResponseCode, "provider confirmed debit failure"); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "no longer open") {
			return err
		}
	}
	if err == nil || payment.ID != (store.PaymentView{}).ID {
		// Release any allowance reservation tied to this payment attempt.
		_ = a.store.ReleaseAllowance(ctx, operation.Reference)
	}
	return nil
}

func (a *App) reconcileWalletReversal(ctx context.Context, operation store.InterswitchWalletOperation, tx ports.CustomerWalletTransaction) error {
	// Conservative: only mark reversal confirmed when lookup explicitly confirms a
	// reversal/credit-like compensating movement or the provider exposes a clear
	// reversed indicator for this reference. Until reversal semantics are fully
	// verified, keep pending.
	if tx.ResponseCode != "" && tx.ResponseCode != "00" {
		return nil
	}
	// If the lookup returns the reversal reference as a credit/reversal movement,
	// accept it; otherwise leave pending.
	if strings.EqualFold(tx.Direction, "CREDIT") || tx.Reversed {
		return a.store.SetInterswitchWalletOperationOutcome(ctx, operation.Reference, store.InterswitchWalletOperationSucceeded, tx.ResponseCode, "reversal confirmed")
	}
	return nil
}

var _ = errors.New
