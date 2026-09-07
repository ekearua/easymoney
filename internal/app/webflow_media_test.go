package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/store"
)

func TestWFMediaKind(t *testing.T) {
	cases := []struct {
		mime string
		kind string
		ok   bool
	}{
		{"image/jpeg", "image", true},
		{"image/png", "image", true},
		{"image/HEIC", "image", true}, // lowered by the caller
		{"audio/webm", "audio", true},
		{"audio/mpeg", "audio", true},
		{"application/octet-stream", "", false},
		{"text/plain", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		kind, ok := wfMediaKind(c.mime)
		if kind != c.kind || ok != c.ok {
			t.Errorf("wfMediaKind(%q) = (%q,%v), want (%q,%v)", c.mime, kind, ok, c.kind, c.ok)
		}
	}
}

func TestWFFieldNameOK(t *testing.T) {
	for _, ok := range []string{"id_slip", "id_voice", "x", "a1_b2"} {
		if !wfFieldNameOK(ok) {
			t.Errorf("wfFieldNameOK(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "id slip", "a;b", "drop table", "field\x00", "1234567890123456789012345678901234567890123456789012345678901234567890"} {
		if wfFieldNameOK(bad) {
			t.Errorf("wfFieldNameOK(%q) = true, want false", bad)
		}
	}
}

func TestWFIDNumberFrom(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"NIN: 12345678901", "12345678901"},
		{"My BVN is 01234567890 thanks", "01234567890"},
		{"no number here", ""},
		{"short 12345", ""},
		{"1234567890x12345678901", "12345678901"}, // first 11-digit run wins
		{"", ""},
	}
	for _, c := range cases {
		if got := wfIDNumberFrom(c.raw); got != c.want {
			t.Errorf("wfIDNumberFrom(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello world", 5); got != "hello…" {
		t.Errorf("truncateRunes short = %q, want %q", got, "hello…")
	}
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("truncateRunes no-op = %q, want %q", got, "hello")
	}
	if got := truncateRunes("", 3); got != "" {
		t.Errorf("truncateRunes empty = %q, want empty", got)
	}
	// Multi-byte runes must not be split mid-character.
	long := "ñañañañañañañañañañañaña"
	if got := truncateRunes(long, 4); got != "ñaña…" {
		t.Errorf("truncateRunes unicode = %q, want %q", got, "ñaña…")
	}
}

func TestFriendlyWebPaymentError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"insufficient balance", store.ErrInsufficientWalletBalance, "Your wallet balance is too low"},
		{"wallet not active", store.ErrWalletNotActive, "Wallet payments need an active wallet"},
		{"allowance message", fmt.Errorf("reserve: %w", &kyc.LimitError{Limit: kyc.LimitSingle, Ceiling: 200_000, Request: 250_000}), "over the single-transaction limit"},
		{"unexpected error falls back", errors.New("boom"), "Payment could not be completed. Please go back and try again."},
		{"nil error falls back", nil, "Payment could not be completed. Please go back and try again."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := friendlyWebPaymentError(c.err)
			if !strings.Contains(got, c.want) {
				t.Errorf("friendlyWebPaymentError(%v) = %q, want substring %q", c.err, got, c.want)
			}
		})
	}
}

// TestFriendlyWebPaymentErrorAllowance ensures a real allowance rejection
// (a *kyc.LimitError, as the reservation layer returns) routes through the
// shared mapper's allowance branch even when wrapped.
func TestFriendlyWebPaymentErrorAllowance(t *testing.T) {
	base := &kyc.LimitError{Limit: kyc.LimitSingle, Ceiling: 200_000, Request: 250_000}
	wrapped := fmt.Errorf("reserve allowance: %w", base)
	got := friendlyWebPaymentError(wrapped)
	if !strings.Contains(got, "single-transaction limit") {
		t.Errorf("friendlyWebPaymentError(wrapped kyc error) = %q, want single-transaction copy", got)
	}
	// Wallet-specific errors still take precedence over the generic fallback.
	if got := friendlyWebPaymentError(store.ErrInsufficientWalletBalance); !strings.Contains(got, "Top up your wallet") {
		t.Errorf("friendlyWebPaymentError(wallet balance) = %q", got)
	}
}
