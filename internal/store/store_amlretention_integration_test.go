package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
)

// G6 (AML spec §5): the MLPPA 2022 record-keeping floor. The retention purge
// must refuse to hard-delete AML-scope records younger than five years even
// when the caller asks for an older cutoff; financial classes must still be
// archived before any purge that does run; a re-run after a simulated
// crash-between-archive-and-delete must converge instead of doubling ledger
// rows; and merchant registrations must leave an archived KYB/CDD snapshot
// behind. Store.AMLRecordRetention is zero on a bare Open, so these tests
// set it explicitly the way the app wires it from AML_RECORD_RETENTION.
func TestAMLRecordRetentionFloorClampsPurge(t *testing.T) { //nolint:parallel // shared scratch database
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
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `TRUNCATE users CASCADE`); err != nil {
		t.Fatal(err)
	}
	// payments.merchant_id is a real FK: take a seeded merchant for the
	// payment rows this test mints.
	var merchantID uuid.UUID
	if err := repository.pool.QueryRow(ctx, `SELECT id FROM merchants ORDER BY name LIMIT 1`).Scan(&merchantID); err != nil {
		t.Fatal(err)
	}
	// The floor under test: everything older than 400 days is fair game,
	// everything younger is not, and the five-year clamp overrides the 400-day
	// ask for AML classes only.
	repository.AMLRecordRetention = 5 * 365 * 24 * time.Hour
	asked := time.Now().Add(-400 * 24 * time.Hour)

	user, err := repository.GetOrCreateUser(ctx, "+2348060010001")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-400 * 24 * time.Hour)
	payment, err := repository.CreatePayment(ctx, domain.Payment{
		ID: uuid.New(), UserID: user.ID, MerchantID: merchantID, AmountKobo: 40_000,
		Currency: "NGN", Status: domain.StatusSucceeded, Provider: "paystack",
		ProviderReference: domain.NewProviderReference(), ReceiptToken: uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A 400-day-old payment: inside the five-year floor, outside the ask.
	if _, err := repository.pool.Exec(ctx, `UPDATE payments SET created_at=$2 WHERE id=$1`, payment.ID, old); err != nil {
		t.Fatal(err)
	}

	report, err := repository.PurgeBefore(ctx, asked)
	if err != nil {
		t.Fatal(err)
	}
	if !report.AMLFloorClamped {
		t.Fatal("expected the AML floor to clamp the 400-day purge request")
	}
	var paymentCount int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE id=$1`, payment.ID).Scan(&paymentCount); err != nil {
		t.Fatal(err)
	}
	if paymentCount != 1 {
		t.Fatal("payment inside the five-year floor must survive retention")
	}
	var ledgerCount int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM archive_ledger WHERE record_type='payment'`).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 0 {
		t.Fatal("no snapshot should exist for a payment the floor protected")
	}

	// Outside the floor: a six-year-old payment is archived, then deleted.
	reallyOld := time.Now().Add(-6 * 365 * 24 * time.Hour)
	if _, err := repository.pool.Exec(ctx, `UPDATE payments SET created_at=$2 WHERE id=$1`, payment.ID, reallyOld); err != nil {
		t.Fatal(err)
	}
	// The second purge uses a cutoff genuinely past the floor: the clamp
	// must not fire, and the payment must archive once and purge once.
	report, err = repository.PurgeBefore(ctx, time.Now().Add(-6*365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.AMLFloorClamped {
		t.Fatal("a six-year ask against a five-year floor is past the boundary and must not be clamped")
	}
	if report.PaymentsArchived != 1 || report.PaymentsPurged != 1 {
		t.Fatalf("six-year-old payment should archive once and purge once: %+v", report)
	}
	var stillThere int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE id=$1`, payment.ID).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 0 {
		t.Fatal("six-year-old payment must be purged")
	}

	// Re-run convergence: purge again and the ledger must not gain a row.
	report, err = repository.PurgeBefore(ctx, asked)
	if err != nil {
		t.Fatal(err)
	}
	if report.PaymentsArchived != 0 {
		t.Fatalf("re-run must not archive again, got %+v", report)
	}
	var ledgerAfter int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM archive_ledger WHERE record_type='payment'`).Scan(&ledgerAfter); err != nil {
		t.Fatal(err)
	}
	if ledgerAfter != 1 {
		t.Fatalf("archive ledger must keep exactly one snapshot, got %d", ledgerAfter)
	}
}

// The 90-day operational purge (the shipped RETENTION_PERIOD default) must
// now leave a merchant-registration snapshot behind instead of silently
// destroying a KYB/CDD record.
func TestMerchantRegistrationArchivedBeforePurge(t *testing.T) { //nolint:parallel // shared scratch database
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
	// A bare Open has a zero floor: the pre-G6 harness behaviour, so the
	// 90-day-style purge below runs unclamped.
	user, err := repository.GetOrCreateUser(ctx, "+2348060010002")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-400 * 24 * time.Hour)
	if _, err := repository.pool.Exec(ctx, `UPDATE users SET created_at=$2,updated_at=$2 WHERE id=$1`, user.ID, old); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateMerchantRegistration(ctx, user.ID, "G6 Test Traders", "retail", "KYB record for the archive test", "g6@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE merchant_registrations SET created_at=$2 WHERE user_id=$1`, user.ID, old); err != nil {
		t.Fatal(err)
	}

	report, err := repository.PurgeBefore(ctx, old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.OperationalPurged < 1 {
		t.Fatalf("merchant registration should count as purged, got %+v", report)
	}
	var live int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM merchant_registrations WHERE user_id=$1`, user.ID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatal("400-day-old merchant registration should be purged")
	}
	var snapshotted int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM archive_ledger WHERE record_type='merchant_registration'`).Scan(&snapshotted); err != nil {
		t.Fatal(err)
	}
	if snapshotted != 1 {
		t.Fatalf("exactly one merchant-registration snapshot expected, got %d", snapshotted)
	}
	var payload []byte
	if err := repository.pool.QueryRow(ctx, `SELECT payload FROM archive_ledger WHERE record_type='merchant_registration' LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"business_name", "reference", "status", "contact_email"} {
		if !strings.Contains(string(payload), field) {
			t.Fatalf("snapshot payload missing %s: %s", field, payload)
		}
	}
}

// A crash between the archive insert and the delete (the transaction boundary
// the guarded writer protects) must converge on re-run.
func TestArchiveGuardedIsIdempotent(t *testing.T) { //nolint:parallel // shared scratch database
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE archive_ledger`); err != nil {
		t.Fatal(err)
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"simulated":"crash"}`)
	if err := repository.appendArchiveGuarded(ctx, tx, "merchant_registration", uuid.NewString(), payload); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var firstCount int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM archive_ledger WHERE record_type='merchant_registration'`).Scan(&firstCount); err != nil {
		t.Fatal(err)
	}
	if firstCount != 1 {
		t.Fatalf("expected one snapshot after the first guarded write, got %d", firstCount)
	}
	// The second guarded write for the same (record_type, subject_id) is the
	// re-run case: no duplicate, no error.
	tx2, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.appendArchiveGuarded(ctx, tx2, "merchant_registration", "duplicate-subject", payload); err != nil {
		t.Fatal(err)
	}
	if err := repository.appendArchiveGuarded(ctx, tx2, "merchant_registration", "duplicate-subject", payload); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var finalCount int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM archive_ledger WHERE record_type='merchant_registration' AND subject_id='duplicate-subject'`).Scan(&finalCount); err != nil {
		t.Fatal(err)
	}
	if finalCount != 1 {
		t.Fatalf("guarded append must be idempotent, got %d rows", finalCount)
	}
}

