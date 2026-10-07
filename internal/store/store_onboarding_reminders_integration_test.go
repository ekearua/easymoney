package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestOnboardingReminderSweep covers the reminder candidate sweep and the
// claim index against a fresh database: stage classification per detection
// bucket (stalled live session, abandoned expired session, never started),
// exclusion of completed users and merchant owners, the attempt cap with
// failed attempts exempt, unanswered-reminder suppression lifted by a newer
// inbound, the minimum gap, and ErrReminderClaimed on a duplicate claim.
func TestOnboardingReminderSweep(t *testing.T) {
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
	raw, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	uniquePhone := func() string {
		return fmt.Sprintf("+234815%06d", time.Now().UnixNano()%1_000_000)
	}
	// GetOrCreateUser auto-completes onboarding for WhatsApp-path users
	// (the INSERT hard-codes onboarding_complete = true): the flag stays
	// false only for users who arrived on the non-WhatsApp channels or on
	// legacy rows. The sweep targets exactly those never-finished signups,
	// so every candidate fixture here resets the flag to false.
	forceIncomplete := func(id interface{ String() string }) {
		t.Helper()
		if _, err := raw.Exec(ctx,
			`UPDATE users SET onboarding_complete = false WHERE id = $1`, id.String()); err != nil {
			t.Fatal(err)
		}
	}
	stallAfter := 12 * time.Hour
	startAfter := 24 * time.Hour
	// Candidates with generous defaults; sub-scenarios tighten via args.
	sweep := func(maxAttempts int, gap time.Duration) []OnboardingReminderCandidate {
		t.Helper()
		got, err := repository.OnboardingReminderCandidates(ctx, stallAfter, startAfter, gap, maxAttempts)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	find := func(cands []OnboardingReminderCandidate, userID interface{ String() string }) *OnboardingReminderCandidate {
		t.Helper()
		for i := range cands {
			if cands[i].UserID.String() == userID.String() {
				return &cands[i]
			}
		}
		return nil
	}

	// --- A: stalled live session classifies as its form stage ------------
	userA, err := repository.GetOrCreateUser(ctx, uniquePhone())
	if err != nil {
		t.Fatal(err)
	}
	forceIncomplete(userA.ID)
	if err := repository.SaveSession(ctx, Session{
		UserID: userA.ID, State: "onboard_details", Data: map[string]string{},
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx,
		`UPDATE conversation_sessions SET updated_at = now() - interval '13 hours' WHERE user_id = $1`,
		userA.ID); err != nil {
		t.Fatal(err)
	}
	cands := sweep(3, 48*time.Hour)
	if cA := find(cands, userA.ID); cA == nil {
		t.Fatal("stalled live-session user missing from candidates")
	} else if cA.Stage != "details" || cA.Channel != "whatsapp" || cA.Recipient != userA.WhatsAppNumber || cA.NextAttemptNo != 1 {
		t.Fatalf("bad candidate for user A: stage=%s channel=%s recipient=%q attempt=%d",
			cA.Stage, cA.Channel, cA.Recipient, cA.NextAttemptNo)
	}

	// --- B: abandoned expired session keeps its final state's stage ------
	userB, err := repository.GetOrCreateUser(ctx, uniquePhone())
	if err != nil {
		t.Fatal(err)
	}
	forceIncomplete(userB.ID)
	if err := repository.SaveSession(ctx, Session{
		UserID: userB.ID, State: "merchant_register_details", Data: map[string]string{},
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx,
		`UPDATE conversation_sessions SET updated_at = now() - interval '13 hours' WHERE user_id = $1`,
		userB.ID); err != nil {
		t.Fatal(err)
	}
	cands = sweep(3, 48*time.Hour)
	if cB := find(cands, userB.ID); cB == nil {
		t.Fatal("abandoned-session user missing from candidates")
	} else if cB.Stage != "kyb" {
		t.Fatalf("abandoned KYB session classified as %q, want kyb", cB.Stage)
	}

	// --- C: never-started user (no session row at all) -------------------
	userC, err := repository.GetOrCreateUser(ctx, uniquePhone())
	if err != nil {
		t.Fatal(err)
	}
	forceIncomplete(userC.ID)
	if _, err := raw.Exec(ctx,
		`UPDATE users SET created_at = now() - interval '25 hours', last_inbound_at = now() - interval '25 hours' WHERE id = $1`,
		userC.ID); err != nil {
		t.Fatal(err)
	}
	cands = sweep(3, 48*time.Hour)
	if cC := find(cands, userC.ID); cC == nil {
		t.Fatal("never-started user missing from candidates")
	} else if cC.Stage != "start" {
		t.Fatalf("never-started user classified as %q, want start", cC.Stage)
	}

	// --- D: completed users are never candidates -------------------------
	userD, err := repository.GetOrCreateUser(ctx, uniquePhone())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx,
		`UPDATE users SET onboarding_complete = true WHERE id = $1`, userD.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveSession(ctx, Session{
		UserID: userD.ID, State: "menu", Data: map[string]string{},
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx,
		`UPDATE conversation_sessions SET updated_at = now() - interval '13 hours' WHERE user_id = $1`,
		userD.ID); err != nil {
		t.Fatal(err)
	}
	if find(sweep(3, 48*time.Hour), userD.ID) != nil {
		t.Fatal("completed user must not be a reminder candidate")
	}

	// --- E: merchant owners are never candidates -------------------------
	userE, err := repository.GetOrCreateUser(ctx, uniquePhone())
	if err != nil {
		t.Fatal(err)
	}
	forceIncomplete(userE.ID)
	if err := repository.SaveSession(ctx, Session{
		UserID: userE.ID, State: "onboard_email", Data: map[string]string{},
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx,
		`UPDATE conversation_sessions SET updated_at = now() - interval '13 hours' WHERE user_id = $1`,
		userE.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx, `
		INSERT INTO merchants (slug, name, category, description)
		VALUES ($1, 'Reminder Test Ltd', 'test', 'merchant-owner exclusion fixture')`,
		fmt.Sprintf("rem-owner-%d", time.Now().UnixNano()%1_000_000)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx, `
		INSERT INTO merchant_owners (merchant_id, user_id)
		SELECT m.id, $1 FROM merchants m WHERE m.slug LIKE 'rem-owner-%'
		AND NOT EXISTS (SELECT 1 FROM merchant_owners mo WHERE mo.user_id = $1)`,
		userE.ID); err != nil {
		t.Fatal(err)
	}
	if find(sweep(3, 48*time.Hour), userE.ID) != nil {
		t.Fatal("merchant owner must not be a reminder candidate")
	}

	// --- F: attempt cap counts sent but not failed ------------------------
	claim := func(u User, attempt int, stage, state string) OnboardingReminder {
		t.Helper()
		r := OnboardingReminder{
			UserID: u.ID, Stage: stage, AttemptNo: attempt, StageState: state,
			Channel: "whatsapp", Recipient: u.WhatsAppNumber, Body: "nudge",
		}
		if err := repository.ClaimOnboardingReminder(ctx, &r); err != nil {
			t.Fatalf("claim %s/%d: %v", stage, attempt, err)
		}
		return r
	}
	r1 := claim(userA, 1, "details", "onboard_details")
	if err := repository.SetOnboardingReminderOutcome(ctx, r1.ID, "sent"); err != nil {
		t.Fatal(err)
	}
	// Cap 1: attempt 1 already sent -> user A excluded; user B unaffected.
	cands = sweep(1, 48*time.Hour)
	if find(cands, userA.ID) != nil {
		t.Fatal("user A must be capped out at max=1 after a sent attempt")
	}
	if find(cands, userB.ID) == nil {
		t.Fatal("user B must remain a candidate (cap is per stage)")
	}
	// A failed attempt does not count toward the cap.
	if err := repository.SetOnboardingReminderOutcome(ctx, r1.ID, "failed"); err != nil {
		t.Fatal(err)
	}
	if find(sweep(1, 48*time.Hour), userA.ID) == nil {
		t.Fatal("failed attempt must not consume the cap")
	}

	// --- G: unanswered reminder suppresses until a newer inbound ---------
	r2 := claim(userA, 2, "details", "onboard_details")
	if err := repository.SetOnboardingReminderOutcome(ctx, r2.ID, "sent"); err != nil {
		t.Fatal(err)
	}
	if find(sweep(3, 48*time.Hour), userA.ID) != nil {
		t.Fatal("an unanswered reminder (sent after last inbound) must suppress the next")
	}
	// Backdate the attempt outside the 48h gap so the gap gate stops firing
	// and the reply (a newer inbound) alone controls eligibility.
	if _, err := raw.Exec(ctx,
		`UPDATE onboarding_reminders SET sent_at = now() - interval '49 hours' WHERE id = $1`, r2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx,
		`UPDATE users SET last_inbound_at = now() WHERE id = $1`, userA.ID); err != nil {
		t.Fatal(err)
	}
	if find(sweep(3, 48*time.Hour), userA.ID) == nil {
		t.Fatal("a reply (newer inbound) must re-qualify the user")
	}

	// --- H: minimum gap between reminders ---------------------------------
	// The last attempt is 49h old: a 72h gap still suppresses, a 48h gap
	// (and any shorter one) does not.
	if find(sweep(3, 72*time.Hour), userA.ID) != nil {
		t.Fatal("a reminder inside the configured gap must suppress the next")
	}
	if find(sweep(3, 48*time.Hour), userA.ID) == nil {
		t.Fatal("a reminder older than the configured gap must not suppress")
	}

	// --- I: duplicate claim loses with ErrReminderClaimed ------------------
	dup := claim(userB, 1, "kyb", "merchant_register_details")
	again := OnboardingReminder{
		UserID: userB.ID, Stage: "kyb", AttemptNo: 1, StageState: "merchant_register_details",
		Channel: "whatsapp", Recipient: userB.WhatsAppNumber, Body: "nudge",
	}
	if err := repository.ClaimOnboardingReminder(ctx, &again); !errors.Is(err, ErrReminderClaimed) {
		t.Fatalf("duplicate claim: want ErrReminderClaimed, got %v", err)
	}
	if err := repository.SetOnboardingReminderOutcome(ctx, dup.ID, "sent"); err != nil {
		t.Fatal(err)
	}
}
