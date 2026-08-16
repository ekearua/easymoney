package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func TestSignMerchantWebhook(t *testing.T) {
	got := SignMerchantWebhook("s3cret", []byte("payload"))
	expected := hmacHex("s3cret", []byte("payload"))
	if got != expected {
		t.Fatalf("signature mismatch: got %s want %s", got, expected)
	}
}

func hmacHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestBuildMerchantWebhookPayload(t *testing.T) {
	paidAt := time.Date(2026, 8, 12, 10, 5, 0, 0, time.UTC)
	payment := store.PaymentView{}
	payment.ID = uuid.New()
	payment.MerchantReference = "ORD-8371"
	payment.Status = domain.StatusSucceeded
	payment.AmountKobo = 250_000
	payment.Currency = "NGN"
	payment.PaidAt = &paidAt
	payment.ReceiptToken = "tok123"

	body, err := buildMerchantWebhookPayload("https://pay.xego.ng", payment, domain.TopicPaymentSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Reference  string     `json:"reference"`
		PaymentID  string     `json:"payment_id"`
		Status     string     `json:"status"`
		Amount     apiMoney   `json:"amount"`
		PaidAt     *time.Time `json:"paid_at"`
		ReceiptURL string     `json:"receipt_url"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Reference != "ORD-8371" || decoded.Status != "succeeded" ||
		decoded.Amount.Value != 250_000 || decoded.Amount.Currency != "NGN" ||
		decoded.ReceiptURL != "https://pay.xego.ng/receipts/tok123" || decoded.PaidAt == nil {
		t.Fatalf("unexpected succeeded payload: %s", body)
	}

	// A failed payment must not carry receipt/paid_at.
	payment.Status = domain.StatusFailed
	payment.PaidAt = nil
	payment.ReceiptToken = "tok123"
	failedBody, err := buildMerchantWebhookPayload("https://pay.xego.ng", payment, domain.TopicPaymentFailed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(failedBody, []byte("receipt_url")) {
		t.Fatalf("failed payload must not include receipt_url: %s", failedBody)
	}
	if bytes.Contains(failedBody, []byte("paid_at")) {
		t.Fatalf("failed payload must not include paid_at: %s", failedBody)
	}
}

// TestMerchantWebhookEndToEnd verifies the consumer enqueues once per event and
// the deliverer POSTs a correctly signed payload to the callback URL.
func TestMerchantWebhookEndToEnd(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	databaseURL := serviceTestDBURL(t, base)
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	truncateServiceData(t, ctx, databaseURL)
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, "+2348012340888")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := createSettledPayment(ctx, t, repository, user, merchant)
	if err != nil {
		t.Fatal(err)
	}

	const secret = "webhook-secret-42"
	var deliveries atomic.Int32
	var lastSig, lastEvent string
	var lastBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastBody = body
		lastEvent = r.Header.Get("X-Xego-Event")
		lastSig = r.Header.Get("X-Xego-Signature")
		deliveries.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := repository.SetMerchantWebhookURL(ctx, merchant.ID, server.URL); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetMerchantWebhookSecret(ctx, merchant.ID, secret); err != nil {
		t.Fatal(err)
	}

	consumer := NewMerchantWebhookConsumer(repository, "https://pay.xego.ng", testLogger())
	fact, err := json.Marshal(domain.PaymentSucceeded{
		PaymentID: payment.ID.String(), UserID: user.ID.String(), MerchantID: merchant.ID.String(),
		Reference: "ref", Provider: "paystack", Currency: "NGN", AmountKobo: 200_000_000, PaidAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := ports.EventMessage{Topic: domain.TopicPaymentSucceeded, Key: "payment:" + payment.ID.String(), Payload: fact}

	if err := consumer.HandlePaymentSucceeded(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if err := consumer.HandlePaymentSucceeded(ctx, msg); err != nil {
		t.Fatal(err)
	}

	deliverer := NewMerchantWebhookDeliverer(repository, nil, testLogger())
	if err := deliverer.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	if err := deliverer.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}

	if got := deliveries.Load(); got != 1 {
		t.Fatalf("expected exactly 1 delivery, got %d", got)
	}
	if lastEvent != domain.TopicPaymentSucceeded {
		t.Fatalf("X-Xego-Event = %q, want %q", lastEvent, domain.TopicPaymentSucceeded)
	}
	if want := hmacHex(secret, lastBody); lastSig != want {
		t.Fatalf("signature mismatch: got %s want %s", lastSig, want)
	}
	var envelope merchantWebhookEnvelope
	if err := json.Unmarshal(lastBody, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.PaymentID != payment.ID.String() || envelope.Status != "succeeded" ||
		envelope.Amount.Value != 200_000_000 || envelope.ReceiptURL == "" {
		t.Fatalf("unexpected delivered envelope: %s", lastBody)
	}

	var status string
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.QueryRow(ctx, `SELECT status FROM merchant_webhook_deliveries WHERE source_id=$1`, payment.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "sent" {
		t.Fatalf("expected delivery status 'sent', got %q", status)
	}
}
