package app

import (
	"testing"

	"whatsapp-payment-demo/internal/store"
)

func TestWFBillAmountKobo(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int64
	}{
		{"naira symbol", "Pay ₦5,000 for dinner", 500_000},
		{"NGN prefix", "Amount: NGN 5,000 Date: 2026-01-15", 500_000},
		{"trailing naira", "total is 2,500 naira", 250_000},
		{"spoken plain", "pay 2500 to Ade's Kitchen", 250_000},
		{"spoken with words", "send Ade's Kitchen 3,750 today", 375_000},
		{"decimal", "₦1,250.50 exactly", 125_050},
		{"empty", "", 0},
		{"no amount", "thanks for coming", 0},
		{"phone must not match", "call 08012345678 for help", 0},
		{"date alone must not match", "pay by 2026-01-15 please", 0},
		{"naira floor", "pay 5 naira only", 500},
		{"below floor", "pay 0.90 naira only", 0},      // 90 kobo < ₦1
		{"huge amount", "pay 99,999,999,999 naira", 0}, // above ₦10m cap
		{"first marked wins", "₦8,000 then NGN 2,000", 800_000},
	}
	for _, c := range cases {
		if got := wfBillAmountKobo(c.text); got != c.want {
			t.Errorf("%s: wfBillAmountKobo(%q) = %d, want %d", c.name, c.text, got, c.want)
		}
	}
}

func TestWFBillMerchantSlug(t *testing.T) {
	merchants := []store.Merchant{
		{Slug: "lagos-lunchbox", Name: "Lagos Lunchbox"},
		{Slug: "kora-books", Name: "Kora Books"},
	}
	cases := []struct {
		text string
		want string
		ok   bool
	}{
		{"pay 2500 to Lagos Lunchbox", "lagos-lunchbox", true},
		{"MERCHANT Ade's Kitchen | AMOUNT 2500", "", true},
		{"kora books invoice", "kora-books", true},
		{"", "", true},
		{"nothing here", "", true},
	}
	for _, c := range cases {
		got, ok := wfBillMerchantSlug(c.text, merchants)
		if got != c.want || ok != c.ok {
			t.Errorf("wfBillMerchantSlug(%q) = %q,%v want %q,%v", c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestWFBillMerchantSlugFuzzy(t *testing.T) {
	merchants := []store.Merchant{
		{Slug: "ades-kitchen", Name: "Ade's Kitchen"},
		{Slug: "kora-books", Name: "Kora Books"},
		{Slug: "shop5", Name: "Shop 5 Plaza"},
	}
	cases := []struct {
		text string
		want string
		ok   bool
	}{
		// Apostrophe-less and partial tokens still resolve.
		{"pay ade kitchen 2500", "ades-kitchen", true},
		{"ade's kitchen", "ades-kitchen", true},
		{"send 3000 to kora book", "kora-books", true},
		// Word order does not matter.
		{"kitchen from ade", "ades-kitchen", true},
		// Numbers are significant: "shop 5" is a real name fragment.
		{"pay shop 5 plaza 1000", "shop5", true},
		// A stray short token must not glue itself onto everything.
		{"pay at the shop 2500", "", true},
		// Marked amounts survive fuzzy matching too.
		{"NGN 3000 to Ade's Kitchen", "ades-kitchen", true},
	}
	for _, c := range cases {
		got, ok := wfBillMerchantSlug(c.text, merchants)
		if got != c.want || ok != c.ok {
			t.Errorf("wfBillMerchantSlug(%q) = %q,%v want %q,%v", c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestWFBillMerchantSlugAmbiguousAsksForConfirmation(t *testing.T) {
	// Two distinct merchants whose names both fully fit the text: ok=false
	// tells the caller to surface a choose-from-list confirmation instead of
	// silently picking one side.
	merchants := []store.Merchant{
		{Slug: "kora-books", Name: "Kora Books"},
		{Slug: "kora-bookshop", Name: "Kora Bookshop"},
	}
	got, ok := wfBillMerchantSlug("pay kora book 2500", merchants)
	if ok || got != "" {
		t.Fatalf("expected ambiguity (empty,false), got %q,%v", got, ok)
	}
	// With branches named, naming one resolves it outright.
	branches := []store.Merchant{
		{Slug: "ades-kitchen-ikeja", Name: "Ade's Kitchen Ikeja"},
		{Slug: "ades-kitchen-yaba", Name: "Ade's Kitchen Yaba"},
	}
	got, ok = wfBillMerchantSlug("pay ade kitchen ikeja 2500", branches)
	if !ok || got != "ades-kitchen-ikeja" {
		t.Fatalf("expected the named branch to win, got %q,%v", got, ok)
	}
}

func TestWFBillMerchantSlugLongestWins(t *testing.T) {
	// A short name that is a substring of another must not shadow it.
	merchants := []store.Merchant{
		{Slug: "kora", Name: "Kora"},
		{Slug: "kora-books", Name: "Kora Books"},
	}
	got, ok := wfBillMerchantSlug("Kora Books rocks", merchants)
	if !ok || got != "kora-books" {
		t.Fatalf("expected the longer name to win, got %q,%v", got, ok)
	}
}

func TestWFBillCapturedTextPrefersTypedAsk(t *testing.T) {
	// The typed ask-bar text is the most deliberate input: when present it
	// wins over the OCR/STT extracts. When absent, the extracts are joined.
	payload := map[string]string{
		wfBillAskField:  "pay Ade's Kitchen ₦2,500",
		wfBillPhotoField: "MERCHANT Lagos Lunchbox | AMOUNT 4000",
		wfBillVoiceField: "pay Kora Books 2500",
	}
	if got := wfBillCapturedText(payload); got != "pay Ade's Kitchen ₦2,500" {
		t.Fatalf("typed ask must win over OCR/STT text, got %q", got)
	}
	onlyMedia := map[string]string{
		wfBillPhotoField: "MERCHANT Lagos Lunchbox | AMOUNT 4000",
		wfBillVoiceField: "pay Kora Books 2500",
	}
	if got := wfBillCapturedText(onlyMedia); got != "MERCHANT Lagos Lunchbox | AMOUNT 4000 pay Kora Books 2500" {
		t.Fatalf("media extracts must be joined when no ask is typed, got %q", got)
	}
	if got := wfBillCapturedText(map[string]string{}); got != "" {
		t.Fatalf("empty payload must produce empty text, got %q", got)
	}
	// End-to-end through the parsers: the typed sentence prefills both
	// merchant and amount exactly like an OCR'd bill would.
	merchants := []store.Merchant{{Slug: "ade", Name: "Ade's Kitchen"}}
	slug, ok := wfBillMerchantSlug(wfBillCapturedText(payload), merchants)
	if !ok || slug != "ade" {
		t.Fatalf("typed ask must resolve the merchant, got %q,%v", slug, ok)
	}
	if kobo := wfBillAmountKobo(wfBillCapturedText(payload)); kobo != 250_000 {
		t.Fatalf("typed ask must resolve the amount to 250000 kobo, got %d", kobo)
	}
}

func TestWFKoboToNairaInput(t *testing.T) {
	cases := []struct {
		kobo int64
		want string
	}{
		{250_000, "2500"},
		{250_050, "2500.50"},
		{500, "5"},
		{0, "0"},
	}
	for _, c := range cases {
		if got := wfKoboToNairaInput(c.kobo); got != c.want {
			t.Errorf("wfKoboToNairaInput(%d) = %q, want %q", c.kobo, got, c.want)
		}
	}
}
