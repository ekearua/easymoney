package service

import (
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/store"
)

func TestLexIndividualPay(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		send         bool
		phone        string
		account      string
		amountKobo   int64
		nameSrcWants string // comma-separated words; "" means don't check
	}{
		{
			name:         "full instruction",
			in:           "send 5000 to 08039999900 gtbank 0123456789",
			send:         true,
			phone:        "+2348039999900",
			account:      "0123456789",
			amountKobo:   500000,
			nameSrcWants: "send to gtbank",
		},
		{
			name:         "faya is a send keyword and a name",
			in:           "faya 2500 to Ada",
			send:         true,
			amountKobo:   250000,
			nameSrcWants: "faya to Ada",
		},
		{
			name:         "pay a merchant",
			in:           "pay Ade's Kitchen 2500",
			send:         true,
			amountKobo:   250000,
			nameSrcWants: "pay Ade s Kitchen",
		},
		{
			name:         "marked naira",
			in:           "transfer 2500 naira to Jumia",
			send:         true,
			amountKobo:   250000,
			nameSrcWants: "transfer naira to Jumia",
		},
		{
			name: "no keyword is not an instruction",
			in:   "what is the price of data",
		},
		{
			name: "keyword alone does not carry fields",
			in:   "pay",
			send: true,
		},
		{
			name:       "a small amount is still payable",
			in:         "send 5 to Ada",
			send:       true,
			amountKobo: 500,
		},
		{
			name:         "phone before amount is not the amount",
			in:           "send to 08039999900 2500",
			send:         true,
			phone:        "+2348039999900",
			amountKobo:   250000,
			nameSrcWants: "send to",
		},
		{
			name:       "234-form phone",
			in:         "send 2000 to 2348039999900 account 0123456789",
			send:       true,
			phone:      "+2348039999900",
			account:    "0123456789",
			amountKobo: 200000,
		},
		{
			name:       "bare national phone",
			in:         "send 2000 to 8039999900",
			send:       true,
			phone:      "+2348039999900",
			amountKobo: 200000,
		},
		{
			name:         "typed merchant instruction keeps digits out of the name",
			in:           "send 3000 to Kora Books 2",
			send:         true,
			amountKobo:   300000,
			nameSrcWants: "send to Kora Books",
		},
		{
			name: "an account-shaped discard from a receipt sentence",
			in:   "check payment reference 1234567890",
			send: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lex := lexIndividualPay(tt.in)
			if lex.send != tt.send {
				t.Fatalf("send = %v, want %v", lex.send, tt.send)
			}
			if lex.phoneE164 != tt.phone {
				t.Errorf("phone = %q, want %q", lex.phoneE164, tt.phone)
			}
			if lex.account != tt.account {
				t.Errorf("account = %q, want %q", lex.account, tt.account)
			}
			if lex.amountKobo != tt.amountKobo {
				t.Errorf("amountKobo = %d, want %d", lex.amountKobo, tt.amountKobo)
			}
			if tt.nameSrcWants != "" && lex.nameSrc != tt.nameSrcWants {
				t.Errorf("nameSrc = %q, want %q", lex.nameSrc, tt.nameSrcWants)
			}
		})
	}
}

