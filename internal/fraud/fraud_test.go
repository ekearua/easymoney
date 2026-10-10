package fraud

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func baseConfig() FraudConfig {
	return FraudConfig{
		Window:                24 * time.Hour,
		FailedAttemptLimit:    3,
		PaymentLimit:          5,
		DisputeLimit:          2,
		RefundLimit:           4,
		PayoutLimit:           3,
		NewDestinationMinKobo: 100_000,
	}
}

func TestRunFraudMonitorFailedAttemptBurst(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	events := []FraudEvent{
		{Rail: RailPayment, Reference: "p1", SubjectKind: SubjectUser, SubjectID: id, Failed: true, OccurredAt: now.Add(-3 * time.Hour)},
		{Rail: RailPayment, Reference: "p2", SubjectKind: SubjectUser, SubjectID: id, Failed: true, OccurredAt: now.Add(-2 * time.Hour)},
		{Rail: RailPayment, Reference: "p3", SubjectKind: SubjectUser, SubjectID: id, Failed: true, OccurredAt: now.Add(-1 * time.Hour)},
	}
	alerts := RunFraudMonitor(events, baseConfig())
	if len(alerts) != 1 {
		t.Fatalf("want 1 alert, got %d: %+v", len(alerts), alerts)
	}
	a := alerts[0]
	if a.Rule != RuleFailedAttemptBurst || a.Severity != SeverityHigh {
		t.Fatalf("unexpected alert: %+v", a)
	}
	if len(a.References) != 3 {
		t.Fatalf("want 3 references, got %v", a.References)
	}
}

func TestRunFraudMonitorIgnoresStaleEvents(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	events := []FraudEvent{
		{Rail: RailPayment, Reference: "old1", SubjectKind: SubjectUser, SubjectID: id, Failed: true, OccurredAt: now.Add(-50 * time.Hour)},
		{Rail: RailPayment, Reference: "old2", SubjectKind: SubjectUser, SubjectID: id, Failed: true, OccurredAt: now.Add(-49 * time.Hour)},
		{Rail: RailPayment, Reference: "recent", SubjectKind: SubjectUser, SubjectID: id, Failed: true, OccurredAt: now},
	}
	alerts := RunFraudMonitor(events, baseConfig())
	if len(alerts) != 0 {
		t.Fatalf("stale events must not fire, got %+v", alerts)
	}
}

func TestRunFraudMonitorSuccessfulPaymentsIgnoreFailures(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	cfg := baseConfig()
	cfg.PaymentLimit = 2
	cfg.FailedAttemptLimit = 0
	events := []FraudEvent{
		{Rail: RailPayment, Reference: "ok1", SubjectKind: SubjectUser, SubjectID: id, OccurredAt: now.Add(-2 * time.Hour)},
		{Rail: RailPayment, Reference: "ok2", SubjectKind: SubjectUser, SubjectID: id, OccurredAt: now.Add(-time.Hour)},
		{Rail: RailPayment, Reference: "fail", SubjectKind: SubjectUser, SubjectID: id, Failed: true, OccurredAt: now},
	}
	alerts := RunFraudMonitor(events, cfg)
	if len(alerts) != 1 || alerts[0].Rule != RulePaymentVelocity {
		t.Fatalf("want payment velocity only, got %+v", alerts)
	}
	if len(alerts[0].References) != 2 {
		t.Fatalf("failed attempt must not count toward payment velocity: %v", alerts[0].References)
	}
}

func TestRunFraudMonitorDisputeAndRefundVelocity(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	events := []FraudEvent{
		{Rail: RailDispute, Reference: "d1", SubjectKind: SubjectMerchant, SubjectID: id, OccurredAt: now.Add(-2 * time.Hour)},
		{Rail: RailDispute, Reference: "d2", SubjectKind: SubjectMerchant, SubjectID: id, OccurredAt: now},
		{Rail: RailRefund, Reference: "r1", SubjectKind: SubjectMerchant, SubjectID: id, OccurredAt: now.Add(-3 * time.Hour)},
		{Rail: RailRefund, Reference: "r2", SubjectKind: SubjectMerchant, SubjectID: id, OccurredAt: now.Add(-2 * time.Hour)},
		{Rail: RailRefund, Reference: "r3", SubjectKind: SubjectMerchant, SubjectID: id, OccurredAt: now.Add(-time.Hour)},
		{Rail: RailRefund, Reference: "r4", SubjectKind: SubjectMerchant, SubjectID: id, OccurredAt: now},
	}
	alerts := RunFraudMonitor(events, baseConfig())
	if got := rulesOf(alerts); !contains(got, RuleDisputeVelocity) || !contains(got, RuleRefundVelocity) {
		t.Fatalf("want dispute+refund velocity, got %v", got)
	}
}

func TestRunFraudMonitorNewPayoutDestination(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	cfg := baseConfig()
	cfg.PayoutLimit = 0
	events := []FraudEvent{
		{Rail: RailPayout, Reference: "po-new", SubjectKind: SubjectMerchant, SubjectID: id, NewDestination: true, AmountKobo: 500_000, OccurredAt: now},
		{Rail: RailPayout, Reference: "po-known", SubjectKind: SubjectMerchant, SubjectID: id, AmountKobo: 500_000, OccurredAt: now},
	}
	alerts := RunFraudMonitor(events, cfg)
	if len(alerts) != 1 || alerts[0].Rule != RuleNewPayoutDestination {
		t.Fatalf("want one new-destination alert, got %+v", alerts)
	}
	if alerts[0].AmountKobo != 500_000 {
		t.Fatalf("amount not carried: %+v", alerts[0])
	}
}

func TestRunFraudMonitorNewDestinationBelowFloor(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	cfg := baseConfig()
	cfg.PayoutLimit = 0
	cfg.NewDestinationMinKobo = 100_000
	events := []FraudEvent{
		{Rail: RailPayout, Reference: "po-small", SubjectKind: SubjectMerchant, SubjectID: id, NewDestination: true, AmountKobo: 99_999, OccurredAt: now},
	}
	if alerts := RunFraudMonitor(events, cfg); len(alerts) != 0 {
		t.Fatalf("payout below floor must not fire: %+v", alerts)
	}
}

func TestRunFraudMonitorZeroLimitDisablesRule(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	events := []FraudEvent{
		{Rail: RailPayment, Reference: "p1", SubjectKind: SubjectUser, SubjectID: id, OccurredAt: now},
	}
	if alerts := RunFraudMonitor(events, FraudConfig{}); len(alerts) != 0 {
		t.Fatalf("zero config must disable all rules, got %+v", alerts)
	}
}

func TestAlertScore(t *testing.T) {
	cases := map[string]float64{SeverityHigh: 60, SeverityMedium: 35, SeverityLow: 15, "": 15}
	for sev, want := range cases {
		if got := AlertScore(sev); got != want {
			t.Fatalf("AlertScore(%q)=%v want %v", sev, got, want)
		}
	}
}

func rulesOf(alerts []FraudAlert) []string {
	out := make([]string, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, a.Rule)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
