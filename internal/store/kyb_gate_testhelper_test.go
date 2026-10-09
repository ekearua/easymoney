package store

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

// seedVerifiedCorporateDocuments records a complete corporate document set and
// has a reviewer verify each key in it, returning the five evidence keys.
//
// The B0 -> B1 hard gate accepts a key only when the document behind it carries
// a reviewer disposition AND a genuine authenticity verdict (spec G12), so any
// test that climbs past B0 has to seed all three: the documents, the reviews,
// and the authenticity verdicts.
func seedVerifiedCorporateDocuments(t *testing.T, ctx context.Context, repository *Store, merchantID uuid.UUID) []string {
	t.Helper()
	docs := KYBDocuments{
		CACRegNumber:     "RC1234567",
		TIN:              "1234567890",
		BeneficialOwners: "Ada Obi 60%, Chidi Obi 40%",
		BusinessAddress:  "12 Marina Road, Lagos Island, Lagos",
		SCUMLNumber:      "SC123456",
		CACDocument:      "RC1234567 ACME LIMITED",
		TINDocument:      "TIN 1234567890 ACME LIMITED",
		AddressDocument:  "Meter 5551234, 12 Marina Road, Lagos",
	}
	if err := repository.UpsertKYBDocuments(ctx, merchantID, docs); err != nil {
		t.Fatalf("seed kyb documents: %v", err)
	}
	evidence := DeriveKYBEvidence(docs)
	if len(evidence) != 5 {
		t.Fatalf("seeded documents justify %d evidence keys, want 5", len(evidence))
	}
	for _, key := range evidence {
		if _, err := repository.ReviewKYBDocument(ctx, merchantID, key, KYBDocumentVerified, "compliance@xego.test", "seeded for test"); err != nil {
			t.Fatalf("verify kyb document %s: %v", key, err)
		}
		if err := repository.SaveKYBDocumentAuthenticity(ctx, merchantID, key, KYBDocumentAuthenticity{
			Status:      ports.AuthenticityGenuine,
			Provider:    "simulated",
			ProviderRef: "seed-" + key,
			Confidence:  0.97,
		}); err != nil {
			t.Fatalf("seed kyb document authenticity %s: %v", key, err)
		}
	}
	for _, key := range BusinessEvidenceRequiredFor(kyc.TierB1) {
		if !containsString(evidence, key) {
			t.Fatalf("seeded evidence missing %s: %v", key, evidence)
		}
	}
	return evidence
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
