package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
)

func TestPostgresPaymentLifecycleAndDeduplication(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE message_outbox,inbound_messages,webhook_deliveries,payment_events,payments,
		         conversation_sessions,users,merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	user, err := repository.GetOrCreateUser(ctx, "+2348012345678")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateUserName(ctx, user.ID, "Demo User"); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateUserEmail(ctx, user.ID, "demo@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := repository.EnqueueInboundMessage(ctx, InboundMessage{ID: "wamid.1", Sender: user.WhatsAppNumber, Text: "hello"})
	if err != nil || !fresh {
		t.Fatalf("first inbound insert: fresh=%v err=%v", fresh, err)
	}
	fresh, err = repository.EnqueueInboundMessage(ctx, InboundMessage{ID: "wamid.1", Sender: user.WhatsAppNumber, Text: "hello"})
	if err != nil || fresh {
		t.Fatalf("duplicate inbound insert: fresh=%v err=%v", fresh, err)
	}
	inbound, err := repository.ClaimInboundMessages(ctx, 10)
	if err != nil || len(inbound) != 1 {
		t.Fatalf("claim inbound: count=%d err=%v", len(inbound), err)
	}
	if err := repository.CompleteInboundMessage(ctx, inbound[0].ID); err != nil {
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
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "duplicate", nil); err != nil || changed {
		t.Fatalf("idempotent transition: changed=%v err=%v", changed, err)
	}
	if _, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusSucceeded, "invalid", nil); err == nil {
		t.Fatal("invalid transition should fail")
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusInitialized, "test", nil); err != nil || !changed {
		t.Fatalf("initialize transition: changed=%v err=%v", changed, err)
	}
	if changed, err := repository.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "test", nil, OutboxSpec{
		UserID: user.ID, Recipient: user.WhatsAppNumber, Kind: "text", Payload: []byte(`{"body":"success"}`),
	}); err != nil || !changed {
		t.Fatalf("terminal transaction: changed=%v err=%v", changed, err)
	}
	messages, err := repository.ClaimOutbox(ctx, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("claim outbox: count=%d err=%v", len(messages), err)
	}
	if err := repository.CompleteOutbox(ctx, messages[0].ID); err != nil {
		t.Fatal(err)
	}

	webhookID, fresh, err := repository.RecordWebhook(ctx, "paystack", "event-1", true, []byte(`{"event":"charge.success"}`))
	if err != nil || !fresh || webhookID == 0 {
		t.Fatalf("record webhook: id=%d fresh=%v err=%v", webhookID, fresh, err)
	}
	_, fresh, err = repository.RecordWebhook(ctx, "paystack", "event-1", true, []byte(`{}`))
	if err != nil || fresh {
		t.Fatalf("duplicate webhook: fresh=%v err=%v", fresh, err)
	}
}

func TestPostgresRetentionKeepsRecentlyActiveUsers(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE users CASCADE`); err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, "+2348099999999")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE users SET created_at=$2,updated_at=now() WHERE id=$1`, user.ID, time.Now().Add(-180*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PurgeBefore(ctx, time.Now().Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`, user.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("recently active old user should survive retention")
	}
}
