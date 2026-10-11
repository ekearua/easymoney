package service

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// TestFormatProgramFunds pins the beneficiary-facing copy for restricted
// (programme) balances. The listing exists so a customer learns the rule before
// they reach a checkout, so the three things that actually prevent a failed
// payment — the balance, the expiry, and the merchants the money may be spent
// at — must each be present and unambiguous.
func TestFormatProgramFunds(t *testing.T) {
	t.Parallel()
	expiry := time.Date(2027, time.June, 30, 12, 0, 0, 0, time.UTC)
	closedAt := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC)

	activePurse := store.ProgramPurse{
		ID:          uuid.New(),
		ProgramCode: "agric-support",
		ProgramName: "Agriculture Support Scheme",
		Status:      store.PurseStatusActive,
		ExpiresAt:   &expiry,
		BalanceKobo: 1_250_000,
	}
	merchants := []store.ProgramMember{
		{MerchantName: "Lagos Farm Inputs", Status: store.ProgramMemberApproved},
		{MerchantName: "Green Agro Stores", Status: store.ProgramMemberApproved},
	}

	tests := []struct {
		name       string
		summaries  []PurseSummary
		wantSubs   []string
		rejectSubs []string
	}{
		{
			name:      "no purses explains what programme funds are",
			summaries: nil,
			wantSubs:  []string{"don't have any programme funds", "organisation approves"},
		},
		{
			name:      "an active purse names the grant, balance, expiry and merchants",
			summaries: []PurseSummary{{Purse: activePurse, Merchants: merchants}},
			wantSubs: []string{
				"Agriculture Support Scheme", "₦12,500", "30 Jun 2027",
				"Lagos Farm Inputs", "Green Agro Stores",
				"can't be withdrawn, sent to another person",
			},
		},
		{
			name: "an unexpiring purse says so instead of leaving the line blank",
			summaries: []PurseSummary{{
				Purse: store.ProgramPurse{
					ProgramName: "Relief Fund", Status: store.PurseStatusActive,
					BalanceKobo: 50_000, ExpiresAt: nil,
				},
			}},
			wantSubs: []string{"no expiry set"},
		},
		{
			name: "a purse with nothing left says the balance is empty",
			summaries: []PurseSummary{{
				Purse: store.ProgramPurse{
					ProgramName: "Relief Fund", Status: store.PurseStatusActive,
					BalanceKobo: 0, ExpiresAt: &expiry,
				},
				Merchants: merchants,
			}},
			wantSubs: []string{"balance is empty"},
		},
		{
			name: "a purse with no approved merchants says so",
			summaries: []PurseSummary{{
				Purse: activePurse, Merchants: nil,
			}},
			wantSubs:   []string{"no merchants are approved for this programme"},
			rejectSubs: []string{"Spend at: \n"},
		},
		{
			name: "a long allow-list is capped and the overflow is named",
			summaries: []PurseSummary{{
				Purse: activePurse,
				Merchants: []store.ProgramMember{
					{MerchantName: "A"}, {MerchantName: "B"}, {MerchantName: "C"},
					{MerchantName: "D"}, {MerchantName: "E"}, {MerchantName: "F"},
					{MerchantName: "G"}, {MerchantName: "H"},
				},
			}},
			wantSubs:   []string{"+2 more"},
			rejectSubs: []string{"G,", "H"},
		},
		{
			name: "only returned purses reads as history, not as money to spend",
			summaries: []PurseSummary{{
				Purse: store.ProgramPurse{
					ProgramName: "Agriculture Support Scheme", Status: store.PurseStatusClosed,
					ReturnedKobo: 500_000, ClosedAt: &closedAt,
				},
			}},
			wantSubs:   []string{"no programme funds left", "returned ₦5,000", "1 Aug 2026"},
			rejectSubs: []string{"Balance:"},
		},
		{
			name: "an active purse plus history shows both",
			summaries: []PurseSummary{
				{Purse: activePurse, Merchants: merchants},
				{Purse: store.ProgramPurse{
					ProgramName: "Fertiliser Grant", Status: store.PurseStatusClosed,
					ReturnedKobo: 200_000, ClosedAt: &closedAt,
				}},
			},
			wantSubs: []string{"Agriculture Support Scheme", "Returned or closed:", "Fertiliser Grant: returned ₦2,000"},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := formatProgramFunds(test.summaries)
			for _, want := range test.wantSubs {
				if !strings.Contains(got, want) {
					t.Fatalf("copy missing %q:\n%s", want, got)
				}
			}
			for _, reject := range test.rejectSubs {
				if strings.Contains(got, reject) {
					t.Fatalf("copy unexpectedly contains %q:\n%s", reject, got)
				}
			}
		})
	}
}

// TestProgrammeFundsCommandAliasesRouteToTheListing guards discoverability from
// both directions: the menu row must exist (and stay inside WhatsApp's list
// limit, which TestMenuRowsStayWithinWhatsAppLimit also checks), and every
// free-text alias a customer might type must resolve to the same intent so the
// mid-session switch prompt does not swallow it.
func TestProgrammeFundsCommandAliasesRouteToTheListing(t *testing.T) {
	t.Parallel()
	if !containsRowID(t, mainMenuRows(), "menu_programs") {
		t.Fatal("the main menu must offer the programme-funds row")
	}
	if !menuRowCovers("menu_programs") {
		t.Fatal("menu_programs must be matched by an explicit handleMenu case")
	}
	for _, alias := range []string{
		"programme funds", "program funds", "my programme funds", "menu_programs",
	} {
		if typ, _ := serviceSwitchIntent(alias); typ != "program_funds" {
			t.Fatalf("serviceSwitchIntent(%q) = %q, want program_funds", alias, typ)
		}
	}
}
