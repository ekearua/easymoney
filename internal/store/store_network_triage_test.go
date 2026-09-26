package store

// TestRecipientLinksRingsAndCases proves the recipient-account edge, the
// ring triage enrichment, and the linked-case lifecycle end to end: two
// users saving the same bank account as payout destination form a
// recipient_account edge; the ring's members carry tiers/bands and alert
// counts; a confirmed alert outcome raises ONE network case for the ring,
// re-detection does not duplicate it, resolution closes it, and new
// confirmed evidence reopens it.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestRecipientLinksRingsAndCases -v

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
)

func TestRecipientLinksRingsAndCases(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE users, merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"transaction_alerts", "transaction_alert_outcomes", "network_cases", "user_payout_destinations", "trusted_devices", "kyc_profiles"} {
		if _, err := repository.pool.Exec(ctx, `TRUNCATE `+tbl+` RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	ada, err := repository.GetOrCreateUser(ctx, "+2348095550301")
	if err != nil {
		t.Fatal(err)
	}
	bode, err := repository.GetOrCreateUser(ctx, "+2348095550302")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE users SET display_name=$2 WHERE id=$1`, ada.ID, "Ada Mule"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `UPDATE users SET display_name=$2 WHERE id=$1`, bode.ID, "Bode Mule"); err != nil {
		t.Fatal(err)
	}
	// Tiers/bands so enrichment has something to show.
	for _, u := range []struct {
		id   uuid.UUID
		tier string
	}{{ada.ID, "L2"}, {bode.ID, "L0"}} {
		if _, err := repository.pool.Exec(ctx, `
			INSERT INTO kyc_profiles (user_id, tier) VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET tier = EXCLUDED.tier`, u.id, u.tier); err != nil {
			t.Fatal(err)
		}
	}

	// Both users save the same recipient bank account — the mule pattern
	// that used to hide inside encrypted flow payloads.
	if _, err := repository.GetOrCreateUserPayoutDestination(ctx, ada.ID, "058", "GTBank", "0123456789", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetOrCreateUserPayoutDestination(ctx, bode.ID, "058", "GTBank", "0123456789", ""); err != nil {
		t.Fatal(err)
	}

	links, err := repository.IdentityLinksWithRecipients(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var recipientEdge bool
	for _, l := range links {
		if l.Kind == "recipient_account" && l.Value == "0123456789" {
			recipientEdge = true
			if l.MaskedValue() != "••••6789" {
				t.Errorf("recipient edge not masked: %q", l.MaskedValue())
			}
		}
	}
	if !recipientEdge {
		t.Fatalf("no recipient_account edge; links=%+v", links)
	}

	rings := DetectRingsFromIdentityLinks(links, 2)
	if len(rings) != 1 || len(rings[0].Members) != 2 {
		t.Fatalf("rings = %+v, want one 2-member ring", rings)
	}
	scored, err := repository.EnrichRings(ctx, rings, links, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(scored) != 1 {
		t.Fatalf("scored = %d, want 1", len(scored))
	}
	r := scored[0]
	if len(r.Members) != 2 {
		t.Fatalf("ring members = %d, want 2", len(r.Members))
	}
	tiers := map[string]bool{}
	for _, m := range r.Members {
		tiers[m.Tier] = true
		if m.RiskBand == "" {
			t.Errorf("member %s has an empty risk band", m.Subject)
		}
	}
	if !tiers["L2"] || !tiers["L0"] {
		t.Errorf("member tiers = %v, want L2 and L0 enriched from profiles", tiers)
	}

	// No alerts yet: no case may be raised.
	if n, err := repository.EnsureNetworkCases(ctx, scored, 1); err != nil || n != 0 {
		t.Fatalf("EnsureNetworkCases without confirmed alerts: n=%d err=%v, want 0", n, err)
	}

	// One member's alert becomes confirmed.
	if _, err := repository.recordMonitorAlerts(ctx, ada.ID, []kyc.MonitorAlert{
		{Rule: "velocity", Severity: "high", Rails: []string{"payment"}, References: []string{"NET-REF-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	alerts, err := repository.ListTransactionAlerts(ctx, "open", 5)
	if err != nil || len(alerts) != 1 {
		t.Fatalf("alerts = %v (err %v), want 1", alerts, err)
	}
	if _, err := repository.ResolveTransactionAlertOutcome(ctx, alerts[0].ID, "resolved", "tester@xego", "confirmed"); err != nil {
		t.Fatal(err)
	}
	scored, err = repository.EnrichRings(ctx, rings, links, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if scored[0].ConfirmedHits != 1 {
		t.Fatalf("ConfirmedHits = %d, want 1 (the ring's alert signal)", scored[0].ConfirmedHits)
	}

	// The confirmed hit raises exactly one linked case.
	n, err := repository.EnsureNetworkCases(ctx, scored, 1)
	if err != nil || n != 1 {
		t.Fatalf("first EnsureNetworkCases: n=%d err=%v, want 1 created", n, err)
	}
	cases, err := repository.ListNetworkCases(ctx, 10)
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases = %d (err %v), want 1", len(cases), err)
	}
	if cases[0].MemberCount != 2 || cases[0].ConfirmedAlerts != 1 || cases[0].Status != "open" {
		t.Errorf("case = %+v, want 2 members, 1 confirmed, open", cases[0])
	}
	// Re-detection with the same evidence must not duplicate or change it.
	if _, err := repository.EnsureNetworkCases(ctx, scored, 1); err != nil {
		t.Fatal(err)
	}
	cases, _ = repository.ListNetworkCases(ctx, 10)
	if len(cases) != 1 {
		t.Fatalf("re-detection duplicated the case: %d", len(cases))
	}

	// Resolution closes the ring with one disposition.
	if _, err := repository.ResolveNetworkCase(ctx, cases[0].ID, "resolved", "tester@xego", "mule ring confirmed, accounts frozen"); err != nil {
		t.Fatal(err)
	}
	cases, _ = repository.ListNetworkCases(ctx, 10)
	if cases[0].Status != "resolved" || cases[0].OutcomeNote == "" {
		t.Errorf("resolved case = %+v, want resolved with note", cases[0])
	}

	// A second confirmed hit reopens the same case (never a new row).
	second, err := repository.recordMonitorAlerts(ctx, ada.ID, []kyc.MonitorAlert{
		{Rule: "structuring", Severity: "high", Rails: []string{"payout"}, References: []string{"NET-REF-2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = second
	alerts, _ = repository.ListTransactionAlerts(ctx, "open", 5)
	if len(alerts) != 1 {
		t.Fatalf("want 1 open alert after confirmation, got %d", len(alerts))
	}
	if _, err := repository.ResolveTransactionAlertOutcome(ctx, alerts[0].ID, "resolved", "tester@xego", "confirmed"); err != nil {
		t.Fatal(err)
	}
	scored, err = repository.EnrichRings(ctx, rings, links, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if scored[0].ConfirmedHits != 2 {
		t.Fatalf("ConfirmedHits = %d, want 2", scored[0].ConfirmedHits)
	}
	if _, err := repository.EnsureNetworkCases(ctx, scored, 1); err != nil {
		t.Fatal(err)
	}
	cases, _ = repository.ListNetworkCases(ctx, 10)
	if len(cases) != 1 {
		t.Fatalf("reopen duplicated the case: %d", len(cases))
	}
	if cases[0].Status != "open" || cases[0].ConfirmedAlerts != 2 {
		t.Errorf("reopened case = %+v, want open with 2 confirmed", cases[0])
	}
}
