package totp

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestGenerateAndValidateRoundTrip(t *testing.T) {
	key, err := Generate("Xego", "admin@example.com")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if key == nil || key.Secret() == "" {
		t.Fatal("expected non-empty secret")
	}
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	if !Validate(code, key.Secret()) {
		t.Fatal("expected code to validate against secret")
	}
	if Validate("000000", key.Secret()) {
		t.Fatal("expected wrong code to fail")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	secret := "JBSWY3DPEHPK3PXP"
	ciphertext, err := EncryptSecret(key, secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(ciphertext, []byte(secret)) {
		t.Fatal("ciphertext must not contain plaintext")
	}
	got, err := DecryptSecret(key, ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != secret {
		t.Fatalf("round trip mismatch: got %q", got)
	}
}

func TestEncryptRejectsWrongKeyLength(t *testing.T) {
	if _, err := EncryptSecret([]byte("short"), "JBSWY3DPEHPK3PXP"); err == nil {
		t.Fatal("expected error for short key")
	}
	if _, err := DecryptSecret([]byte("short"), []byte("data")); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	ciphertext, err := EncryptSecret(key, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	ciphertext[len(ciphertext)-1] ^= 0xFF
	if _, err := DecryptSecret(key, ciphertext); err == nil {
		t.Fatal("expected authentication failure on tampered ciphertext")
	}
}

func TestKeyURIContainsSecretAndIssuer(t *testing.T) {
	uri := KeyURI("Xego", "admin@example.com", "JBSWY3DPEHPK3PXP")
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("unexpected uri: %s", uri)
	}
	if !strings.Contains(uri, "secret=JBSWY3DPEHPK3PXP") {
		t.Fatalf("uri missing secret: %s", uri)
	}
	if !strings.Contains(uri, "issuer=Xego") {
		t.Fatalf("uri missing issuer: %s", uri)
	}
}
