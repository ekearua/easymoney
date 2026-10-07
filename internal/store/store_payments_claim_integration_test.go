package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestClaimInboundMessagesPerConversation pins the cross-replica claim
// contract behind the concurrent worker pool: two Store instances (two
// "replicas") can never lease the same conversation at once, a conversation
// blocked by an active lease becomes claimable again the moment that lease
// completes, and an expired lease (crashed worker) is reclaimed after the
// five-minute window instead of wedging the conversation forever.
func TestClaimInboundMessagesPerConversation(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	replica1, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer replica1.Close()
	replica2, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer replica2.Close()
	if err := replica1.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	// Isolate from leftovers of other runs: only this test's rows matter.
	if _, err := raw.Exec(ctx, `DELETE FROM inbound_messages`); err != nil {
		t.Fatal(err)
	}

	const (
		recipientA = "+2348000000001"
		recipientB = "+2348000000002"
	)
	seed := func(id, recipient string) {
		t.Helper()
		if _, err := raw.Exec(ctx, `
			INSERT INTO inbound_messages(provider_message_id,channel,sender,recipient,payload)
			VALUES ($1,'whatsapp',$2,$2,'{"text":"hi"}')`, id, recipient); err != nil {
			t.Fatal(err)
		}
	}
	claimIDs := func(s *Store) map[string]bool {
		t.Helper()
		messages, err := s.ClaimInboundMessages(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		ids := make(map[string]bool, len(messages))
		for _, m := range messages {
			ids[m.ID] = true
		}
		return ids
	}

	// Both conversations claim in one batch on replica 1.
	seed("claim.a1", recipientA)
	seed("claim.b1", recipientB)
	got := claimIDs(replica1)
	if !got["claim.a1"] || !got["claim.b1"] || len(got) != 2 {
		t.Fatalf("expected both seeded messages in one claim, got %v", got)
	}

	// A new message for A arrives while A is leased: replica 2 must not see
	// conversation A (the advisory lock + lease hold it), and there is
	// nothing else due, so the claim comes back empty.
	seed("claim.a2", recipientA)
	if got := claimIDs(replica2); len(got) != 0 {
		t.Fatalf("expected no rows while conversation A is leased, got %v", got)
	}

	// Completing A's leased message frees the conversation; replica 2 then
	// claims A's pending message.
	if err := replica1.CompleteInboundMessage(ctx, "claim.a1"); err != nil {
		t.Fatal(err)
	}
	got = claimIDs(replica2)
	if !got["claim.a2"] || len(got) != 1 {
		t.Fatalf("expected claim.a2 after conversation A freed, got %v", got)
	}

	// Expired lease recovery: pretend replica 2 crashed mid-flight by
	// rewinding the lease clock; the row stays 'processing' but becomes
	// claimable again after the window, exactly like the pre-existing
	// at-least-once behaviour.
	if _, err := raw.Exec(ctx, `
		UPDATE inbound_messages SET available_at = now() - interval '1 second'
		WHERE provider_message_id = 'claim.a2'`); err != nil {
		t.Fatal(err)
	}
	got = claimIDs(replica1)
	if !got["claim.a2"] || len(got) != 1 {
		t.Fatalf("expected expired lease to be reclaimed, got %v", got)
	}

	// Completing the reclaimed message frees the conversation again: a
	// freshly-pending message for A is then claimable by the other replica,
	// while the reclaimed row shows its second attempt.
	if err := replica1.CompleteInboundMessage(ctx, "claim.a2"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx, `
		UPDATE inbound_messages SET attempts = 0, status = 'pending', available_at = now()
		WHERE provider_message_id = 'claim.a1'`); err != nil {
		t.Fatal(err)
	}
	got = claimIDs(replica2)
	if len(got) != 1 || !got["claim.a1"] {
		t.Fatalf("expected only the freshly-pending claim.a1, got %v", got)
	}
	var attempts int
	if err := raw.QueryRow(ctx,
		`SELECT attempts FROM inbound_messages WHERE provider_message_id='claim.a2'`,
	).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("expected claim.a2 attempted twice (original + reclaim), got %d", attempts)
	}
}
