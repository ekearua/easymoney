package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/vtpass"
	"whatsapp-payment-demo/internal/store"
)

// Seed refreshes the baseline merchant fixtures.
func (a *App) Seed(ctx context.Context) error {
	return a.store.Seed(ctx)
}

// Reconcile verifies unresolved Interswitch transactions.
func (a *App) Reconcile(ctx context.Context) error {
	return a.payments.Reconcile(ctx)
}

// ReconcileThreeWay runs the C17 three-way reconciliation (internal vs ledger
// vs bank) and returns a short summary suitable for the CLI.
func (a *App) ReconcileThreeWay(ctx context.Context) error {
	run, items, err := a.store.RunReconciliation(ctx, "auto", "cli")
	if err != nil {
		return err
	}
	a.logger.InfoContext(ctx, "three-way reconciliation complete", "run_id", run.ID, "status", run.Status, "discrepancies", len(items))
	for _, it := range items {
		a.logger.WarnContext(ctx, "reconciliation discrepancy",
			"category", it.Category, "reference", it.Reference,
			"expected_kobo", it.ExpectedKobo, "actual_kobo", it.ActualKobo, "detail", it.Detail)
	}
	return nil
}

// Settle cuts a merchant's payable into a batch and dispatches its payout,
// returning a summary for the CLI. Passing a batch number that already exists
// replays the batch and its payout idempotently.
func (a *App) Settle(ctx context.Context, merchantID uuid.UUID, batchNo string) error {
	batch, err := a.settlements.Cut(ctx, merchantID, batchNo)
	if err != nil {
		return err
	}
	payout, err := a.settlements.RequestPayout(ctx, batchNo, nil)
	if err != nil {
		return err
	}
	a.logger.InfoContext(ctx, "settlement complete",
		"batch_no", batch.BatchNo, "merchant_id", batch.MerchantID.String(),
		"total_kobo", batch.TotalKobo, "status", batch.Status,
		"payout", payout.Status, "payout_id", payout.ID.String(), "external_ref", payout.ExternalRef)
	return nil
}

// Refund refunds a merchant's succeeded payment and posts the ledger reversal.
func (a *App) Refund(ctx context.Context, merchantSlug, paymentReference string) error {
	merchant, err := a.store.MerchantBySlug(ctx, merchantSlug)
	if err != nil {
		return fmt.Errorf("merchant %q: %w", merchantSlug, err)
	}
	payment, err := a.store.PaymentByMerchantReference(ctx, merchant.ID, paymentReference)
	if err != nil {
		return fmt.Errorf("payment %q: %w", paymentReference, err)
	}
	refund, err := a.refunds.Refund(ctx, payment.ID.String(), "CLI refund")
	if err != nil {
		return err
	}
	a.logger.InfoContext(ctx, "refund complete",
		"refund_id", refund.ID.String(), "payment_id", payment.ID.String(),
		"amount_kobo", refund.AmountKobo, "status", refund.Status)
	return nil
}

// WalletBalance prints an individual wallet's balance and status for the CLI.
func (a *App) WalletBalance(ctx context.Context, phone string) error {
	user, err := a.store.FindUserByPhone(ctx, phone)
	if err != nil {
		return fmt.Errorf("user %q: %w", phone, err)
	}
	wallet, err := a.store.WalletByOwner(ctx, store.WalletOwnerUser, user.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		a.logger.InfoContext(ctx, "no wallet yet for user (created on first KYC touch)", "phone", phone)
		return nil
	}
	if err != nil {
		return fmt.Errorf("wallet for %s: %w", phone, err)
	}
	balance, err := a.store.WalletBalance(ctx, wallet.ID)
	if err != nil {
		return err
	}
	a.logger.InfoContext(ctx, "wallet balance",
		"phone", phone, "user_id", user.ID.String(), "wallet_id", wallet.ID.String(),
		"status", wallet.Status, "balance_kobo", balance, "balance", domain.FormatNGN(balance))
	return nil
}

// WalletWithdraw moves money from an individual wallet to the external payout
// rail. The money-out allowance is reserved first (keyed by a fresh ref) and
// released if the withdrawal fails, mirroring the payment draft pattern.
func (a *App) WalletWithdraw(ctx context.Context, phone string, amountKobo int64) error {
	if amountKobo <= 0 {
		return errors.New("withdrawal amount must be positive")
	}
	user, err := a.store.FindUserByPhone(ctx, phone)
	if err != nil {
		return fmt.Errorf("user %q: %w", phone, err)
	}
	profile, err := a.store.KYCProfileByUser(ctx, user.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		profile, err = a.store.EnsureKYCProfile(ctx, user.ID)
	}
	if err != nil {
		return err
	}
	reservationRef := "wallet-withdraw:" + uuid.New().String()
	if err := a.store.ReserveAllowance(ctx, store.AllowanceReservation{
		AccountType: store.AccountIndividual,
		SubjectID:   user.ID,
		Direction:   kyc.DirOut,
		Tier:        profile.Tier,
		AmountKobo:  amountKobo,
		Ref:         reservationRef,
	}); err != nil {
		return err
	}
	if err := a.store.WithdrawFromUserWallet(ctx, user.ID, amountKobo, reservationRef,
		"Wallet withdrawal "+phone); err != nil {
		_ = a.store.ReleaseAllowance(ctx, reservationRef)
		return err
	}
	a.logger.InfoContext(ctx, "wallet withdrawal completed",
		"phone", phone, "user_id", user.ID.String(), "amount_kobo", amountKobo,
		"amount", domain.FormatNGN(amountKobo), "ref", reservationRef)
	return nil
}

