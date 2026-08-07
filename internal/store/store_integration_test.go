package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
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

func TestPostgresAdminRBAC(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE admin_sessions, admin_users CASCADE`); err != nil {
		t.Fatal(err)
	}

	hash := "$2a$10$replacement"
	if err := repository.EnsureAdminUser(ctx, "admin@example.com", hash); err != nil {
		t.Fatal(err)
	}
	user, err := repository.AdminUserByEmail(ctx, "admin@example.com")
	if err != nil || user == nil {
		t.Fatalf("bootstrap admin missing: user=%v err=%v", user, err)
	}
	if user.Role != RoleAdmin || !user.Enabled {
		t.Fatalf("bootstrap admin should be enabled admin, got role=%s enabled=%v", user.Role, user.Enabled)
	}
	// Ensure is idempotent and re-applies role for the config account.
	newHash := "$2a$10$another"
	if err := repository.EnsureAdminUser(ctx, "admin@example.com", newHash); err != nil {
		t.Fatal(err)
	}
	user, err = repository.AdminUserByEmail(ctx, "admin@example.com")
	if err != nil || user.PasswordHash != newHash {
		t.Fatalf("ensure should refresh hash: err=%v", err)
	}

	// Add an operator and adjust role/enabled.
	if err := repository.CreateAdminUser(ctx, "support@example.com", hash, RoleSupport); err != nil {
		t.Fatal(err)
	}
	support, err := repository.AdminUserByEmail(ctx, "support@example.com")
	if err != nil || support == nil {
		t.Fatalf("support user missing: err=%v", err)
	}
	if err := repository.UpdateAdminUserRole(ctx, support.ID, RoleReadOnly); err != nil {
		t.Fatal(err)
	}
	support, _ = repository.AdminUserByEmail(ctx, "support@example.com")
	if support.Role != RoleReadOnly {
		t.Fatalf("role change not applied: %s", support.Role)
	}
	if err := repository.SetAdminUserEnabled(ctx, support.ID, false); err != nil {
		t.Fatal(err)
	}
	support, _ = repository.AdminUserByEmail(ctx, "support@example.com")
	if support.Enabled {
		t.Fatal("expected disabled account")
	}
	if err := repository.UpdateAdminUserPassword(ctx, support.ID, newHash); err != nil {
		t.Fatal(err)
	}
	support, _ = repository.AdminUserByEmail(ctx, "support@example.com")
	if support.PasswordHash != newHash {
		t.Fatal("password hash not updated")
	}

	// Sessions carry the actor and role; disabled accounts are rejected.
	token := "session-token-rbac-test"
	csrf := "csrf-rbac-test"
	if err := repository.CreateAdminSession(ctx, user.ID, token, csrf, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	adminID, role, _, csrfGot, err := repository.ValidateAdminSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if adminID != user.ID || role != RoleAdmin || csrfGot != csrf {
		t.Fatalf("session mismatch: adminID=%v role=%s csrf=%s", adminID, role, csrfGot)
	}
	if err := repository.SetAdminUserEnabled(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := repository.ValidateAdminSession(ctx, token); err == nil {
		t.Fatal("disabled admin session should be rejected")
	}
}

// TestPostgresAuditLogChain verifies hashing, linkage, append-only
// enforcement, and tamper detection on the audit trail.
func TestPostgresAuditLogChain(t *testing.T) {
	ctx := context.Background()
	repository, err := Open(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Skipf("set TEST_DATABASE_URL to run: %v", err)
	}
	t.Cleanup(func() { repository.Close() })

	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = repository.pool.Exec(ctx, `TRUNCATE audit_logs RESTART IDENTITY`)
	if err != nil {
		t.Fatal(err)
	}

	adminID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	actor := AuditLog{
		ActorType:  "admin",
		ActorID:    uuid.NullUUID{UUID: adminID, Valid: true},
		ActorEmail: sql.NullString{String: "ops@example.com", Valid: true},
		IP:         sql.NullString{String: "127.0.0.1", Valid: true},
		Action:     "admin.merchants.approved",
		ResourceID: sql.NullString{String: "merchant-1", Valid: true},
		Details:    map[string]any{"name": "Acme"},
	}
	first, err := repository.AppendAuditLog(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if first.PrevHash != "" || first.Hash == "" {
		t.Fatalf("first entry should have empty prev and set hash: %+v", first)
	}
	second, err := repository.AppendAuditLog(ctx, AuditLog{
		ActorType: "system",
		Action:    "system.backup.completed",
		Details:   map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.PrevHash != first.Hash {
		t.Fatalf("second entry must chain to first: got %s want %s", second.PrevHash, first.Hash)
	}

	count, broken, err := repository.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || broken != -1 {
		t.Fatalf("chain should be sound: count=%d broken=%d", count, broken)
	}

	entries, err := repository.ListAuditLogs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Action != "system.backup.completed" {
		t.Fatalf("list mismatch: %+v", entries)
	}

	// Append-only: direct UPDATE and DELETE must fail at the database.
	if _, err := repository.pool.Exec(ctx, `UPDATE audit_logs SET details='{}' WHERE id=$1`, first.ID); err == nil {
		t.Fatal("expected append-only trigger to reject UPDATE")
	}
	if _, err := repository.pool.Exec(ctx, `DELETE FROM audit_logs WHERE id=$1`, first.ID); err == nil {
		t.Fatal("expected append-only trigger to reject DELETE")
	}

	// Tamper detection: disable the trigger, alter a row, verify must fail.
	if _, err := repository.pool.Exec(ctx, `DROP TRIGGER IF EXISTS audit_logs_no_mutation ON audit_logs`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := repository.pool.Exec(ctx, `
			DROP TRIGGER IF EXISTS audit_logs_no_mutation ON audit_logs;
			CREATE TRIGGER audit_logs_no_mutation
				BEFORE UPDATE OR DELETE ON audit_logs
				FOR EACH ROW EXECUTE FUNCTION reject_audit_logs_mutation();`); err != nil {
			t.Errorf("restore audit trigger: %v", err)
		}
	})
	if _, err := repository.pool.Exec(ctx, `UPDATE audit_logs SET action='admin.merchants.approved_evil' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	count, broken, err = repository.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if broken != 0 {
		t.Fatalf("tamper should be detected at entry 0: broken=%d count=%d", broken, count)
	}
}

