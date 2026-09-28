package store

import (
	"testing"

	"whatsapp-payment-demo/internal/kyc"
)

// TestKYBDocumentDerivationReviewAndGate walks the whole corporate-evidence
// lifecycle against a real database: derivation counts only submitted
// documents, a reviewer disposition is what turns a claim into evidence,
// replacing a document invalidates the disposition it carried, and the B0 -> B1
// advance stays refused until every key is verified.
func TestKYBDocumentDerivationReviewAndGate(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	corporate := BusinessEvidenceRequiredFor(kyc.TierB1)

	partial := KYBDocuments{
		CACRegNumber: "RC1234567",
		CACDocument:  "RC1234567 ACME LIMITED",
		TINDocument:  "TIN 1234567890 ACME LIMITED",
	}
	if got := DeriveKYBEvidence(partial); len(got) != 2 {
		t.Fatalf("derived %v, want the two documents that were actually submitted", got)
	}
	if got := VerifiedKYBEvidence(partial); len(got) != 0 {
		t.Fatalf("nothing is evidence before a reviewer acts, got %v", got)
	}
	if err := repository.UpsertKYBDocuments(ctx, merchant.ID, partial); err != nil {
		t.Fatal(err)
	}
	// A reviewer cannot disposition a document the merchant never submitted.
	if _, err := repository.ReviewKYBDocument(ctx, merchant.ID, kyc.EvSCUMLSubmitted, KYBDocumentVerified, "rev@xego.test", ""); err == nil {
		t.Fatal("reviewing an unsubmitted document must fail")
	}
	// The hard gate refuses the advance while the documents are merely on file.
	if _, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB1, corporate, nil); err == nil {
		t.Fatal("B0->B1 with unverified documents must fail")
	}
	// Verify one key: it becomes evidence and the other stays a claim.
	after, err := repository.ReviewKYBDocument(ctx, merchant.ID, kyc.EvCACVerified, KYBDocumentVerified, "rev@xego.test", "matches the register")
	if err != nil {
		t.Fatal(err)
	}
	if got := VerifiedKYBEvidence(after); len(got) != 1 || got[0] != kyc.EvCACVerified {
		t.Fatalf("verified = %v, want just %s", got, kyc.EvCACVerified)
	}
	if _, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB1, corporate, nil); err == nil {
		t.Fatal("B0->B1 with only one verified document must fail")
	}
	// Replacing a document invalidates the disposition it carried: the
	// reviewer verified the certificate that was on file, not this one.
	changed := partial
	changed.CACRegNumber = "RC9999999"
	if err := repository.UpsertKYBDocuments(ctx, merchant.ID, changed); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repository.KYBDocumentsByMerchant(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Reviews) != 0 {
		t.Fatalf("replacing a document must clear its review, got %+v", reloaded.Reviews)
	}
	if got := VerifiedKYBEvidence(reloaded); len(got) != 0 {
		t.Fatalf("verified after replacement = %v, want nothing", got)
	}
	// A re-capture of the same claim changes no document, so it is allowed
	// through the upsert without disturbing anything.
	// Complete the set: every document present and verified opens the gate.
	seedVerifiedCorporateDocuments(t, ctx, repository, merchant.ID)
	profile, err := repository.AdvanceKYBTier(ctx, merchant.ID, kyc.TierB1, corporate, nil)
	if err != nil || profile.Tier != kyc.TierB1 {
		t.Fatalf("B0->B1 with a fully verified set should pass: tier=%s err=%v", profile.Tier, err)
	}
}

// TestKYBIdenticalResubmissionKeepsReviews pins the other half of the
// invalidation rule: re-submitting the same documents must not throw away a
// reviewer's work.
func TestKYBIdenticalResubmissionKeepsReviews(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	docs := KYBDocuments{
		CACRegNumber:    "RC1234567",
		CACDocument:     "RC1234567 ACME LIMITED",
		BusinessAddress: "12 Marina Road, Lagos Island, Lagos",
	}
	if err := repository.UpsertKYBDocuments(ctx, merchant.ID, docs); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReviewKYBDocument(ctx, merchant.ID, kyc.EvCACVerified, KYBDocumentVerified, "rev@xego.test", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpsertKYBDocuments(ctx, merchant.ID, docs); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repository.KYBDocumentsByMerchant(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := VerifiedKYBEvidence(reloaded); len(got) != 1 || got[0] != kyc.EvCACVerified {
		t.Fatalf("an identical resubmission dropped the review: %v", got)
	}
}

// TestKYBLocationRoundTrip pins that a captured position and the verification
// proposal are stored independently of the reviewer dispositions, so a
// re-capture never silently invalidates a document review.
func TestKYBLocationRoundTrip(t *testing.T) {
	repository, ctx := openAllowanceTestRepository(t)
	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpsertKYBDocuments(ctx, merchant.ID, KYBDocuments{
		BusinessAddress: "12 Marina Road, Lagos Island, Lagos",
		AddressDocument: "Meter 5551234, 12 Marina Road, Lagos",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReviewKYBDocument(ctx, merchant.ID, kyc.EvLocationVerified, KYBDocumentVerified, "rev@xego.test", ""); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordKYBLocation(ctx, merchant.ID, 6.4531, 3.3958, 25, "matched", "simulated"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveKYBVerification(ctx, merchant.ID, kyc.EvLocationVerified, KYBDocumentVerification{
		Status:    "matched",
		Provider:  "simulated",
		MatchName: true,
		Message:   "within tolerance",
	}); err != nil {
		t.Fatal(err)
	}
	docs, err := repository.KYBDocumentsByMerchant(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if docs.LocationLat == nil || *docs.LocationLat != 6.4531 || docs.LocationStatus != "matched" {
		t.Fatalf("location fields = %+v", docs)
	}
	if docs.LocationAccuracyM == nil || *docs.LocationAccuracyM != 25 {
		t.Fatalf("accuracy = %v", docs.LocationAccuracyM)
	}
	if docs.LocationCapturedAt == nil {
		t.Fatal("capture time should be recorded")
	}
	if got := VerifiedKYBEvidence(docs); len(got) != 1 || got[0] != kyc.EvLocationVerified {
		t.Fatalf("the document review must survive a location capture, got %v", got)
	}
	if v, ok := docs.Verifications[kyc.EvLocationVerified]; !ok || v.Status != "matched" {
		t.Fatalf("verification proposal = %+v", docs.Verifications)
	}
}