// PurgeExpiredData enforces the configured retention period.
func (a *App) PurgeExpiredData(ctx context.Context) error {
	report, err := a.store.PurgeBefore(ctx, time.Now().Add(-a.cfg.RetentionPeriod))
	if err == nil {
		a.logger.InfoContext(ctx, "retention completed",
			"users_purged", report.UsersPurged,
			"payments_archived", report.PaymentsArchived,
			"payments_purged", report.PaymentsPurged,
			"invoices_archived", report.InvoicesArchived,
			"invoices_purged", report.InvoicesPurged,
			"thrift_purged", report.ThriftPurged,
			"operational_purged", report.OperationalPurged,
			"audit_archived", report.AuditArchived,
			"audit_purged", report.AuditPurged,
		)
	}
	return err
}

// RescreenDue re-runs sanctions/PEP screening for profiles whose last result
// is older than the configured period. A strong/blocked rescreen downgrades
// the profile below L2 and opens a manual review case.
func (a *App) RescreenDue(ctx context.Context) error {
	due, err := a.store.KYCProfilesDueForRescreen(ctx, time.Now().Add(-a.cfg.KYCRescreenPeriod), 50)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		a.logger.InfoContext(ctx, "no KYC profiles due for rescreen")
		return nil
	}
	screened, blocked := 0, 0
	for _, profile := range due {
		individual, err := a.store.IndividualProfileByUser(ctx, profile.UserID)
		if err != nil {
			a.logger.WarnContext(ctx, "rescreen skipped (no individual profile)", "user_id", profile.UserID.String())
			continue
		}
		decision, err := a.sanctionsScreener.Screen(ctx, ports.ScreeningRequest{LegalName: individual.LegalName})
		if err != nil {
			a.logger.WarnContext(ctx, "rescreen provider error", "user_id", profile.UserID.String(), "error", err)
			continue
		}
		if _, err := a.store.RecordScreeningResult(ctx, store.ScreeningResult{
			UserID: profile.UserID, Provider: "simulated", Decision: decision.Decision,
			MatchedNames: decision.MatchedNames,
		}, a.cfg.KYCRescreenPeriod); err != nil {
			a.logger.WarnContext(ctx, "rescreen record failed", "user_id", profile.UserID.String(), "error", err)
			continue
		}
		if _, err := a.store.RecomputeRiskScore(ctx, profile.UserID); err != nil {
			a.logger.WarnContext(ctx, "rescreen risk recompute failed", "user_id", profile.UserID.String(), "error", err)
		}
		screened++
		if kyc.BlockedByScreening(decision.Decision) {
			blocked++
			if _, err := a.store.DowngradeKYCTier(ctx, profile.UserID, kyc.TierL1, "rescreen "+decision.Decision, nil); err != nil {
				a.logger.WarnContext(ctx, "rescreen downgrade failed", "user_id", profile.UserID.String(), "error", err)
			}
			if _, err := a.store.CreateManualReviewCase(ctx, store.ManualReviewCase{
				UserID: profile.UserID, CaseType: "screening", Reason: "rescreen decision " + decision.Decision,
			}); err != nil {
				a.logger.WarnContext(ctx, "rescreen review case failed", "user_id", profile.UserID.String(), "error", err)
			}
		}
	}

	// Business KYB profiles are rescreened by legal entity name. A blocked
	// rescreen forces the business back to B0 and stops further advancement,
	// which immediately lowers the payout ceilings ReserveAllowance enforces.
	businessDue, err := a.store.BusinessKYBProfilesDueForRescreen(ctx, time.Now().Add(-a.cfg.KYCRescreenPeriod), 50)
	if err != nil {
		a.logger.WarnContext(ctx, "list business profiles for rescreen", "error", err)
	}
	businessScreened, businessBlocked := 0, 0
	for _, profile := range businessDue {
		decision, err := a.sanctionsScreener.Screen(ctx, ports.ScreeningRequest{LegalName: profile.MerchantName})
		if err != nil {
			a.logger.WarnContext(ctx, "business rescreen provider error", "merchant_id", profile.MerchantID.String(), "error", err)
			continue
		}
		if err := a.store.RecordKYBScreening(ctx, profile.MerchantID, decision.Decision, decision.MatchedNames, a.cfg.KYCRescreenPeriod); err != nil {
			a.logger.WarnContext(ctx, "business rescreen record failed", "merchant_id", profile.MerchantID.String(), "error", err)
			continue
		}
		businessScreened++
		if kyc.BlockedByScreening(decision.Decision) {
			businessBlocked++
			if _, err := a.store.DowngradeKYBTier(ctx, profile.MerchantID, kyc.TierB0, "rescreen "+decision.Decision, nil); err != nil {
				a.logger.WarnContext(ctx, "business rescreen downgrade failed", "merchant_id", profile.MerchantID.String(), "error", err)
			}
		}
	}
	a.logger.InfoContext(ctx, "KYC rescreen completed", "due", len(due), "screened", screened, "blocked", blocked,
		"business_due", len(businessDue), "business_screened", businessScreened, "business_blocked", businessBlocked)
	return nil
}

