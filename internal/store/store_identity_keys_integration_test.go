package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
)

// TestIdentityKeyDerivation pins the key's shape without a database: the key is
// digit-normalised (so a spaced BVN hashes the same as a bare one), the two
// identity types occupy separate key spaces, and a non-numeric input makes no
// uniqueness claim at all.
func TestIdentityKeyDerivation(t *testing.T) {
	t.Parallel()
	if a, b := IdentityKey(kyc.EvBVNVerified, "2222 222 2222"), IdentityKey(kyc.EvBVNVerified, "22222222222"); a != b {
		t.Errorf("spacing must not change the key: %q != %q", a, b)
	}
	if IdentityKey(kyc.EvBVNVerified, "not-a-number") != "" {
		t.Error("a non-numeric input must yield no identity key")
	}
	if IdentityKey(kyc.EvBVNVerified, "22222222222") == IdentityKey(kyc.EvNINVerified, "22222222222") {
		t.Error("BVN and NIN must be separate key spaces")
	}
	if !IdentityKeySupported(kyc.EvBVNVerified) || !IdentityKeySupported(kyc.EvNINVerified) {
		t.Error("BVN and NIN are identity keys")
	}
	if IdentityKeySupported(kyc.EvGovPhotoID) || IdentityKeySupported(kyc.EvEDDCompleted) {
		t.Error("only BVN and NIN participate in the duplicate-identity check")
	}
}

// G11 (AML spec §3.7): duplicate-identity detection. One person -> one
// customer. The first profile to verify a BVN owns it; a second profile
// presenting the same number is refused with ErrIdentityInUse, records no
// evidence, and the collision opens one identity review case (idempotently).
func TestIdentityKeyUniqueness(t *testing.T) { //nolint:parallel // shared scratch database
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
	if _, err := repository.pool.Exec(ctx,
		`TRUNCATE identity_keys, manual_review_cases, customer_verifications`); err != nil {
		t.Fatal(err)
	}
	first := insertIdentityTestUser(t, ctx, repository, "First")
	second := insertIdentityTestUser(t, ctx, repository, "Second")

	bvn := fmt.Sprintf("22%09d", time.Now().UnixNano()%1_000_000_000)

	// The first profile claims the BVN, and the overlap query names it.
	if _, err := repository.RecordCustomerVerification(ctx, CustomerVerification{
		UserID: first, VerificationType: kyc.EvBVNVerified, Provider: "simulated",
		Result: map[string]any{"id_type": "bvn", "id_number": bvn},
	}); err != nil {
		t.Fatalf("first claim must succeed: %v", err)
	}
	owner, found, err := repository.IdentityKeyOwner(ctx, kyc.EvBVNVerified, bvn)
	if err != nil || !found || owner != first {
		t.Fatalf("overlap query must name the first profile (owner=%s found=%v err=%v)", owner, found, err)
	}

	// The same user re-verifying is idempotent, not a conflict.
	if _, err := repository.RecordCustomerVerification(ctx, CustomerVerification{
		UserID: first, VerificationType: kyc.EvBVNVerified, Provider: "simulated",
		Result: map[string]any{"id_type": "bvn", "id_number": bvn},
	}); err != nil {
		t.Fatalf("same-user re-verification must not conflict: %v", err)
	}

	// A second profile on the same BVN is refused.
	if _, err := repository.RecordCustomerVerification(ctx, CustomerVerification{
		UserID: second, VerificationType: kyc.EvBVNVerified, Provider: "simulated",
		Result: map[string]any{"id_type": "bvn", "id_number": bvn},
	}); !errors.Is(err, ErrIdentityInUse) {
		t.Fatalf("second profile on the same BVN must be refused with ErrIdentityInUse, got %v", err)
	}

	var verifications, cases int
	if err := repository.pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_verifications WHERE user_id=$1 AND verification_type=$2`,
		second, kyc.EvBVNVerified).Scan(&verifications); err != nil {
		t.Fatal(err)
	}
	if verifications != 0 {
		t.Fatalf("a refused duplicate must record no evidence, found %d rows", verifications)
	}
	if err := repository.pool.QueryRow(ctx,
		`SELECT count(*) FROM manual_review_cases WHERE user_id=$1 AND case_type='identity'`,
		second).Scan(&cases); err != nil {
		t.Fatal(err)
	}
	if cases != 1 {
		t.Fatalf("a duplicate must open exactly 1 identity case, got %d", cases)
	}

	// A retry stays refused and does not duplicate the open case.
	if _, err := repository.RecordCustomerVerification(ctx, CustomerVerification{
		UserID: second, VerificationType: kyc.EvBVNVerified, Provider: "simulated",
		Result: map[string]any{"id_type": "bvn", "id_number": bvn},
	}); !errors.Is(err, ErrIdentityInUse) {
		t.Fatalf("a retry must stay refused, got %v", err)
	}
	if err := repository.pool.QueryRow(ctx,
		`SELECT count(*) FROM manual_review_cases WHERE user_id=$1 AND case_type='identity'`,
		second).Scan(&cases); err != nil {
		t.Fatal(err)
	}
	if cases != 1 {
		t.Fatalf("a retry must not duplicate the identity case, got %d", cases)
	}

	// A NIN is its own key space: the same digits as a BVN do not collide.
	if _, err := repository.RecordCustomerVerification(ctx, CustomerVerification{
		UserID: second, VerificationType: kyc.EvNINVerified, Provider: "simulated",
		Result: map[string]any{"id_type": "nin", "id_number": bvn},
	}); err != nil {
		t.Fatalf("a NIN with the same digits as a BVN is a different key and must succeed: %v", err)
	}

	// A fresh BVN is free on the second profile.
	other := fmt.Sprintf("33%09d", time.Now().UnixNano()%999_999_999)
	if _, err := repository.RecordCustomerVerification(ctx, CustomerVerification{
		UserID: second, VerificationType: kyc.EvBVNVerified, Provider: "simulated",
		Result: map[string]any{"id_type": "bvn", "id_number": other},
	}); err != nil {
		t.Fatalf("a fresh BVN must be claimable: %v", err)
	}
}

func insertIdentityTestUser(t *testing.T, ctx context.Context, repository *Store, label string) uuid.UUID {
	t.Helper()
	contact := fmt.Sprintf("+234810%06d", time.Now().UnixNano()%1_000_000)
	var userID uuid.UUID
	if err := repository.pool.QueryRow(ctx, `
		INSERT INTO users(id, display_name, whatsapp_number, email)
		VALUES($1,$2,$3,$4)
		RETURNING id`,
		uuid.New(), label+" Identity Test", contact, "identity-"+contact+"@test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return userID
}
