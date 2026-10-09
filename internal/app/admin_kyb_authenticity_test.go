package app

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// TestKYBDocumentViewsSurfaceAuthenticity pins the G12 reviewer-panel data
// surface: the provider's authenticity verdict is shown beside the human
// decision for the same document, a key the provider has not judged shows
// nothing, and a tampered verdict is visible without hiding the review.
func TestKYBDocumentViewsSurfaceAuthenticity(t *testing.T) {
	t.Parallel()
	docs := store.KYBDocuments{
		MerchantID:   uuid.New(),
		CACDocument:  "RC1234567 ACME LIMITED",
		CACRegNumber: "RC1234567",
		Reviews: map[string]store.KYBDocumentReview{
			kyc.EvCACVerified: {Status: store.KYBDocumentVerified, Reviewer: "compliance@faya.test"},
		},
		Authenticity: map[string]store.KYBDocumentAuthenticity{
			kyc.EvCACVerified: {
				Status:   ports.AuthenticityTampered,
				Provider: "simulated",
				Message:  "seal mismatch",
			},
		},
	}

	views := kybDocumentViews(docs)
	var cacView *kybDocumentView
	for i := range views {
		if views[i].Key == kyc.EvCACVerified {
			cacView = &views[i]
		}
	}
	if cacView == nil {
		t.Fatalf("the CAC evidence key is missing from the reviewer panel: %+v", views)
	}
	if cacView.Status != store.KYBDocumentVerified {
		t.Fatalf("review status = %q, want %q", cacView.Status, store.KYBDocumentVerified)
	}
	if !strings.Contains(cacView.Authenticity, ports.AuthenticityTampered) ||
		!strings.Contains(cacView.Authenticity, "seal mismatch") {
		t.Fatalf("authenticity must be shown beside the review, got %q", cacView.Authenticity)
	}
	// A key with no document and no verdict shows nothing on either surface.
	for _, v := range views {
		if v.Key == kyc.EvSCUMLSubmitted {
			if v.Authenticity != "" {
				t.Fatalf("an unjudged document must show no authenticity verdict, got %q", v.Authenticity)
			}
			if v.Status != "" {
				t.Fatalf("an unsubmitted document must show no review, got %q", v.Status)
			}
		}
	}
}