// RecomputeAllRisk refreshes the ML/FT risk band for every KYC profile. Used
// by the demo CLI when risk events are backfilled or thresholds change.
func (a *App) RecomputeAllRisk(ctx context.Context) error {
	profiles, err := a.store.ListKYCProfiles(ctx, 500)
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		a.logger.InfoContext(ctx, "no KYC profiles to score")
		return nil
	}
	recomputed := 0
	for _, profile := range profiles {
		if _, err := a.store.RecomputeRiskScore(ctx, profile.UserID); err != nil {
			a.logger.WarnContext(ctx, "risk recompute failed", "user_id", profile.UserID.String(), "error", err)
			continue
		}
		recomputed++
	}
	a.logger.InfoContext(ctx, "risk recompute completed", "profiles", len(profiles), "recomputed", recomputed)
	return nil
}

// MonitorTransactions runs the C14 transaction-monitoring rules over the
// configured lookback window and returns how many alerts were raised.
func (a *App) MonitorTransactions(ctx context.Context) error {
	raised, err := a.store.RunTransactionMonitor(ctx, time.Now().Add(-a.cfg.MonitorVelocityWindow), a.monitorConfig())
	if err != nil {
		return err
	}
	a.logger.InfoContext(ctx, "transaction monitor completed", "alerts", raised, "window", a.cfg.MonitorVelocityWindow.String())
	return nil
}

// monitorConfig builds the kyc.MonitorConfig shared by the periodic monitor
// and the Phase 3 event-driven compliance consumer.
func (a *App) monitorConfig() kyc.MonitorConfig {
	return kyc.MonitorConfig{
		VelocityWindow:     a.cfg.MonitorVelocityWindow,
		VelocityLimit:      a.cfg.MonitorVelocityLimit,
		StructuringWindow:  a.cfg.MonitorStructuringWindow,
		StructuringCount:   a.cfg.MonitorStructuringCount,
		StructuringFloor:   a.cfg.MonitorStructuringFloor,
		StructuringCeil:    a.cfg.MonitorStructuringCeil,
		RoundAmountStep:    a.cfg.MonitorRoundAmountStep,
		RoundAmountMin:     a.cfg.MonitorRoundAmountMin,
		HighRiskCategories: a.cfg.MonitorHighRiskCategories,
	}
}

// SyncVTPassDataPlans imports every current VTPass data variation into Xego's catalog.
func (a *App) SyncVTPassDataPlans(ctx context.Context) error {
	client := vtpass.NewWithTimeout(a.cfg.VTPassBaseURL, a.cfg.VTPassAPIKey, a.cfg.VTPassPublicKey, a.cfg.VTPassSecretKey, a.cfg.VTPassTimeout)
	networks := []string{"MTN", "AIRTEL", "GLO", "9MOBILE"}
	for _, network := range networks {
		serviceID := vtpass.ServiceIDForNetwork(network)
		variations, err := client.ListDataVariations(ctx, serviceID)
		if err != nil {
			return fmt.Errorf("sync %s variations: %w", network, err)
		}
		for index, variation := range variations {
			code := vtpass.PlanCodeFromVariation(network, variation.VariationCode)
			dataSize := extractDataSize(variation.Name)
			validity := extractValidity(variation.Name)
			if err := a.store.UpsertDataPlanFromProvider(ctx, network, code, variation.Name, dataSize, validity, variation.AmountKobo, variation.VariationCode, (index+1)*10); err != nil {
				return fmt.Errorf("upsert %s %s: %w", network, variation.VariationCode, err)
			}
		}
		a.logger.InfoContext(ctx, "synced VTPass data variations", "network", network, "count", len(variations))
	}
	return nil
}
