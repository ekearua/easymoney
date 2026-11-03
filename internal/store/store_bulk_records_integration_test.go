package store

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
)

// TestUpsertBulkRecipientRoundTrip guards the records upload path: the insert
// must supply exactly one argument per placeholder, and every template field
// (including Bank Code and geography) must survive the round trip.
func TestUpsertBulkRecipientRoundTrip(t *testing.T) {
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

	merchant, err := createTestMerchant(ctx, repository, "records-"+uuid.NewString(), "Records Merchant")
	if err != nil {
		t.Fatal(err)
	}

	in := BulkRecipientInput{
		PhoneE164:     "07061234567",
		DisplayName:   "Ahmed Musa",
		Source:        "upload",
		SourceFile:    "Records Template.xlsx",
		BVN:           "21234567",
		NIN:           "12345678890",
		VIN:           "1234545677888",
		AccountNumber: "2033334455",
		BankName:      "UBA",
		BankCode:      "058",
		AccountName:   "Ahmed Musa",
		State:         "Taraba",
		LGA:           "Jalingo",
		Ward:          "Ward 2",
		PollingUnit:   "Polling unit 2",
	}
	row, err := repository.UpsertBulkRecipient(ctx, merchant.ID, in)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if row.PhoneE164 != "+2347061234567" || row.BankCode != "058" || row.State != "Taraba" || row.PollingUnit != "Polling unit 2" {
		t.Fatalf("unexpected inserted row: %+v", row)
	}
	if row.VerificationStatus != RecipientUnverified {
		t.Fatalf("verification = %q, want %q", row.VerificationStatus, RecipientUnverified)
	}

	in.DisplayName = "Ahmed M Musa"
	updated, err := repository.UpsertBulkRecipient(ctx, merchant.ID, in)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ID != row.ID || updated.DisplayName != "Ahmed M Musa" {
		t.Fatalf("unexpected updated row: %+v", updated)
	}
}
