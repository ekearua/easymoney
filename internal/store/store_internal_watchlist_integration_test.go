package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
)

// G3 (AML spec §5): the owned internal watchlist. The list is CRUD'd by
// admins and enforced in RecordScreeningResult: any screening of a listed
// identity is rewritten to blocked with provider internal_watchlist, the
// kyc_profiles.last_screening_decision write-through sees the interdiction,
// and a plain vendor clear on an unlisted identity is untouched.
func TestInternalWatchlistCRUDAndLookup(t *testing.T) { //nolint:parallel // shared scratch database
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE internal_watchlist`); err != nil {
		t.Fatal(err)
	}
	dob := time.Date(1988, 3, 14, 0, 0, 0, 0, time.UTC)
	added, err := repository.AddInternalWatchlistEntry(ctx, InternalWatchlistEntry{
		FullName:    "Ada   Obi", // deliberately messy: normalisation is the point
		DateOfBirth: &dob,
		PhoneNumber: "+2348060000001",
		Reason:      "confirmed fraud, police ref 1234",
		ListedBy:    "compliance@xego",
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.ID == uuid.Nil {
		t.Fatal("expected a persisted id")
	}

	// Re-adding the same identity updates instead of duplicating.
	again, err := repository.AddInternalWatchlistEntry(ctx, InternalWatchlistEntry{
		FullName: "ada obi", DateOfBirth: &dob,
		Reason: "updated reason", ListedBy: "other@xego",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != added.ID {
		t.Fatalf("re-add must be idempotent: %s vs %s", again.ID, added.ID)
	}
	list, err := repository.ListInternalWatchlist(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Reason != "updated reason" {
		t.Fatalf("re-add must refresh the row in place, got %+v", list)
	}

	// Lookup: normalised name matches, wrong DOB does not, unlisted does not.
	if listed, _, err := repository.IsInternallyWatchlisted(ctx, "ADA OBI", &dob, "", ""); err != nil || !listed {
		t.Fatalf("normalised name+dob should match, listed=%v err=%v", listed, err)
	}
	wrongDOB := dob.AddDate(1, 0, 0)
	if listed, _, err := repository.IsInternallyWatchlisted(ctx, "Ada Obi", &wrongDOB, "", ""); err != nil || listed {
		t.Fatalf("a different DOB must not match, listed=%v err=%v", listed, err)
	}
	if listed, _, err := repository.IsInternallyWatchlisted(ctx, "Nobody Atall", nil, "", ""); err != nil || listed {
		t.Fatalf("unlisted identity must not match, listed=%v err=%v", listed, err)
	}

	if err := repository.RemoveInternalWatchlistEntry(ctx, added.ID); err != nil {
		t.Fatal(err)
	}
	if listed, _, err := repository.IsInternallyWatchlisted(ctx, "ada obi", &dob, "", ""); err != nil || listed {
		t.Fatalf("entry should be gone after removal, listed=%v err=%v", listed, err)
	}
}

func TestScreeningBlockedByInternalWatchlist(t *testing.T) { //nolint:parallel // shared scratch database
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE internal_watchlist, screening_results, kyc_profiles CASCADE`); err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, "+2348060000002")
	if err != nil {
		t.Fatal(err)
	}

	dob := time.Date(1991, 7, 7, 0, 0, 0, 0, time.UTC)
	if _, err := repository.AddInternalWatchlistEntry(ctx, InternalWatchlistEntry{
		FullName: "Bola Watchlisted", DateOfBirth: &dob,
		Reason: "internal test listing", ListedBy: "compliance@xego",
	}); err != nil {
		t.Fatal(err)
	}

	// A vendor says clear, but the platform's own list says blocked.
	recorded, err := repository.RecordScreeningResult(ctx, ScreeningResult{
		UserID: user.ID, Provider: "simulated", Decision: kyc.ScreenClear,
		MatchedNames: []string{}, LegalName: "bola   watchlisted", DateOfBirth: &dob,
	}, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Decision != kyc.ScreenBlocked {
		t.Fatalf("internal watchlist must force blocked, got %q", recorded.Decision)
	}
	if recorded.Provider != "internal_watchlist" || recorded.InternalWatchlistID == uuid.Nil {
		t.Fatalf("interdiction provenance missing: provider=%q watchlistID=%s", recorded.Provider, recorded.InternalWatchlistID)
	}

	// The decision write-through protects the ladder.
	var last string
	if err := repository.pool.QueryRow(ctx,
		`SELECT last_screening_decision FROM kyc_profiles WHERE user_id=$1`, user.ID).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last != kyc.ScreenBlocked {
		t.Fatalf("kyc profile must carry the interdiction, got %q", last)
	}

	// Audit trail carries the listing reason.
	var details []byte
	if err := repository.pool.QueryRow(ctx,
		`SELECT details FROM audit_logs WHERE action='kyc.screening_recorded' ORDER BY id DESC LIMIT 1`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(details), "internal test listing") {
		t.Fatalf("audit should name the listing reason, got %s", details)
	}

	// An unlisted identity keeps the vendor's decision untouched.
	clear, err := repository.RecordScreeningResult(ctx, ScreeningResult{
		UserID: user.ID, Provider: "simulated", Decision: kyc.ScreenClear,
		MatchedNames: []string{}, LegalName: "Chidi Unlisted",
	}, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if clear.Decision != kyc.ScreenClear {
		t.Fatalf("unlisted identity must keep the vendor decision, got %q", clear.Decision)
	}
}
