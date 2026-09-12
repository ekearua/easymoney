package config

import (
	"strings"
	"testing"
)

func TestProductionRequiresAtLeastOnePaymentProvider(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYMENT_PROVIDER", "interswitch")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("DATA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "INTERSWITCH_CLIENT_ID") {
		t.Fatalf("expected INTERSWITCH_CLIENT_ID validation, got %v", err)
	}
}

func TestProductionAllowsInterswitchOnly(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYMENT_PROVIDER", "interswitch")
	t.Setenv("INTERSWITCH_CLIENT_ID", "cid_test_ok")
	t.Setenv("INTERSWITCH_CLIENT_SECRET", "cs_test_ok")
	t.Setenv("INTERSWITCH_MERCHANT_CODE", "M1000")
	t.Setenv("INTERSWITCH_WEBHOOK_SECRET", "wh_test_ok")
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
		t.Fatalf("interswitch-only config should be valid in production, got %v", err)
	}
}

func TestProductionRejectsInterswitchSecretWithoutClientID(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYMENT_PROVIDER", "interswitch")
	t.Setenv("INTERSWITCH_CLIENT_SECRET", "cs_test_ok")
	t.Setenv("INTERSWITCH_MERCHANT_CODE", "M1000")
	t.Setenv("INTERSWITCH_WEBHOOK_SECRET", "wh_test_ok")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("DATA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("BASE_URL", "https://example.com")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "INTERSWITCH_CLIENT_ID") {
		t.Fatalf("expected INTERSWITCH_CLIENT_ID validation, got %v", err)
	}
}

func TestProductionRejectsDemoCodeInChat(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("INTERSWITCH_CLIENT_ID", "cid_test_ok")
	t.Setenv("INTERSWITCH_MERCHANT_CODE", "M1000")
	t.Setenv("INTERSWITCH_WEBHOOK_SECRET", "wh_test_ok")
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
	t.Setenv("INTERSWITCH_CLIENT_ID", "cid_test_ok")
	t.Setenv("INTERSWITCH_MERCHANT_CODE", "M1000")
	t.Setenv("INTERSWITCH_WEBHOOK_SECRET", "wh_test_ok")
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
	t.Setenv("INTERSWITCH_CLIENT_ID", "cid_test_ok")
	t.Setenv("INTERSWITCH_MERCHANT_CODE", "M1000")
	t.Setenv("INTERSWITCH_WEBHOOK_SECRET", "wh_test_ok")
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
	t.Setenv("INTERSWITCH_CLIENT_ID", "cid_test_ok")
	t.Setenv("INTERSWITCH_MERCHANT_CODE", "M1000")
	t.Setenv("INTERSWITCH_WEBHOOK_SECRET", "wh_test_ok")
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
	t.Setenv("INTERSWITCH_CLIENT_ID", "cid_test_ok")
	t.Setenv("INTERSWITCH_MERCHANT_CODE", "M1000")
	t.Setenv("INTERSWITCH_WEBHOOK_SECRET", "wh_test_ok")
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

func TestCheckoutRenderEnv(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{"defaults_to_hosted_fields", "", "hosted_fields", true},
		{"hosted_fields", "hosted_fields", "hosted_fields", true},
		{"legacy", "legacy", "legacy", true},
		{"rejects_unknown", "newwebpay", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("INTERSWITCH_CHECKOUT_RENDER", tc.value)
			cfg, err := Load()
			if !tc.ok {
				if err == nil || !strings.Contains(err.Error(), "INTERSWITCH_CHECKOUT_RENDER") {
					t.Fatalf("expected INTERSWITCH_CHECKOUT_RENDER rejection, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.InterswitchCheckoutRender != tc.want {
				t.Fatalf("render = %q, want %q (INTERSWITCH_CHECKOUT_RENDER)", cfg.InterswitchCheckoutRender, tc.want)
			}
		})
	}
}
