package crypto

import (
	"strings"
	"testing"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestSealOpenRoundTrip(t *testing.T) {
	envelope, err := Seal(testKey, []byte(`{"body":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(envelope) {
		t.Fatal("envelope should carry the v1 prefix")
	}
	plain, err := Open(testKey, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != `{"body":"hello"}` {
		t.Fatalf("round trip mismatch: %q", plain)
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	a, _ := Seal(testKey, []byte("same"))
	b, _ := Seal(testKey, []byte("same"))
	if a == b {
		t.Fatal("equal plaintext must never share a ciphertext")
	}
}

func TestOpenPlaintextPassthrough(t *testing.T) {
	plain, err := Open(testKey, `{"body":"legacy"}`)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != `{"body":"legacy"}` {
		t.Fatalf("legacy plaintext should pass through, got %q", plain)
	}
}

func TestOpenEmpty(t *testing.T) {
	plain, err := Open(testKey, "")
	if err != nil || plain != nil {
		t.Fatalf("empty input should return nil,nil: %v %v", plain, err)
	}
}

func TestOpenWrongKey(t *testing.T) {
	envelope, _ := Seal(testKey, []byte("secret"))
	wrong := []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if _, err := Open(wrong, envelope); err == nil {
		t.Fatal("wrong key must fail to open")
	}
}

func TestOpenTampered(t *testing.T) {
	envelope, _ := Seal(testKey, []byte("secret"))
	tampered := envelope[:len(envelope)-1] + "A"
	if _, err := Open(testKey, tampered); err == nil {
		t.Fatal("tampered envelope must fail to open")
	}
}

func TestOpenShortEnvelope(t *testing.T) {
	if _, err := Open(testKey, "enc:v1:"+strings.Repeat("A", 4)); err == nil {
		t.Fatal("truncated envelope should error")
	}
}

func TestSealWrongKeySize(t *testing.T) {
	if _, err := Seal([]byte("short"), []byte("x")); err != ErrKeySize {
		t.Fatalf("expected ErrKeySize, got %v", err)
	}
	if _, err := Open([]byte("short"), "enc:v1:"); err != ErrKeySize {
		t.Fatalf("expected ErrKeySize, got %v", err)
	}
}

func TestNonEmptyCiphertext(t *testing.T) {
	envelope, _ := Seal(testKey, []byte("x"))
	raw := strings.TrimPrefix(envelope, envelopePrefix)
	if len(raw) < 40 {
		t.Fatalf("ciphertext should be substantially larger than plaintext, got %d chars", len(raw))
	}
}
