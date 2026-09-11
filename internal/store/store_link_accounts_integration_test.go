package store

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// linkTestDigest mirrors the service's linkCodeDigest so the store test can
// build a candidate and hash without importing the service package.
func linkTestDigest(phone, code string) []byte {
	sum := sha256.Sum256([]byte("+" + phone + ":" + code))
	return sum[:]
}

func TestPostgresAccountLinking(t *testing.T) {
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
		TRUNCATE message_outbox,inbound_messages,payment_events,payments,
		         conversation_sessions,users,merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	// The WhatsApp row is the surviving primary account.
	primary, err := repository.GetOrCreateUser(ctx, "+2348012345678")
	if err != nil {
		t.Fatal(err)
	}
	// The Instagram row is the channel the user is linking from.
	source, err := repository.GetOrCreateInstagramUser(ctx, "1896009485081509", "xego_tester")
	if err != nil {
		t.Fatal(err)
	}

	// A link request is created bound to the claimed phone number.
	phone := "+2348012345678"
	code := "123456"
	codeHash, err := bcrypt.GenerateFromPassword(linkTestDigest(phone, code), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateLinkRequest(ctx, source.ID, "instagram", phone, codeHash, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Wrong code increments attempts and fails verification.
	if ok, err := repository.VerifyLinkRequest(ctx, source.ID, "instagram", phone, linkTestDigest(phone, "000000")); err != nil || ok {
		t.Fatalf("wrong code: ok=%v err=%v", ok, err)
	}
	// Correct code consumes the request.
	if ok, err := repository.VerifyLinkRequest(ctx, source.ID, "instagram", phone, linkTestDigest(phone, code)); err != nil || !ok {
		t.Fatalf("correct code: ok=%v err=%v", ok, err)
	}
	// A consumed request cannot be used again.
	if ok, err := repository.VerifyLinkRequest(ctx, source.ID, "instagram", phone, linkTestDigest(phone, code)); err != nil || ok {
		t.Fatalf("replayed code: ok=%v err=%v", ok, err)
	}

	// Merging folds the Instagram handle into the primary and tombstones source.
	if err := repository.MergeUsers(ctx, source.ID, primary.ID, "instagram", "1896009485081509", "xego_tester"); err != nil {
		t.Fatal(err)
	}

	resolved, err := repository.FindUserByChannelHandle(ctx, "instagram", "1896009485081509")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != primary.ID {
		t.Fatalf("instagram handle resolved to %s, want primary %s", resolved.ID, primary.ID)
	}
	if !resolved.InstagramConfirmedAt.Valid {
		t.Fatal("expected instagram_confirmed_at stamped on the primary")
	}

	tombstone, err := repository.UserByID(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !tombstone.MergedIntoID.Valid || tombstone.MergedIntoID.UUID != primary.ID {
		t.Fatalf("source tombstone merged_into_id = %v, want %s", tombstone.MergedIntoID, primary.ID)
	}
	if tombstone.InstagramIGSID.Valid {
		t.Fatalf("tombstone must release its instagram handle, got %q", tombstone.InstagramIGSID.String)
	}

	// A fresh resolve of the handle returns the primary, not a new row.
	if err := repository.MergeUsers(ctx, primary.ID, primary.ID, "instagram", "", ""); err != nil {
		t.Fatalf("merging a user into itself should be a no-op, got %v", err)
	}
	if resolved2, err := repository.FindUserByChannelHandle(ctx, "instagram", "1896009485081509"); err != nil || resolved2.ID != primary.ID {
		t.Fatalf("handle moved off primary after no-op merge: id=%v err=%v", resolved2.ID, err)
	}
}
