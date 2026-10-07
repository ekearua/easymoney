package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExampleEnvTemplateLoads pins .env.example to values the loader actually
// accepts. When the simulated providers were retired the template kept
// advertising them (plus a phantom ALLOW_SIMULATED_RAILS_IN_PROD override), so
// a copied template could not boot. This guard reads the real file, applies
// every assignment in its documented development mode, and fails if the
// template drifts back to unloadable values.
func TestExampleEnvTemplateLoads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	if strings.Contains(string(raw), "ALLOW_SIMULATED_RAILS_IN_PROD") {
		t.Fatal(".env.example advertises ALLOW_SIMULATED_RAILS_IN_PROD, which the loader never reads")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("template line is not an assignment: %q", line)
		}
		t.Setenv(strings.TrimSpace(key), strings.TrimSpace(value))
	}
	if _, err := Load(); err != nil {
		t.Fatalf(".env.example no longer loads: %v", err)
	}
}

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
	setProductionBaseEnv(t)
	t.Setenv("INTERSWITCH_CLIENT_SECRET", "cs_test_ok")
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

// setProductionBaseEnv sets the minimum environment that satisfies the
// production requirements so rail-guard tests reach the new validation
// without tripping earlier checks. It describes a fully live, real-rail
// production deployment (every secondary rail defaults to a real provider).
func setProductionBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYMENT_PROVIDER", "interswitch")
	t.Setenv("INTERSWITCH_CLIENT_ID", "cid_test_ok")
	t.Setenv("INTERSWITCH_MERCHANT_CODE", "M1000")
	t.Setenv("INTERSWITCH_WEBHOOK_SECRET", "wh_test_ok")
	t.Setenv("INTERSWITCH_CHECKOUT_MODE", "LIVE")
	t.Setenv("INTERSWITCH_TRANSFER_BASE_URL", "https://live.transfer.example.com")
	t.Setenv("INTERSWITCH_SOURCE_ACCOUNT", "1234567890")
	t.Setenv("VTPASS_API_KEY", "vtpass-key")
	t.Setenv("VTPASS_PUBLIC_KEY", "vtpass-public")
	t.Setenv("VTPASS_SECRET_KEY", "vtpass-secret")
	t.Setenv("NINBVNPORTAL_API_KEY", "nin-key")
	t.Setenv("SCREENING_API_BASE", "https://screen.example.com")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	t.Setenv("DATA_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("TOTP_ENABLED", "true")
	t.Setenv("TOTP_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
}

func TestProductionRejectsSimulatedRailsByDefault(t *testing.T) {
	setProductionBaseEnv(t)
	t.Setenv("PAYOUT_PROVIDER", "simulated")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "PAYOUT_PROVIDER") {
		t.Fatalf("expected simulated payout rail rejection, got %v", err)
	}
}

func TestProductionRejectsSimulatorProviders(t *testing.T) {
	cases := []struct{ key, value string }{
		{"REFUND_PROVIDER", "simulated"},
		{"DATA_PROVIDER", "simulated"},
		{"DATA_PROVIDER", "simulator"},
		{"IDENTITY_PROVIDER", "simulated"},
		{"SCREENING_PROVIDER", "simulated"},
		{"SCREENING_PROVIDER", "simulator"},
		{"AI_PROVIDER", "simulated"},
		{"BANK_TRANSFER_MODE", "simulate"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			setProductionBaseEnv(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("expected %s rejection, got %v", tc.key, err)
			}
		})
	}
}

