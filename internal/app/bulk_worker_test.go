package app

// Bulk worker wiring: due jobs are claimed and dispatched to the right stage,
// and only a batch that reached a terminal state proves the job ran to
// completion (a failed job is requeued with backoff, leaving the batch stuck).
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestBulkWorker -count=1 -v

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// TestBulkWorkerSkipsWhenIdle documents the two guards runBulkJobs applies
// before it claims anything: no orchestrator, or no free worker slot. Both
// paths run with a nil store, so reaching the claim would panic.
func TestBulkWorkerSkipsWhenIdle(t *testing.T) {
	a := &App{logger: slog.Default()}
	a.runBulkJobs(context.Background())

	busy := &App{
		logger:       slog.Default(),
		bulkPayments: service.NewBulkPaymentsService(nil, nil, nil, service.BulkPaymentsConfig{}),
		bulkSlots:    make(chan struct{}, 1),
	}
	busy.bulkSlots <- struct{}{}
	busy.runBulkJobs(context.Background())
}

// TestHandleBulkJobRejectsUnknownKind keeps the dispatch switch total: an
// unrecognised job kind fails loudly instead of silently completing.
func TestHandleBulkJobRejectsUnknownKind(t *testing.T) {
	a := &App{logger: slog.Default()}
	err := a.handleBulkJob(context.Background(), store.BulkJob{Kind: "bulk_stage3"})
	if err == nil || !strings.Contains(err.Error(), `unknown bulk job kind "bulk_stage3"`) {
		t.Fatalf("err = %v, want the unknown-kind error", err)
	}
}

// TestBulkWorkerRunsBothStages drives one batch through the worker exactly as
// the 2s ticker does: stage 1 goes out immediately, stage 2 waits for the TTL,
// and the batch only ends up terminal because both jobs completed without
// being requeued.
func TestBulkWorkerRunsBothStages(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the bulk worker test against a real PostgreSQL")
	}
	ctx := context.Background()
	repository, err := store.Open(ctx, bulkTestDBURL(t, base))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	messenger := &simMessenger{}
	// A one-second TTL keeps the stage-2 wait inside the test.
	bulk := service.NewBulkPaymentsService(repository,
		map[string]ports.Messenger{service.ChannelWhatsApp: messenger}, logger,
		service.BulkPaymentsConfig{BaseURL: "https://xego.test", RadiusM: 50, SendRatePerSec: 1000, LocationTTL: time.Second})
	a := &App{
		logger: logger, store: repository, bulkPayments: bulk,
		bulkSlots: make(chan struct{}, 8),
	}

	// Jobs earlier runs left behind would otherwise share this worker's claim
	// window and make "no jobs left" ambiguous.
	for {
		stale, err := repository.ClaimBulkJobs(ctx, 50)
		if err != nil {
			t.Fatalf("drain stale jobs: %v", err)
		}
		if len(stale) == 0 {
			break
		}
	}

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	phone := fmt.Sprintf("+234809%05d07", time.Now().UnixNano()%100_000)
	center, err := repository.CreateMerchantCenter(ctx, merchant.ID, "Worker spot", 6.5244, 3.3792, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertCenterUser(ctx, merchant.ID, center.ID, phone, "Worker tester", "manual"); err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	// CreateBulkBatch (not the service) is used so the worker test never
	// depends on a funded wallet; it still schedules the stage-1 job exactly
	// as a real batch would.
	batch, err := repository.CreateBulkBatch(ctx, store.NewBulkBatch{
		MerchantID: merchant.ID, CenterID: center.ID, AmountKobo: 100_000,
		Currency: "NGN", TTLSeconds: 1, CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}

	// Stage 1 is due immediately.
	a.runBulkJobs(ctx)
	a.workerWg.Wait()

	after, err := repository.BulkBatchByIDAny(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != store.BulkBatchStage2 || after.RequestedCount != 1 {
		t.Fatalf("after stage 1: status=%s requested=%d, want %s/1", after.Status, after.RequestedCount, store.BulkBatchStage2)
	}
	items, err := repository.ListBulkBatchItems(ctx, batch.ID, "", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items: %d (err %v)", len(items), err)
	}
	if items[0].Status != store.BulkItemLocationSent || items[0].Channel != service.ChannelWhatsApp {
		t.Fatalf("item = %s/%s, want %s/%s", items[0].Status, items[0].Channel, store.BulkItemLocationSent, service.ChannelWhatsApp)
	}
	var delivered bool
	for _, sent := range messenger.snapshot() {
		if sent.kind == "location_request" && sent.to == phone {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("stage 1 did not send a location request: %+v", messenger.snapshot())
	}
	if due, err := repository.ClaimBulkJobs(ctx, 5); err != nil || len(due) != 0 {
		t.Fatalf("stage 2 should not be due yet: %d jobs (err %v)", len(due), err)
	}

	// Stage 2 waits out the response window, then expires the silent recipient.
	select {
	case <-time.After(1500 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	a.runBulkJobs(ctx)
	a.workerWg.Wait()

	final, err := repository.BulkBatchByIDAny(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != store.BulkBatchFailed {
		t.Fatalf("batch status = %s, want %s (nobody was eligible to be paid)", final.Status, store.BulkBatchFailed)
	}
	items, err = repository.ListBulkBatchItems(ctx, batch.ID, "", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items after stage 2: %d (err %v)", len(items), err)
	}
	if items[0].Status != store.BulkItemExpired || items[0].FailureReason == "" {
		t.Fatalf("item = %s/%q, want an expired non-responder", items[0].Status, items[0].FailureReason)
	}
	if remaining, err := repository.ClaimBulkJobs(ctx, 5); err != nil || len(remaining) != 0 {
		t.Fatalf("both jobs should be finished, not requeued: %d claimable (err %v)", len(remaining), err)
	}
}
