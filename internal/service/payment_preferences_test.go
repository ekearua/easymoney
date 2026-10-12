package service

import (
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// TestOrderRailsByPreference pins the ordering contract: the remembered rail
// moves to the front, everything else keeps its relative order, and a rail we
// do not offer on this surface is ignored rather than dropped.
func TestOrderRailsByPreference(t *testing.T) {
	t.Parallel()
	rails := []string{ProviderInterswitch, ProviderWallet, ProviderBankTransfer}

	cases := []struct {
		name       string
		remembered string
		want       []string
	}{
		{"nothing remembered leaves the order alone", "", rails},
		{"wallet remembered moves first", ProviderWallet, []string{ProviderWallet, ProviderInterswitch, ProviderBankTransfer}},
		{"bank transfer remembered moves first", ProviderBankTransfer, []string{ProviderBankTransfer, ProviderInterswitch, ProviderWallet}},
		{"already-first rail keeps its place", ProviderInterswitch, []string{ProviderInterswitch, ProviderWallet, ProviderBankTransfer}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OrderRailsByPreference(tc.remembered, append([]string(nil), rails...))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOrderRailsByPreferenceIgnoresUnknownRail: a remembered rail this surface
// does not offer (a retired rail, or one only another surface supports) must
// leave the choices untouched. Dropping a rail because a preference mentions it
// would remove a way to pay.
func TestOrderRailsByPreferenceIgnoresUnknownRail(t *testing.T) {
	t.Parallel()
	rails := []string{ProviderInterswitch, ProviderBankTransfer}
	got := OrderRailsByPreference("card_on_file", rails)
	if len(got) != 2 || got[0] != ProviderInterswitch || got[1] != ProviderBankTransfer {
		t.Fatalf("unknown remembered rail changed the list: %v", got)
	}
	if got := OrderRailsByPreference(ProviderWallet, []string{ProviderInterswitch}); len(got) != 1 {
		t.Fatalf("single-rail list should be returned unchanged, got %v", got)
	}
}

// TestOrderMethodButtonsByPreference covers the chat picker, where WhatsApp's
// three-button limit means ordering is the only way to surface a habit.
func TestOrderMethodButtonsByPreference(t *testing.T) {
	t.Parallel()
	buttons := []ports.InteractiveButton{
		{ID: "method_card", Title: "Card checkout"},
		{ID: "method_wallet", Title: "Pay from wallet"},
		{ID: "method_bank_transfer", Title: "Bank transfer"},
	}

	got := orderMethodButtonsByPreference(ProviderBankTransfer, buttons)
	if len(got) != 3 || got[0].ID != "method_bank_transfer" {
		t.Fatalf("bank transfer should lead, got %v", got)
	}
	// The remaining buttons keep their order, and no button is lost.
	if got[1].ID != "method_card" || got[2].ID != "method_wallet" {
		t.Fatalf("rest of the picker was reordered: %v", got)
	}

	// Nothing remembered, an unoffered rail, and a picker with no rail-bearing
	// ids all leave the original slice alone.
	if ids := idsOf(orderMethodButtonsByPreference("", buttons)); ids[0] != "method_card" {
		t.Fatalf("empty preference changed the picker: %v", ids)
	}
	if ids := idsOf(orderMethodButtonsByPreference("card_on_file", buttons)); ids[0] != "method_card" {
		t.Fatalf("unoffered preference changed the picker: %v", ids)
	}
	odd := []ports.InteractiveButton{{ID: "next", Title: "Continue"}, {ID: "back", Title: "Back"}}
	if ids := idsOf(orderMethodButtonsByPreference(ProviderWallet, odd)); ids[0] != "next" {
		t.Fatalf("rail-less buttons should be untouched: %v", ids)
	}
}

func idsOf(buttons []ports.InteractiveButton) []string {
	out := make([]string, 0, len(buttons))
	for _, b := range buttons {
		out = append(out, b.ID)
	}
	return out
}

// TestPaymentPreferenceChannel: a preference is remembered per surface, so the
// mapping from a payment's channel to a surface has to be deliberate. An
// unrecognised channel (SMS, a test harness) remembers nothing rather than
// guessing a surface and polluting it.
func TestPaymentPreferenceChannel(t *testing.T) {
	t.Parallel()
	cases := map[string]string{ChannelWhatsApp: store.PreferenceChannelWhatsApp,
		"WHATSAPP":                store.PreferenceChannelWhatsApp,
		ChannelCheckout:           store.PreferenceChannelWeb,
		"web":                     store.PreferenceChannelWeb,
		"browser":                 store.PreferenceChannelWeb,
		ChannelSMS:                "",
		"":                        "",
		"telegram-bot-under-test": "",
	}
	for in, want := range cases {
		if got := PaymentPreferenceChannel(in); got != want {
			t.Errorf("PaymentPreferenceChannel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSavedRailNoticeDoesNotOverPromise is the copy contract: the notice tells
// the payer we put their habit first, and must not read as though a card is
// stored — none is, and saying otherwise would be a trust problem the moment
// they found out.
func TestSavedRailNoticeDoesNotOverPromise(t *testing.T) {
	t.Parallel()
	for _, rail := range []string{ProviderInterswitch, ProviderBankTransfer, ProviderWallet} {
		notice := SavedRailNotice(rail)
		if notice == "" {
			t.Fatalf("no notice for known rail %q", rail)
		}
		if !strings.Contains(notice, "Nothing is charged") {
			t.Errorf("notice for %q does not say nothing is charged: %q", rail, notice)
		}
		lower := strings.ToLower(notice)
		for _, forbidden := range []string{"saved card", "stored card", "card on file", "we saved your"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("notice for %q implies stored card data (%q): %q", rail, forbidden, notice)
			}
		}
	}
	if got := SavedRailNotice("card_on_file"); got != "" {
		t.Fatalf("unknown rail produced a notice: %q", got)
	}
	if got := SavedRailNotice(""); got != "" {
		t.Fatalf("empty rail produced a notice: %q", got)
	}
}

// TestRailClassification: only declared rails are remembered, and the exempt
// providers are the two that are not collection rails at all.
func TestRailClassification(t *testing.T) {
	t.Parallel()
	for _, rail := range DeclaredPaymentRails {
		if !IsDeclaredPaymentRail(rail) {
			t.Errorf("%q is in DeclaredPaymentRails but not recognised", rail)
		}
	}
	for _, notRail := range []string{ProviderAuto, ProviderSimulatedData, "", "card_on_file"} {
		if IsDeclaredPaymentRail(notRail) {
			t.Errorf("%q should not be a declared payment rail", notRail)
		}
	}
	if !IsExemptProviderValue(ProviderAuto) || !IsExemptProviderValue(ProviderSimulatedData) {
		t.Error("router selector and data provider must be exempt from the rail/fee guard")
	}
	if IsExemptProviderValue(ProviderInterswitch) {
		t.Error("a real rail must not be exempt from the rail/fee guard")
	}
}
