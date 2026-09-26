package store

// TestTrustFabricKPIsAndOutcomes proves the M4 feedback loop and the M5
// intelligence aggregate end to end: resolving an alert records a per-rule
// outcome label, resolution stamps reviewed_at, and TrustFabricKPIs turns
// those labels into the false-positive rate, median resolution time, and
// the risk-band mix — the Trust Fabric plan's own KPI set.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestTrustFabricKPIsAndOutcomes -v

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
)

func TestTrustFabricKPIsAndOutcomes(t *testing.T) {
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
	// Clean slate for the tables this test drives.
	for _, tbl := range []string{"transaction_alerts", "transaction_alert_outcomes", "kyc_profiles", "trusted_devices"} {
		if _, err := repository.pool.Exec(ctx, `TRUNCATE `+tbl+` RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	// One user + profile so the band mix has a row and alerts have an owner.
	payer, err := repository.GetOrCreateUser(ctx, "+2348095550144")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, payer.ID); err != nil {
		t.Fatalf("ensure kyc profile: %v", err)
	}

	// Raise three alerts through the real ingestion path (so rule, severity,
	// rails, and references carry the production shape).
	alerts := []kyc.MonitorAlert{
		{Rule: "velocity", Severity: "high", Rails: []string{"payment"}, References: []string{"TEST-KPI-REF-1"}},
		{Rule: "structuring", Severity: "high", Rails: []string{"standing_credit"}, References: []string{"TEST-KPI-REF-2"}},
		{Rule: "velocity", Severity: "medium", Rails: []string{"payout"}, References: []string{"TEST-KPI-REF-3"}},
	}
	if n, err := repository.recordMonitorAlerts(ctx, payer.ID, alerts); err != nil || n != 3 {
		t.Fatalf("recordMonitorAlerts: n=%d err=%v", n, err)
	}

	raised, err := repository.ListTransactionAlerts(ctx, "open", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(raised) != 3 {
		t.Fatalf("expected 3 open alerts, got %d", len(raised))
	}

	// Resolve: one confirmed, one false positive, one left open (undetermined
	// acknowledgements are excluded from the false-positive percentage).
	if _, err := repository.ResolveTransactionAlertOutcome(ctx, raised[0].ID, "resolved", "tester@xego", "confirmed"); err != nil {
		t.Fatalf("resolve confirmed: %v", err)
	}
	if _, err := repository.ResolveTransactionAlertOutcome(ctx, raised[1].ID, "resolved", "tester@xego", "false_positive"); err != nil {
		t.Fatalf("resolve false positive: %v", err)
	}

	// M4: the outcome rows exist, labelled per rule.
	counts, err := repository.AlertOutcomeCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byRule := map[string]AlertOutcomeCount{}
	for _, c := range counts {
		byRule[c.Rule] = c
	}
	if byRule["velocity"].Confirmed != 1 {
		t.Errorf("velocity confirmed = %d, want 1", byRule["velocity"].Confirmed)
	}
	if byRule["structuring"].FalsePositive != 1 {
		t.Errorf("structuring false_positive = %d, want 1", byRule["structuring"].FalsePositive)
	}

	// M5: the KPI aggregate reflects the labels, the resolution stamps, and
	// the band mix.
	kpis, err := repository.TrustFabricKPIs(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("TrustFabricKPIs: %v", err)
	}
	if kpis.ConfirmedOutcomes != 1 || kpis.FalsePositiveOutcomes != 1 {
		t.Errorf("outcome counts = (%d confirmed, %d FP), want (1, 1)", kpis.ConfirmedOutcomes, kpis.FalsePositiveOutcomes)
	}
	if kpis.AlertFalsePositivePct != 50 {
		t.Errorf("AlertFalsePositivePct = %v, want 50", kpis.AlertFalsePositivePct)
	}
	if kpis.MedianAlertResolutionMins <= 0 {
		t.Errorf("MedianAlertResolutionMins = %v, want > 0 (both alerts resolved just now)", kpis.MedianAlertResolutionMins)
	}
	// The band mix has exactly the one seeded profile under a valid band
	// (a fresh profile with no recent activity scores high — the risk-band
	// refresh's intent).
	bandTotal := kpis.BandCounts["low"] + kpis.BandCounts["medium"] + kpis.BandCounts["high"]
	if bandTotal != 1 {
		t.Errorf("band mix total = %d (%v), want 1 (the seeded profile)", bandTotal, kpis.BandCounts)
	}
	if kpis.MonitoredMovements7d != 0 {
		t.Errorf("MonitoredMovements7d = %d, want 0 (no money movements were seeded)", kpis.MonitoredMovements7d)
	}

	// Re-dispositioning appends a new outcome row and moves the aggregate:
	// confirming the false-positive alert makes the window 2/3 confirmed.
	if _, err := repository.ResolveTransactionAlertOutcome(ctx, raised[1].ID, "resolved", "tester@xego", "confirmed"); err != nil {
		t.Fatalf("re-resolve: %v", err)
	}
	kpis2, err := repository.TrustFabricKPIs(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if kpis2.ConfirmedOutcomes != 2 || kpis2.FalsePositiveOutcomes != 1 {
		t.Errorf("after re-disposition: (%d confirmed, %d FP), want (2, 1)", kpis2.ConfirmedOutcomes, kpis2.FalsePositiveOutcomes)
	}
}

// TestTrustedDeviceTouchAndAggregate covers the M2 device wiring: the touch
// is idempotent per (user, fingerprint), the latest sighting advances
// last_seen_at only, counts aggregate per user, and the KYC-table summary
// carries the newest device label.
func TestTrustedDeviceTouchAndAggregate(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE users, merchants, trusted_devices RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	payer, err := repository.GetOrCreateUser(ctx, "+2348095550155")
	if err != nil {
		t.Fatal(err)
	}

	// Same fingerprint twice: still one row; the second touch only advances
	// last_seen_at.
	fp := DeviceFingerprint("Mozilla/5.0 (Android 14; Pixel 8) Chrome/120", "en-NG,en;q=0.9")
	if fp == "" {
		t.Fatal("DeviceFingerprint returned empty for a real user agent")
	}
	if err := repository.TouchTrustedDevice(ctx, payer.ID, fp, "Android Chrome · checkout"); err != nil {
		t.Fatalf("touch device: %v", err)
	}
	first, err := repository.TrustedDevicesForUser(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 device row after first touch, got %d", len(first))
	}
	firstSeen := first[0].FirstSeenAt
	if err := repository.TouchTrustedDevice(ctx, payer.ID, fp, "Android Chrome · checkout"); err != nil {
		t.Fatalf("touch device again: %v", err)
	}
	second, err := repository.TrustedDevicesForUser(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("expected 1 device row after duplicate touch, got %d", len(second))
	}
	if !second[0].FirstSeenAt.Equal(firstSeen) {
		t.Errorf("FirstSeenAt changed on duplicate touch: %v -> %v", firstSeen, second[0].FirstSeenAt)
	}
	if second[0].Label != "Android Chrome · checkout" {
		t.Errorf("Label = %q, want the touched label", second[0].Label)
	}

	// A distinct fingerprint adds a row and shows in the per-user summary.
	if err := repository.TouchTrustedDevice(ctx, payer.ID, DeviceFingerprint("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Safari/605", "en-NG"), "iOS Safari · merchant portal"); err != nil {
		t.Fatalf("touch second device: %v", err)
	}
	summaries, err := repository.DeviceSummaryForUsers(ctx, []uuid.UUID{payer.ID})
	if err != nil {
		t.Fatal(err)
	}
	sum, ok := summaries[payer.ID]
	if !ok {
		t.Fatal("device summary missing for the touched user")
	}
	if sum.DeviceCount != 2 {
		t.Errorf("DeviceCount = %d, want 2", sum.DeviceCount)
	}
	if sum.LastLabel != "iOS Safari · merchant portal" {
		t.Errorf("LastLabel = %q, want the most recent device's label", sum.LastLabel)
	}

	// The fingerprint derivation is stable and language-scoped.
	if DeviceFingerprint("Mozilla/5.0 Test", "en-NG,en;q=0.9") != DeviceFingerprint("Mozilla/5.0 Test", "en-NG,en;q=0.9") {
		t.Error("DeviceFingerprint not stable for identical input")
	}
	if DeviceFingerprint("Mozilla/5.0 Test", "en-NG") == DeviceFingerprint("Mozilla/5.0 Test", "fr-FR") {
		t.Error("DeviceFingerprint identical across accept-languages")
	}
}
