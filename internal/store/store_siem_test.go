package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSIEMEvents(t *testing.T) {
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
		TRUNCATE business_event_outbox,audit_logs,payments RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	// Insert business events.
	for _, topic := range []string{"payment.succeeded", "payment.failed", "payout.succeeded"} {
		if err := repository.InsertBusinessEvent(ctx, topic, "payment:"+uuid.NewString(), []byte(`{"payment_id":"`+uuid.NewString()+`"}`)); err != nil {
			t.Fatal(err)
		}
	}

	// Insert an audit log entry.
	if _, err := repository.AppendAuditLog(ctx, AuditLog{
		ActorType:    "admin",
		ActorEmail:   sql.NullString{String: "ops@xego.local", Valid: true},
		Action:       "admin.payout.retried",
		ResourceType: sql.NullString{String: "payout", Valid: true},
		ResourceID:   sql.NullString{String: uuid.NewString(), Valid: true},
		IP:           sql.NullString{String: "127.0.0.1", Valid: true},
		Details:      map[string]any{"reason": "test"},
	}); err != nil {
		t.Fatal(err)
	}

	// Query all events.
	events, err := repository.ListSIEMEvents(ctx, SIEMFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 {
		t.Fatalf("expected >= 4 events, got %d", len(events))
	}

	// Filter by topic prefix.
	events, err = repository.ListSIEMEvents(ctx, SIEMFilter{Topic: "payment.", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 payment events, got %d", len(events))
	}

	// Filter by source.
	events, err = repository.ListSIEMEvents(ctx, SIEMFilter{Source: "audit", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	if events[0].Topic != "admin.payout.retried" {
		t.Fatalf("expected admin.payout.retried, got %s", events[0].Topic)
	}
	if events[0].Actor != "ops@xego.local" {
		t.Fatalf("expected ops@xego.local, got %s", events[0].Actor)
	}
	if events[0].IP != "127.0.0.1" {
		t.Fatalf("expected 127.0.0.1, got %s", events[0].IP)
	}

	// Filter by time range.
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)
	events, err = repository.ListSIEMEvents(ctx, SIEMFilter{From: &past, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 {
		t.Fatalf("expected >= 4 events with time filter, got %d", len(events))
	}

	// Future time range should return nothing.
	future := now.Add(1 * time.Hour)
	events, err = repository.ListSIEMEvents(ctx, SIEMFilter{From: &future, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events with future filter, got %d", len(events))
	}
}