func TestProductionRequiresLiveRails(t *testing.T) {
	cases := []struct {
		key, value string
		want       string
	}{
		{"INTERSWITCH_CHECKOUT_MODE", "TEST", "INTERSWITCH_CHECKOUT_MODE"},
		{"INTERSWITCH_TRANSFER_BASE_URL", "", "INTERSWITCH_TRANSFER_BASE_URL"},
		{"INTERSWITCH_SOURCE_ACCOUNT", "", "INTERSWITCH_SOURCE_ACCOUNT"},
		{"NINBVNPORTAL_API_KEY", "", "NINBVNPORTAL_API_KEY"},
		{"SCREENING_API_BASE", "", "SCREENING_API_BASE"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			setProductionBaseEnv(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateProductionRails(t *testing.T) {
	live := Config{
		PaymentProvider:            "interswitch",
		InterswitchCheckoutMode:    "LIVE",
		BankTransferMode:           "interswitch",
		PayoutProvider:             "interswitch",
		RefundProvider:             "interswitch",
		InterswitchTransferBaseURL: "https://live.transfer.example.com",
		InterswitchSourceAccount:   "1234567890",
		DataProvider:               "vtpass",
		IdentityProvider:           "ninbvnportal",
		NINBVNPortalKey:            "nin-key",
		ScreeningProvider:          "http",
		ScreeningAPIBase:           "https://screen.example.com",
	}
	if err := validateProductionRails(live); err != nil {
		t.Fatalf("fully live config rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"missing_screening_base", func(c *Config) { c.ScreeningAPIBase = "" }, "SCREENING_API_BASE"},
		{"test_checkout_mode", func(c *Config) { c.InterswitchCheckoutMode = "TEST" }, "INTERSWITCH_CHECKOUT_MODE"},
		{"missing_transfer_base", func(c *Config) { c.InterswitchTransferBaseURL = "" }, "INTERSWITCH_TRANSFER_BASE_URL"},
		{"missing_source_account", func(c *Config) { c.InterswitchSourceAccount = "" }, "INTERSWITCH_SOURCE_ACCOUNT"},
		{"missing_nin_key", func(c *Config) { c.NINBVNPortalKey = "" }, "NINBVNPORTAL_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := live
			tc.mutate(&cfg)
			err := validateProductionRails(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCheckoutModeValidation(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("INTERSWITCH_CHECKOUT_MODE", "bogus")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "INTERSWITCH_CHECKOUT_MODE") {
		t.Fatalf("expected INTERSWITCH_CHECKOUT_MODE rejection, got %v", err)
	}
}

func TestScreeningProviderValidation(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("SCREENING_PROVIDER", "bogus")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "SCREENING_PROVIDER") {
		t.Fatalf("expected SCREENING_PROVIDER rejection, got %v", err)
	}
}

// TestOnboardingReminderValidation pins the reminder knobs' loader bounds: an
// attempt cap outside 1-10 and a quiet hour outside 0-23 are rejected by name,
// and the documented defaults load.
func TestOnboardingReminderValidation(t *testing.T) {
	t.Setenv("APP_ENV", "development")

	t.Run("rejects_attempt_cap_out_of_bounds", func(t *testing.T) {
		t.Setenv("ONBOARDING_REMINDER_MAX", "0")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "ONBOARDING_REMINDER_MAX") {
			t.Fatalf("expected ONBOARDING_REMINDER_MAX rejection, got %v", err)
		}
	})

	t.Run("rejects_quiet_hour_out_of_range", func(t *testing.T) {
		t.Setenv("ONBOARDING_REMINDER_MAX", "3")
		t.Setenv("ONBOARDING_REMINDER_QUIET_START", "24")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "ONBOARDING_REMINDER_QUIET_START") {
			t.Fatalf("expected ONBOARDING_REMINDER_QUIET_START rejection, got %v", err)
		}
	})

	t.Run("defaults_load", func(t *testing.T) {
		t.Setenv("ONBOARDING_REMINDER_MAX", "3")
		t.Setenv("ONBOARDING_REMINDER_QUIET_START", "20")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.OnboardingReminderMax != 3 || cfg.OnboardingReminderQuietStart != 20 {
			t.Fatalf("knobs not loaded: max=%d quietStart=%d", cfg.OnboardingReminderMax, cfg.OnboardingReminderQuietStart)
		}
	})
}

func TestSMSProviderValidation(t *testing.T) {
	t.Setenv("APP_ENV", "development")

	t.Run("rejects_unknown_provider", func(t *testing.T) {
		t.Setenv("SMS_PROVIDER", "bogus")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "SMS_PROVIDER") {
			t.Fatalf("expected SMS_PROVIDER rejection, got %v", err)
		}
	})

	t.Run("http_requires_base_url", func(t *testing.T) {
		t.Setenv("SMS_PROVIDER", "http")
		t.Setenv("SMS_API_BASE", "")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "SMS_API_BASE") {
			t.Fatalf("expected SMS_API_BASE rejection, got %v", err)
		}
	})

	t.Run("http_with_base_url_ok", func(t *testing.T) {
		t.Setenv("SMS_PROVIDER", "http")
		t.Setenv("SMS_API_BASE", "https://sms.example.test/rest/v1/send")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.SMSProvider != "http" || cfg.SMSAPIBase != "https://sms.example.test/rest/v1/send" {
			t.Fatalf("unexpected sms config: %+v", cfg)
		}
	})

	t.Run("webhook_ok", func(t *testing.T) {
		t.Setenv("SMS_PROVIDER", "webhook")
		t.Setenv("SMS_API_BASE", "")
		_, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestAIProviderValidation(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("AI_PROVIDER", "bogus")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AI_PROVIDER") {
		t.Fatalf("expected AI_PROVIDER rejection, got %v", err)
	}
}

func TestAIMaxRPMParse(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("AI_MAX_REQUESTS_PER_MINUTE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AIMaxRPM != 30 {
		t.Fatalf("default AIMaxRPM = %d, want 30", cfg.AIMaxRPM)
	}
	t.Setenv("AI_MAX_REQUESTS_PER_MINUTE", "5")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AIMaxRPM != 5 {
		t.Fatalf("AIMaxRPM = %d, want 5", cfg.AIMaxRPM)
	}
	t.Setenv("AI_MAX_REQUESTS_PER_MINUTE", "-3")
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "AI_MAX_REQUESTS_PER_MINUTE") {
		t.Fatalf("expected negative AIMaxRPM rejection, got %v", err)
	}
}

func TestAIProductionGuard(t *testing.T) {
	setProductionBaseEnv(t)

	t.Setenv("AI_ENABLED", "true")
	t.Setenv("AI_PROVIDER", "simulated")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AI_PROVIDER") {
		t.Fatalf("expected simulated AI rejection, got %v", err)
	}

	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("AI_MAX_REQUESTS_PER_MINUTE", "30")
	_, err = Load()
	if err != nil {
		t.Fatalf("live AI provider should pass the production guard, got %v", err)
	}

	t.Setenv("AI_MAX_REQUESTS_PER_MINUTE", "0")
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "AI_MAX_REQUESTS_PER_MINUTE") {
		t.Fatalf("expected zero-RPM rejection in production, got %v", err)
	}
}

// TestAMLRecordRetentionValidation pins the G6 record-keeping floor: config
// refuses a retention period below the MLPPA five-year boundary, refuses a
// RETENTION_PERIOD that asks to purge past the floor, and accepts both the
// documented default and an explicit zero (the dev/harness escape hatch).
func TestAMLRecordRetentionValidation(t *testing.T) {
	t.Setenv("AML_RECORD_RETENTION", "2160h") // 90 days: below the floor
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AML_RECORD_RETENTION") {
		t.Fatalf("expected sub-five-year AML retention rejection, got %v", err)
	}

	t.Setenv("AML_RECORD_RETENTION", "43800h") // 5y, but RETENTION_PERIOD asks for 10y
	t.Setenv("RETENTION_PERIOD", "87600h")
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "RETENTION_PERIOD") {
		t.Fatalf("expected RETENTION_PERIOD-above-floor rejection, got %v", err)
	}

	t.Setenv("RETENTION_PERIOD", "2160h") // the template's 90d sits under the floor
	cfg, err := Load()
	if err != nil {
		t.Fatalf("5y floor with 90d retention period should load, got %v", err)
	}
	if cfg.AMLRecordRetention != 5*365*24*3600*1e9 {
		t.Fatalf("expected 5y AML floor, got %s", cfg.AMLRecordRetention)
	}

	t.Setenv("AML_RECORD_RETENTION", "0") // documented dev escape hatch
	_, err = Load()
	if err != nil {
		t.Fatalf("explicit zero should disable the floor, got %v", err)
	}
}
