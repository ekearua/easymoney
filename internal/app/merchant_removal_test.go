package app

import (
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/store"
)

// TestMerchantRemovalBlockedError guards the admin-facing message shown when a
// merchant cannot be hard-deleted. The message must name the records that
// actually block removal, because admins previously saw "payments or invoices"
// for merchants whose only blocker was something else entirely.
func TestMerchantRemovalBlockedError(t *testing.T) {
	cases := []struct {
		name   string
		counts store.MerchantFinancialRecordCounts
		want   string
	}{
		{"single payment", store.MerchantFinancialRecordCounts{Payments: 1}, "1 payment and cannot be removed"},
		{"several payments", store.MerchantFinancialRecordCounts{Payments: 7}, "7 payments and cannot be removed"},
		{"ledger only", store.MerchantFinancialRecordCounts{Ledger: 3}, "3 ledger entries and cannot be removed"},
		{"checkout and invoice", store.MerchantFinancialRecordCounts{Checkouts: 1, Invoices: 2}, "2 invoices, 1 checkout and cannot be removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := merchantRemovalBlockedError("Shop", tc.counts)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want substring %q", got, tc.want)
			}
			if !strings.HasPrefix(got, "Shop has ") {
				t.Fatalf("message must name the merchant: %q", got)
			}
			if !strings.Contains(got, "Disable the merchant instead") {
				t.Fatalf("message must point at the safe action: %q", got)
			}
		})
	}
}
