package service

import (
	"strings"
	"testing"
)

// Tests for the tolerant parsers behind the aggregated chat data-collection
// steps. These keep the aggregation honest: labeled and unlabeled input,
// partial captures preserved across re-asks, and the per-field validation
// rules unchanged from the per-field chains.

func TestParseTier1DetailsLabeledAndUnlabeled(t *testing.T) {
	t.Parallel()
	d := ParseTier1Details("DOB: 1992-05-24\nGender: Female\nAddress: 14 Admiralty Way, Lekki, Lagos")
	if d.DOB != "1992-05-24" || d.Gender != "female" || d.Address != "14 Admiralty Way, Lekki, Lagos" {
		t.Fatalf("labeled parse = %+v", d)
	}
	if !d.Complete() {
		t.Fatalf("complete set reported missing: %v", d.Missing())
	}

	d = ParseTier1Details("1992-05-24\nfemale\n14 Admiralty Way, Lekki, Lagos")
	if d.DOB != "1992-05-24" || d.Gender != "female" || d.Address != "14 Admiralty Way, Lekki, Lagos" {
		t.Fatalf("unlabeled parse = %+v", d)
	}

	// One line: date plus address text after it.
	d = ParseTier1Details("Born 1992-05-24 at 14 Admiralty Way, Lekki Lagos")
	if d.DOB != "1992-05-24" {
		t.Fatalf("DOB from prose = %q", d.DOB)
	}
	if d.Address != "at 14 Admiralty Way, Lekki Lagos" {
		t.Fatalf("address residue = %q", d.Address)
	}
}

func TestParseTier1DetailsDDMMYYYY(t *testing.T) {
	t.Parallel()
	// Unambiguous day>12 reads as DD-MM-YYYY.
	d := ParseTier1Details("24-12-1992\nGender: FEMALE\naddress: 14 Admiralty Way, Lekki")
	if d.DOB != "1992-12-24" {
		t.Fatalf("DD-MM-YYYY DOB = %q, want 1992-12-24", d.DOB)
	}
}

func TestParseTier1DetailsPartialCaptures(t *testing.T) {
	t.Parallel()
	d := ParseTier1Details("DOB: 1992-05-24")
	if d.DOB == "" || d.Gender != "" || d.Address != "" {
		t.Fatalf("partial parse = %+v", d)
	}
	got := d.Missing()
	want := []string{"gender (MALE or FEMALE)", "residential address"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Missing() = %v, want %v", got, want)
	}
	if len(d.Captured()) == 0 {
		t.Fatal("Captured() should list the captured DOB")
	}
}

func TestValidateTier1DetailsRules(t *testing.T) {
	t.Parallel()
	if field := ValidateTier1Details(Tier1Details{DOB: "2015-01-01", Gender: "female", Address: "14 Admiralty Way, Lekki"}); field != "date of birth" {
		t.Fatalf("under-18 field = %q", field)
	}
	if field := ValidateTier1Details(Tier1Details{DOB: "1992-05-24", Gender: "other", Address: "14 Admiralty Way, Lekki"}); field != "gender" {
		t.Fatalf("bad gender field = %q", field)
	}
	if field := ValidateTier1Details(Tier1Details{DOB: "1992-05-24", Gender: "female", Address: "short"}); field != "residential address" {
		t.Fatalf("short address field = %q", field)
	}
	if field := ValidateTier1Details(Tier1Details{DOB: "1992-05-24", Gender: "female", Address: "14 Admiralty Way, Lekki"}); field != "" {
		t.Fatalf("valid set flagged %q", field)
	}
}

func TestParseIndividualProfileDetails(t *testing.T) {
	t.Parallel()
	d := ParseIndividualProfileDetails("Name: Ada Obi\nDOB: 1992-05-24\nGender: Female\nAddress: 14 Admiralty Way, Lekki, Lagos\nOccupation: Software engineer")
	if d.LegalName != "Ada Obi" || d.DOB != "1992-05-24" || d.Gender != "female" ||
		d.Address != "14 Admiralty Way, Lekki, Lagos" || d.Occupation != "Software engineer" {
		t.Fatalf("labeled parse = %+v", d)
	}
	if !d.Complete() {
		t.Fatalf("complete set reported missing: %v", d.Missing())
	}

	// Unlabeled: first short line is the name, last is the occupation, the
	// long line is the address.
	d = ParseIndividualProfileDetails("Ada Obi\n1992-05-24\nfemale\n14 Admiralty Way, Lekki, Lagos\nSoftware engineer")
	if d.LegalName != "Ada Obi" || d.Occupation != "Software engineer" || d.Address != "14 Admiralty Way, Lekki, Lagos" {
		t.Fatalf("unlabeled parse = %+v", d)
	}
}

