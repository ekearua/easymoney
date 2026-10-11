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
			name:       "a purse with no approved merchants says so",
			summaries:  []PurseSummary{{Purse: activePurse, Merchants: nil}},
			wantSubs:   []string{"no merchants are approved for this programme"},
			rejectSubs: []string{"Spend at: \\n"},
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

// TestProgramPurseDetailLines pins the purse lines the details surfaces render:
// an active purse lists balance, expiry and the approved-merchant count; an
// empty purse says the balance is spent rather than implying there is money; a
// returned grant appears as history; and a customer with no purses gets no
// lines at all so the section disappears rather than showing an empty list.
func TestProgramPurseDetailLines(t *testing.T) {
	t.Parallel()
	expiry := time.Date(2027, time.June, 30, 12, 0, 0, 0, time.UTC)
	closedAt := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC)

	active := PurseSummary{
		Purse: store.ProgramPurse{
			ProgramCode: "agric-support", ProgramName: "Agriculture Support Scheme",
			Status: store.PurseStatusActive, ExpiresAt: &expiry, BalanceKobo: 1_250_000,
		},
		Merchants: []store.ProgramMember{
			{MerchantName: "Lagos Farm Inputs", Status: store.ProgramMemberApproved},
			{MerchantName: "Green Agro Stores", Status: store.ProgramMemberApproved},
		},
	}

	tests := []struct {
		name       string
		summaries  []PurseSummary
		wantSubs   []string
		rejectSubs []string
	}{
		{
			name:      "no purses yields no lines",
			summaries: nil,
			wantSubs:  nil,
		},
		{
			name:      "an active purse shows balance, expiry and merchant count",
			summaries: []PurseSummary{active},
			wantSubs:  []string{"Agriculture Support Scheme", "balance ₦12,500", "expires 30 Jun 2027", "2 approved merchants"},
		},
		{
			name: "a single approved merchant is not pluralised",
			summaries: []PurseSummary{{
				Purse:     store.ProgramPurse{ProgramName: "Relief Fund", Status: store.PurseStatusActive, ExpiresAt: nil, BalanceKobo: 50_000},
				Merchants: []store.ProgramMember{{MerchantName: "One Shop", Status: store.ProgramMemberApproved}},
			}},
			wantSubs: []string{"1 approved merchant", "no expiry"},
		},
		{
			name: "a spent purse says the balance is gone instead of implying money",
			summaries: []PurseSummary{{
				Purse:     store.ProgramPurse{ProgramName: "Relief Fund", Status: store.PurseStatusActive, ExpiresAt: &expiry, BalanceKobo: 0},
				Merchants: nil,
			}},
			wantSubs:   []string{"balance spent"},
			rejectSubs: []string{"balance ₦0"},
		},
		{
			name: "a returned grant reads as history naming the returned amount",
			summaries: []PurseSummary{{
				Purse: store.ProgramPurse{
					ProgramName: "Fertiliser Grant", Status: store.PurseStatusClosed,
					ReturnedKobo: 200_000, ClosedAt: &closedAt,
				},
			}},
			wantSubs:   []string{"Programme funds (returned)", "unspent ₦2,000 returned to the sponsor", "on 1 Aug 2026"},
			rejectSubs: []string{},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := programPurseDetailLines(test.summaries)
			if test.name == "no purses yields no lines" {
				if len(got) != 0 {
					t.Fatalf("no purses must yield no lines, got %d: %v", len(got), got)
				}
				return
			}
			joined := ""
			for _, line := range got {
				joined += line.Term + ": " + line.Desc + "\n"
			}
			for _, want := range test.wantSubs {
				if !strings.Contains(joined, want) {
					t.Fatalf("lines missing %q:\n%s", want, joined)
				}
			}
			for _, reject := range test.rejectSubs {
				if strings.Contains(joined, reject) {
					t.Fatalf("lines unexpectedly contain %q:\n%s", reject, joined)
				}
			}
		})
	}
}
