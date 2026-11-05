package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// bulkServiceTestDBURL returns a dedicated database for this integration test,
// recreated on every run, so batches, wallets, and settlement state left by an
// earlier execution (or by any other suite touching TEST_DATABASE_URL) can
// never leak into the assertions. It accepts either the URL form used by the
// store/app suites or the keyword-form DSN the rest of the service suite
// expects.
func bulkServiceTestDBURL(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	mainDB := cfg.ConnConfig.Database
	if mainDB == "" {
		mainDB = "postgres"
	}
	bulkDB := mainDB + "_bulk_service"
	dropCreate := func(conn *pgx.Conn, name string) {
		t.Helper()
		if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
			t.Fatal(err)
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		dsn := regexp.MustCompile(`(^|\s)database=\S+`).ReplaceAllString(strings.TrimSpace(base), "")
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		dropCreate(conn, bulkDB)
		return dsn + " database=" + bulkDB
	}
	admin := *parsed
	admin.Path = "/postgres"
	bulk := *parsed
	bulk.Path = "/" + bulkDB
	conn, err := pgx.Connect(ctx, admin.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	dropCreate(conn, bulkDB)
	return bulk.String()
}

// bulkE2EMessenger records every outbound stage-1 message by destination so the
// test can assert who was asked for a location and who was invited instead.
type bulkE2EMessenger struct {
	locations []string
	texts     []string
	links     []string
}

func (m *bulkE2EMessenger) SendText(_ context.Context, to, _ string) error {
	m.texts = append(m.texts, to)
	return nil
}

func (m *bulkE2EMessenger) SendInteractive(context.Context, ports.InteractiveMessage) error {
	return nil
}

func (m *bulkE2EMessenger) SendCheckout(context.Context, string, string, string) error {
	return nil
}

func (m *bulkE2EMessenger) SendLink(_ context.Context, to, _, url, _ string) error {
	m.links = append(m.links, to+"|"+url)
	return nil
}

func (m *bulkE2EMessenger) SendTemplate(_ context.Context, to, _ string, _ []string) error {
	m.texts = append(m.texts, to)
	return nil
}

func (m *bulkE2EMessenger) SendImage(context.Context, string, []byte, string) error {
	return nil
}

func (m *bulkE2EMessenger) SendLocationRequest(_ context.Context, to, _ string) error {
	m.locations = append(m.locations, to)
	return nil
}

// TestBulkPaymentsEndToEnd drives a batch through both stages against a real
// PostgreSQL database: recipient snapshot, affordability block, wallet funding
// through the settlement cut, the stage-1 location request, geo scoring of each
// capture, the Tier-2 KYC gate, the debit-per-success payout, and the finalised
// counters. It then replays the payout (idempotency) and runs a second batch
// whose capture arrives after the response window closed.
func TestBulkPaymentsEndToEnd(t *testing.T) {
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

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	// Sweep anything an earlier run left unsettled so the wallet delta below is
	// exactly the payment this test creates.
	if _, err := repository.CutSettlement(ctx, merchant.ID, "BULK-SWEEP-"+uuid.NewString()[:8], time.Time{}, 0); err != nil &&
		!errors.Is(err, store.ErrNothingToSettle) {
		t.Fatalf("sweep settlement: %v", err)
	}

	run := fmt.Sprintf("%05d", time.Now().UnixNano()%100_000)
	phoneAt := func(i int) string { return "+234803" + run + fmt.Sprintf("%02d", i) }
	paidPhone, tier1Phone, farPhone := phoneAt(1), phoneAt(2), phoneAt(3)
	blurryPhone, silentPhone, invitePhone := phoneAt(4), phoneAt(5), phoneAt(6)
	payerPhone := phoneAt(7)

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
			UserID: userID, Provider: "bulk-e2e", Decision: kyc.ScreenClear,
			LegalName: "Bulk E2E", ScreenedAt: time.Now().UTC(),
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

	paidID := register(paidPhone)
	toTier2(paidID)
	// Registered but only Tier 1: location verified, then refused at the KYC gate.
	tier1ID := register(tier1Phone)
	farID := register(farPhone)
	toTier2(farID)
	blurryID := register(blurryPhone)
	toTier2(blurryID)
	silentID := register(silentPhone)
	toTier2(silentID)
	// The payer exists only so the merchant wallet can be funded through the
	// real settlement cut.
	payerID := register(payerPhone)
	toTier2(payerID)

	const centerLat, centerLng = 6.5244, 3.3792
	center, err := repository.CreateMerchantCenter(ctx, merchant.ID, "Bulk E2E "+run, centerLat, centerLng, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, phone := range []string{paidPhone, tier1Phone, farPhone, blurryPhone, silentPhone, invitePhone} {
		if _, err := repository.UpsertCenterUser(ctx, merchant.ID, center.ID, phone, "recipient", "manual"); err != nil {
			t.Fatal(err)
		}
	}

	messenger := &bulkE2EMessenger{}
	svc := NewBulkPaymentsService(repository,
		map[string]ports.Messenger{ChannelWhatsApp: messenger}, slog.Default(), BulkPaymentsConfig{
			BaseURL: "https://faya.test", LocationTTL: 5 * time.Minute, SendRatePerSec: 1000,
		}, nil, nil)

	// A batch the wallet cannot cover must be refused before anything is sent.
	balance, _, err := repository.BusinessWalletBalance(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateBatch(ctx, merchant.ID, center.ID, balance/6+1_000_000, payerID); !errors.Is(err, ErrBulkInsufficientFunds) {
		t.Fatalf("over-budget batch error = %v, want %v", err, ErrBulkInsufficientFunds)
	}

	// fund settles one fresh payment so the business wallet rises by exactly
	// the amount paid in — the same route the merchant's money really arrives.
	fund := func(amount int64) {
		t.Helper()
		before, _, err := repository.BusinessWalletBalance(ctx, merchant.ID)
		if err != nil {
			t.Fatal(err)
		}
		token, err := domain.NewReceiptToken()
		if err != nil {
			t.Fatal(err)
		}
		payment, err := repository.CreatePayment(ctx, domain.Payment{
			ID: uuid.New(), UserID: payerID, MerchantID: merchant.ID, AmountKobo: amount,
			Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
			ProviderReference: "bulk-e2e-" + uuid.NewString()[:12], ReceiptToken: token,
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
		if err := repository.ApplyPaymentSplits(ctx, payment.ID, merchant.ID, []store.SplitSpec{{
			SplitType: "merchant_receivable", Account: store.LedgerAccountMerchantPayable,
			AmountKobo: amount, Currency: "NGN", Description: "Merchant receivable",
		}}); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CutSettlement(ctx, merchant.ID, "BULK-"+uuid.NewString()[:12], time.Time{}, 0); err != nil {
			t.Fatalf("cut settlement: %v", err)
		}
		after, _, err := repository.BusinessWalletBalance(ctx, merchant.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after-before != amount {
			t.Fatalf("wallet delta = %d, want %d", after-before, amount)
		}
	}

	const amountKobo = int64(250_000)
	fund(4_000_000)

	batch, err := svc.CreateBatch(ctx, merchant.ID, center.ID, amountKobo, payerID)
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if batch.TotalTargets != 6 {
		t.Fatalf("batch targets = %d, want 6", batch.TotalTargets)
	}

	if err := svc.RunStage1(ctx, batch.ID); err != nil {
		t.Fatalf("stage 1: %v", err)
	}
	// Five registered recipients are asked for their location; the sixth has no
	// Xego account and is invited to create one instead.
	if len(messenger.locations) != 5 {
		t.Fatalf("location requests = %d, want 5", len(messenger.locations))
	}
	if len(messenger.texts) != 1 || messenger.texts[0] != invitePhone {
		t.Fatalf("invites = %v, want [%s]", messenger.texts, invitePhone)
	}
	afterStage1, err := repository.BulkBatchByIDAny(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStage1.RequestedCount != 5 || afterStage1.Status != store.BulkBatchStage1 {
		t.Fatalf("after stage 1: status=%s requested=%d, want %s/5",
			afterStage1.Status, afterStage1.RequestedCount, store.BulkBatchStage1)
	}

	captures := []struct {
		phone              string
		lat, lng, accuracy float64
	}{
		{paidPhone, centerLat + 0.0001, centerLng + 0.0001, 10},
		{tier1Phone, centerLat + 0.0001, centerLng, 10},
		{farPhone, centerLat + 0.02, centerLng, 10},
		{blurryPhone, centerLat, centerLng, 800},
	}
	for _, c := range captures {
		found, err := svc.RecordLocation(ctx, c.phone, c.lat, c.lng, c.accuracy)
		if err != nil || !found {
			t.Fatalf("capture %s: found=%v err=%v", c.phone, found, err)
		}
	}

	beforePayout, _, err := repository.BusinessWalletBalance(ctx, merchant.ID)
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
		t.Fatalf("batch status = %s, want %s (one paid, the rest ineligible)", final.Status, store.BulkBatchPartial)
	}
	if final.PaidCount != 1 || final.FailedCount != 4 || final.ExpiredCount != 1 || final.TotalDebitedKobo != amountKobo {
		t.Fatalf("counters paid=%d failed=%d expired=%d debited=%d, want 1/4/1/%d",
			final.PaidCount, final.FailedCount, final.ExpiredCount, final.TotalDebitedKobo, amountKobo)
	}

	items, err := repository.ListBulkBatchItems(ctx, batch.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	byPhone := map[string]store.BulkBatchItem{}
	for _, item := range items {
		byPhone[item.PhoneE164] = item
	}
	want := map[string]string{
		paidPhone:   store.BulkItemPaid,
		tier1Phone:  store.BulkItemKYCIneligible,
		farPhone:    store.BulkItemOutOfRange,
		blurryPhone: store.BulkItemPoorAccuracy,
		silentPhone: store.BulkItemExpired,
		invitePhone: store.BulkItemNotRegistered,
	}
	for phone, status := range want {
		if got := byPhone[phone].Status; got != status {
			t.Fatalf("item %s = %s, want %s", phone, got, status)
		}
	}

	// The verified recipient is paid into their wallet, and the merchant is
	// debited exactly once for exactly the money that landed.
	paidWallet, err := repository.EnsureUserWallet(ctx, paidID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repository.WalletBalance(ctx, paidWallet.ID); err != nil {
		t.Fatal(err)
	} else if got != amountKobo {
		t.Fatalf("recipient wallet = %d, want %d", got, amountKobo)
	}
	for _, id := range []uuid.UUID{tier1ID, farID, blurryID, silentID} {
		wallet, err := repository.EnsureUserWallet(ctx, id, "")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := repository.WalletBalance(ctx, wallet.ID); err != nil {
			t.Fatal(err)
		} else if got != 0 {
			t.Fatalf("ineligible recipient %s was credited %d kobo, want 0", id, got)
		}
	}
	afterPayout, _, err := repository.BusinessWalletBalance(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterPayout != beforePayout-amountKobo {
		t.Fatalf("merchant wallet = %d, want %d (debit only the payment that landed)",
			afterPayout, beforePayout-amountKobo)
	}

	// Replaying the payout must be a no-op: nothing left to pay, nothing to debit.
	if err := svc.RunStage2(ctx, batch.ID); err != nil {
		t.Fatalf("stage 2 replay: %v", err)
	}
	replayed, _, err := repository.BusinessWalletBalance(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != afterPayout {
		t.Fatalf("stage 2 replay moved the merchant wallet: %d -> %d", afterPayout, replayed)
	}
	if got, err := repository.WalletBalance(ctx, paidWallet.ID); err != nil {
		t.Fatal(err)
	} else if got != amountKobo {
		t.Fatalf("stage 2 replay double-credited the recipient: %d, want %d", got, amountKobo)
	}

	// A capture that arrives after the response window must expire instead of
	// paying, so a late reply can never move money the merchant did not expect.
	lateSvc := NewBulkPaymentsService(repository,
		map[string]ports.Messenger{ChannelWhatsApp: &bulkE2EMessenger{}}, slog.Default(), BulkPaymentsConfig{
			BaseURL: "https://faya.test", LocationTTL: time.Second, SendRatePerSec: 1000,
		}, nil, nil)
	lateBatch, err := lateSvc.CreateBatch(ctx, merchant.ID, center.ID, amountKobo, payerID)
	if err != nil {
		t.Fatalf("create late batch: %v", err)
	}
	if err := lateSvc.RunStage1(ctx, lateBatch.ID); err != nil {
		t.Fatalf("late stage 1: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	found, err := lateSvc.RecordLocation(ctx, paidPhone, centerLat, centerLng, 10)
	if err != nil || !found {
		t.Fatalf("late capture: found=%v err=%v", found, err)
	}
	lateItems, err := repository.ListBulkBatchItems(ctx, lateBatch.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lateItems) != 6 {
		t.Fatalf("late batch items = %d, want 6", len(lateItems))
	}
	if err := lateSvc.RunStage2(ctx, lateBatch.ID); err != nil {
		t.Fatalf("late stage 2: %v", err)
	}
	lateFinal, err := repository.BulkBatchByIDAny(ctx, lateBatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lateFinal.PaidCount != 0 || lateFinal.TotalDebitedKobo != 0 {
		t.Fatalf("late batch paid=%d debited=%d, want 0/0", lateFinal.PaidCount, lateFinal.TotalDebitedKobo)
	}
	if lateFinal.Status != store.BulkBatchFailed {
		t.Fatalf("late batch status = %s, want %s", lateFinal.Status, store.BulkBatchFailed)
	}
	if got, _, err := repository.BusinessWalletBalance(ctx, merchant.ID); err != nil {
		t.Fatal(err)
	} else if got != afterPayout {
		t.Fatalf("late batch moved the merchant wallet: %d -> %d", afterPayout, got)
	}

	if count, broken, err := repository.VerifyLedgerChain(ctx); err != nil {
		t.Fatalf("verify ledger chain: %v", err)
	} else if broken != -1 || count == 0 {
		t.Fatalf("ledger chain broken at entry %d of %d", broken, count)
	}
}
