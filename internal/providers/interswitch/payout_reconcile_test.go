package interswitch

import (
	"context"
	"net/http"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

// normalizeTransferStatus must never turn an unsettled or unrecognised status
// into "failed": that is the outcome reconciliation exists to prevent.
func TestNormalizeTransferStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status, code, want string
	}{
		{"Completed", "90000", ports.PayoutReconcileSucceeded},
		{"SUCCESSFUL", "", ports.PayoutReconcileSucceeded},
		{"", "90000", ports.PayoutReconcileSucceeded},
		{"Failed", "51", ports.PayoutReconcileFailed},
		{"DECLINED", "51", ports.PayoutReconcileFailed},
		{"Rejected", "", ports.PayoutReconcileFailed},
		{"Reversed", "", ports.PayoutReconcileFailed},
		{"Pending", "", ports.PayoutReconcilePending},
		{"Processing", "", ports.PayoutReconcilePending},
		{"", "", ports.PayoutReconcileUnknown},
		{"Something New", "0", ports.PayoutReconcilePending},
	}
	for _, tc := range cases {
		if got := normalizeTransferStatus(tc.status, tc.code); got != tc.want {
			t.Errorf("normalizeTransferStatus(%q, %q) = %q, want %q", tc.status, tc.code, got, tc.want)
		}
	}
}

// ReconcilePayout must ask the v5 query endpoint with the payout reference as
// requestRef and report the rail's verdict, so a timed-out TransferFunds that
// actually executed is completed instead of marked failed.
func TestReconcilePayoutReportsRailVerdict(t *testing.T) {
	t.Parallel()
	srv := newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != queryPath || r.Method != http.MethodGet {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("requestRef"); got != "payout-77" {
			t.Fatalf("requestRef query = %q, want the payout reference", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestReference":"payout-77","status":"Completed","transactionRef":"PBL|LOC|CA|ABP|AC|260918000000|U4AN48D7","transactionResponseCode":"90000","amount":"250000","currencyCode":"566"}`))
	})
	client := New(Options{ClientID: "c", ClientSecret: "s", BaseURL: srv.URL, TransferBaseURL: srv.URL})

	got, err := client.ReconcilePayout(context.Background(), "payout-77")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ports.PayoutReconcileSucceeded {
		t.Fatalf("status = %q, want %q", got.Status, ports.PayoutReconcileSucceeded)
	}
	if got.ExternalRef != "PBL|LOC|CA|ABP|AC|260918000000|U4AN48D7" {
		t.Fatalf("external ref = %q", got.ExternalRef)
	}
}
