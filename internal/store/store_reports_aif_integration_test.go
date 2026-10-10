package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// G15: AIFReport extracts the platform's accounts-of-record register: active
// merchant settlement accounts only, with the merchant id, holder name,
// account number, bank code, NGN currency, informational reason code, and a
// stable "acct:<id>" reference.
func TestAIFReport(t *testing.T) {
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

	// A unique merchant per run so the account-of-record rows cannot collide
	// with other tests sharing the scratch database.
	suffix := time.Now().UnixNano()
	var merchantID uuid.UUID
	if err := repository.pool.QueryRow(ctx, `
		INSERT INTO merchants (slug, name, category, description)
		VALUES($1, 'AIF Integration Merchant', 'Test', 'AIF report test')
		RETURNING id`, fmt.Sprintf("aif-test-%d", suffix)).Scan(&merchantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(ctx, `DELETE FROM merchants WHERE id=$1`, merchantID)
	})

	var accountID uuid.UUID
	accountNumber := fmt.Sprintf("90%08d", suffix%100_000_000)
	if err := repository.pool.QueryRow(ctx, `
		INSERT INTO merchant_settlement_accounts (merchant_id, bank_code, account_number, account_name, status)
		VALUES($1, '058', $2, 'AIF Test Holder', 'active')
		RETURNING id`, merchantID, accountNumber).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	// A disabled account of record must never appear in the filing.
	disabledNumber := fmt.Sprintf("91%08d", suffix%100_000_000)
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO merchant_settlement_accounts (merchant_id, bank_code, account_number, account_name, status)
		VALUES($1, '011', $2, 'AIF Disabled Holder', 'disabled')`, merchantID, disabledNumber); err != nil {
		t.Fatal(err)
	}

	rows, err := repository.AIFReport(ctx, 1000)
	if err != nil {
		t.Fatalf("AIFReport: %v", err)
	}

	var found bool
	for _, r := range rows {
		if r.AccountNumber == disabledNumber {
			t.Fatal("AIF filing must exclude disabled accounts of record")
		}
		if r.AccountNumber != accountNumber {
			continue
		}
		found = true
		if r.CustomerID != merchantID {
			t.Errorf("CustomerID = %s, want merchant %s", r.CustomerID, merchantID)
		}
		if r.AccountHolder != "AIF Test Holder" {
			t.Errorf("AccountHolder = %q, want %q", r.AccountHolder, "AIF Test Holder")
		}
		if r.BankCode != "058" {
			t.Errorf("BankCode = %q, want 058", r.BankCode)
		}
		if r.Currency != "NGN" {
			t.Errorf("Currency = %q, want NGN", r.Currency)
		}
		if r.ReasonCode != "informational" {
			t.Errorf("ReasonCode = %q, want informational", r.ReasonCode)
		}
		if want := "acct:" + accountID.String(); r.Reference != want {
			t.Errorf("Reference = %q, want %q", r.Reference, want)
		}
		if r.ReportDate.IsZero() {
			t.Error("ReportDate must be stamped at generation")
		}
	}
	if !found {
		t.Fatalf("active account of record %s missing from the AIF filing", accountNumber)
	}
}
