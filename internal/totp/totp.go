// Package totp provides RFC 6238 time-based one-time passwords for admin and
// merchant two-factor authentication, including AES-GCM encryption of the
// stored shared secret.
package totp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// Generate creates a new TOTP key for the given issuer and account. The
// returned key exposes the base32 shared secret and an otpauth:// URL for
// QR rendering.
func Generate(issuer, account string) (*otp.Key, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		SecretSize:  20,
	})
	if err != nil {
		return nil, fmt.Errorf("generate totp key: %w", err)
	}
	return key, nil
}

// Validate checks a submitted code against the base32 shared secret using the
// default 30-second period with a one-step skew.
func Validate(code, secret string) bool {
	return totp.Validate(code, secret)
}

// KeyURI builds the otpauth:// provisioning URI for a stored secret so the
// QR code can be re-rendered.
func KeyURI(issuer, account, secret string) string {
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) +
		"?secret=" + secret + "&issuer=" + url.QueryEscape(issuer)
}

// RandomKeyHex returns a 32-byte random encryption key as 64 hex characters.
func RandomKeyHex() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("random totp key: %w", err)
	}
	return hex.EncodeToString(key), nil
}

// EncryptSecret wraps the base32 shared secret with AES-256-GCM using the
// given 32-byte key. The random nonce is prepended to the ciphertext.
func EncryptSecret(key []byte, secret string) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("totp encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("totp cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("totp gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("totp nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, []byte(secret), nil), nil
}

// DecryptSecret reverses EncryptSecret.
func DecryptSecret(key []byte, data []byte) (string, error) {
	if len(key) != 32 {
		return "", errors.New("totp encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("totp cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("totp gcm: %w", err)
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("totp ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("totp decrypt: %w", err)
	}
	return string(plaintext), nil
}
