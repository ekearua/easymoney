package app

import (
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/store"
)

// TestTokenSpendLabel pins the naira conversion of the token-trend hover
// labels: the configured tokens-per-naira rate turns a token count into an
// estimated naira figure (with the raw tokens kept alongside), an unset rate
// and zero-token days fall back to the plain count, and sub-naira days stay
// visible instead of rounding to ₦0.
func TestTokenSpendLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		tokens       int64
		tokensPerNGN int64
		want         string
	}{
		{"spike day reads as money", 300_000, 250, "≈ ₦1,200 (300000 tokens)"},
		{"small day keeps sub-naira precision", 100, 250, "≈ ₦0.40 (100 tokens)"},
		{"unset rate falls back to tokens", 4500, 0, "4500 tokens"},
		{"zero-token day shows zero", 0, 250, "0 tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenSpendLabel(tc.tokens, tc.tokensPerNGN)
			if got != tc.want {
				t.Fatalf("tokenSpendLabel(%d, %d) = %q, want %q", tc.tokens, tc.tokensPerNGN, got, tc.want)
			}
			// The raw token count must always survive alongside the money
			// figure so the estimate stays auditable.
			if tc.tokensPerNGN > 0 && tc.tokens > 0 && !strings.Contains(got, "tokens") {
				t.Fatalf("label %q lost the raw token count", got)
			}
		})
	}
}

// TestMediaTokenTrendSVGMoneyLabels ensures the rendered sparkline carries
// the naira estimate in each bar's hover title, and an unset rate renders
// token-only labels (the chart itself must never silently drop the scale).
func TestMediaTokenTrendSVGMoneyLabels(t *testing.T) {
	t.Parallel()
	svg := string(mediaTokenTrendSVG([]store.TokenDayStat{{Day: time.Now(), Tokens: 300_000}}, 250))
	if !strings.Contains(svg, "≈ ₦1,200") {
		t.Fatalf("sparkline labels must show the naira estimate, got %q", svg)
	}
	tokenSVG := string(mediaTokenTrendSVG([]store.TokenDayStat{{Day: time.Now(), Tokens: 4500}}, 0))
	if !strings.Contains(tokenSVG, "4500 tokens") || strings.Contains(tokenSVG, "₦") {
		t.Fatalf("unset rate must render token-only labels, got %q", tokenSVG)
	}
}
