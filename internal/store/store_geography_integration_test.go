package store

import (
	"context"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/kyc"

	"github.com/google/uuid"
)

// TestGeographyInputsRoundTrip pins the G14 capture and read-back: the
// individual geography write survives through both the individual profile and
// the KYC ladder join, including creating a profile row for a user who has
// not onboarded yet.
func TestGeographyInputsRoundTrip(t *testing.T) {
	repository := openRiskTestStore(t)
	ctx := context.Background()

	user, err := repository.GetOrCreateUser(ctx, "+2348090002101")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetIndividualGeography(ctx, user.ID, "NG", kyc.VolumeBandHigh); err != nil {
		t.Fatal(err)
	}
	profile, err := repository.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.CountryISO != "NG" || profile.ExpectedVolumeBand != kyc.VolumeBandHigh {
		t.Fatalf("KYCProfileByUser geography = %q/%q, want NG/%s", profile.CountryISO, profile.ExpectedVolumeBand, kyc.VolumeBandHigh)
	}
	individual, err := repository.IndividualProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if individual.CountryISO != "NG" || individual.ExpectedVolumeBand != kyc.VolumeBandHigh {
		t.Fatalf("IndividualProfileByUser geography = %q/%q, want NG/%s", individual.CountryISO, individual.ExpectedVolumeBand, kyc.VolumeBandHigh)
	}

	// A user with no individual profile at all gets one created by the write.
	leaf, err := repository.GetOrCreateUser(ctx, "+2348090002102")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetIndividualGeography(ctx, leaf.ID, "gb", kyc.VolumeBandLow); err != nil {
		t.Fatal(err)
	}
	leafProfile, err := repository.IndividualProfileByUser(ctx, leaf.ID)
	if err != nil {
		t.Fatalf("geography write must open a missing profile: %v", err)
	}
	if leafProfile.CountryISO != "gb" || leafProfile.ExpectedVolumeBand != kyc.VolumeBandLow {
		t.Fatalf("created profile geography = %q/%q, want gb/%s", leafProfile.CountryISO, leafProfile.ExpectedVolumeBand, kyc.VolumeBandLow)
	}
}

// TestBusinessGeographyRoundTrip pins the merchant side of G14 through both the
// single-profile read and the list the admin console renders.
func TestBusinessGeographyRoundTrip(t *testing.T) {
	repository := openRiskTestStore(t)
	ctx := context.Background()

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetBusinessGeography(ctx, merchant.ID, "NG", kyc.VolumeBandVeryHigh); err != nil {
		t.Fatal(err)
	}
	profile, err := repository.KYBProfileByMerchant(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.CountryISO != "NG" || profile.ExpectedVolumeBand != kyc.VolumeBandVeryHigh {
		t.Fatalf("KYBProfileByMerchant geography = %q/%q, want NG/%s", profile.CountryISO, profile.ExpectedVolumeBand, kyc.VolumeBandVeryHigh)
	}
	profiles, err := repository.ListKYBProfiles(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range profiles {
		if p.MerchantID == merchant.ID {
			found = true
			if p.CountryISO != "NG" || p.ExpectedVolumeBand != kyc.VolumeBandVeryHigh {
				t.Fatalf("ListKYBProfiles geography = %q/%q, want NG/%s", p.CountryISO, p.ExpectedVolumeBand, kyc.VolumeBandVeryHigh)
			}
		}
	}
	if !found {
		t.Fatal("merchant missing from ListKYBProfiles")
	}
}

// TestRecomputeRiskScoreAddsGeographyWeight pins the score wiring end to end: a
// profile whose country of residence is on the FATF list scores exactly the
// geography weight above an otherwise identical profile with no declared
// country.
func TestRecomputeRiskScoreAddsGeographyWeight(t *testing.T) {
	repository := openRiskTestStore(t)
	ctx := context.Background()

	plain, err := repository.GetOrCreateUser(ctx, "+2348090002201")
	if err != nil {
		t.Fatal(err)
	}
	risky, err := repository.GetOrCreateUser(ctx, "+2348090002202")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{plain.ID, risky.ID} {
		if _, err := repository.EnsureKYCProfile(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.SetIndividualGeography(ctx, risky.ID, "IR", ""); err != nil {
		t.Fatal(err)
	}

	plainProfile, err := repository.RecomputeRiskScore(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	riskyProfile, err := repository.RecomputeRiskScore(ctx, risky.ID)
	if err != nil {
		t.Fatal(err)
	}
	if riskyProfile.RiskScore != plainProfile.RiskScore+kyc.RiskGeographyWeight {
		t.Fatalf("risky score = %v, plain score = %v, want +%v", riskyProfile.RiskScore, plainProfile.RiskScore, kyc.RiskGeographyWeight)
	}
}

// TestExportUserDataIncludesGeography pins that the DSR export carries the two
// G14 fields alongside the rest of the KYC record.
func TestExportUserDataIncludesGeography(t *testing.T) {
	repository := openRiskTestStore(t)
	ctx := context.Background()

	user, err := repository.GetOrCreateUser(ctx, "+2348090002301")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetIndividualGeography(ctx, user.ID, "IR", kyc.VolumeBandHigh); err != nil {
		t.Fatal(err)
	}
	export, err := repository.ExportUserData(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	blob := string(export.KYCProfile)
	if !strings.Contains(blob, "country_iso") || !strings.Contains(blob, "expected_volume_band") {
		t.Fatalf("export KYC profile missing geography fields: %s", blob)
	}
	if !strings.Contains(blob, "IR") || !strings.Contains(blob, kyc.VolumeBandHigh) {
		t.Fatalf("export KYC profile missing geography values: %s", blob)
	}
}