func TestPhoneClass(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"08039999900", true},
		{"09012345678", true},
		{"08123456789", true},
		{"08058765432", true},
		{"2348039999900", true},
		{"8039999900", true},
		{"0123456789", false},    // account number, not a mobile prefix
		{"0801234567", false},    // too short
		{"2346000000000", false}, // 234 dialling that is not a mobile prefix
		{"5000", false},
		{"12345678901", false}, // not starting with a mobile prefix
	}
	for _, tt := range tests {
		if got := phoneClass(tt.in); got != tt.want {
			t.Errorf("phoneClass(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIPNameHelpers(t *testing.T) {
	if got := ipNormalizeName("Ade's Kitchen"); got != "ades kitchen" {
		t.Fatalf("ipNormalizeName = %q, want %q", got, "ades kitchen")
	}
	if !ipTokensMatch(strings.Fields("send to ade s kitchen"), []string{"ades", "kitchen"}) {
		t.Fatal("token match should tolerate the apostrophe-truncated 'ade'")
	}
	if ipTokensMatch(strings.Fields("to ada"), []string{"ades", "kitchen"}) {
		t.Fatal("unrelated tokens must not match a merchant name")
	}
	if got := ipNameFromTokens([]string{"to", "ada", "muo"}); got != "ada muo" {
		t.Fatalf("ipNameFromTokens = %q, want %q", got, "ada muo")
	}
	if got := strings.Join(ipStripTokens([]string{"faya", "to", "gtbank", "ops"}, []string{"gtbank"}), " "); got != "faya ops" {
		t.Fatalf("ipStripTokens = %q, want %q", got, "faya ops")
	}
}

func TestMerchantSlugFromText(t *testing.T) {
	merchants := []store.Merchant{
		{Slug: "ade-kitchen", Name: "Ade's Kitchen"},
		{Slug: "jumia", Name: "Jumia"},
		{Slug: "kora-books", Name: "Kora Books"},
		{Slug: "lagos-lunchbox", Name: "Lagos Lunchbox"},
	}
	slug, ok := merchantSlugFromText("pay to ade s kitchen", merchants)
	if !ok || slug != "ade-kitchen" {
		t.Fatalf("explicit 'Ade's Kitchen' resolved to slug=%q ok=%v, want ade-kitchen/true", slug, ok)
	}
	if slug, ok := merchantSlugFromText("to ada", merchants); !ok || slug != "" {
		t.Fatalf("no merchant in 'to ada' should resolve empty/ok, got slug=%q ok=%v", slug, ok)
	}
	if slug, ok := merchantSlugFromText("jumia 5000", merchants); !ok || slug != "jumia" {
		t.Fatalf("one-word merchant 'Jumia' should resolve, got slug=%q ok=%v", slug, ok)
	}
	// Two merchants that share the same display name in different towns are
	// indistinguishable and must report ambiguity rather than pick one winner.
	closeOnes := []store.Merchant{
		{Slug: "kora-books-lagos", Name: "Kora Books"},
		{Slug: "kora-books-abuja", Name: "Kora Books"},
	}
	if _, ok := merchantSlugFromText("send to kora books", closeOnes); ok {
		t.Fatal("two merchants sharing a name should report ambiguity")
	}
}

func TestIndividualPayHintRouting(t *testing.T) {
	base := IndividualPayHint{SendIntent: true}
	if base.Routeable() {
		t.Fatal("a bare keyword must not be routeable")
	}
	withName := IndividualPayHint{SendIntent: true, Name: "ada"}
	if !withName.Routeable() || withName.MerchantTarget() || !withName.IndividualTarget() {
		t.Fatal("a named recipient belongs to the individual flow")
	}
	merchantOnly := IndividualPayHint{SendIntent: true, MerchantSlug: "jumia"}
	if !merchantOnly.Routeable() || !merchantOnly.MerchantTarget() || merchantOnly.IndividualTarget() {
		t.Fatal("a matched merchant alone is a merchant target")
	}
	both := IndividualPayHint{SendIntent: true, MerchantSlug: "jumia", RecipientPhone: "+2348039999900"}
	if both.MerchantTarget() {
		t.Fatal("a phone should pin the instruction to an individual even when a merchant name matched")
	}
	if !both.IndividualTarget() {
		t.Fatal("phone + merchant name must still open the individual flow")
	}
	amb := IndividualPayHint{SendIntent: true, MerchantAmbiguous: true}
	if amb.IndividualTarget() || !amb.AmbiguousMerchant() {
		t.Fatal("an ambiguous merchant must route to the picker, not silently pick a flow")
	}
}
