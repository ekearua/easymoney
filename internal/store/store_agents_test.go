package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestAgentPayoutAllowed(t *testing.T) {
	active := func(p AgentProfile) AgentProfile {
		p.Status = AgentStatusActive
		return p
	}
	tests := []struct {
		name      string
		profile   AgentProfile
		amount    int64
		current   int64
		paidToday int64
		wantErr   error
	}{
		{"positive payout allowed", active(AgentProfile{}), 5000, 0, 0, nil},
		{"pending agent refused", AgentProfile{Status: AgentStatusPending}, 5000, 0, 0, ErrAgentNotActive},
		{"suspended agent refused", AgentProfile{Status: AgentStatusSuspended}, 5000, 0, 0, ErrAgentNotActive},
		{"above per-payout cap refused", active(AgentProfile{PerPayoutLimitKobo: 10000}), 10001, 0, 0, ErrAgentPayoutLimitExceeded},
		{"at per-payout cap allowed", active(AgentProfile{PerPayoutLimitKobo: 10000}), 10000, 0, 0, nil},
		{"daily cap refused", active(AgentProfile{DailyPayoutLimitKobo: 20000}), 5000, 0, 16000, ErrAgentPayoutLimitExceeded},
		{"at daily cap allowed", active(AgentProfile{DailyPayoutLimitKobo: 20000}), 4000, 0, 16000, nil},
		{"outstanding float cap refused", active(AgentProfile{MaxOutstandingFloatKobo: 50000}), 20000, 40000, 0, ErrAgentFloatCapExceeded},
		{"at float cap allowed", active(AgentProfile{MaxOutstandingFloatKobo: 50000}), 10000, 40000, 0, nil},
		{"zero limits mean uncapped", active(AgentProfile{}), 1_000_000, 1_000_000, 1_000_000, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := agentPayoutAllowed(tt.profile, tt.amount, tt.current, tt.paidToday)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("want allowed, got %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("want %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestAgentPayoutAllowedRejectsNonPositive(t *testing.T) {
	if err := agentPayoutAllowed(AgentProfile{Status: AgentStatusActive}, 0, 0, 0); err == nil {
		t.Fatal("a zero-amount payout must be refused")
	}
	if err := agentPayoutAllowed(AgentProfile{Status: AgentStatusActive}, -1, 0, 0); err == nil {
		t.Fatal("a negative-amount payout must be refused")
	}
}

// TestDefaultAgentLimitsShipClosed pins the deny-by-default posture: a freshly
// created agent has no numeric limits (0 = uncapped) but is pending, so the
// predicate refuses it anyway until an operator sets limits and activates it.
func TestDefaultAgentLimitsShipClosed(t *testing.T) {
	for _, tier := range []string{AgentTierStandard, AgentTierSuper, AgentTierMax, "unexpected"} {
		maxOutstanding, perPayout, daily, commission := DefaultAgentLimits(tier)
		if maxOutstanding != 0 || perPayout != 0 || daily != 0 || commission != 0 {
			t.Fatalf("tier %q should ship with zero limits, got %d/%d/%d/%d",
				tier, maxOutstanding, perPayout, daily, commission)
		}
	}
	if err := agentPayoutAllowed(AgentProfile{Status: AgentStatusPending}, 1, 0, 0); !errors.Is(err, ErrAgentNotActive) {
		t.Fatalf("a default pending agent must refuse a payout, got %v", err)
	}
}

func TestAgentAccountCodes(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	if got, want := AgentFloatAccountCode(id), "2400_agent_float:"+id.String(); got != want {
		t.Fatalf("float account code = %q, want %q", got, want)
	}
	if got, want := AgentDepositAccountCode(id), "2401_agent_deposit:"+id.String(); got != want {
		t.Fatalf("deposit account code = %q, want %q", got, want)
	}
	if got, want := RemittancePayableAccountCode(id), "2350_remittance_payable:"+id.String(); got != want {
		t.Fatalf("remittance payable code = %q, want %q", got, want)
	}
	for _, code := range []string{
		AgentFloatAccountCode(id), AgentDepositAccountCode(id), RemittancePayableAccountCode(id),
	} {
		if !isKnownLedgerAccount(code) {
			t.Fatalf("agent ledger account %q must be accepted by the chart guard", code)
		}
	}
}
