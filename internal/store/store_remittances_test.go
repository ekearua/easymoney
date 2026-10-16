package store

import (
	"testing"
)

// TestRemittanceCodeAndHash pins the credential contract the sender/recipient
// flows depend on: codes are fixed-length decimal, salts are unique, and the
// stored digest is a salted one-way function of the presented code.
func TestRemittanceCodeAndHash(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		code, err := NewRemittanceCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != PickupCodeLength {
			t.Fatalf("code %q length = %d, want %d", code, len(code), PickupCodeLength)
		}
		for _, c := range code {
			if c < '0' || c > '9' {
				t.Fatalf("code %q has a non-digit %q", code, c)
			}
		}
		seen[code] = true
	}
	// With 10^6 space and 200 draws, collisions are vanishingly unlikely;
	// a fixed/constant generator would collapse to a handful.
	if len(seen) < 190 {
		t.Fatalf("codes are not random: only %d distinct out of 200", len(seen))
	}

	saltA, err := NewRemittanceSalt()
	if err != nil {
		t.Fatal(err)
	}
	saltB, err := NewRemittanceSalt()
	if err != nil {
		t.Fatal(err)
	}
	if saltA == saltB || len(saltA) != 32 {
		t.Fatalf("salts not unique/32-hex: %q %q", saltA, saltB)
	}

	code := "123456"
	if RemittanceCodeHash(saltA, code) != RemittanceCodeHash(saltA, code) {
		t.Fatal("hash is not deterministic for the same salt and code")
	}
	if RemittanceCodeHash(saltA, code) == RemittanceCodeHash(saltB, code) {
		t.Fatal("hash ignores the salt")
	}
	if RemittanceCodeHash(saltA, code) == RemittanceCodeHash(saltA, "654321") {
		t.Fatal("hash ignores the code")
	}
}
