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
	}{
		{"pay 2500 to Lagos Lunchbox", "lagos-lunchbox"},
		{"MERCHANT Ade's Kitchen | AMOUNT 2500", ""},
		{"kora books invoice", "kora-books"},
		{"", ""},
		{"nothing here", ""},
	}
	for _, c := range cases {
		if got := wfBillMerchantSlug(c.text, merchants); got != c.want {
			t.Errorf("wfBillMerchantSlug(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestWFBillMerchantSlugLongestWins(t *testing.T) {
	// A short name that is a substring of another must not shadow it.
	merchants := []store.Merchant{
		{Slug: "kora", Name: "Kora"},
		{Slug: "kora-books", Name: "Kora Books"},
	}
	if got := wfBillMerchantSlug("Kora Books rocks", merchants); got != "kora-books" {
		t.Fatalf("expected the longer name to win, got %q", got)
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
