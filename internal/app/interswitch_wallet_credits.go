package app

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/store"
)

func (a *App) standingCreditsRequireProviderVerification() bool {
	return a != nil && a.cfg.InterswitchWalletsEnabled && a.interswitch != nil && strings.TrimSpace(a.cfg.InterswitchMerchantCode) != ""
}

// applyStandingCreditMirror records the bank-transfer credit as pending and, in
// provider-wallet mode, only mirrors it into the local ledger after Interswitch
// confirms the wallet transaction. When provider verification is unavailable or
// the transaction is not yet visible, the credit remains pending for worker
// reconciliation instead of being guessed into the balance.
func (a *App) applyStandingCreditMirror(ctx context.Context, account store.StandingAccount, credit interswitchStandingCredit) (bool, error) {
	if _, err := a.store.RecordStandingCredit(ctx, account.UserID, account.AccountNumber, credit.Reference, credit.AmountKobo); err != nil {
		return false, err
	}
	if !a.standingCreditsRequireProviderVerification() {
		return a.store.ApplyStandingCredit(ctx, account.UserID, credit.Reference, credit.AmountKobo, "Standing account bank transfer credit")
	}
	return a.verifyAndApplyStandingCredit(ctx, account.UserID, credit.Reference, credit.AmountKobo)
}

func (a *App) verifyAndApplyStandingCredit(ctx context.Context, userID uuid.UUID, reference string, amountKobo int64) (bool, error) {
	if !a.standingCreditsRequireProviderVerification() {
		return false, nil
	}
	if reference = strings.TrimSpace(reference); reference == "" || amountKobo <= 0 {
		return false, nil
	}
	transaction, err := a.interswitch.CustomerWalletTransaction(ctx, a.cfg.InterswitchMerchantCode, reference)
	if err != nil {
		// The webhook can arrive before the transaction lookup is visible; leave
		// the recorded credit pending so reconciliation can retry without risking
		// a duplicate mirror posting.
		return false, nil
	}
	if transaction.Reversed || !strings.EqualFold(transaction.Direction, "CREDIT") || transaction.AmountKobo != amountKobo {
		return false, nil
	}
	if transaction.CurrencyCode != "" && transaction.CurrencyCode != "566" {
		return false, errors.New("interswitch wallet credit currency is not NGN")
	}
	if transaction.ResponseCode != "" && transaction.ResponseCode != "00" {
		return false, nil
	}
	applied, err := a.store.ApplyStandingCredit(ctx, userID, reference, amountKobo, "Standing account bank transfer credit")
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (a *App) reconcilePendingStandingCredits(ctx context.Context) {
	if a == nil || a.store == nil || !a.standingCreditsRequireProviderVerification() {
		return
	}
	credits, err := a.store.PendingStandingCredits(ctx, 100)
	if err != nil {
		a.logger.WarnContext(ctx, "standing credit reconciliation list failed", "error", err)
		return
	}
	for _, credit := range credits {
		applied, err := a.verifyAndApplyStandingCredit(ctx, credit.UserID, credit.ProviderReference, credit.AmountKobo)
		if err != nil {
			a.logger.WarnContext(ctx, "standing credit reconciliation failed", "reference", credit.ProviderReference, "user", credit.UserID, "error", err)
			continue
		}
		if applied {
			a.logger.InfoContext(ctx, "standing account credit mirrored after verification", "reference", credit.ProviderReference, "user", credit.UserID, "amount_kobo", credit.AmountKobo)
			a.monitorStandingCredit(ctx, credit.UserID, credit.ProviderReference, credit.AmountKobo)
		}
	}
	_ = errors.Is(nil, pgx.ErrNoRows)
}
