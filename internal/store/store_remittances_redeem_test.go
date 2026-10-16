package store

import "testing"

// TestRemittanceIDHash pins the per-customer cap key: the hash must be
// deterministic so the same recipient's cash-outs aggregate across agents, must
// ignore ID formatting (spaces, dashes, case), and must change with the pepper,
// the ID type and the ID number. It must never echo the plaintext ID.
func TestRemittanceIDHash(t *testing.T) {
	const pepper = "server-pepper"
	base := RemittanceIDHash(pepper, "nin", "1234-5678 901")

	if base != RemittanceIDHash(pepper, "nin", "1234-5678 901") {
		t.Fatal("hash is not deterministic for the same inputs")
	}
	// Formatting must not change a recipient's identity.
	if base != RemittanceIDHash(pepper, "NIN", "12345678901") {
		t.Fatal("hash is sensitive to ID formatting or type case")
	}
	if base == RemittanceIDHash("other-pepper", "nin", "12345678901") {
		t.Fatal("hash ignores the pepper")
	}
	if base == RemittanceIDHash(pepper, "bvn", "12345678901") {
		t.Fatal("hash ignores the ID type")
	}
	if base == RemittanceIDHash(pepper, "nin", "12345678902") {
		t.Fatal("hash ignores the ID number")
	}
	if len(base) != 64 {
		t.Fatalf("hash length = %d, want 64 hex chars (SHA-256)", len(base))
	}
	for _, c := range base {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("hash %q is not lowercase hex", base)
		}
	}
}

// TestNormalizeGovernmentID pins the canonicalisation that makes the cap key
// stable across how a given ID is typed.
func TestNormalizeGovernmentID(t *testing.T) {
	cases := map[string]string{
		"1234-5678 901": "12345678901",
		"  ab/cd  ":     "ABCD",
		"":              "",
		"!!":            "",
	}
	for in, want := range cases {
		if got := normalizeGovernmentID(in); got != want {
			t.Errorf("normalizeGovernmentID(%q) = %q, want %q", in, got, want)
		}
	}
}
