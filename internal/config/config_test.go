package config

import (
	"strings"
	"testing"
)

func TestProductionRequiresAtLeastOnePaymentProvider(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYMENT_PROVIDER", "paystack")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("DATA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "PAYSTACK_SECRET_KEY") {
		t.Fatalf("expected PAYSTACK_SECRET_KEY validation, got %v", err)
	}
}

func TestProductionAllowsFlutterwaveOnly(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYMENT_PROVIDER", "flutterwave")
	t.Setenv("FLUTTERWAVE_SECRET_KEY", "FLWSECK_TEST_ok")
	t.Setenv("FLUTTERWAVE_PUBLIC_KEY", "FLWPUBK_TEST_ok")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("DATA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("TOTP_ENABLED", "true")
	t.Setenv("TOTP_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("BASE_URL", "https://example.com")
	_, err := Load()
	if err != nil {
		t.Fatalf("flutterwave-only config should be valid in production, got %v", err)
	}
}

func TestProductionRejectsFlutterwaveKeyWithoutPublicKey(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYMENT_PROVIDER", "paystack")
	t.Setenv("FLUTTERWAVE_SECRET_KEY", "FLWSECK_TEST_ok")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_ok")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("DATA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("BASE_URL", "https://example.com")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "FLUTTERWAVE_PUBLIC_KEY") {
		t.Fatalf("expected FLUTTERWAVE_PUBLIC_KEY validation, got %v", err)
	}
}

func TestProductionRejectsDemoCodeInChat(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_ok")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("DATA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("EMAIL_DEMO_CODE_IN_CHAT", "true")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "EMAIL_DEMO_CODE_IN_CHAT") {
		t.Fatalf("expected demo-code-in-chat rejection, got %v", err)
	}
}

func TestProductionRequiresTOTPKey(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_ok")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("DATA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("TOTP_ENABLED", "true")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "TOTP_ENCRYPTION_KEY") {
		t.Fatalf("expected missing TOTP key rejection, got %v", err)
	}
}

func TestProductionRejectsMalformedTOTPKey(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_ok")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("DATA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("TOTP_ENABLED", "true")
	t.Setenv("TOTP_ENCRYPTION_KEY", "short")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "64 hex") {
		t.Fatalf("expected malformed TOTP key rejection, got %v", err)
	}
}

func TestProductionRequiresDataEncryptionKey(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_ok")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("BASE_URL", "https://example.com")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATA_ENCRYPTION_KEY") {
		t.Fatalf("expected missing DATA_ENCRYPTION_KEY rejection, got %v", err)
	}
}

func TestProductionRejectsMalformedDataEncryptionKey(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_ok")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("DATA_ENCRYPTION_KEY", "not-hex-short")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "64 hex") {
		t.Fatalf("expected malformed DATA_ENCRYPTION_KEY rejection, got %v", err)
	}
}
