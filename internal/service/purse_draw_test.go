package service

import (
	"testing"

	"whatsapp-payment-demo/internal/store"
)

// TestSplitPurseDraw pins the partial-draw arithmetic: the purse funds as much
// of the base as it holds, the wallet funds the rest, and the fee is charged
// only on the wallet portion.
func TestSplitPurseDraw(t *testing.T) {
	t.Parallel()
	// A simple 10% fee-on-wallet to keep the expected numbers obvious.
	fee := func(kobo int64) int64 { return kobo / 10 }

	cases := []struct {
		name       string
		balance    int64
		base       int64
		wantPurse  int64
		wantWallet int64
		wantFee    int64
	}{
		{"empty purse is not selectable (caller filters), split stays all-wallet", 0, 10_000, 0, 10_000, 1_000},
		{"purse cannot cover: partial draw", 3_000, 10_000, 3_000, 7_000, 700},
		{"purse exactly covers: fee-free", 10_000, 10_000, 10_000, 0, 0},
		{"purse exceeds base: capped at base, fee-free", 25_000, 10_000, 10_000, 0, 0},
		{"one kobo short: wallet carries one kobo plus its fee", 9_999, 10_000, 9_999, 1, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := splitPurseDraw(store.ProgramPurse{BalanceKobo: tc.balance}, tc.base, fee)
			if got.purseKobo != tc.wantPurse || got.walletKobo != tc.wantWallet || got.feeKobo != tc.wantFee {
				t.Fatalf("splitPurseDraw(balance=%d, base=%d) = purse %d wallet %d fee %d; want purse %d wallet %d fee %d",
					tc.balance, tc.base, got.purseKobo, got.walletKobo, got.feeKobo, tc.wantPurse, tc.wantWallet, tc.wantFee)
			}
			if got.baseKobo() != tc.base {
				t.Fatalf("baseKobo() = %d, want %d", got.baseKobo(), tc.base)
			}
			if got.totalKobo() != tc.base+tc.wantFee {
				t.Fatalf("totalKobo() = %d, want %d", got.totalKobo(), tc.base+tc.wantFee)
			}
		})
	}
}

// TestSplitPurseDrawNoFeeRail: the invoice/web rail adds no surcharge, so the
// wallet portion is fee-free and total equals base.
func TestSplitPurseDrawNoFeeRail(t *testing.T) {
	t.Parallel()
	got := splitPurseDraw(store.ProgramPurse{BalanceKobo: 2_500}, 10_000, nil)
	if got.purseKobo != 2_500 || got.walletKobo != 7_500 || got.feeKobo != 0 {
		t.Fatalf("no-fee split = purse %d wallet %d fee %d; want 2500/7500/0", got.purseKobo, got.walletKobo, got.feeKobo)
	}
	if got.totalKobo() != 10_000 {
		t.Fatalf("no-fee total = %d, want 10000", got.totalKobo())
	}
}
