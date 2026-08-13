package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
)

func TestPostgresBusinessEventOutboxOnPaymentTransition(t *testing.T) {
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
	truncateAll(t, ctx, repository)
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	user, err := repository.GetOrCreateUser(ctx, "+2348012340001")
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
	token, err := domain.NewReceiptToken()
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 50_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
		ProviderReference: domain.NewProviderReference(), ReceiptToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
		t.Fatalf("transition: changed=%v err=%v", changed, err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusInitialized, "test", nil); err != nil || !changed {
		t.Fatalf("initialize: changed=%v err=%v", changed, err)
	}
	if changed, err := repository.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "test", nil, OutboxSpec{
		UserID: user.ID, Recipient: user.WhatsAppNumber, Kind: "text", Payload: []byte(`{"body":"ok"}`),
	}); err != nil || !changed {
		t.Fatalf("succeed: changed=%v err=%v", changed, err)
	}

	events, err := repository.ClaimBusinessEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 business event, got %d", len(events))
	}
	event := events[0]
	if event.Topic != domain.TopicPaymentSucceeded {
		t.Fatalf("expected topic %q, got %q", domain.TopicPaymentSucceeded, event.Topic)
	}
	if want := "payment:" + payment.ID.String(); event.Key != want {
		t.Fatalf("expected key %q, got %q", want, event.Key)
	}
	var payload domain.PaymentSucceeded
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PaymentID != payment.ID.String() || payload.UserID != user.ID.String() || payload.MerchantID != merchant.ID.String() {
		t.Fatalf("unexpected event payload: %+v", payload)
	}
	if payload.AmountKobo != 50_000 || payload.Provider != "paystack" {
		t.Fatalf("unexpected event amount/provider: %+v", payload)
	}
	if err := repository.CompleteBusinessEvent(ctx, event.ID); err != nil {
		t.Fatal(err)
	}
	// The event is not claimed again after completion.
	events, err = repository.ClaimBusinessEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no pending events after completion, got %d", len(events))
	}

	// A failed transition emits payment.failed.
	token2, err := domain.NewReceiptToken()
	if err != nil {
		t.Fatal(err)
	}
	payment2, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 50_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
		ProviderReference: domain.NewProviderReference(), ReceiptToken: token2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment2.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
		t.Fatalf("transition: changed=%v err=%v", changed, err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment2.ID, domain.StatusInitialized, "test", nil); err != nil || !changed {
		t.Fatalf("initialize: changed=%v err=%v", changed, err)
	}
	if changed, err := repository.TransitionPaymentWithOutbox(ctx, payment2.ID, domain.StatusFailed, "test", map[string]any{"reason": "card declined"}, OutboxSpec{
		UserID: user.ID, Recipient: user.WhatsAppNumber, Kind: "text", Payload: []byte(`{"body":"failed"}`),
	}); err != nil || !changed {
		t.Fatalf("fail: changed=%v err=%v", changed, err)
	}
	events, err = repository.ClaimBusinessEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Topic != domain.TopicPaymentFailed {
		t.Fatalf("expected payment.failed event, got %+v", events)
	}
}

func TestPostgresRunTransactionMonitorForPaymentIdempotent(t *testing.T) {
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
	truncateAll(t, ctx, repository)
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	user, err := repository.GetOrCreateUser(ctx, "+2348012340002")
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
	makeSettled := func(amountKobo int64) domain.Payment {
		token, err := domain.NewReceiptToken()
		if err != nil {
			t.Fatal(err)
		}
		payment, err := repository.CreatePayment(ctx, domain.Payment{
			ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: amountKobo,
			Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
			ProviderReference: domain.NewProviderReference(), ReceiptToken: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "test", nil); err != nil || !changed {
			t.Fatalf("transition: changed=%v err=%v", changed, err)
		}
		if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusInitialized, "test", nil); err != nil || !changed {
			t.Fatalf("initialize: changed=%v err=%v", changed, err)
		}
		if changed, err := repository.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "test", nil, OutboxSpec{
			UserID: user.ID, Recipient: user.WhatsAppNumber, Kind: "text", Payload: []byte(`{"body":"ok"}`),
		}); err != nil || !changed {
			t.Fatalf("succeed: changed=%v err=%v", changed, err)
		}
		time.Sleep(20 * time.Millisecond) // distinct paid_at so the monitor sees both
		return payment
	}

	// Structuring rule: two payments in [floor, ceil) inside the window.
	first := makeSettled(200_000_000)
	second := makeSettled(250_000_000)
	cfg := kyc.MonitorConfig{
		StructuringWindow: 24 * time.Hour,
		StructuringCount:  2,
		StructuringFloor:  100_000_000,
		StructuringCeil:   1_000_000_000,
	}
	input, err := repository.PaymentTransactionInput(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raised, err := repository.RunTransactionMonitorForPayment(ctx, input, cfg); err != nil || raised != 0 {
		t.Fatalf("first payment: raised=%d err=%v", raised, err)
	}
	input, err = repository.PaymentTransactionInput(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	raised, err := repository.RunTransactionMonitorForPayment(ctx, input, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if raised == 0 {
		t.Fatal("expected structuring alert on second payment")
	}
	alerts, err := repository.ListTransactionAlerts(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Rule != kyc.RuleStructuring {
		t.Fatalf("expected 1 structuring alert, got %+v", alerts)
	}
	// Redelivery of the same event must not re-raise the alert.
	raised, err = repository.RunTransactionMonitorForPayment(ctx, input, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if raised != 0 {
		t.Fatalf("redelivery raised %d alerts, want 0", raised)
	}
	alerts, err = repository.ListTransactionAlerts(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected alerts to stay at 1, got %d", len(alerts))
	}
}

func TestPostgresClaimMerchantNotificationIdempotent(t *testing.T) {
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
	truncateAll(t, ctx, repository)
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, "+2348012340003")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	token, err := domain.NewReceiptToken()
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 50_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
		ProviderReference: domain.NewProviderReference(), ReceiptToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimMerchantNotification(ctx, payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected first claim to succeed")
	}
	claimed, err = repository.ClaimMerchantNotification(ctx, payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("expected second claim to be rejected")
	}
}

func truncateAll(t *testing.T, ctx context.Context, repository *Store) {
	t.Helper()
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE business_event_outbox,transaction_alerts,risk_events,manual_review_cases,
		         message_outbox,inbound_messages,webhook_deliveries,payment_events,payments,
		         conversation_sessions,users,merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
}
