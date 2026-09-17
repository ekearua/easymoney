package store

import (
	"context"
	"crypto/rand"
	"os"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
)

// normalizePageBounds must sanitize (floor at 10 for non-positive, cap at 100)
// without ever silently *downgrading* an explicit large request to 10 — the
// old >25→10 downgrade truncated the merchant picker, the ask-bar matcher,
// and the merchant console's Payments/Invoices pages invisibly.
func TestNormalizePageBounds(t *testing.T) {
	cases := []struct {
		offset, limit    int
		wantOff, wantLim int
	}{
		{0, 0, 0, 10},    // non-positive → default
		{0, -5, 0, 10},   // negative → default
		{0, 10, 0, 10},   // pass-through
		{0, 25, 0, 25},   // pass-through (old code downgraded this to 10)
		{0, 60, 0, 60},   // explicit larger page now honored (old: 10)
		{0, 100, 0, 100}, // at the cap
		{0, 500, 0, 100}, // capped, not downgraded
		{-3, 20, 0, 20},  // negative offset floored
	}
	for _, c := range cases {
		gotOff, gotLim := normalizePageBounds(c.offset, c.limit)
		if gotOff != c.wantOff || gotLim != c.wantLim {
			t.Errorf("normalizePageBounds(%d, %d) = (%d, %d), want (%d, %d)",
				c.offset, c.limit, gotOff, gotLim, c.wantOff, c.wantLim)
		}
	}
}

// seedClampMerchants opens a store, migrates it, wipes the merchants table,
// and inserts n active test merchants.
func seedClampMerchants(t *testing.T, ctx context.Context, n int) *Store {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key[:])
	if _, err := repository.pool.Exec(ctx, `TRUNCATE merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if _, err := repository.pool.Exec(ctx, `INSERT INTO merchants (slug, name, category, description, active)
			VALUES ($1, $2, 'Test', 'page-clamp regression', true)`,
			"clamp-test-"+string(rune('a'+i/26))+string(rune('a'+i%26)),
			"Clamp Test Merchant "+string(rune('A'+i-1))); err != nil {
			t.Fatal(err)
		}
	}
	return repository
}

// TestSearchMerchantsBeyondOnePage seeds 30 active merchants and proves the
// whole list is reachable in one call — the regression behind the pay-flow
// merchant picker and the typed-ask matcher only seeing the first page.
func TestSearchMerchantsBeyondOnePage(t *testing.T) {
	ctx := context.Background()
	repository := seedClampMerchants(t, ctx, 30)

	merchants, hasMore, err := repository.SearchMerchants(ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) != 30 {
		t.Fatalf("expected all 30 merchants in one call, got %d", len(merchants))
	}
	if hasMore {
		t.Fatal("hasMore must be false when everything fit on one page")
	}
	if len(merchants) <= 10 {
		t.Fatalf("a >25 request must not be downgraded to 10 rows; got %d", len(merchants))
	}

	// Sanity: paging still works within the sanitized bounds.
	page2, _, err := repository.SearchMerchants(ctx, "", 25, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 5 {
		t.Fatalf("second page of 30 merchants with page size 25 should hold 5, got %d", len(page2))
	}
}

// TestPaymentsByMerchantIDHonorsLimit proves the merchant console's Payments
// page gets the 50 rows it asks for instead of the old silent 10.
func TestPaymentsByMerchantIDHonorsLimit(t *testing.T) {
	ctx := context.Background()
	repository := seedClampMerchants(t, ctx, 1)
	merchant, err := repository.MerchantBySlug(ctx, "clamp-test-ab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `TRUNCATE payment_events, payments, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, "+2348012349501")
	if err != nil {
		t.Fatal(err)
	}
	const want = 12
	for i := 0; i < want; i++ {
		receipt, err := domain.NewReceiptToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreatePayment(ctx, domain.Payment{
			ID: uuid.New(), UserID: user.ID, MerchantID: merchant.ID, AmountKobo: int64(1000 + i),
			Currency: "NGN", Status: domain.StatusSucceeded, Provider: "interswitch",
			ProviderReference: domain.NewProviderReference(), ReceiptToken: receipt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	payments, err := repository.PaymentsByMerchantID(ctx, merchant.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(payments) != want {
		t.Fatalf("PaymentsByMerchantID(50) returned %d rows, want %d (page clamp downgraded requests to 10?)", len(payments), want)
	}
}
