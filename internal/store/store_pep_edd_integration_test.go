package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
)

// G1 (AML spec §5): PEP EDD with senior-management approval. A PEP screening
// determination auto-opens one pep_edd case; while it is open every tier
// advancement is refused; the SM disposition is its own audited status and
// unblocks the ladder; a non-PEP determination opens nothing.
func TestPEPEDDCaseLifecycle(t *testing.T) { //nolint:parallel // shared scratch database
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
	// Self-contained fixtures: a unique contact per run (the suite shares one
	// scratch database) and a clean case/screening slate for this ladder.
	if _, err := repository.pool.Exec(ctx, `TRUNCATE manual_review_cases`); err != nil {
		t.Fatal(err)
	}
	contact := fmt.Sprintf("+234809%06d", time.Now().UnixNano()%1_000_000)
	var userID uuid.UUID
	if err := repository.pool.QueryRow(ctx, `
		INSERT INTO users(id, display_name, whatsapp_number, email)
		VALUES($1,'PEP Test',$2,$3)
		RETURNING id`,
		uuid.New(), contact, "pep-"+contact+"@test").Scan(&userID); err != nil {
		t.Fatal(err)
	}

	// A clear screening opens nothing.
	if _, err := repository.RecordScreeningResult(ctx, ScreeningResult{
		UserID: userID, Provider: "simulated", Decision: kyc.ScreenClear,
		LegalName: "Ada Obi", PhoneNumber: contact,
	}, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if open, err := repository.HasOpenPEPEDDCase(ctx, userID); err != nil || open {
		t.Fatalf("clear screening must open no pep_edd case (open=%v err=%v)", open, err)
	}

	// A PEP determination (possible) opens exactly one case.
	if _, err := repository.RecordScreeningResult(ctx, ScreeningResult{
		UserID: userID, Provider: "simulated", Decision: kyc.ScreenPossible,
		LegalName: "Ada Obi", PhoneNumber: contact,
		Message: "matched PEP list entry",
	}, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	opened, err := repository.HasOpenPEPEDDCase(ctx, userID)
	if err != nil || !opened {
		t.Fatalf("PEP determination must open a pep_edd case (open=%v err=%v)", opened, err)
	}

	// Idempotency: a second PEP result does not duplicate the open case.
	if _, err := repository.RecordScreeningResult(ctx, ScreeningResult{
		UserID: userID, Provider: "simulated", Decision: kyc.ScreenStrong,
		LegalName: "Ada Obi", PhoneNumber: contact,
	}, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var nCases int
	if err := repository.pool.QueryRow(ctx, `
		SELECT count(*) FROM manual_review_cases
		WHERE user_id=$1 AND case_type='pep_edd' AND status='pending'`, userID).Scan(&nCases); err != nil {
		t.Fatal(err)
	}
	if nCases != 1 {
		t.Fatalf("expected exactly 1 open pep_edd case, got %d", nCases)
	}

	// The gate: every tier advancement is refused while the case is open.
	actor := &AuditLog{ActorType: "admin", ActorEmail: sql.NullString{String: "sm@xego", Valid: true}}
	if _, err := repository.AdvanceKYCTier(ctx, userID, kyc.TierL1,
		[]string{kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails}, actor); err == nil {
		t.Fatal("advancement must be refused while a pep_edd case is open")
	}

	// Senior-management rejection disposes the case (own status, audited),
	// after which advancement works again.
	cases, err := repository.ListManualReviewCases(ctx, "pending", 10)
	if err != nil {
		t.Fatal(err)
	}
	var caseID int64
	for _, c := range cases {
		if c.CaseType == "pep_edd" && c.UserID == userID {
			caseID = c.ID
		}
	}
	if caseID == 0 {
		t.Fatal("no pending pep_edd case found for the user")
	}
	if _, err := repository.ReviewPEPEDDCase(ctx, caseID, false, "sm@xego", "cannot clear: unexplained wealth", actor); err != nil {
		t.Fatal(err)
	}
	if opened, err := repository.HasOpenPEPEDDCase(ctx, userID); err != nil || opened {
		t.Fatalf("case must be closed after SM disposition (open=%v err=%v)", opened, err)
	}
	var status, reviewer string
	if err := repository.pool.QueryRow(ctx, `
		SELECT status, COALESCE(reviewer_email,'') FROM manual_review_cases WHERE id=$1`, caseID).Scan(&status, &reviewer); err != nil {
		t.Fatal(err)
	}
	if status != "sm_rejected" || reviewer != "sm@xego" {
		t.Fatalf("expected sm_rejected by sm@xego, got %q by %q", status, reviewer)
	}

	// The ordinary reviewer path must not be able to dispose a pep_edd case:
	// open a fresh one and try approve via ReviewManualReviewCase.
	if _, err := repository.RecordScreeningResult(ctx, ScreeningResult{
		UserID: userID, Provider: "simulated", Decision: kyc.ScreenBlocked,
		LegalName: "Ada Obi", PhoneNumber: contact,
	}, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	cases, err = repository.ListManualReviewCases(ctx, "pending", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.CaseType == "pep_edd" && c.UserID == userID {
			caseID = c.ID
		}
	}
	if _, err := repository.ReviewManualReviewCase(ctx, caseID, true, "clerk@xego", "oops ordinary approve", actor); err == nil {
		t.Fatal("ordinary approve must not be able to dispose a pep_edd case")
	} else if !strings.Contains(err.Error(), "pep_edd") {
		t.Fatalf("unexpected error from ordinary review of pep_edd case: %v", err)
	}

	// SM approval closes it and attaches the EDD evidence key.
	if _, err := repository.ReviewPEPEDDCase(ctx, caseID, true, "sm2@xego", "EDD complete, source of funds verified", actor); err != nil {
		t.Fatal(err)
	}
	profile, err := repository.KYCProfileByUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	hasEDD := false
	for _, e := range profile.Evidence {
		if e == kyc.EvEDDCompleted {
			hasEDD = true
		}
	}
	if !hasEDD {
		t.Fatalf("SM approval must attach the %s evidence key; profile evidence: %v", kyc.EvEDDCompleted, profile.Evidence)
	}
}
