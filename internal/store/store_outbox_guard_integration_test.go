package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestOutboxDeliveryOutcomeRows exercises the reminder delivery guard's read
// against a real database. Only terminal WhatsApp rows inside the window
// count: rows that reached the provider window on sent_at, but a row that
// exhausted its retries never got a sent_at stamp and must window on
// created_at — otherwise the guard is blind to exactly the provider outage
// it exists for. Non-terminal rows, other channels, and rows outside the
// window are excluded.
func TestOutboxDeliveryOutcomeRows(t *testing.T) { //nolint:parallel // shared scratch database
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE message_outbox`); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	inWindow := now.Add(-10 * time.Minute)
	outWindow := now.Add(-3 * time.Hour)
	insert := func(channel, status string, sentAt *time.Time, createdAt time.Time) {
		t.Helper()
		if _, err := repository.pool.Exec(ctx, `
			INSERT INTO message_outbox (recipient, kind, payload, status, channel, sent_at, created_at)
			VALUES ('+2348000000000', 'text', '{}'::jsonb, $1, $2, $3, $4)`,
			status, channel, sentAt, createdAt); err != nil {
			t.Fatal(err)
		}
	}

	// Counted: in-window WhatsApp rows that reached a terminal state.
	insert("whatsapp", "delivered", &inWindow, inWindow)
	insert("whatsapp", "failed", &inWindow, inWindow)
	// Counted: retry-exhausted failure with sent_at IS NULL — the provider-
	// outage case the guard must see.
	insert("whatsapp", "failed", nil, inWindow)
	// Excluded: created outside the window.
	insert("whatsapp", "failed", nil, outWindow)
	// Excluded: still retrying (not terminal), and accepted-but-no-webhook-yet.
	insert("whatsapp", "pending", nil, now)
	insert("whatsapp", "sent", &inWindow, inWindow)
	// Excluded: other channel.
	insert("telegram", "delivered", &inWindow, inWindow)
	// Excluded: terminal but delivered outside the window.
	insert("whatsapp", "delivered", &outWindow, outWindow)

	got, err := repository.OutboxDeliveryOutcomeRows(ctx, time.Hour)
	if err != nil {
		t.Fatalf("delivery-guard read must execute against a real database: %v", err)
	}
	counts := map[string]int{}
	for _, status := range got {
		counts[status]++
	}
	if len(got) != 3 || counts["delivered"] != 1 || counts["failed"] != 2 {
		t.Fatalf("guard rows = %v (delivered=%d failed=%d), want one delivered + two failed including the sent_at-less one",
			got, counts["delivered"], counts["failed"])
	}

	// The window is honored: every terminal row above is older than 30s, so
	// a tightened window sees an empty board and the guard stays open.
	got, err = repository.OutboxDeliveryOutcomeRows(ctx, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("30s window must exclude every seeded row, got %v", got)
	}
}
