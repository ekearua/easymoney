package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"

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
// capture, the Tier-2 KYC gate, the debit-per-success payout, and the
// finalised counters. It then replays the payout (idempotency) and runs a
// second batch whose capture arrives after the response window closed.
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

	// Unique numbers per run so repeated executions never collide.
	run := fmt.Sprintf("%05d", time.Now().UnixNano()%100_000)
	phoneAt := func(i int) string { return "+234809" + run + fmt.Sprintf("%02d", i) }

	const amountKobo = int64(500_000)
	center, err := repository.CreateMerchantCenter(ctx, merchant.ID, "Bulk E2E "+run, 6.5244, 3.3792, 50)
	if err != nil {
		t.Fatal(err)
	}
	nearPhone, farPhone, lowKycPhone, silentPhone, blurryPhone, strangerPhone :=
		phoneAt(1), phoneAt(2), phoneAt(3), phoneAt(4), phoneAt(5), phoneAt(6)
	for i, phone := range []string{nearPhone, farPhone, lowKycPhone, silentPhone, blurryPhone, strangerPhone} {
		if _, err := repository.UpsertCenterUser(ctx, merchant.ID, center.ID, phone, fmt.Sprintf("r%d", i), "manual"); err != nil {
			t.Fatal(err)
		}
	}

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
	nearID := register(nearPhone)
	farID := register(farPhone)
	lowKycID := register(lowKycPhone)
	silentID := register(silentPhone)
	blurryID := register(blurryPhone)
	payerID := register(phoneAt(9))

	advance := func(userID uuid.UUID, to string) {
		t.Helper()
		if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
			UserID: userID, Provider: "bulk-e2e", Decision: kyc.ScreenClear,
			LegalName: "Bulk E2E", ScreenedAt: time.Now().UTC(),
		}, 0); err != nil {
			t.Fatalf("record screening: %v", err)
		}
		if _, err := repository.AdvanceKYCTierTo(ctx, userID, to, []string{
			kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails,
			kyc.EvGovPhotoID, kyc.EvBVNVerified, kyc.EvNINVerified,
		}, nil); err != nil {
			t.Fatalf("advance to %s: %v", to, err)
		}
	}
	advance(nearID, kyc.TierL2)
	advance(farID, kyc.TierL2)
	advance(lowKycID, kyc.TierL1)
	advance(silentID, kyc.TierL2)
	advance(blurryID, kyc.TierL2)

	// fund settles one fresh payment so the business wallet rises by exactly
	// the amount paid in.
	fund := func(amount int64) int64 {
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
			ProviderReference: "bulk-e2e-" + run, ReceiptToken: token,
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
		return after
	}

	messenger := &bulkE2EMessenger{}
	svc := NewBulkPaymentsService(repository,
		map[string]ports.Messenger{ChannelWhatsApp: messenger}, slog.Default(), BulkPaymentsConfig{
			BaseURL: "https://xego.test", LocationTTL: 5 * time.Minute, SendRatePerSec: 1000,
		})

	findItem := func(t *testing.T, items []store.BulkBatchItem, phone string) store.BulkBatchItem {
		t.Helper()
		for _, item := range items {
			if item.PhoneE164 == phone {
				return item
			}
		}
		t.Fatalf("no batch item for %s", phone)
		return store.BulkBatchItem{}
	}
	// claimDue claims the jobs the worker ticker would pick up, runs the ones
	// belonging to this test's batches, requeues everything else, and returns
	// the kinds it executed.
	claimDue := func(runner *BulkPaymentsService, batchIDs ...uuid.UUID) []string {
		t.Helper()
		mine := make(map[uuid.UUID]bool, len(batchIDs))
		for _, id := range batchIDs {
			mine[id] = true
		}
		ran := []string{}
		jobs, err := repository.ClaimBulkJobs(ctx, 10)
		if err != nil {
			t.Fatalf("claim jobs: %v", err)
		}
		for _, job := range jobs {
			if !mine[job.BatchID] {
				if err := repository.RetryBulkJob(ctx, job.ID, "not owned by this test"); err != nil {
					t.Fatalf("requeue foreign job: %v", err)
				}
				continue
			}
			var runErr error
			switch job.Kind {
			case store.BulkJobStage1:
				runErr = runner.RunStage1(ctx, job.BatchID)
			case store.BulkJobStage2:
				runErr = runner.RunStage2(ctx, job.BatchID)
			default:
				t.Fatalf("unexpected job kind %q", job.Kind)
			}
			if runErr != nil {
				t.Fatalf("job %s: %v", job.Kind, runErr)
			}
			if err := repository.CompleteBulkJob(ctx, job.ID); err != nil {
				t.Fatalf("complete job: %v", err)
			}
			ran = append(ran, job.Kind)
		}
		return ran
	}

	// An unfunded wallet must block the batch before anything is queued.
	if _, err := svc.CreateBatch(ctx, merchant.ID, center.ID, 1_000_000_000, payerID); !errors.Is(err, ErrBulkInsufficientFunds) {
		t.Fatalf("affordability: want ErrBulkInsufficientFunds, got %v", err)
	}

	balance := fund(4_000_000)
	batch, err := svc.CreateBatch(ctx, merchant.ID, center.ID, amountKobo, payerID)
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if batch.TotalTargets != 6 || batch.ComplianceFlag {
		t.Fatalf("batch targets=%d flag=%v, want 6 targets and no compliance flag", batch.TotalTargets, batch.ComplianceFlag)
	}
	requestsBefore := len(messenger.locations)
	if ran := claimDue(svc, batch.ID); len(ran) != 1 || ran[0] != store.BulkJobStage1 {
		t.Fatalf("executed jobs %v, want exactly one %s", ran, store.BulkJobStage1)
	}

	afterStage1, err := repository.BulkBatchByIDAny(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStage1.Status != store.BulkBatchStage2 || afterStage1.RequestedCount != 5 || afterStage1.Stage2DueAt == nil {
		t.Fatalf("stage 1: status=%s requested=%d stage2Due=%v, want stage2/5/scheduled",
			afterStage1.Status, afterStage1.RequestedCount, afterStage1.Stage2DueAt)
	}
	if got := len(messenger.locations) - requestsBefore; got != 5 {
		t.Fatalf("location requests = %d, want 5", got)
	}
	if len(messenger.texts) != 1 || messenger.texts[0] != strangerPhone {
		t.Fatalf("invite texts = %v, want one invite to %s", messenger.texts, strangerPhone)
	}
	items, err := repository.ListBulkBatchItems(ctx, batch.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, phone := range []string{nearPhone, farPhone, lowKycPhone, silentPhone, blurryPhone} {
		item := findItem(t, items, phone)
		if item.Status != store.BulkItemLocationSent || item.Channel != ChannelWhatsApp {
			t.Fatalf("%s: status=%s channel=%s, want %s on whatsapp", phone, item.Status, item.Channel, store.BulkItemLocationSent)
		}
	}
	if item := findItem(t, items, strangerPhone); item.Status != store.BulkItemNotRegistered {
		t.Fatalf("stranger: status=%s, want %s", item.Status, store.BulkItemNotRegistered)
	}

	centerLat, centerLng := center.Latitude, center.Longitude
	cases := []struct {
		phone    string
		lat      float64
		lng      float64
		accuracy float64
		want     string
	}{
		{nearPhone, centerLat + 0.0001, centerLng, 8, store.BulkItemLocationReceived},
		{farPhone, centerLat + 0.0020, centerLng, 8, store.BulkItemOutOfRange},
		{lowKycPhone, centerLat + 0.0001, centerLng, 8, store.BulkItemLocationReceived},
		{blurryPhone, centerLat + 0.0001, centerLng, 400, store.BulkItemPoorAccuracy},
	}
	for _, tc := range cases {
		found, err := svc.RecordLocation(ctx, tc.phone, tc.lat, tc.lng, tc.accuracy)
		if err != nil || !found {
			t.Fatalf("capture %s: found=%v err=%v", tc.phone, found, err)
		}
	}
	if found, err := svc.RecordLocation(ctx, phoneAt(8), centerLat, centerLng, 8); err != nil || found {
		t.Fatalf("capture without an open request: found=%v err=%v", found, err)
	}

	items, err = repository.ListBulkBatchItems(ctx, batch.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		item := findItem(t, items, tc.phone)
		if item.Status != tc.want {
			t.Fatalf("%s: status=%s, want %s", tc.phone, item.Status, tc.want)
		}
		if item.DistanceM == nil || item.AccuracyM == nil {
			t.Fatalf("%s: distance/accuracy not stored", tc.phone)
		}
	}
	if d := *findItem(t, items, nearPhone).DistanceM; d > 50 {
		t.Fatalf("near recipient is %v m from the center, want <= 50", d)
	}
	if d := *findItem(t, items, farPhone).DistanceM; d <= 50 {
		t.Fatalf("far recipient is %v m from the center, want > 50", d)
	}
	// A second capture is ignored once the item has an outcome.
	if found, err := svc.RecordLocation(ctx, nearPhone, centerLat, centerLng, 8); err != nil || found {
		t.Fatalf("duplicate capture: found=%v err=%v", found, err)
	}

	balanceBeforePayout := balance
	if err := svc.RunStage2(ctx, batch.ID); err != nil {
		t.Fatalf("stage 2: %v", err)
	}

	final, err := repository.BulkBatchByIDAny(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != store.BulkBatchPartial || final.PaidCount != 1 || final.FailedCount != 4 || final.ExpiredCount != 1 {
		t.Fatalf("finalised batch = status %s paid %d failed %d expired %d, want partial 1/4/1",
			final.Status, final.PaidCount, final.FailedCount, final.ExpiredCount)
	}
	if final.RequestedCount != 5 || final.EligibleCount != 1 || final.TotalDebitedKobo != amountKobo {
		t.Fatalf("counters requested=%d eligible=%d debited=%d, want 5/1/%d",
			final.RequestedCount, final.EligibleCount, final.TotalDebitedKobo, amountKobo)
	}

	items, err = repository.ListBulkBatchItems(ctx, batch.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	paid := findItem(t, items, nearPhone)
	if paid.Status != store.BulkItemPaid || paid.UserID == nil || *paid.UserID != nearID {
		t.Fatalf("paid item = status %s user %v, want paid by %s", paid.Status, paid.UserID, nearID)
	}
	if want := fmt.Sprintf("bulk:%s:%s", batch.BatchNo, paid.ID); paid.CreditRef != want {
		t.Fatalf("credit ref = %q, want %q", paid.CreditRef, want)
	}
	if item := findItem(t, items, lowKycPhone); item.Status != store.BulkItemKYCIneligible {
		t.Fatalf("low-kyc recipient = %s, want %s", item.Status, store.BulkItemKYCIneligible)
	}
	if item := findItem(t, items, silentPhone); item.Status != store.BulkItemExpired {
		t.Fatalf("silent recipient = %s, want %s", item.Status, store.BulkItemExpired)
	}

	walletBalance, _, err := repository.BusinessWalletBalance(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := balanceBeforePayout - amountKobo; walletBalance != want {
		t.Fatalf("business wallet = %d, want %d (debit only the payment that landed)", walletBalance, want)
	}
	recipientWallet, err := repository.WalletByOwner(ctx, store.WalletOwnerUser, nearID)
	if err != nil {
		t.Fatal(err)
	}
	credited, err := repository.WalletBalance(ctx, recipientWallet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if credited != amountKobo {
		t.Fatalf("recipient wallet = %d, want %d", credited, amountKobo)
	}
	for _, userID := range []uuid.UUID{farID, lowKycID, silentID, blurryID} {
		w, err := repository.WalletByOwner(ctx, store.WalletOwnerUser, userID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := repository.WalletBalance(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got != 0 {
			t.Fatalf("ineligible recipient %s was credited %d kobo, want 0", userID, got)
		}
	}

	// Replaying the payout must not debit the merchant twice.
	if ref, err := repository.PostBulkPaymentItem(ctx, paid.ID); err != nil || ref != paid.CreditRef {
		t.Fatalf("replay: ref=%q err=%v, want the original %q", ref, err, paid.CreditRef)
	}
	replayed, _, err := repository.BusinessWalletBalance(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != walletBalance {
		t.Fatalf("replay moved the wallet: %d -> %d", walletBalance, replayed)
	}
	if err := svc.RunStage2(ctx, batch.ID); err != nil {
		t.Fatalf("stage 2 replay: %v", err)
	}
	replayed, _, err = repository.BusinessWalletBalance(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != walletBalance {
		t.Fatalf("stage 2 replay moved the wallet: %d -> %d", walletBalance, replayed)
	}

	count, broken, err := repository.VerifyLedgerChain(ctx)
	if err != nil {
		t.Fatalf("verify ledger chain: %v", err)
	}
	if broken != -1 || count == 0 {
		t.Fatalf("ledger chain broken at entry %d of %d", broken, count)
	}

	// A capture that lands after the response window closed is expired instead
	// of paid, and the delayed stage-2 job still runs.
	latePhone := phoneAt(7)
	lateCenter, err := repository.CreateMerchantCenter(ctx, merchant.ID, "Bulk E2E late "+run, 6.5244, 3.3792, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertCenterUser(ctx, merchant.ID, lateCenter.ID, latePhone, "late", "manual"); err != nil {
		t.Fatal(err)
	}
	lateID := register(latePhone)
	advance(lateID, kyc.TierL2)

	shortSvc := NewBulkPaymentsService(repository,
		map[string]ports.Messenger{ChannelWhatsApp: messenger}, slog.Default(), BulkPaymentsConfig{
			BaseURL: "https://xego.test", LocationTTL: 2 * time.Second, SendRatePerSec: 1000,
		})
	lateBatch, err := shortSvc.CreateBatch(ctx, merchant.ID, lateCenter.ID, amountKobo, payerID)
	if err != nil {
		t.Fatalf("late batch: %v", err)
	}
	if ran := claimDue(shortSvc, lateBatch.ID); len(ran) != 1 || ran[0] != store.BulkJobStage1 {
		t.Fatalf("executed jobs %v, want exactly one %s", ran, store.BulkJobStage1)
	}
	lateItem := findItem(t, bulkItems(t, repository, lateBatch.ID), latePhone)
	if lateItem.Status != store.BulkItemLocationSent {
		t.Fatalf("late item = %s, want %s", lateItem.Status, store.BulkItemLocationSent)
	}

	time.Sleep(2200 * time.Millisecond)

	found, err := shortSvc.RecordLocation(ctx, latePhone, centerLat, centerLng, 8)
	if err != nil || !found {
		t.Fatalf("late capture: found=%v err=%v", found, err)
	}
	lateItem = findItem(t, bulkItems(t, repository, lateBatch.ID), latePhone)
	if lateItem.Status != store.BulkItemExpired {
		t.Fatalf("late item = %s, want %s", lateItem.Status, store.BulkItemExpired)
	}

	if ran := claimDue(shortSvc, lateBatch.ID); len(ran) != 1 || ran[0] != store.BulkJobStage2 {
		t.Fatalf("executed jobs %v, want exactly one %s", ran, store.BulkJobStage2)
	}
	finalLate, err := repository.BulkBatchByIDAny(ctx, lateBatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalLate.Status != store.BulkBatchFailed || finalLate.PaidCount != 0 {
		t.Fatalf("late batch = %s paid %d, want failed with no payout", finalLate.Status, finalLate.PaidCount)
	}
	bal, _, err := repository.BusinessWalletBalance(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bal != replayed {
		t.Fatalf("late batch moved the wallet: %d -> %d", replayed, bal)
	}
}

func bulkItems(t *testing.T, repository *store.Store, batchID uuid.UUID) []store.BulkBatchItem {
	t.Helper()
	items, err := repository.ListBulkBatchItems(context.Background(), batchID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	return items
}