func TestParsePhotoIDDetails(t *testing.T) {
	t.Parallel()
	cases := []struct{ input, kind, number string }{
		{"PASSPORT A12345678", "passport", "A12345678"},
		{"A12345678 passport", "passport", "A12345678"},
		{"ID type: Passport\nID number: A12345678", "passport", "A12345678"},
		{"drivers licence ABC123456", "drivers_licence", "ABC123456"},
		{"voters card 98765432100", "voters_card", "98765432100"},
		{"national id NIN-123456", "national_id", "NIN-123456"},
	}
	for _, c := range cases {
		d := ParsePhotoIDDetails(c.input)
		if d.IDType != c.kind || d.IDNumber != c.number {
			t.Errorf("ParsePhotoIDDetails(%q) = %+v, want %s/%s", c.input, d, c.kind, c.number)
		}
	}
	d := ParsePhotoIDDetails("passport")
	if d.IDType != "passport" || d.IDNumber != "" {
		t.Fatalf("type-only parse = %+v", d)
	}
	if got := d.Missing(); len(got) != 1 || !strings.Contains(got[0], "ID number") {
		t.Fatalf("Missing() = %v", got)
	}
}

func TestParseBVNNIN(t *testing.T) {
	t.Parallel()
	d := ParseBVNNIN("BVN: 22222222222\nNIN: 12345678901")
	if d.BVN != "22222222222" || d.NIN != "12345678901" {
		t.Fatalf("labeled = %+v", d)
	}
	d = ParseBVNNIN("12345678901\n22222222222")
	if d.BVN != "12345678901" || d.NIN != "22222222222" {
		t.Fatalf("positional = %+v", d)
	}
	d = ParseBVNNIN("NIN 99999999999 bvn 22222222222")
	if d.BVN != "22222222222" || d.NIN != "99999999999" {
		t.Fatalf("one-line labels = %+v", d)
	}
	d = ParseBVNNIN("22222222222")
	if d.Complete() {
		t.Fatalf("one number must not report complete: %+v", d)
	}
	if len(d.Missing()) != 1 {
		t.Fatalf("one number should leave exactly one missing: %v", d.Missing())
	}
	if d.BVN != "22222222222" || d.NIN != "" {
		t.Fatalf("partial = %+v", d)
	}
}

func TestParseEDDDetails(t *testing.T) {
	t.Parallel()
	d := ParseEDDDetails("Utility ref: 12345678901\nSource of funds: SALARY")
	if d.AddressProof != "12345678901" || d.SourceOfFunds != "SALARY" {
		t.Fatalf("labeled = %+v", d)
	}
	if !d.Complete() {
		t.Fatalf("complete set reported missing: %v", d.Missing())
	}
	d = ParseEDDDetails("SELF-DECLARED BUSINESS")
	if d.AddressProof != "SELF-DECLARED" || d.SourceOfFunds != "BUSINESS" {
		t.Fatalf("self-declared = %+v", d)
	}
	d = ParseEDDDetails("Meter 5550012345, GIFTS")
	if d.SourceOfFunds != "GIFTS" || d.AddressProof != "Meter 5550012345," {
		t.Fatalf("mixed = %+v", d)
	}
}

func TestMissingPartsWording(t *testing.T) {
	t.Parallel()
	if got := missingParts([]string{"a"}); got != "Still need your a." {
		t.Fatalf("single = %q", got)
	}
	if got := missingParts([]string{"a", "b"}); got != "Still need your a and b." {
		t.Fatalf("two = %q", got)
	}
	if got := missingParts([]string{"a", "b", "c"}); got != "Still need your a, b and c." {
		t.Fatalf("three = %q", got)
	}
}