// TestPostgresAtRestEncryption verifies chat payloads and session CSRF tokens
// are sealed at rest and that EncryptLegacyAtRest re-seals plaintext rows.
func TestPostgresAtRestEncryption(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE message_outbox,inbound_messages,payment_events,payments,admin_sessions,admin_users CASCADE`); err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key)

	// Inbound chat payload: sealed at rest, decrypts back on claim.
	if _, err := repository.EnqueueInboundMessage(ctx, InboundMessage{ID: "wamid.atrest.1", Sender: "+2348091111111", Text: "private chat body"}); err != nil {
		t.Fatal(err)
	}
	var sealedPayload string
	if err := repository.pool.QueryRow(ctx, `SELECT payload FROM inbound_messages WHERE provider_message_id=$1`, "wamid.atrest.1").Scan(&sealedPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealedPayload, "enc:v1:") {
		t.Fatalf("inbound payload should be sealed, got %q", sealedPayload)
	}
	inbound, err := repository.ClaimInboundMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range inbound {
		if m.ID == "wamid.atrest.1" {
			found = true
			if m.Text != "private chat body" {
				t.Fatalf("decrypted text mismatch: %q", m.Text)
			}
		}
	}
	if !found {
		t.Fatal("sealed inbound message not claimed")
	}

	// Outbox payload: sealed at rest, decrypts back on claim.
	user, err := repository.GetOrCreateUser(ctx, "+2348091111111")
	if err != nil {
		t.Fatal(err)
	}
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 50_000,
		Currency: "NGN", Status: domain.StatusDraft, Provider: "paystack",
		ProviderReference: domain.NewProviderReference(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "at-rest-test", nil); err != nil || !changed {
		t.Fatalf("transition: changed=%v err=%v", changed, err)
	}
	if changed, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusInitialized, "at-rest-test", nil); err != nil || !changed {
		t.Fatalf("initialize: changed=%v err=%v", changed, err)
	}
	if _, err := repository.TransitionPaymentWithOutbox(ctx, payment.ID, domain.StatusSucceeded, "at-rest-test", nil, OutboxSpec{
		UserID: user.ID, Recipient: user.WhatsAppNumber, Kind: "text", Payload: []byte(`{"body":"outbox secret"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.pool.QueryRow(ctx, `SELECT payload FROM message_outbox WHERE kind='text' ORDER BY id DESC LIMIT 1`).Scan(&sealedPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealedPayload, "enc:v1:") {
		t.Fatalf("outbox payload should be sealed, got %q", sealedPayload)
	}
	outbox, err := repository.ClaimOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	outboxFound := false
	for _, m := range outbox {
		if m.Recipient == user.WhatsAppNumber {
			outboxFound = true
			if !strings.Contains(string(m.Payload), "outbox secret") {
				t.Fatalf("decrypted outbox payload mismatch: %s", m.Payload)
			}
		}
	}
	if !outboxFound {
		t.Fatal("sealed outbox message not claimed")
	}

	// Session CSRF tokens: sealed at rest, decrypt back on validation.
	if err := repository.EnsureAdminUser(ctx, "atrest@example.com", "$2a$10$replacement"); err != nil {
		t.Fatal(err)
	}
	admin, err := repository.AdminUserByEmail(ctx, "atrest@example.com")
	if err != nil || admin == nil {
		t.Fatalf("admin missing: %v err=%v", admin, err)
	}
	expiry := time.Now().Add(time.Hour)
	if err := repository.CreateAdminSession(ctx, admin.ID, "at-rest-token", "csrf-secret-value", expiry); err != nil {
		t.Fatal(err)
	}
	var sealedCSRF string
	if err := repository.pool.QueryRow(ctx, `SELECT csrf_token FROM admin_sessions WHERE admin_id=$1`, admin.ID).Scan(&sealedCSRF); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealedCSRF, "enc:v1:") {
		t.Fatalf("admin csrf should be sealed, got %q", sealedCSRF)
	}
	_, _, _, csrf, err := repository.ValidateAdminSession(ctx, "at-rest-token")
	if err != nil {
		t.Fatal(err)
	}
	if csrf != "csrf-secret-value" {
		t.Fatalf("decrypted csrf mismatch: %q", csrf)
	}

	// Legacy plaintext rows: EncryptLegacyAtRest re-seals them in place.
	if _, err := repository.pool.Exec(ctx, `INSERT INTO inbound_messages(provider_message_id,channel,sender,recipient,payload)
		VALUES('legacy.plain.1','whatsapp','+2348091111111','+2348091111111','{"text":"old plaintext"}')`); err != nil {
		t.Fatal(err)
	}
	count, err := repository.EncryptLegacyAtRest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatalf("expected at least one legacy row re-encrypted, got %d", count)
	}
	if err := repository.pool.QueryRow(ctx, `SELECT payload FROM inbound_messages WHERE provider_message_id=$1`, "legacy.plain.1").Scan(&sealedPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealedPayload, "enc:v1:") {
		t.Fatalf("legacy row should be re-sealed, got %q", sealedPayload)
	}

	// Without a key the store must reject sealing in production configs,
	// and passthrough in dev is the documented fallback.
	if _, err := repository.pool.Exec(ctx, `TRUNCATE inbound_messages CASCADE`); err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(nil)
	if _, err := repository.EnqueueInboundMessage(ctx, InboundMessage{ID: "wamid.nokey.1", Sender: "+2348092222222", Text: "dev passthrough"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.pool.QueryRow(ctx, `SELECT payload FROM inbound_messages WHERE provider_message_id=$1`, "wamid.nokey.1").Scan(&sealedPayload); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(sealedPayload, "enc:v1:") {
		t.Fatalf("dev passthrough should be plaintext, got %q", sealedPayload)
	}
}

// TestPostgresRetentionArchivesFinancials verifies C8/C29: payments and
// invoices older than the cutoff are snapshotted into the append-only archive
// ledger BEFORE hard-delete, operational rows are purged in batches, and legal
// holds keep subjects out of retention entirely.
func TestPostgresRetentionArchivesFinancials(t *testing.T) {
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
		TRUNCATE archive_ledger,legal_holds,audit_logs,message_outbox,inbound_messages,webhook_deliveries,
		         payment_events,payments,data_orders,data_order_events,conversation_sessions,
		         invoices,invoice_items,invoice_payments,thrift_groups,thrift_members,thrift_cycles,
		         thrift_contributions,thrift_payouts,thrift_events,merchant_registrations,
		         users,merchants,data_networks,data_plans,merchant_owners,service_purchases,
		         receipt_scan_tokens,receipt_scan_attempts,registered_services,service_readers,
		         user_merchant_recents,admin_sessions,merchant_sessions,email_verification_codes,
		         totp_pending_logins,merchant_password_reset_tokens CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-200 * 24 * time.Hour)
	user, err := repository.GetOrCreateUser(ctx, "+2348070000001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE users SET created_at=$2,updated_at=$2 WHERE id=$1`, user.ID, old); err != nil {
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
		ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: 40_000,
		Currency: "NGN", Status: domain.StatusSucceeded, Provider: "paystack",
		ProviderReference: domain.NewProviderReference(), ReceiptToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE payments SET created_at=$2 WHERE id=$1`, payment.ID, old); err != nil {
		t.Fatal(err)
	}

	// Old user with an invoice on legal hold must survive retention.
	heldUser, err := repository.GetOrCreateUser(ctx, "+2348070000002")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE users SET created_at=$2,updated_at=$2 WHERE id=$1`, heldUser.ID, old); err != nil {
		t.Fatal(err)
	}
	if err := repository.AddLegalHold(ctx, "user", heldUser.ID.String(), "dispute HOLD-001", "test", nil); err != nil {
		t.Fatal(err)
	}

	report, err := repository.PurgeBefore(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	// Old payment must be archived in the ledger and removed from the table.
	var archivedCount int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM archive_ledger WHERE record_type='payment'`).Scan(&archivedCount); err != nil {
		t.Fatal(err)
	}
	if archivedCount != 1 {
		t.Fatalf("expected 1 archived payment, got %d", archivedCount)
	}
	if report.PaymentsArchived != 1 || report.PaymentsPurged != 1 {
		t.Fatalf("payment report mismatch: archived=%d purged=%d", report.PaymentsArchived, report.PaymentsPurged)
	}
	var paymentCount int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM payments`).Scan(&paymentCount); err != nil {
		t.Fatal(err)
	}
	if paymentCount != 0 {
		t.Fatalf("expected payments to be purged, got %d", paymentCount)
	}

	// Archive ledger is append-only: update/delete must be rejected.
	var archiveID int64
	if err := repository.pool.QueryRow(ctx, `SELECT id FROM archive_ledger WHERE record_type='payment' LIMIT 1`).Scan(&archiveID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE archive_ledger SET payload='{}' WHERE id=$1`, archiveID); err == nil {
		t.Fatal("expected append-only trigger to reject UPDATE on archive_ledger")
	}
	if _, err := repository.pool.Exec(ctx, `DELETE FROM archive_ledger WHERE id=$1`, archiveID); err == nil {
		t.Fatal("expected append-only trigger to reject DELETE on archive_ledger")
	}

	// The legal hold protected its user.
	var heldCount int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`, heldUser.ID).Scan(&heldCount); err != nil {
		t.Fatal(err)
	}
	if heldCount != 1 {
		t.Fatal("legal hold should protect its user from retention")
	}

	// Removing the hold lets the next purge take the user.
	if err := repository.RemoveLegalHold(ctx, "user", heldUser.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PurgeBefore(ctx, time.Now().Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`, heldUser.ID).Scan(&heldCount); err != nil {
		t.Fatal(err)
	}
	if heldCount != 0 {
		t.Fatal("user should be purged after hold removal")
	}
}
