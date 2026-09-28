package store

import (
	"context"
	"os"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/kyc"
)

// openRiskTestStore gives a migrated, empty database with the demo seeds, so a
// risk-score test starts from a known profile.
func openRiskTestStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE message_outbox,inbound_messages,webhook_deliveries,payment_events,payments,
		         conversation_sessions,transaction_alert_outcomes,transaction_alerts,
		         manual_review_cases,risk_events,screening_results,customer_verifications,
		         kyc_profiles,users,merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	return repository
}

// latestAlertID returns the id of the most recent alert for a user.
func latestAlertID(t *testing.T, repository *Store, userID any) int64 {
	t.Helper()
	var id int64
	if err := repository.pool.QueryRow(context.Background(),
		`SELECT id FROM transaction_alerts WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestFalsePositiveCancelsAlertRisk pins the feedback loop that stops the score
// from ratcheting: a rule fires (the score rises), a reviewer resolves it as a
// false positive, and the weight the alert added is cancelled — exactly, once,
// and dated with the observation it corrects so the pair cannot drift apart as
// it ages.
func TestFalsePositiveCancelsAlertRisk(t *testing.T) {
	ctx := context.Background()
	repository := openRiskTestStore(t)
	user, err := repository.GetOrCreateUser(ctx, "+2348090001010")
	if err != nil {
		t.Fatal(err)
	}
	before, err := repository.RecomputeRiskScore(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	const rule, severity = kyc.RuleVelocity, kyc.AlertHigh
	if err := repository.insertTransactionAlert(ctx, user.ID, kyc.MonitorAlert{
		Rule:       rule,
		Severity:   severity,
		Rails:      []string{kyc.RailPayment},
		References: []string{"pay-velocity-1"},
		AmountKobo: 450_000,
		Details:    map[string]any{"count": 14},
	}); err != nil {
		t.Fatal(err)
	}
	raised, err := repository.RecomputeRiskScore(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raised.RiskScore <= before.RiskScore {
		t.Fatalf("an alert must raise the score: before=%v after=%v", before.RiskScore, raised.RiskScore)
	}

	// The observation carries the alert id, which is what lets the reviewer's
	// disposition cancel exactly this weight.
	var linked int
	if err := repository.pool.QueryRow(ctx, `
		SELECT count(*) FROM risk_events
		WHERE user_id=$1 AND event_type=$2 AND details->>'alert_id' IS NOT NULL`,
		user.ID, monitorRiskEventType(rule)).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 1 {
		t.Fatalf("alert observation must carry its alert id, found %d linked events", linked)
	}

	alertID := latestAlertID(t, repository, user.ID)
	alert, err := repository.ResolveTransactionAlertOutcome(ctx, alertID, "resolved", "rev@xego.test", "false_positive")
	if err != nil {
		t.Fatal(err)
	}
	if alert.Details["outcome"] != "false_positive" {
		t.Fatalf("outcome = %v, want false_positive", alert.Details["outcome"])
	}
	cleared, err := repository.RecomputeRiskScore(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.RiskScore != before.RiskScore {
		t.Fatalf("a dismissed alert must leave the score where it was: want %v, got %v", before.RiskScore, cleared.RiskScore)
	}

	// Exactly one correction, carrying the negated weight …
	var corrections int
	var correctionScore float64
	if err := repository.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(min(score),0) FROM risk_events
		WHERE user_id=$1 AND event_type=$2`,
		user.ID, monitorRiskEventClearedType(rule)).Scan(&corrections, &correctionScore); err != nil {
		t.Fatal(err)
	}
	if corrections != 1 {
		t.Fatalf("expected exactly one correction, found %d", corrections)
	}
	if want := -kyc.AlertScore(severity); correctionScore != want {
		t.Fatalf("correction score = %v, want %v", correctionScore, want)
	}
	// … dated with the observation it corrects, not with the review.
	var sameTime bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT o.occurred_at = c.occurred_at
		FROM risk_events o
		JOIN risk_events c ON c.user_id=o.user_id
		WHERE o.user_id=$1 AND o.event_type=$2 AND c.event_type=$3`,
		user.ID, monitorRiskEventType(rule), monitorRiskEventClearedType(rule)).Scan(&sameTime); err != nil {
		t.Fatal(err)
	}
	if !sameTime {
		t.Fatal("the correction must be dated with the observation it corrects")
	}

	// Re-resolving the same alert records no second correction.
	if _, err := repository.ResolveTransactionAlertOutcome(ctx, alertID, "resolved", "rev@xego.test", "false_positive"); err != nil {
		t.Fatal(err)
	}
	if err := repository.pool.QueryRow(ctx, `
		SELECT count(*) FROM risk_events WHERE user_id=$1 AND event_type=$2`,
		user.ID, monitorRiskEventClearedType(rule)).Scan(&corrections); err != nil {
		t.Fatal(err)
	}
	if corrections != 1 {
		t.Fatalf("re-resolution must be idempotent, found %d corrections", corrections)
	}

	// A confirmed alert stands: escalation is not a correction.
	if err := repository.insertTransactionAlert(ctx, user.ID, kyc.MonitorAlert{
		Rule:       kyc.RuleStructuring,
		Severity:   kyc.AlertMedium,
		Rails:      []string{kyc.RailPayment},
		References: []string{"pay-structuring-1"},
		AmountKobo: 900_000,
		Details:    map[string]any{"count": 3},
	}); err != nil {
		t.Fatal(err)
	}
	confirmedID := latestAlertID(t, repository, user.ID)
	if _, err := repository.ResolveTransactionAlertOutcome(ctx, confirmedID, "escalated", "rev@xego.test", ""); err != nil {
		t.Fatal(err)
	}
	var structuringCorrections int
	if err := repository.pool.QueryRow(ctx, `
		SELECT count(*) FROM risk_events WHERE user_id=$1 AND event_type=$2`,
		user.ID, monitorRiskEventClearedType(kyc.RuleStructuring)).Scan(&structuringCorrections); err != nil {
		t.Fatal(err)
	}
	if structuringCorrections != 0 {
		t.Fatalf("an escalated alert must stand, found %d corrections", structuringCorrections)
	}
	afterEscalation, err := repository.RecomputeRiskScore(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterEscalation.RiskScore <= before.RiskScore {
		t.Fatalf("a confirmed alert must keep raising the score: before=%v after=%v", before.RiskScore, afterEscalation.RiskScore)
	}
}

// TestRiskScoreDecaysOldObservations pins the decay wiring end to end: with the
// half-life set, an observation dated two half-lives ago is worth a quarter of
// its weight; with it unset (the default a bare Open gives), nothing decays.
func TestRiskScoreDecaysOldObservations(t *testing.T) {
	ctx := context.Background()
	repository := openRiskTestStore(t)
	user, err := repository.GetOrCreateUser(ctx, "+2348090001011")
	if err != nil {
		t.Fatal(err)
	}

	// The profile is L0, whose "identity not yet verified" adjustment adds 10.
	const tierAdjustment = 10
	old := time.Now().UTC().Add(-180 * 24 * time.Hour)
	for _, event := range []RiskEvent{
		{UserID: user.ID, EventType: "monitor_velocity", Score: 40},
		{UserID: user.ID, EventType: "monitor_structuring", Score: 40, OccurredAt: old},
	} {
		if _, err := repository.RecordRiskEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	// Decay off (default): both count in full.
	undecayed, err := repository.RecomputeRiskScore(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := float64(80 + tierAdjustment); undecayed.RiskScore != want {
		t.Fatalf("without a half-life every observation counts in full: want %v, got %v", want, undecayed.RiskScore)
	}

	// Decay on: the 180-day-old event is worth a quarter.
	repository.RiskDecayHalfLife = 90 * 24 * time.Hour
	decayed, err := repository.RecomputeRiskScore(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := float64(40 + 10 + tierAdjustment); decayed.RiskScore != want {
		t.Fatalf("with a 90-day half-life the aged event is worth a quarter: want %v, got %v", want, decayed.RiskScore)
	}
	if decayed.RiskScore >= undecayed.RiskScore {
		t.Fatalf("decay must lower the score: decayed=%v undecayed=%v", decayed.RiskScore, undecayed.RiskScore)
	}
}
