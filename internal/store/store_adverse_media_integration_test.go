package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/kyc"
)

// TestAdverseMediaScreeningRecordsAndCases covers the G9 adverse-media store
// path: a non-clear determination is persisted and opens (idempotently) one
// 'adverse_media' review case, a clear determination opens none, and the rows
// are listed newest first.
func TestAdverseMediaScreeningRecordsAndCases(t *testing.T) {
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
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key[:])
	// Unique phone per run so a re-used identity never leaks across runs.
	user, err := repository.GetOrCreateUser(ctx, fmt.Sprintf("+2348023%06d", time.Now().UnixNano()%1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(ctx, `DELETE FROM adverse_media_screenings WHERE user_id=$1`, user.ID)
		_, _ = repository.pool.Exec(ctx, `DELETE FROM manual_review_cases WHERE user_id=$1`, user.ID)
		_, _ = repository.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, user.ID)
	})

	openCases := func() int {
		var n int
		if err := repository.pool.QueryRow(ctx,
			`SELECT count(*) FROM manual_review_cases WHERE user_id=$1 AND case_type='adverse_media' AND status='pending'`,
			user.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1) A non-clear determination persists the row and opens exactly one case.
	strong, err := repository.RecordAdverseMediaScreening(ctx, AdverseMediaScreening{
		UserID:   user.ID,
		Provider: "simulated",
		Decision: kyc.ScreenStrong,
		MatchedHits: []AdverseMediaHit{{
			Source: "OpenSanctions ng_nigsac_sanctions (simulated)", Title: "record flagged for adverse media",
			ProviderRef: "sim-mediawatch-1", ListVersion: "sim-1",
		}},
		MatchedNames: []string{"record flagged for adverse media"},
		LegalName:    "Adverse Mediawatch Ltd",
		ListVersion:  "sim-1",
		ProviderRef:  "sim-mediawatch-1",
		Message:      "resemblance",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strong.ID == 0 {
		t.Fatal("expected a persisted screening id")
	}
	if got := openCases(); got != 1 {
		t.Fatalf("non-clear screening opened %d cases, want 1", got)
	}

	// 2) A second non-clear determination must not open a duplicate case.
	if _, err := repository.RecordAdverseMediaScreening(ctx, AdverseMediaScreening{
		UserID: user.ID, Provider: "simulated", Decision: kyc.ScreenBlocked,
		LegalName: "Adverse Mediawatch Ltd", Message: "blocked",
	}); err != nil {
		t.Fatal(err)
	}
	if got := openCases(); got != 1 {
		t.Fatalf("repeat non-clear screening opened %d cases, want 1 (idempotent)", got)
	}

	// 3) A clear determination is recorded but opens no case.
	clear, err := repository.RecordAdverseMediaScreening(ctx, AdverseMediaScreening{
		UserID: user.ID, Provider: "simulated", Decision: kyc.ScreenClear,
		LegalName: "Adverse Mediawatch Ltd", Message: "no match",
	})
	if err != nil {
		t.Fatal(err)
	}
	if clear.ID == 0 {
		t.Fatal("expected the clear screening to persist")
	}
	if got := openCases(); got != 1 {
		t.Fatalf("clear screening changed open cases to %d, want 1", got)
	}

	// 4) The user's rows are listed newest first and carry the hit payload.
	rows, err := repository.AdverseMediaScreeningsByUser(ctx, user.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0].Decision != kyc.ScreenClear {
		t.Fatalf("newest row = %q, want clear (newest first)", rows[0].Decision)
	}
	if rows[2].Decision != kyc.ScreenStrong || len(rows[2].MatchedHits) != 1 {
		t.Fatalf("oldest row = %q hits=%d, want strong with 1 hit", rows[2].Decision, len(rows[2].MatchedHits))
	}
	if rows[2].MatchedHits[0].ProviderRef != "sim-mediawatch-1" {
		t.Fatalf("hit provider ref = %q", rows[2].MatchedHits[0].ProviderRef)
	}

	// 5) The global list also sees the rows.
	all, err := repository.ListAdverseMediaScreenings(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, scr := range all {
		if scr.UserID == user.ID {
			seen++
		}
	}
	if seen != 3 {
		t.Fatalf("global list saw %d of the user's rows, want 3", seen)
	}
}
