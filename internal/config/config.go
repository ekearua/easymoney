// Package config loads and validates environment-based application settings.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config contains every runtime setting needed by the service.
type Config struct {
	Environment string
	AppName     string
	BaseURL     string
	HTTPAddr    string
	DatabaseURL string
	LogLevel    slog.Level

	AdminEmail        string
	AdminPasswordHash string

	PaystackSecretKey string
	PaystackBaseURL   string

	WhatsAppVerifyToken    string
	WhatsAppAppSecret      string
	WhatsAppAccessToken    string
	WhatsAppPhoneNumberID  string
	WhatsAppGraphVersion   string
	WhatsAppTemplateName   string
	WhatsAppTemplateLocale string

	TelegramEnabled       bool
	TelegramBotToken      string
	TelegramWebhookSecret string
	TelegramAPIBase       string

	SMSEnabled       bool
	SMSProvider      string
	SMSWebhookSecret string
	SMSSenderID      string
	SMSAPIBase       string
	SMSAPIKey        string

	DataProvider    string
	VTPassBaseURL   string
	VTPassAPIKey    string
	VTPassPublicKey string
	VTPassSecretKey string

	PaymentMinKobo  int64
	PaymentMaxKobo  int64
	RetentionPeriod time.Duration
	SessionTTL      time.Duration
	ReceiptTTL      time.Duration
}

