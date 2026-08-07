package service

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestEmailCodeHashIsSaltedAndVerifiable(t *testing.T) {
	email := "user@example.com"
	code := "123456"
	hash, err := emailCodeHash(email, code)
	if err != nil {
		t.Fatalf("emailCodeHash: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword(hash, emailCodeDigest(email, code)); err != nil {
		t.Fatalf("digest should match its hash: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword(hash, emailCodeDigest(email, "654321")); err == nil {
		t.Fatal("wrong code should not verify")
	}
	again, err := emailCodeHash(email, code)
	if err != nil {
		t.Fatalf("emailCodeHash second: %v", err)
	}
	if string(hash) == string(again) {
		t.Fatal("bcrypt salt should make identical codes hash differently")
	}
}

func TestEmailCodeDigestNormalizes(t *testing.T) {
	email := " User@Example.COM "
	digest := emailCodeDigest(email, "12 34-56")
	clean := emailCodeDigest("user@example.com", "123456")
	if string(digest) != string(clean) {
		t.Fatal("digest should normalize email case/space and code punctuation")
	}
}
