package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// TestBulkProgramDisbursementEndToEnd drives a *program* bulk batch through the
// real orchestrator against a real PostgreSQL database, which is the only place
// the two halves of the feature meet: the service decides who is paid (routing
// a program batch to PostBulkProgramPaymentItem instead of PostBulkPaymentItem)
// and the store decides what paying them means.
//
// The assertions are about the restriction holding under a normal happy path:
//
//   - an enrolled recipient's purse is credited and their ordinary wallet is not,
//     so the money can only be spent where the program allows;
//   - a recipient who is KYC-eligible but NOT enrolled is refused (item failed
//     as program_ineligible) instead of being paid into a withdrawable balance;
//   - the sponsor is debited exactly once, for exactly the money that landed;
//   - replaying the stage leaves the book untouched.
func TestBulkProgramDisbursementEndToEnd(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	databaseURL := bulkServiceTestDBURL(t, base)
	ctx := context.Background()
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key[:])
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	sponsor, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	// Sweep anything a previous run left unsettled so the wallet delta below is
	// exactly the disbursement this test creates.
	if _, err := repository.CutSettlement(ctx, sponsor.ID, "PROG-SWEEP-"+uuid.NewString()[:8], time.Time{}, 0); err != nil &&
		!errors.Is(err, store.ErrNothingToSettle) {
		t.Fatalf("sweep settlement: %v", err)
	}

	run := fmt.Sprintf("%05d", time.Now().UnixNano()%100_000)
	phoneAt := func(i int) string { return "+234810" + run + fmt.Sprintf("%02d", i) }
	enrolledPhone, unlistedPhone, payerPhone := phoneAt(1), phoneAt(2), phoneAt(3)

	register := func(phone string) uuid.UUID {
		t.Helper()
		user, err := repository.GetOrCreateUser(ctx, phone)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.ConfirmUserNumber(ctx, user.ID); err != nil {
			t.Fatal(err)
		}
		return user.ID
	}
	toTier2 := func(userID uuid.UUID) {
		t.Helper()
		if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
			UserID: userID, Provider: "program-e2e", Decision: kyc.ScreenClear,
			LegalName: "Program E2E", ScreenedAt: time.Now().UTC(),
		}, 0); err != nil {
			t.Fatalf("record screening: %v", err)
		}
		if _, err := repository.AdvanceKYCTierTo(ctx, userID, kyc.TierL2, []string{
			kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails,
			kyc.EvGovPhotoID, kyc.EvBVNVerified, kyc.EvNINVerified,
		}, nil); err != nil {
			t.Fatalf("advance to Tier 2: %v", err)
		}
	}
	enrolledID := register(enrolledPhone)
	unlistedID := register(unlistedPhone)
	payerID := register(payerPhone)
	toTier2(enrolledID)
	toTier2(unlistedID)
	// Payer confirmation is irrelevant to the batch; it exists only so the
	// sponsor wallet can be funded through the real settlement cut.
	toTier2(payerID)

	program, err := repository.CreateProgram(ctx, store.NewProgram{
		Code:                "prog-e2e-" + run,
		Name:                "Program E2E " + run,
		SponsorMerchantID:   sponsor.ID,
		PurposeLabel:        "approved inputs",
		DefaultValidityDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnrollUser(ctx, program.ID, enrolledID, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	center, err := repository.CreateMerchantCenter(ctx, sponsor.ID, "Program E2E "+run, 6.5244, 3.3792, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, phone := range []string{enrolledPhone, unlistedPhone} {
		if _, err := repository.UpsertCenterUser(ctx, sponsor.ID, center.ID, phone, "recipient", "manual"); err != nil {
			t.Fatal(err)
		}
	}

	// fund settles one fresh payment so the business wallet rises by exactly
	// the amount paid in — the same route the merchant's money really arrives.
	fund := func(amount int64) {
		t.Helper()
		before, _, err := repository.BusinessWalletBalance(ctx, sponsor.ID)
		if err != nil {
			t.Fatal(err)
		}
		token, err := domain.NewReceiptToken()
		if err != nil {
			t.Fatal(err)
		}
		payment, err := repository.CreatePayment(ctx, domain.Payment{
			ID: uuid.New(), UserID: payerID, MerchantID: sponsor.ID, AmountKobo: amount,
			Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
			ProviderReference: "program-e2e-" + run, ReceiptToken: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range []domain.PaymentStatus{
			domain.StatusAwaitingConfirmation, domain.StatusInitialized, domain.StatusSucceeded,
		} {
			if changed, err := repository.TransitionPayment(ctx, payment.ID, status, "test", nil); err != nil || !changed {
				t.Fatalf("transition %s: changed=%v err=%v", status, changed, err)
			}
		}
		if err := repository.ApplyPaymentSplits(ctx, payment.ID, sponsor.ID, []store.SplitSpec{{
			SplitType: "merchant_receivable", Account: store.LedgerAccountMerchantPayable,
			AmountKobo: amount, Currency: "NGN", Description: "Merchant receivable",
		}}); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CutSettlement(ctx, sponsor.ID, "PROG-"+uuid.NewString()[:12], time.Time{}, 0); err != nil {
			t.Fatalf("cut settlement: %v", err)
		}
		after, _, err := repository.BusinessWalletBalance(ctx, sponsor.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after-before != amount {
			t.Fatalf("wallet delta = %d, want %d", after-before, amount)
		}
	}

	const amountKobo = int64(250_000)
	fund(4_000_000)

	messenger := &bulkE2EMessenger{}
	svc := NewBulkPaymentsService(repository,
		map[string]ports.Messenger{ChannelWhatsApp: messenger}, slog.Default(), BulkPaymentsConfig{
			BaseURL: "https://faya.test", LocationTTL: 5 * time.Minute, SendRatePerSec: 1000,
		})

	batch, err := svc.CreateBatch(ctx, sponsor.ID, center.ID, amountKobo, payerID)
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if batch.TotalTargets != 2 {
		t.Fatalf("batch targets = %d, want 2", batch.TotalTargets)
	}
	if err := repository.SetBulkBatchProgram(ctx, batch.ID, program.ID); err != nil {
		t.Fatalf("mark batch as a program disbursement: %v", err)
	}
	if err := svc.RunStage1(ctx, batch.ID); err != nil {
		t.Fatalf("stage 1: %v", err)
	}
	for _, phone := range []string{enrolledPhone, unlistedPhone} {
		found, err := svc.RecordLocation(ctx, phone, center.Latitude, center.Longitude, 8)
		if err != nil || !found {
			t.Fatalf("capture %s: found=%v err=%v", phone, found, err)
		}
	}

	balanceBefore, _, err := repository.BusinessWalletBalance(ctx, sponsor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RunStage2(ctx, batch.ID); err != nil {
		t.Fatalf("stage 2: %v", err)
	}

	final, err := repository.BulkBatchByIDAny(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != store.BulkBatchPartial {
		t.Fatalf("batch status = %s, want %s (one paid, one refused)", final.Status, store.BulkBatchPartial)
	}
	if final.PaidCount != 1 || final.FailedCount != 1 || final.TotalDebitedKobo != amountKobo {
		t.Fatalf("counters paid=%d failed=%d debited=%d, want 1/1/%d",
			final.PaidCount, final.FailedCount, final.TotalDebitedKobo, amountKobo)
	}

	items, err := repository.ListBulkBatchItems(ctx, batch.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	byPhone := map[string]store.BulkBatchItem{}
	for _, item := range items {
		byPhone[item.PhoneE164] = item
	}
	if item := byPhone[enrolledPhone]; item.Status != store.BulkItemPaid {
		t.Fatalf("enrolled item = %s, want %s", item.Status, store.BulkItemPaid)
	}
	refused := byPhone[unlistedPhone]
	if refused.Status != store.BulkItemFailed || refused.FailureCode != "program_ineligible" {
		t.Fatalf("unenrolled item = %s/%s, want failed/program_ineligible", refused.Status, refused.FailureCode)
	}

	// The money is in the purse, and only there.
	purse, err := repository.ProgramPurseForUser(ctx, program.ID, enrolledID)
	if err != nil {
		t.Fatal(err)
	}
	if purse.BalanceKobo != amountKobo {
		t.Fatalf("purse balance = %d, want %d", purse.BalanceKobo, amountKobo)
	}
	enrolledWallet, err := repository.EnsureUserWallet(ctx, enrolledID, "")
	if err != nil {
		t.Fatal(err)
	}
	if balance, err := repository.WalletBalance(ctx, enrolledWallet.ID); err != nil {
		t.Fatal(err)
	} else if balance != 0 {
		t.Fatalf("the enrollee's ordinary wallet holds %d kobo, want 0", balance)
	}
	unlistedWallet, err := repository.EnsureUserWallet(ctx, unlistedID, "")
	if err != nil {
		t.Fatal(err)
	}
	if balance, err := repository.WalletBalance(ctx, unlistedWallet.ID); err != nil {
		t.Fatal(err)
	} else if balance != 0 {
		t.Fatalf("the refused recipient was credited %d kobo, want 0", balance)
	}

	afterPayout, _, err := repository.BusinessWalletBalance(ctx, sponsor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterPayout != balanceBefore-amountKobo {
		t.Fatalf("sponsor wallet = %d, want %d (debit only the payment that landed)",
			afterPayout, balanceBefore-amountKobo)
	}

	// Replaying the stage must be a no-op: nothing left to pay, nothing to debit.
	if err := svc.RunStage2(ctx, batch.ID); err != nil {
		t.Fatalf("stage 2 replay: %v", err)
	}
	replayed, _, err := repository.BusinessWalletBalance(ctx, sponsor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != afterPayout {
		t.Fatalf("stage 2 replay moved the sponsor wallet: %d -> %d", afterPayout, replayed)
	}
	replayedPurse, err := repository.ProgramPurseForUser(ctx, program.ID, enrolledID)
	if err != nil {
		t.Fatal(err)
	}
	if replayedPurse.BalanceKobo != amountKobo {
		t.Fatalf("stage 2 replay double-funded the purse: %d, want %d", replayedPurse.BalanceKobo, amountKobo)
	}

	if count, broken, err := repository.VerifyLedgerChain(ctx); err != nil {
		t.Fatalf("verify ledger chain: %v", err)
	} else if broken != -1 || count == 0 {
		t.Fatalf("ledger chain broken at entry %d of %d", broken, count)
	}
}
