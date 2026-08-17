package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	memorybus "whatsapp-payment-demo/internal/bus/memory"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// serviceTestDBURL returns a database URL dedicated to the service package so
// its integration tests never share tables with the store package when the
// whole suite runs in parallel. The database is created on first use.
func serviceTestDBURL(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	mainDB := cfg.ConnConfig.Database
	serviceDB := mainDB + "_service"
	// Config.ConnString() returns the original string verbatim, so the database
	// change would be dropped; build the DSN manually instead.
	dsn := regexp.MustCompile(`(^|\s)database=\S+`).ReplaceAllString(strings.TrimSpace(base), "")
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, serviceDB).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		if _, err := conn.Exec(ctx, `CREATE DATABASE "`+serviceDB+`"`); err != nil {
			t.Fatal(err)
		}
	}
	return dsn + " database=" + serviceDB
}

// TestEventPublisherDrainsOutboxToBus verifies the Phase 3 write path: a
// business event committed with the payment transition is drained onto the bus
// and delivered to a subscriber.
func TestEventPublisherDrainsOutboxToBus(t *testing.T) {
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
	user, err := repository.GetOrCreateUser(ctx, "+2348012340010")
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

	bus := memorybus.New(4, testLogger())
	defer bus.Close()
	subCtx, stop := context.WithCancel(ctx)
	defer stop()

	var delivered atomic.Int32
	var seenPayment atomic.Value
	if err := bus.Subscribe(subCtx, domain.TopicPaymentSucceeded, "test.consumer", func(_ context.Context, msg ports.EventMessage) error {
		delivered.Add(1)
		seenPayment.Store(string(msg.Payload))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	publisher := NewEventPublisher(repository, bus, testLogger())
	if err := publisher.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && delivered.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if delivered.Load() != 1 {
		t.Fatalf("expected 1 delivered event, got %d", delivered.Load())
	}
	var payload domain.PaymentSucceeded
	if err := json.Unmarshal([]byte(seenPayment.Load().(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PaymentID != payment.ID.String() {
		t.Fatalf("expected payment %s, got %s", payment.ID, payload.PaymentID)
	}
}

// TestComplianceConsumerRaisesAlertEventDriven verifies the compliance
// consumer raises a monitoring alert from a payment.succeeded event and is
// idempotent on redelivery.
func TestComplianceConsumerRaisesAlertEventDriven(t *testing.T) {
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
	user, err := repository.GetOrCreateUser(ctx, "+2348012340011")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createSettledPayment(ctx, t, repository, user, merchant); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	second, err := createSettledPayment(ctx, t, repository, user, merchant)
	if err != nil {
		t.Fatal(err)
	}

	consumer := NewComplianceConsumer(repository, kyc.MonitorConfig{
		StructuringWindow: 24 * time.Hour,
		StructuringCount:  2,
		StructuringFloor:  100_000_000,
		StructuringCeil:   1_000_000_000,
	}, testLogger())
	payload, err := json.Marshal(domain.PaymentSucceeded{
		PaymentID:  second.ID.String(),
		UserID:     user.ID.String(),
		MerchantID: merchant.ID.String(),
		Reference:  "ref",
		Provider:   "paystack",
		Currency:   "NGN",
		AmountKobo: 200_000_000,
		PaidAt:     time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := ports.EventMessage{Topic: domain.TopicPaymentSucceeded, Key: "payment:" + second.ID.String(), Payload: payload}
	if err := consumer.HandlePaymentSucceeded(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if err := consumer.HandlePaymentSucceeded(ctx, msg); err != nil {
		t.Fatal(err)
	}
	alerts, err := repository.ListTransactionAlerts(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Rule != kyc.RuleStructuring {
		t.Fatalf("expected exactly 1 structuring alert after redelivery, got %+v", alerts)
	}
}

func createSettledPayment(ctx context.Context, t *testing.T, repository *store.Store, user store.User, merchant store.Merchant) (domain.Payment, error) {
	t.Helper()
	token, err := domain.NewReceiptToken()
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 200_000_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
		ProviderReference: domain.NewProviderReference(), ReceiptToken: token,
	})
	if err != nil {
		return domain.Payment{}, err
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
		t.Fatalf("transition: changed=%v err=%v", changed, err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusInitialized, "test", nil); err != nil || !changed {
		t.Fatalf("initialize: changed=%v err=%v", changed, err)
	}
	if changed, err := repository.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "test", nil, store.OutboxSpec{
		UserID: user.ID, Recipient: user.WhatsAppNumber, Kind: "text", Payload: []byte(`{"body":"ok"}`),
	}); err != nil || !changed {
		t.Fatalf("succeed: changed=%v err=%v", changed, err)
	}
	return payment, nil
}

func truncateServiceData(t *testing.T, ctx context.Context, databaseURL string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		TRUNCATE business_event_outbox,transaction_alerts,risk_events,manual_review_cases,
		         message_outbox,inbound_messages,webhook_deliveries,payment_events,payments,
		         refunds,disputes,
		         merchant_settlement_accounts,settlement_batches,settlement_lines,payouts,
		         merchant_webhook_deliveries,conversation_sessions,users,merchants
		         RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
}
