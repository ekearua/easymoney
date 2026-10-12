package service

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// TestOrderByRail pins the ordering contract shared by the chat and web pickers:
// the remembered rail moves to the front, everything else keeps its relative
// order, and a rail we do not offer on this surface is ignored rather than
// dropped.
func TestOrderByRail(t *testing.T) {
	t.Parallel()
	rails := []string{ProviderInterswitch, ProviderWallet, ProviderBankTransfer}
	railOf := func(s string) string { return s }

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
			got := OrderByRail(tc.remembered, append([]string(nil), rails...), railOf)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOrderByRailIgnoresUnknownRail: a remembered rail this surface does not
// offer (a retired rail, or one only another surface supports) must leave the
// choices untouched. Dropping a rail because a preference mentions it would
// remove a way to pay.
func TestOrderByRailIgnoresUnknownRail(t *testing.T) {
	t.Parallel()
	railOf := func(s string) string { return s }
	rails := []string{ProviderInterswitch, ProviderBankTransfer}
	got := OrderByRail("card_on_file", rails, railOf)
	if len(got) != 2 || got[0] != ProviderInterswitch || got[1] != ProviderBankTransfer {
		t.Fatalf("unknown remembered rail changed the list: %v", got)
	}
	if got := OrderByRail(ProviderWallet, []string{ProviderInterswitch}, railOf); len(got) != 1 {
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

// TestSavedMethodLines pins the details-surface formatter: one payer-facing
// line per surface and rail, de-duplicated across scopes (a merchant habit must
// not repeat the standing choice), unknown rails dropped, and no lines at all
// when nothing is remembered.
func TestSavedMethodLines(t *testing.T) {
	t.Parallel()
	prefs := []store.PaymentPreference{
		{Channel: store.PreferenceChannelWhatsApp, Scope: store.PreferenceScopeDefault, Rail: ProviderInterswitch},
		{Channel: store.PreferenceChannelWhatsApp, Scope: store.PreferenceScopeMerchant, ScopeRef: "merchant-1", Rail: ProviderInterswitch},
		{Channel: store.PreferenceChannelWeb, Scope: store.PreferenceScopeDefault, Rail: ProviderBankTransfer},
		{Channel: store.PreferenceChannelWhatsApp, Scope: store.PreferenceScopeDefault, Rail: "card_on_file"},
	}
	got := SavedMethodLines(prefs)
	want := []string{"Card checkout — in chat", "Bank transfer — on the web"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("SavedMethodLines = %v, want %v", got, want)
	}
	if lines := SavedMethodLines(nil); len(lines) != 0 {
		t.Fatalf("no preferences must yield no lines, got %v", lines)
	}
}

// TestRememberRailOnSuccess pins the exclusion at the write path: only a
// merchant collection is a habit worth remembering. A wallet top-up funds the
// payer's own wallet and an individual pay settles a recipient payout; if
// either were remembered, a top-up would reorder the rails on the next merchant
// purchase. Every collection rail, by contrast, is remembered whatever it is.
func TestRememberRailOnSuccess(t *testing.T) {
	t.Parallel()
	svc := &PaymentService{}
	view := func(provider, metadata string) store.PaymentView {
		return store.PaymentView{Payment: domain.Payment{Provider: provider, Metadata: json.RawMessage(metadata)}}
	}

	collections := []struct {
		name     string
		provider string
		metadata string
	}{
		{"card collection", ProviderInterswitch, ""},
		{"wallet collection", ProviderWallet, ""},
		{"bank transfer collection", ProviderBankTransfer, ""},
		{"collection with unrelated metadata", ProviderWallet, `{"request_code":"XG-1"}`},
	}
	for _, tc := range collections {
		if !svc.rememberRailOnSuccess(view(tc.provider, tc.metadata)) {
			t.Errorf("%s must be remembered", tc.name)
		}
	}

	notHabits := []struct {
		name     string
		provider string
		metadata string
	}{
		{"wallet top-up", ProviderInterswitch, `{"wallet_topup":{"amount_kobo":500000}}`},
		{"individual pay", ProviderBankTransfer, `{"individual_pay":{"recipient_phone":"+2348000000000","amount_kobo":100000}}`},
	}
	for _, tc := range notHabits {
		if svc.rememberRailOnSuccess(view(tc.provider, tc.metadata)) {
			t.Errorf("%s must not be remembered as a merchant habit", tc.name)
		}
	}
}

// TestSavedRailNoticeSuffix pins the one place a picker's body learns the
// remembered-habit sentence: it is empty when we remember nothing, and
// otherwise carries exactly one blank-line-separated notice (so a picker that
// concatenates it does not accidentally double-space or forget it).
func TestSavedRailNoticeSuffix(t *testing.T) {
	t.Parallel()
	if got := savedRailNoticeSuffix(""); got != "" {
		t.Fatalf("no remembered rail must add no suffix, got %q", got)
	}
	if got := savedRailNoticeSuffix("card_on_file"); got != "" {
		t.Fatalf("an unknown remembered rail must add no suffix, got %q", got)
	}
	got := savedRailNoticeSuffix(ProviderBankTransfer)
	want := "\n\n" + SavedRailNotice(ProviderBankTransfer)
	if got != want {
		t.Fatalf("suffix = %q, want %q", got, want)
	}
}

// TestChatMethodButtonsReachTheSharedOrderingHelper is the drift guard for the
// chat pickers. Every method button a picker renders has to be built inside an
// orderMethodButtonsByPreference(...) call, because that call is the only thing
// that surfaces the payer's remembered rail (WhatsApp's three-button limit
// leaves no room for a separate "saved" button). A new picker that hand-builds
// a method_* slice would silently ignore the preference, so the build fails
// here instead.
func TestChatMethodButtonsReachTheSharedOrderingHelper(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		var rawButtons []token.Pos
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok && isMethodButtonLiteral(lit) {
				rawButtons = append(rawButtons, lit.Pos())
			}
			return true
		})
		for _, pos := range rawButtons {
			if !insideOrderingHelperCall(file, pos) {
				t.Errorf("%s:%s: method button must be built inside orderMethodButtonsByPreference(...)",
					name, fset.Position(pos))
			}
		}
	}
}

// isMethodButtonLiteral reports whether the composite literal is a
// ports.InteractiveButton whose ID is one of the method_* picker buttons.
func isMethodButtonLiteral(lit *ast.CompositeLit) bool {
	selector, ok := lit.Type.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "InteractiveButton" {
		return false
	}
	if pkg, ok := selector.X.(*ast.Ident); !ok || pkg.Name != "ports" {
		return false
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "ID" {
			continue
		}
		value, ok := kv.Value.(*ast.BasicLit)
		if !ok || value.Kind != token.STRING {
			continue
		}
		id, err := strconv.Unquote(value.Value)
		if err == nil && strings.HasPrefix(id, "method_") {
			return true
		}
	}
	return false
}

// insideOrderingHelperCall reports whether pos falls within a call to
// orderMethodButtonsByPreference.
func insideOrderingHelperCall(file *ast.File, pos token.Pos) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "orderMethodButtonsByPreference" {
			return true
		}
		if pos >= call.Pos() && pos <= call.End() {
			found = true
		}
		return !found
	})
	return found
}
