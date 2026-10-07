package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/store"
)

// TestOnboardingReminderSweepService drives the reminder sweep end to end
// against a fresh database: the in-window WhatsApp user gets exactly one
// outbox text, a second sweep is suppressed by the unanswered reminder, the
// out-of-window user without an approved template is recorded as
// pending_template and never messaged raw, the Telegram user is texted on
// its own channel regardless of the window, and the sweep respects quiet
// hours. The store keeps its data key unset here, so outbox payloads are
// plaintext JSON and directly assertable.
func TestOnboardingReminderSweepService(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	databaseURL := serviceTestDBURL(t, base)
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	truncateServiceData(t, ctx, databaseURL)
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	cfg := config.Config{
		OnboardingReminderStallAfter: 12 * time.Hour,
		OnboardingReminderStartAfter: 24 * time.Hour,
		OnboardingReminderGap:        48 * time.Hour,
		OnboardingReminderMax:        3,
		// Quiet hours disabled for most of the run; the quiet-hours
		// scenario flips them on for one sweep.
		OnboardingReminderQuietStart: 0,
		OnboardingReminderQuietEnd:   0,
	}
	svc := NewOnboardingReminderService(cfg, repository, testLogger())

	stallSession := func(user store.User, state string, expiresAt time.Time) {
		t.Helper()
		if err := repository.SaveSession(ctx, store.Session{
			UserID: user.ID, State: state, Data: map[string]string{},
			ExpiresAt: expiresAt,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(ctx,
			`UPDATE conversation_sessions SET updated_at = now() - interval '13 hours' WHERE user_id = $1`,
			user.ID); err != nil {
			t.Fatal(err)
		}
	}
	newStalled := func(suffix string) store.User {
		t.Helper()
		user, err := repository.GetOrCreateUser(ctx, "+234816"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(ctx,
			`UPDATE users SET onboarding_complete = false WHERE id = $1`, user.ID); err != nil {
			t.Fatal(err)
		}
		return user
	}
	countReminders := func(userID uuid.UUID) (int, []string) {
		t.Helper()
		rows, err := raw.Query(ctx,
			`SELECT outcome FROM onboarding_reminders WHERE user_id = $1 ORDER BY sent_at`, userID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var n int
		var outcomes []string
		for rows.Next() {
			var o string
			if err := rows.Scan(&o); err != nil {
				t.Fatal(err)
			}
			n++
			outcomes = append(outcomes, o)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return n, outcomes
	}
	countOutbox := func(userID uuid.UUID) int {
		t.Helper()
		var n int
		if err := raw.QueryRow(ctx,
			`SELECT COUNT(*) FROM message_outbox WHERE user_id = $1`, userID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	outboxBody := func(userID uuid.UUID) string {
		t.Helper()
		var payload []byte
		err := raw.QueryRow(ctx,
			`SELECT payload FROM message_outbox WHERE user_id = $1 AND kind = 'text' ORDER BY id DESC LIMIT 1`,
			userID).Scan(&payload)
		if err != nil {
			t.Fatal(err)
		}
		return string(payload)
	}

	// --- 1: in-window WhatsApp user gets one text ------------------------
	inWindow := newStalled(fmt.Sprintf("%06d", time.Now().UnixNano()%1_000_000))
	stallSession(inWindow, "onboard_details", time.Now().Add(30*time.Minute))
	if _, err := raw.Exec(ctx,
		`UPDATE users SET last_inbound_at = now() - interval '13 hours' WHERE id = $1`, inWindow.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.RunSweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != 1 {
		t.Fatalf("first sweep: want 1 claimed, got %d", claimed)
	}
	n, outcomes := countReminders(inWindow.ID)
	if n != 1 || outcomes[0] != "sent" {
		t.Fatalf("in-window user: want one 'sent' reminder, got %d %v", n, outcomes)
	}
	if countOutbox(inWindow.ID) != 1 {
		t.Fatal("in-window user must produce exactly one outbox row")
	}
	body := outboxBody(inWindow.ID)
	if !strings.Contains(body, "date of birth") {
		t.Fatalf("outbox payload must carry the details-stage copy, got %q", body)
	}

	// --- 2: immediate second sweep is suppressed --------------------------
	claimed, err = svc.RunSweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != 0 {
		t.Fatalf("second sweep must be suppressed by the unanswered reminder, got %d", claimed)
	}

	// --- 3: out-of-window WhatsApp user without a template is suppressed --
	outWindow := newStalled(fmt.Sprintf("%06d", (time.Now().UnixNano()+7)%1_000_000))
	stallSession(outWindow, "onboard_email", time.Now().Add(30*time.Minute))
	if _, err := raw.Exec(ctx,
		`UPDATE users SET last_inbound_at = now() - interval '30 hours' WHERE id = $1`, outWindow.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err = svc.RunSweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != 0 {
		t.Fatalf("out-of-window sweep must claim nothing, got %d", claimed)
	}
	n, outcomes = countReminders(outWindow.ID)
	if n != 1 || outcomes[0] != "pending_template" {
		t.Fatalf("out-of-window user: want one 'pending_template' reminder, got %d %v", n, outcomes)
	}
	if countOutbox(outWindow.ID) != 0 {
		t.Fatal("out-of-window user without a template must not be messaged raw")
	}

	// --- 4: Telegram user is texted on its own channel, window or not -----
	tg := newStalled(fmt.Sprintf("%06d", (time.Now().UnixNano()+13)%1_000_000))
	chatID := "555001122"
	if _, err := raw.Exec(ctx,
		`UPDATE users SET telegram_chat_id = $2, last_inbound_at = now() - interval '30 hours' WHERE id = $1`,
		tg.ID, chatID); err != nil {
		t.Fatal(err)
	}
	stallSession(tg, "merchant_register_details", time.Now().Add(30*time.Minute))
	claimed, err = svc.RunSweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != 1 {
		t.Fatalf("telegram sweep: want 1 claimed, got %d", claimed)
	}
	var tgChannel, tgRecipient string
	if err := raw.QueryRow(ctx,
		`SELECT channel, recipient FROM message_outbox WHERE user_id = $1 ORDER BY id DESC LIMIT 1`,
		tg.ID).Scan(&tgChannel, &tgRecipient); err != nil {
		t.Fatal(err)
	}
	if tgChannel != "telegram" || tgRecipient != chatID {
		t.Fatalf("telegram reminder: got channel=%s recipient=%s", tgChannel, tgRecipient)
	}

	// --- 5: quiet hours silence the whole sweep ----------------------------
	// A fresh stalled user (no reminder rows yet) proves the quiet-hours
	// check gates the sweep itself rather than the fixtures' own
	// suppressions doing the work.
	quietUser := newStalled(fmt.Sprintf("%06d", (time.Now().UnixNano()+29)%1_000_000))
	stallSession(quietUser, "onboard_name", time.Now().Add(30*time.Minute))
	if _, err := raw.Exec(ctx,
		`UPDATE users SET last_inbound_at = now() - interval '13 hours' WHERE id = $1`, quietUser.ID); err != nil {
		t.Fatal(err)
	}
	quiet := cfg
	quiet.OnboardingReminderQuietStart = time.Now().Hour()
	quiet.OnboardingReminderQuietEnd = (time.Now().Hour() + 1) % 24
	quietSvc := NewOnboardingReminderService(quiet, repository, testLogger())
	before, err := repository.OnboardingReminderCandidates(ctx,
		quiet.OnboardingReminderStallAfter, quiet.OnboardingReminderStartAfter,
		quiet.OnboardingReminderGap, quiet.OnboardingReminderMax)
	if err != nil {
		t.Fatal(err)
	}
	foundQuietUser := false
	for _, c := range before {
		if c.UserID == quietUser.ID {
			foundQuietUser = true
		}
	}
	if !foundQuietUser {
		t.Fatal("precondition: the fresh stalled user must be a candidate for the quiet-hours probe")
	}
	claimed, err = quietSvc.RunSweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != 0 {
		t.Fatalf("quiet-hours sweep must claim nothing, got %d", claimed)
	}
}

// TestReminderQuietHoursUnit pins the wrap-around window arithmetic directly:
// 20-8 means 20:00-07:59 quiet, a zero-width window disables quiet hours, and
// an ordinary daytime window excludes the afternoon.
func TestReminderQuietHoursUnit(t *testing.T) {
	svc := &OnboardingReminderService{cfg: config.Config{
		OnboardingReminderQuietStart: 20, OnboardingReminderQuietEnd: 8,
	}}
	if !svc.inQuietHours(time.Date(2026, 10, 5, 21, 0, 0, 0, time.Local)) {
		t.Fatal("21:00 must be inside a 20-8 quiet window")
	}
	if !svc.inQuietHours(time.Date(2026, 10, 5, 3, 0, 0, 0, time.Local)) {
		t.Fatal("03:00 must be inside a 20-8 quiet window")
	}
	if svc.inQuietHours(time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)) {
		t.Fatal("12:00 must be outside a 20-8 quiet window")
	}
	zero := &OnboardingReminderService{cfg: config.Config{
		OnboardingReminderQuietStart: 0, OnboardingReminderQuietEnd: 0,
	}}
	if zero.inQuietHours(time.Date(2026, 10, 5, 3, 0, 0, 0, time.Local)) {
		t.Fatal("a zero-width window must disable quiet hours")
	}
	day := &OnboardingReminderService{cfg: config.Config{
		OnboardingReminderQuietStart: 1, OnboardingReminderQuietEnd: 6,
	}}
	if day.inQuietHours(time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)) {
		t.Fatal("12:00 must be outside a 1-6 quiet window")
	}
	if !day.inQuietHours(time.Date(2026, 10, 5, 4, 0, 0, 0, time.Local)) {
		t.Fatal("04:00 must be inside a 1-6 quiet window")
	}
}

// TestReminderCopyPins pins the stage-to-message contract: every message is
// one nudge with exactly one call to action, uses the display name when
// present, and the never-started fallback stays generic.
func TestReminderCopyPins(t *testing.T) {
	details := reminderCopy("details", "Ada")
	if !strings.Contains(details, "Ada") || !strings.Contains(details, "date of birth") || !strings.Contains(details, "reply") {
		t.Fatalf("details copy must greet Ada, list the Tier-1 fields, and end in a reply CTA: %q", details)
	}
	kyb := reminderCopy("kyb", "")
	if !strings.Contains(kyb, "CAC") || !strings.Contains(kyb, "TIN") {
		t.Fatalf("kyb copy must name the business documents: %q", kyb)
	}
	start := reminderCopy("start", "Bayo")
	if strings.Contains(start, "date of birth") || strings.Contains(start, "CAC") {
		t.Fatalf("start copy must stay generic: %q", start)
	}
	for _, stage := range []string{"name", "email", "email_code", "details", "confirm", "kyb", "start"} {
		if !strings.Contains(strings.ToLower(reminderCopy(stage, "")), "reply") {
			t.Fatalf("stage %q copy must end in the reply CTA", stage)
		}
	}
}
