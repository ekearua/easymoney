package store

import (
	"context"
	"crypto/rand"
	"os"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
)

func TestMerchantWebhookDeliveries(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
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
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE merchant_webhook_deliveries,payments,users,merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}

	// Config round-trip: setting the URL preserves the secret.
	if err := repository.SetMerchantWebhookURL(ctx, merchant.ID, "https://shop.example.com/xego/webhook"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetMerchantWebhookSecret(ctx, merchant.ID, "first-secret"); err != nil {
		t.Fatal(err)
	}
	cfg, err := repository.MerchantWebhookConfig(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://shop.example.com/xego/webhook" || cfg.Secret != "first-secret" {
		t.Fatalf("config round-trip: got %+v", cfg)
	}
	if err := repository.SetMerchantWebhookURL(ctx, merchant.ID, "https://shop.example.com/hook"); err != nil {
		t.Fatal(err)
	}
	cfg, err = repository.MerchantWebhookConfig(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://shop.example.com/hook" || cfg.Secret != "first-secret" {
		t.Fatalf("secret should survive url-only update: got %+v", cfg)
	}

	user, err := repository.GetOrCreateUser(ctx, "+2348012340999")
	if err != nil {
		t.Fatal(err)
	}
	payment := domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 100_000,
		Currency: "NGN", Status: domain.StatusSucceeded, Provider: "paystack",
		ProviderReference: "ref-webhook-test", ReceiptToken: "tok",
	}
	if _, err := repository.CreatePayment(ctx, payment); err != nil {
		t.Fatal(err)
	}

	// Enqueue is idempotent per (merchant, payment, event).
	queued, err := repository.EnqueueMerchantWebhook(ctx, merchant.ID, payment.ID, domain.TopicPaymentSucceeded, []byte(`{"status":"succeeded"}`))
	if err != nil || !queued {
		t.Fatalf("first enqueue: queued=%v err=%v", queued, err)
	}
	queued, err = repository.EnqueueMerchantWebhook(ctx, merchant.ID, payment.ID, domain.TopicPaymentSucceeded, []byte(`{"status":"succeeded"}`))
	if err != nil || queued {
		t.Fatalf("duplicate enqueue: queued=%v err=%v", queued, err)
	}

	// Claim leases due rows, marks them sending, and applies the backoff window.
	deliveries, err := repository.ClaimMerchantWebhooks(ctx, 10)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("claim: count=%d err=%v", len(deliveries), err)
	}
	if string(deliveries[0].Payload) != `{"status":"succeeded"}` {
		t.Fatalf("claimed payload mismatch: %s", deliveries[0].Payload)
	}
	claimed, err := repository.ClaimMerchantWebhooks(ctx, 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("re-claim before available_at: count=%d err=%v", len(claimed), err)
	}
	if err := repository.CompleteMerchantWebhook(ctx, deliveries[0].ID); err != nil {
		t.Fatal(err)
	}
	done, err := repository.ClaimMerchantWebhooks(ctx, 10)
	if err != nil || len(done) != 0 {
		t.Fatalf("claim after complete: count=%d err=%v", len(done), err)
	}

	// Retry escalates to a bounded schedule and dead-letters at the cap.
	payment2 := domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 200_000,
		Currency: "NGN", Status: domain.StatusSucceeded, Provider: "paystack",
		ProviderReference: "ref-webhook-retry", ReceiptToken: "tok2",
	}
	if _, err := repository.CreatePayment(ctx, payment2); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnqueueMerchantWebhook(ctx, merchant.ID, payment2.ID, domain.TopicPaymentSucceeded, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	pending, err := repository.ClaimMerchantWebhooks(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("claim retry: count=%d err=%v", len(pending), err)
	}
	if err := repository.RetryMerchantWebhook(ctx, pending[0].ID, pending[0].Attempts, "boom"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 5; i++ {
		// The exponential backoff schedules the next attempt in the future;
		// force it due so the claim/retry cycle can be exercised here.
		if _, err := repository.pool.Exec(ctx, `UPDATE merchant_webhook_deliveries SET available_at=now() WHERE id=$1`, pending[0].ID); err != nil {
			t.Fatal(err)
		}
		next, err := repository.ClaimMerchantWebhooks(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(next) != 1 {
			t.Fatalf("retry iteration %d: expected 1 pending, got %d", i, len(next))
		}
		if err := repository.RetryMerchantWebhook(ctx, next[0].ID, next[0].Attempts, "boom"); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	if err := repository.pool.QueryRow(ctx, `SELECT status FROM merchant_webhook_deliveries WHERE id=$1`, pending[0].ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("expected dead-letter status 'failed', got %q", status)
	}
	var lastError string
	if err := repository.pool.QueryRow(ctx, `SELECT last_error FROM merchant_webhook_deliveries WHERE id=$1`, pending[0].ID).Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if lastError == "" {
		t.Fatal("expected a recorded last_error on dead-letter")
	}

	// Config is stored so no plaintext secret survives outside the seal.
	var storedSecret string
	if err := repository.pool.QueryRow(ctx, `SELECT webhook_secret FROM merchants WHERE id=$1`, merchant.ID).Scan(&storedSecret); err != nil {
		t.Fatal(err)
	}
	if storedSecret != "" && storedSecret == "first-secret" {
		t.Fatal("webhook secret must not be stored in plaintext")
	}
}
