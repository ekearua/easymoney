package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
)

// TestProgramPurseLifecycle pins the restricted-funds invariants at the store
// layer, which is where they actually have to hold:
//
//   - enrollment is a precondition for a purse (no standing consent, no purse);
//   - money lands in the purse, and a replayed funding reference cannot double it;
//   - the withdrawal rail cannot reach a purse balance, because a purse is a
//     different ledger account, not a tag on the wallet;
//   - only an approved merchant is selectable, and a block revokes it
//     immediately — including for a payment already drafted;
//   - the system merchants (person-to-person transfer, wallet top-up) can never
//     be approved, so program money cannot become a transfer;
//   - an expired purse is refused at spend time and its balance is repatriated
//     to the sponsor exactly once.
func TestProgramPurseLifecycle(t *testing.T) {
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
	// Store integration tests own the scratch database (see
	// store_wallets_integration_test.go): truncate to a known baseline so the
	// fixtures below are deterministic.
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE program_purses, program_enrollments, merchant_program_members, merchant_programs,
		         payments, ledger_entries, allowance_usage, wallet_accounts,
		         business_kyb_profiles, kyc_profiles, users, merchants
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	merchants, err := repository.ListActiveMerchants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) < 2 {
		t.Skip("needs at least two seeded active merchants")
	}
	sponsor, acceptor := merchants[0], merchants[1]

	program, err := repository.CreateProgram(ctx, NewProgram{
		Code:                "prog-integration",
		Name:                "Integration program",
		SponsorMerchantID:   sponsor.ID,
		PurposeLabel:        "test goods",
		DefaultValidityDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	beneficiary, err := repository.GetOrCreateUser(ctx, "+2348012349100")
	if err != nil {
		t.Fatal(err)
	}

	// Enrollment is the precondition for holding program money.
	if _, err := repository.EnsureProgramPurse(ctx, program.ID, beneficiary.ID); !errors.Is(err, ErrProgramNotEnrolled) {
		t.Fatalf("a purse without an enrollment must be refused, got %v", err)
	}
	if _, err := repository.EnrollUser(ctx, program.ID, beneficiary.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	// The internal system merchants are inactive, which is what keeps program
	// money from ever funding a transfer or a top-up.
	topup, err := repository.WalletTopupSystemMerchant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.AddProgramMerchant(ctx, program.ID, topup.ID, uuid.Nil); err == nil {
		t.Fatal("the wallet top-up system merchant must never be an approved program merchant")
	}

	if err := repository.AddProgramMerchant(ctx, program.ID, acceptor.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	purse, err := repository.EnsureProgramPurse(ctx, program.ID, beneficiary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if purse.AccountCode != PurseAccountCode(purse.ID) {
		t.Fatalf("unexpected purse account code %q", purse.AccountCode)
	}
	if purse.BalanceKobo != 0 {
		t.Fatalf("a new purse must start empty, got %d", purse.BalanceKobo)
	}

	// The sponsor is funded, then disburses: a common pair of balanced postings.
	sponsorWallet, err := repository.EnsureBusinessWallet(ctx, sponsor.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.postLedgerPair(ctx, tx, "test:sponsor-funding", "test", "1",
		LedgerAccountOperatingBank, sponsorWallet.AccountCode, "NGN",
		"Test sponsor funding", "system", 1_000_000, nil); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreditProgramPurse(ctx, purse.ID, sponsorWallet.AccountCode, 500_000,
		"test:disbursement-1", "Test program disbursement"); err != nil {
		t.Fatal(err)
	}
	// Replaying the same journal reference must not fund the purse twice.
	if err := repository.CreditProgramPurse(ctx, purse.ID, sponsorWallet.AccountCode, 500_000,
		"test:disbursement-1", "Test program disbursement replay"); err != nil {
		t.Fatal(err)
	}
	funded, err := repository.ProgramPurseByID(ctx, purse.ID)
	if err != nil {
		t.Fatal(err)
	}
	if funded.BalanceKobo != 500_000 {
		t.Fatalf("purse should hold exactly the one disbursement, got %d", funded.BalanceKobo)
	}

	// The withdrawal rail cannot see purse money. The beneficiary's wallet is
	// activated first (L1) so the ONLY reason a withdrawal can fail is the empty
	// ordinary balance — the 500,000 kobo sitting in the purse must not make it
	// withdrawable.
	if _, err := repository.EnsureUserWallet(ctx, beneficiary.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := repository.ActivateUserWallet(ctx, beneficiary.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.WithdrawFromUserWallet(ctx, beneficiary.ID, 100_000, "test:withdraw-1", "test"); !errors.Is(err, ErrInsufficientWalletBalance) {
		t.Fatalf("restricted funds must not be withdrawable, got %v", err)
	}

	// Selection: the approved merchant is usable, anything else is not.
	picked, ok, err := repository.ApprovedPurseForPayee(ctx, beneficiary.ID, acceptor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || picked.ID != purse.ID {
		t.Fatalf("an approved merchant should select the purse (ok=%v picked=%s)", ok, picked.ID)
	}
	if _, ok, err := repository.ApprovedPurseForPayee(ctx, beneficiary.ID, sponsor.ID); err != nil || ok {
		t.Fatalf("an unapproved merchant must not select the purse (ok=%v err=%v)", ok, err)
	}

	// The authoritative gate, inside a transaction.
	gate, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PurseSpendAllowedTx(ctx, gate, purse.ID, acceptor.ID); err != nil {
		t.Fatalf("an approved spend should be allowed, got %v", err)
	}
	if err := repository.PurseSpendAllowedTx(ctx, gate, purse.ID, sponsor.ID); !errors.Is(err, ErrPurseMerchantNotApproved) {
		t.Fatalf("an unapproved spend should be refused, got %v", err)
	}
	_ = gate.Rollback(ctx)

	// Blocking takes effect immediately, which is the whole reason the gate is
	// re-evaluated at spend time rather than trusted from the draft.
	if err := repository.BlockProgramMerchant(ctx, program.ID, acceptor.ID, "test block"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repository.ApprovedPurseForPayee(ctx, beneficiary.ID, acceptor.ID); err != nil || ok {
		t.Fatalf("a blocked merchant must not be selectable (ok=%v err=%v)", ok, err)
	}
	blocked, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PurseSpendAllowedTx(ctx, blocked, purse.ID, acceptor.ID); !errors.Is(err, ErrPurseMerchantNotApproved) {
		t.Fatalf("a blocked merchant must be refused at spend time, got %v", err)
	}
	_ = blocked.Rollback(ctx)
	if err := repository.AddProgramMerchant(ctx, program.ID, acceptor.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	// Expiry: once the window closes the purse can no longer fund anything, and
	// the unspent balance goes home to the sponsor.
	if _, err := repository.pool.Exec(ctx,
		`UPDATE program_purses SET expires_at = now() - interval '1 hour' WHERE id=$1`, purse.ID); err != nil {
		t.Fatal(err)
	}
	lapsed, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PurseSpendAllowedTx(ctx, lapsed, purse.ID, acceptor.ID); !errors.Is(err, ErrPurseExpired) {
		t.Fatalf("an expired purse must be refused even at an approved merchant, got %v", err)
	}
	_ = lapsed.Rollback(ctx)

	expired, err := repository.ExpiredPurses(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	var listed bool
	for _, item := range expired {
		if item.Purse.ID == purse.ID {
			listed = true
			if item.SponsorMerchantID != sponsor.ID {
				t.Fatalf("lapsed purse resolved to sponsor %s, want %s", item.SponsorMerchantID, sponsor.ID)
			}
		}
	}
	if !listed {
		t.Fatal("a lapsed purse with money in it should be listed for return")
	}

	returned, err := repository.ReturnExpiredPurse(ctx, purse.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if returned != 500_000 {
		t.Fatalf("the full unspent balance should be returned, got %d", returned)
	}
	again, err := repository.ReturnExpiredPurse(ctx, purse.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("a closed purse must not return twice, got %d", again)
	}

	closed, err := repository.ProgramPurseByID(ctx, purse.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != PurseStatusClosed || closed.ReturnedKobo != 500_000 || closed.BalanceKobo != 0 {
		t.Fatalf("closed purse state wrong: status=%s returned=%d balance=%d",
			closed.Status, closed.ReturnedKobo, closed.BalanceKobo)
	}

	// The sponsor is made whole: funded 1,000,000, disbursed 500,000, and got
	// the unspent 500,000 back.
	sponsorBalance, err := repository.WalletBalance(ctx, sponsorWallet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sponsorBalance != 1_000_000 {
		t.Fatalf("sponsor should be whole after the return, got %d", sponsorBalance)
	}

	summary, err := repository.ProgramSummary(ctx, program.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.EnrolledCount != 1 || summary.ApprovedMerchants != 1 {
		t.Fatalf("unexpected program counts %+v", summary)
	}
	if summary.DisbursedKobo != 500_000 || summary.ReturnedKobo != 500_000 || summary.HeldKobo != 0 || summary.SpentKobo != 0 {
		t.Fatalf("unexpected program money roll-up %+v", summary)
	}
	if summary.ActivePurseCount != 0 {
		t.Fatalf("a returned purse should not count as active, got %d", summary.ActivePurseCount)
	}
}

// TestProgramDisbursement pins the money movement of a program disbursement at
// the layer that actually performs it. The service decides *who* to pay; this
// test pins what paying them does, and — more importantly — what it must refuse
// to do:
//
//   - an enrolled recipient's purse is credited, and the sponsor's business
//     wallet is debited exactly once;
//   - replaying the same item returns the original credit reference and cannot
//     fund the purse twice;
//   - a recipient with no active enrollment is refused with ErrProgramNotEnrolled
//     rather than paid, and no wallet of theirs is credited. Paying them anyway
//     would convert restricted money into a withdrawable balance, which is the
//     one outcome the whole program construct exists to prevent.
func TestProgramDisbursement(t *testing.T) {
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
		TRUNCATE bulk_jobs, bulk_batch_items, bulk_batches, center_users, merchant_centers,
		         program_purses, program_enrollments, merchant_program_members, merchant_programs,
		         payments, ledger_entries, allowance_usage, wallet_accounts,
		         business_kyb_profiles, kyc_profiles, users, merchants
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	merchants, err := repository.ListActiveMerchants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) < 2 {
		t.Skip("needs at least two seeded active merchants")
	}
	sponsor, acceptor := merchants[0], merchants[1]

	program, err := repository.CreateProgram(ctx, NewProgram{
		Code:                "prog-disbursement",
		Name:                "Disbursement program",
		SponsorMerchantID:   sponsor.ID,
		PurposeLabel:        "inputs",
		DefaultValidityDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.AddProgramMerchant(ctx, program.ID, acceptor.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}

	enrolled, err := repository.GetOrCreateUser(ctx, "+2348012349301")
	if err != nil {
		t.Fatal(err)
	}
	unlisted, err := repository.GetOrCreateUser(ctx, "+2348012349302")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnrollUser(ctx, program.ID, enrolled.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	purse, err := repository.EnsureProgramPurse(ctx, program.ID, enrolled.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A batch over both recipients, marked as a program disbursement.
	center, err := repository.CreateMerchantCenter(ctx, sponsor.ID, "Disbursement center", 6.5244, 3.3792, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, phone := range []string{enrolled.WhatsAppNumber, unlisted.WhatsAppNumber} {
		if _, err := repository.UpsertCenterUser(ctx, sponsor.ID, center.ID, phone, "recipient", "manual"); err != nil {
			t.Fatal(err)
		}
	}
	const amountKobo = int64(300_000)
	batch, err := repository.CreateBulkBatch(ctx, NewBulkBatch{
		MerchantID: sponsor.ID, CenterID: center.ID, AmountKobo: amountKobo, Currency: "NGN", TTLSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetBulkBatchProgram(ctx, batch.ID, program.ID); err != nil {
		t.Fatal(err)
	}
	items, err := repository.ListBulkBatchItems(ctx, batch.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("batch has %d items, want 2", len(items))
	}
	byPhone := map[string]BulkBatchItem{}
	for _, item := range items {
		byPhone[item.PhoneE164] = item
	}
	enrolledItem, ok := byPhone[enrolled.WhatsAppNumber]
	if !ok {
		t.Fatalf("no item for the enrolled recipient (%s); items: %+v", enrolled.WhatsAppNumber, byPhone)
	}
	unlistedItem, ok := byPhone[unlisted.WhatsAppNumber]
	if !ok {
		t.Fatalf("no item for the unenrolled recipient (%s); items: %+v", unlisted.WhatsAppNumber, byPhone)
	}
	// Both responded in range: the only difference left is enrollment.
	for _, pair := range []struct {
		item   BulkBatchItem
		userID uuid.UUID
	}{{enrolledItem, enrolled.ID}, {unlistedItem, unlisted.ID}} {
		if err := repository.SetBulkItemUserID(ctx, pair.item.ID, pair.userID); err != nil {
			t.Fatal(err)
		}
		if err := repository.SetBulkItemLocation(ctx, pair.item.ID, 6.5244, 3.3792, 8, 3, BulkItemLocationReceived); err != nil {
			t.Fatal(err)
		}
	}

	// The sponsor is pre-funded, so a debit is observable.
	sponsorWallet, err := repository.EnsureBusinessWallet(ctx, sponsor.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	funding, err := repository.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.postLedgerPair(ctx, funding, "test:program-funding", "test", "1",
		LedgerAccountOperatingBank, sponsorWallet.AccountCode, "NGN",
		"Test sponsor funding", "system", 1_000_000, nil); err != nil {
		_ = funding.Rollback(ctx)
		t.Fatal(err)
	}
	if err := funding.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The enrolled recipient is paid into their purse, not their wallet.
	wantRef := fmt.Sprintf("bulk:%s:%s", batch.BatchNo, enrolledItem.ID)
	ref, err := repository.PostBulkProgramPaymentItem(ctx, enrolledItem.ID)
	if err != nil {
		t.Fatalf("post program item: %v", err)
	}
	if ref != wantRef {
		t.Fatalf("credit ref = %q, want %q", ref, wantRef)
	}
	funded, err := repository.ProgramPurseByID(ctx, purse.ID)
	if err != nil {
		t.Fatal(err)
	}
	if funded.BalanceKobo != amountKobo {
		t.Fatalf("purse balance = %d, want %d", funded.BalanceKobo, amountKobo)
	}

	// The restricted money must not be reachable as ordinary spendable balance.
	enrolledWallet, err := repository.EnsureUserWallet(ctx, enrolled.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if balance, err := repository.WalletBalance(ctx, enrolledWallet.ID); err != nil {
		t.Fatal(err)
	} else if balance != 0 {
		t.Fatalf("the enrollee's ordinary wallet holds %d kobo; program money must land in the purse", balance)
	}

	sponsorAfter, err := repository.WalletBalance(ctx, sponsorWallet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sponsorAfter != 1_000_000-amountKobo {
		t.Fatalf("sponsor wallet = %d, want %d after one disbursement", sponsorAfter, 1_000_000-amountKobo)
	}

	// Replay: same reference, no second funding, no second debit.
	replayRef, err := repository.PostBulkProgramPaymentItem(ctx, enrolledItem.ID)
	if err != nil {
		t.Fatalf("replay program item: %v", err)
	}
	if replayRef != wantRef {
		t.Fatalf("replay ref = %q, want the original %q", replayRef, wantRef)
	}
	replayed, err := repository.ProgramPurseByID(ctx, purse.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.BalanceKobo != amountKobo {
		t.Fatalf("replay double-funded the purse: %d, want %d", replayed.BalanceKobo, amountKobo)
	}
	if again, err := repository.WalletBalance(ctx, sponsorWallet.ID); err != nil {
		t.Fatal(err)
	} else if again != sponsorAfter {
		t.Fatalf("replay moved the sponsor wallet: %d -> %d", sponsorAfter, again)
	}

	// The unenrolled recipient is refused, not quietly paid.
	if _, err := repository.PostBulkProgramPaymentItem(ctx, unlistedItem.ID); !errors.Is(err, ErrProgramNotEnrolled) {
		t.Fatalf("an unenrolled recipient must be refused with ErrProgramNotEnrolled, got %v", err)
	}
	unlistedWallet, err := repository.EnsureUserWallet(ctx, unlisted.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if balance, err := repository.WalletBalance(ctx, unlistedWallet.ID); err != nil {
		t.Fatal(err)
	} else if balance != 0 {
		t.Fatalf("the unenrolled recipient was credited %d kobo", balance)
	}
	if final, err := repository.WalletBalance(ctx, sponsorWallet.ID); err != nil {
		t.Fatal(err)
	} else if final != sponsorAfter {
		t.Fatalf("the refused item moved the sponsor wallet: %d -> %d", sponsorAfter, final)
	}

	// The refused item keeps its place: it was not marked paid, so a later
	// enrollment can still be paid deliberately.
	after, err := repository.ListBulkBatchItems(ctx, batch.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after {
		switch item.PhoneE164 {
		case enrolled.WhatsAppNumber:
			if item.Status != BulkItemPaid || item.CreditRef != wantRef {
				t.Fatalf("enrolled item = %s ref %q, want paid ref %q", item.Status, item.CreditRef, wantRef)
			}
		case unlisted.WhatsAppNumber:
			if item.Status != BulkItemLocationReceived {
				t.Fatalf("refused item status = %s, want it left at %s", item.Status, BulkItemLocationReceived)
			}
		}
	}
}
