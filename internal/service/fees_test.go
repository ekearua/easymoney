package service

import (
	"testing"

	"whatsapp-payment-demo/internal/config"
)

func TestXegoCollectionFee(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		FeeCardBPS:           200,
		FeeCardFixedKobo:     10_000,
		FeeCardCapKobo:       350_000,
		FeeDVABPS:            150,
		FeeDVAFixedKobo:      0,
		FeeDVACapKobo:        150_000,
		FeeTransferBPS:       180,
		FeeTransferFixedKobo: 0,
		FeeTransferCapKobo:   250_000,
	}

	tests := []struct {
		channel      string
		amountKobo   int64
		expectedFee  int64
		description  string
	}{
		{"card", 50_000, 11_000, "2% of ₦500 + ₦100 = ₦110"},
		{"card", 1_000_000, 30_000, "2% of ₦10,000 + ₦100 = ₦300"},
		{"card", 20_000_000, 350_000, "cap at ₦3,500 for ₦200,000 card payment"},
		{"card", 17_000_000, 350_000, "breakpoint: exact cap"},
		{"card", 17_000_001, 350_000, "breakpoint: cap hit"},
		{"dva", 10_000_000, 150_000, "DVA 1.5% cap at ₦1,500"},
		{"dva", 10_000_001, 150_000, "DVA cap hit just over"},
		{"dva", 50_000, 750, "DVA 1.5% of ₦500 = ₦7.50"},
		{"transfer", 13_900_000, 250_000, "Transfer 1.8% of ₦139,000 hits cap"},
		{"transfer", 13_900_001, 250_000, "Transfer cap hit"},
		{"transfer", 50_000, 900, "Transfer 1.8% of ₦500 = ₦9"},
		{"bank_transfer", 10_000_000, 150_000, "bank_transfer uses DVA params"},
		{"unknown_channel", 100_000, 0, "unknown channel returns zero fee"},
		{"card", 0, 0, "zero amount returns zero fee"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			got := XegoCollectionFee(cfg, tc.channel, tc.amountKobo)
			if got.FeeKobo != tc.expectedFee {
				t.Fatalf("channel=%q amount=%d: got fee %d, want %d",
					tc.channel, tc.amountKobo, got.FeeKobo, tc.expectedFee)
			}
		})
	}
}

func TestXegoCollectionFeeNeverExceedsAmount(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		FeeCardBPS:       200,
		FeeCardFixedKobo: 10_000,
		FeeCardCapKobo:   350_000,
	}
	fee := XegoCollectionFee(cfg, "card", 1_000)
	if fee.FeeKobo > 1_000 {
		t.Fatalf("fee %d exceeds amount 1000", fee.FeeKobo)
	}
}

func TestXegoPayoutFee(t *testing.T) {
	t.Parallel()
	cfg := config.Config{FeeNIPPayoutFlatKobo: 10_000}
	if got := XegoPayoutFee(cfg, 50_000); got != 10_000 {
		t.Fatalf("expected ₦100 flat, got %d", got)
	}
	if got := XegoPayoutFee(cfg, 5_000); got != 5_000 {
		t.Fatalf("fee capped at amount, got %d", got)
	}
	if got := XegoPayoutFee(cfg, 0); got != 0 {
		t.Fatalf("zero amount yields zero fee, got %d", got)
	}
}

func TestMerchantReceivable(t *testing.T) {
	t.Parallel()
	if got := MerchantReceivable(100_000, 15_000); got != 85_000 {
		t.Fatalf("expected 85000, got %d", got)
	}
	if got := MerchantReceivable(10_000, 20_000); got != 0 {
		t.Fatalf("fee exceeds amount, expected 0, got %d", got)
	}
}
