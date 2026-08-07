// Package crypto seals and opens application-level encryption at rest.
// Values are wrapped in a versioned envelope so ciphertexts stay readable
// after key rotation policies change: enc:v1:<base64(nonce || tag || ct)>.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	envelopePrefix = "enc:v1:"
	nonceSize      = 12
)

var (
	// ErrNotSealed is returned when a value was not produced by Seal.
	ErrNotSealed = errors.New("crypto: value is not an encrypted envelope")
	// ErrKeySize is returned when the AES key is not 32 bytes.
	ErrKeySize = errors.New("crypto: AES-256-GCM requires a 32-byte key")
)

// Seal encrypts plaintext with AES-256-GCM and returns a versioned envelope.
// Each call uses a fresh random nonce, so equal inputs never share ciphertext.
func Seal(key, plaintext []byte) (string, error) {
	if len(key) != 32 {
		return "", ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	packed := make([]byte, 0, len(nonce)+len(sealed))
	packed = append(packed, nonce...)
	packed = append(packed, sealed...)
	return envelopePrefix + base64.StdEncoding.EncodeToString(packed), nil
}

// Open decrypts a Seal-produced envelope. Plaintext inputs (legacy rows or a
// disabled-key mode) are returned unchanged, so callers can mix encrypted and
// legacy data safely during migration.
func Open(key []byte, envelope string) ([]byte, error) {
	if envelope == "" {
		return nil, nil
	}
	if !strings.HasPrefix(envelope, envelopePrefix) {
		return []byte(envelope), nil
	}
	if len(key) != 32 {
		return nil, ErrKeySize
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(envelope, envelopePrefix))
	if err != nil {
		return nil, fmt.Errorf("crypto: decode envelope: %w", err)
	}
	if len(raw) < nonceSize+16 {
		return nil, errors.New("crypto: envelope too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := raw[:nonceSize]
	ciphertext := raw[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: open envelope (wrong key or tampered data): %w", err)
	}
	return plaintext, nil
}

// IsSealed reports whether value is a Seal-produced envelope.
func IsSealed(value string) bool {
	return strings.HasPrefix(value, envelopePrefix)
}