// Load reads settings from the process environment and applies safe local defaults.
func Load() (Config, error) {
	cfg := Config{
		Environment:            env("APP_ENV", "development"),
		AppName:                env("APP_NAME", "Xego"),
		BaseURL:                strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		HTTPAddr:               env("HTTP_ADDR", ":8080"),
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		AdminEmail:             strings.ToLower(strings.TrimSpace(env("ADMIN_EMAIL", "admin@example.com"))),
		AdminPasswordHash:      os.Getenv("ADMIN_PASSWORD_HASH"),
		PaystackSecretKey:      os.Getenv("PAYSTACK_SECRET_KEY"),
		PaystackBaseURL:        strings.TrimRight(env("PAYSTACK_BASE_URL", "https://api.paystack.co"), "/"),
		WhatsAppVerifyToken:    os.Getenv("WHATSAPP_VERIFY_TOKEN"),
		WhatsAppAppSecret:      os.Getenv("WHATSAPP_APP_SECRET"),
		WhatsAppAccessToken:    os.Getenv("WHATSAPP_ACCESS_TOKEN"),
		WhatsAppPhoneNumberID:  os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		WhatsAppGraphVersion:   strings.TrimSpace(os.Getenv("WHATSAPP_GRAPH_VERSION")),
		WhatsAppTemplateName:   env("WHATSAPP_STATUS_TEMPLATE", "payment_status_update"),
		WhatsAppTemplateLocale: env("WHATSAPP_TEMPLATE_LOCALE", "en"),
		TelegramEnabled:        envBool("TELEGRAM_ENABLED", false),
		TelegramBotToken:       os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramWebhookSecret:  os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		TelegramAPIBase:        strings.TrimRight(env("TELEGRAM_API_BASE", "https://api.telegram.org"), "/"),
		SMSEnabled:             envBool("SMS_ENABLED", false),
		SMSProvider:            env("SMS_PROVIDER", "webhook"),
		SMSWebhookSecret:       os.Getenv("SMS_WEBHOOK_SECRET"),
		SMSSenderID:            env("SMS_SENDER_ID", "Xego"),
		SMSAPIBase:             strings.TrimRight(os.Getenv("SMS_API_BASE"), "/"),
		SMSAPIKey:              os.Getenv("SMS_API_KEY"),
		DataProvider:           strings.ToLower(env("DATA_PROVIDER", "simulated")),
		VTPassBaseURL:          strings.TrimRight(env("VTPASS_BASE_URL", "https://sandbox.vtpass.com/api"), "/"),
		VTPassAPIKey:           os.Getenv("VTPASS_API_KEY"),
		VTPassPublicKey:        os.Getenv("VTPASS_PUBLIC_KEY"),
		VTPassSecretKey:        os.Getenv("VTPASS_SECRET_KEY"),
		PaymentMinKobo:         envInt64("PAYMENT_MIN_KOBO", 10_000),
		PaymentMaxKobo:         envInt64("PAYMENT_MAX_KOBO", 10_000_000),
		RetentionPeriod:        envDuration("RETENTION_PERIOD", 90*24*time.Hour),
		SessionTTL:             envDuration("CONVERSATION_TTL", 30*time.Minute),
		ReceiptTTL:             envDuration("RECEIPT_TTL", 90*24*time.Hour),
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	if cfg.PaymentMinKobo <= 0 || cfg.PaymentMaxKobo < cfg.PaymentMinKobo {
		return Config{}, fmt.Errorf("payment limits are invalid")
	}
	if cfg.WhatsAppGraphVersion == "" && cfg.Environment != "production" {
		cfg.WhatsAppGraphVersion = "v23.0"
	}
	if cfg.WhatsAppGraphVersion != "" && !regexp.MustCompile(`^v[0-9]+\.[0-9]+$`).MatchString(cfg.WhatsAppGraphVersion) {
		return Config{}, fmt.Errorf("WHATSAPP_GRAPH_VERSION must look like v25.0")
	}
	if cfg.Environment == "production" {
		for name, value := range map[string]string{
			"DATABASE_URL":             cfg.DatabaseURL,
			"ADMIN_PASSWORD_HASH":      cfg.AdminPasswordHash,
			"PAYSTACK_SECRET_KEY":      cfg.PaystackSecretKey,
			"WHATSAPP_VERIFY_TOKEN":    cfg.WhatsAppVerifyToken,
			"WHATSAPP_APP_SECRET":      cfg.WhatsAppAppSecret,
			"WHATSAPP_ACCESS_TOKEN":    cfg.WhatsAppAccessToken,
			"WHATSAPP_PHONE_NUMBER_ID": cfg.WhatsAppPhoneNumberID,
			"WHATSAPP_GRAPH_VERSION":   cfg.WhatsAppGraphVersion,
		} {
			if value == "" {
				return Config{}, fmt.Errorf("%s is required in production", name)
			}
		}
		if !strings.HasPrefix(cfg.PaystackSecretKey, "sk_test_") {
			return Config{}, fmt.Errorf("PAYSTACK_SECRET_KEY must be a Paystack test key")
		}
		if cfg.TelegramEnabled {
			for name, value := range map[string]string{
				"TELEGRAM_BOT_TOKEN":      cfg.TelegramBotToken,
				"TELEGRAM_WEBHOOK_SECRET": cfg.TelegramWebhookSecret,
			} {
				if value == "" {
					return Config{}, fmt.Errorf("%s is required when TELEGRAM_ENABLED=true", name)
				}
			}
		}
		if cfg.SMSEnabled && cfg.SMSWebhookSecret == "" {
			return Config{}, fmt.Errorf("SMS_WEBHOOK_SECRET is required when SMS_ENABLED=true")
		}
		if cfg.DataProvider == "vtpass" {
			for name, value := range map[string]string{
				"VTPASS_API_KEY":    cfg.VTPassAPIKey,
				"VTPASS_PUBLIC_KEY": cfg.VTPassPublicKey,
				"VTPASS_SECRET_KEY": cfg.VTPassSecretKey,
			} {
				if value == "" {
					return Config{}, fmt.Errorf("%s is required when DATA_PROVIDER=vtpass", name)
				}
			}
		}
		publicURL, err := url.Parse(cfg.BaseURL)
		if err != nil || publicURL.Scheme != "https" || publicURL.Host == "" {
			return Config{}, fmt.Errorf("BASE_URL must be a public HTTPS URL in production")
		}
	}
	return cfg, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt64(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
