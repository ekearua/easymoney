// Package config loads and validates environment-based application settings.
package config

import (
	"encoding/hex"
	"errors"
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
	LogFormat   string

	// PaymentProvider selects the default payment gateway ("paystack", "vtpass", etc.).
	PaymentProvider string

	// DataEncryptionKey is the AES-256-GCM key for application-level encryption
	// at rest (chat payloads, session CSRF tokens). Required in production;
	// empty elsewhere runs in plaintext passthrough.
	DataEncryptionKey []byte

	AdminEmail        string
	AdminPasswordHash string

	TOTPEnabled       bool
	TOTPEncryptionKey string

	EmailConfirmationEnabled bool
	EmailDemoCodeInChat      bool
	EmailVerificationTTL     time.Duration
	SMTPHost                 string
	SMTPPort                 int
	SMTPUsername             string
	SMTPPassword             string
	SMTPFrom                 string

	PaystackSecretKey string
	PaystackBaseURL   string

	WhatsAppVerifyToken    string
	WhatsAppAppSecret      string
	WhatsAppAccessToken    string
	WhatsAppPhoneNumberID  string
	WhatsAppPhoneNumber    string
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

	DataProvider        string
	VTPassBaseURL       string
	VTPassAPIKey        string
	VTPassPublicKey     string
	VTPassSecretKey     string
	VTPassWebhookSecret string
	VTPassTimeout       time.Duration

	IdentityProvider  string
	NINBVNPortalKey   string
	NINBVNPortalURL   string
	NINBVNPortalTimeout time.Duration
	ScreeningProvider string
	KYCRescreenPeriod time.Duration

	// AI / OCR / STT configuration (Phase 3-5).
	AIEnabled  bool
	AIProvider string
	AIAPIKey   string
	AIAIModel  string
	AITimeout  time.Duration
	AIMaxRPM   int

	MonitorVelocityWindow     time.Duration
	MonitorVelocityLimit      int
	MonitorStructuringWindow  time.Duration
	MonitorStructuringCount   int
	MonitorStructuringFloor   int64
	MonitorStructuringCeil    int64
	MonitorRoundAmountStep    int64
	MonitorRoundAmountMin     int64
	MonitorHighRiskCategories []string

	// ReportCTRThresholdKobo is the amount above which a succeeded payment is
	// included in the currency transaction report (C15). Defaults to the
	// ₦10,000,000 CBN cash-reporting threshold.
	ReportCTRThresholdKobo int64

	PaymentMinKobo  int64
	PaymentMaxKobo  int64
	RetentionPeriod time.Duration
	SessionTTL      time.Duration
	ReceiptTTL      time.Duration

	RedisURL string

	// EventBus selects the Phase 3 event backbone: "memory" (default, the
	// Kafka-compatible in-memory bus) or "kafka" (segmentio/kafka-go).
	EventBus           string
	EventBusPartitions int
	KafkaBrokers       []string
	KafkaGroupID       string

	RateLimitWebhooksPerMinute int
	RateLimitPublicPerMinute   int
	RateLimitScanPerMinute     int
	RateLimitAPIKeysPerMinute  int

	SettlementFeeBps int

	PayoutMinKobo         int64
	PayoutMaxKobo         int64
	PayoutDailyCapKobo    int64
	PayoutDailyCountLimit int

	AuthSessionTTL time.Duration

	InvoiceAcceptedNumbers []string
}

// Load reads settings from the process environment and applies safe local defaults.
func Load() (Config, error) {
	cfg := Config{
		Environment:                env("APP_ENV", "development"),
		AppName:                    env("APP_NAME", "Xego"),
		BaseURL:                    strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		HTTPAddr:                   env("HTTP_ADDR", ":8080"),
		DatabaseURL:                os.Getenv("DATABASE_URL"),
		PaymentProvider:            env("PAYMENT_PROVIDER", "paystack"),
		AdminEmail:                 strings.ToLower(strings.TrimSpace(env("ADMIN_EMAIL", "admin@example.com"))),
		AdminPasswordHash:          os.Getenv("ADMIN_PASSWORD_HASH"),
		TOTPEnabled:                envBool("TOTP_ENABLED", env("APP_ENV", "development") == "production"),
		TOTPEncryptionKey:          strings.TrimSpace(os.Getenv("TOTP_ENCRYPTION_KEY")),
		EmailConfirmationEnabled:   envBool("EMAIL_CONFIRMATION_ENABLED", true),
		EmailDemoCodeInChat:        envBool("EMAIL_DEMO_CODE_IN_CHAT", false),
		EmailVerificationTTL:       envDuration("EMAIL_VERIFICATION_TTL", 10*time.Minute),
		SMTPHost:                   strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:                   int(envInt64("SMTP_PORT", 587)),
		SMTPUsername:               os.Getenv("SMTP_USERNAME"),
		SMTPPassword:               os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:                   strings.TrimSpace(os.Getenv("SMTP_FROM")),
		PaystackSecretKey:          os.Getenv("PAYSTACK_SECRET_KEY"),
		PaystackBaseURL:            strings.TrimRight(env("PAYSTACK_BASE_URL", "https://api.paystack.co"), "/"),
		WhatsAppVerifyToken:        os.Getenv("WHATSAPP_VERIFY_TOKEN"),
		WhatsAppAppSecret:          os.Getenv("WHATSAPP_APP_SECRET"),
		WhatsAppAccessToken:        os.Getenv("WHATSAPP_ACCESS_TOKEN"),
		WhatsAppPhoneNumberID:      os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		WhatsAppPhoneNumber:        normalizeE164(os.Getenv("WHATSAPP_PHONE_NUMBER")),
		WhatsAppGraphVersion:       strings.TrimSpace(os.Getenv("WHATSAPP_GRAPH_VERSION")),
		WhatsAppTemplateName:       env("WHATSAPP_STATUS_TEMPLATE", "payment_status_update"),
		WhatsAppTemplateLocale:     env("WHATSAPP_TEMPLATE_LOCALE", "en"),
		TelegramEnabled:            envBool("TELEGRAM_ENABLED", false),
		TelegramBotToken:           os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramWebhookSecret:      os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		TelegramAPIBase:            strings.TrimRight(env("TELEGRAM_API_BASE", "https://api.telegram.org"), "/"),
		SMSEnabled:                 envBool("SMS_ENABLED", false),
		SMSProvider:                env("SMS_PROVIDER", "webhook"),
		SMSWebhookSecret:           os.Getenv("SMS_WEBHOOK_SECRET"),
		SMSSenderID:                env("SMS_SENDER_ID", "Xego"),
		SMSAPIBase:                 strings.TrimRight(os.Getenv("SMS_API_BASE"), "/"),
		SMSAPIKey:                  os.Getenv("SMS_API_KEY"),
		DataProvider:               strings.ToLower(env("DATA_PROVIDER", "simulated")),
		IdentityProvider:           strings.ToLower(env("IDENTITY_PROVIDER", "simulated")),
		NINBVNPortalKey:            os.Getenv("NINBVNPORTAL_API_KEY"),
		NINBVNPortalURL:            strings.TrimRight(env("NINBVNPORTAL_BASE_URL", "https://ninbvnportal.com/api"), "/"),
		NINBVNPortalTimeout:        envDuration("NINBVNPORTAL_TIMEOUT", 30*time.Second),
		ScreeningProvider:          strings.ToLower(env("SCREENING_PROVIDER", "simulated")),
		AIEnabled:                  envBool("AI_ENABLED", false),
		AIProvider:                 strings.ToLower(env("AI_PROVIDER", "simulated")),
		AIAPIKey:                   os.Getenv("AI_API_KEY"),
		AIAIModel:                  env("AI_MODEL", ""),
		AITimeout:                  envDuration("AI_TIMEOUT", 30*time.Second),
		AIMaxRPM:                   int(envInt64("AI_MAX_REQUESTS_PER_MINUTE", 30)),
		VTPassBaseURL:              strings.TrimRight(env("VTPASS_BASE_URL", "https://sandbox.vtpass.com/api"), "/"),
		VTPassAPIKey:               os.Getenv("VTPASS_API_KEY"),
		VTPassPublicKey:            os.Getenv("VTPASS_PUBLIC_KEY"),
		VTPassSecretKey:            os.Getenv("VTPASS_SECRET_KEY"),
		VTPassWebhookSecret:        os.Getenv("VTPASS_WEBHOOK_SECRET"),
		VTPassTimeout:              envDuration("VTPASS_TIMEOUT", 45*time.Second),
		PaymentMinKobo:             envInt64("PAYMENT_MIN_KOBO", 10_000),
		PaymentMaxKobo:             envInt64("PAYMENT_MAX_KOBO", 10_000_000),
		RetentionPeriod:            envDuration("RETENTION_PERIOD", 90*24*time.Hour),
		KYCRescreenPeriod:          envDuration("KYC_RESCREEN_PERIOD", 90*24*time.Hour),
		MonitorVelocityWindow:      envDuration("MONITOR_VELOCITY_WINDOW", 24*time.Hour),
		MonitorVelocityLimit:       int(envInt64("MONITOR_VELOCITY_LIMIT", 10)),
		MonitorStructuringWindow:   envDuration("MONITOR_STRUCTURING_WINDOW", 24*time.Hour),
		MonitorStructuringCount:    int(envInt64("MONITOR_STRUCTURING_COUNT", 3)),
		MonitorStructuringFloor:    envInt64("MONITOR_STRUCTURING_FLOOR_KOBO", 4_000_000),
		MonitorStructuringCeil:     envInt64("MONITOR_STRUCTURING_CEIL_KOBO", 10_000_000),
		MonitorRoundAmountStep:     envInt64("MONITOR_ROUND_AMOUNT_STEP_KOBO", 1_000_000),
		MonitorRoundAmountMin:      envInt64("MONITOR_ROUND_AMOUNT_MIN_KOBO", 1_000_000),
		ReportCTRThresholdKobo:     envInt64("REPORT_CTR_THRESHOLD_KOBO", 1_000_000_000),
		SessionTTL:                 envDuration("CONVERSATION_TTL", 30*time.Minute),
		ReceiptTTL:                 envDuration("RECEIPT_TTL", 90*24*time.Hour),
		RedisURL:                   strings.TrimSpace(os.Getenv("REDIS_URL")),
		EventBus:                   strings.ToLower(env("EVENT_BUS", "memory")),
		EventBusPartitions:         int(envInt64("EVENT_BUS_PARTITIONS", 4)),
		KafkaGroupID:               env("KAFKA_GROUP_ID", "xego"),
		RateLimitWebhooksPerMinute: int(envInt64("RATE_LIMIT_WEBHOOKS_PER_MINUTE", 120)),
		RateLimitPublicPerMinute:   int(envInt64("RATE_LIMIT_PUBLIC_PER_MINUTE", 60)),
		RateLimitScanPerMinute:     int(envInt64("RATE_LIMIT_SCAN_PER_MINUTE", 30)),
		RateLimitAPIKeysPerMinute:  int(envInt64("RATE_LIMIT_API_KEYS_PER_MINUTE", 300)),

		SettlementFeeBps: int(envInt64("SETTLEMENT_FEE_BPS", 250)),

		PayoutMinKobo:         envInt64("PAYOUT_MIN_KOBO", 100_00),
		PayoutMaxKobo:         envInt64("PAYOUT_MAX_KOBO", 10_000_00),
		PayoutDailyCapKobo:    envInt64("PAYOUT_DAILY_CAP_KOBO", 50_000_00),
		PayoutDailyCountLimit: int(envInt64("PAYOUT_DAILY_COUNT_LIMIT", 10)),

		AuthSessionTTL: envDuration("AUTH_SESSION_TTL", 12*time.Hour),
	}
	if raw := os.Getenv("INVOICE_ACCEPTED_NUMBERS"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				cfg.InvoiceAcceptedNumbers = append(cfg.InvoiceAcceptedNumbers, s)
			}
		}
	}
	if raw := os.Getenv("MONITOR_HIGH_RISK_CATEGORIES"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				cfg.MonitorHighRiskCategories = append(cfg.MonitorHighRiskCategories, s)
			}
		}
	}
	if raw := os.Getenv("KAFKA_BROKERS"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.KafkaBrokers = append(cfg.KafkaBrokers, s)
			}
		}
	}
	if cfg.EventBus != "memory" && cfg.EventBus != "kafka" {
		return Config{}, fmt.Errorf("EVENT_BUS must be \"memory\" or \"kafka\", got %q", cfg.EventBus)
	}
	if cfg.EventBus == "kafka" && len(cfg.KafkaBrokers) == 0 {
		return Config{}, errors.New("KAFKA_BROKERS is required when EVENT_BUS=kafka")
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	cfg.LogFormat = strings.ToLower(env("LOG_FORMAT", "json"))
	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		return Config{}, fmt.Errorf("LOG_FORMAT must be \"json\" or \"text\", got %q", cfg.LogFormat)
	}
	if raw := strings.TrimSpace(os.Getenv("DATA_ENCRYPTION_KEY")); raw != "" {
		key, err := hex.DecodeString(raw)
		if err != nil || len(key) != 32 {
			return Config{}, errors.New("DATA_ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
		}
		cfg.DataEncryptionKey = key
	}
	if cfg.PaymentMinKobo <= 0 || cfg.PaymentMaxKobo < cfg.PaymentMinKobo {
		return Config{}, fmt.Errorf("payment limits are invalid")
	}
	if cfg.SMTPFrom == "" {
		cfg.SMTPFrom = cfg.AdminEmail
	}
	if cfg.SMTPPort <= 0 {
		cfg.SMTPPort = 587
	}
	if cfg.WhatsAppGraphVersion == "" && cfg.Environment != "production" {
		cfg.WhatsAppGraphVersion = "v23.0"
	}
	if cfg.WhatsAppGraphVersion != "" && !regexp.MustCompile(`^v[0-9]+\.[0-9]+$`).MatchString(cfg.WhatsAppGraphVersion) {
		return Config{}, fmt.Errorf("WHATSAPP_GRAPH_VERSION must look like v25.0")
	}
	if cfg.Environment == "production" {
		if cfg.PaymentProvider == "simulated" {
			return Config{}, errors.New("PAYMENT_PROVIDER=simulated is not allowed in production")
		}
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
		if len(cfg.DataEncryptionKey) == 0 {
			return Config{}, errors.New("DATA_ENCRYPTION_KEY is required in production")
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
		if cfg.EmailDemoCodeInChat {
			return Config{}, errors.New("EMAIL_DEMO_CODE_IN_CHAT is not allowed in production")
		}
		if cfg.TOTPEnabled {
			if cfg.TOTPEncryptionKey == "" {
				return Config{}, errors.New("TOTP_ENCRYPTION_KEY is required when TOTP_ENABLED=true")
			}
			key, err := hex.DecodeString(cfg.TOTPEncryptionKey)
			if err != nil || len(key) != 32 {
				return Config{}, errors.New("TOTP_ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
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

// normalizeE164 strips formatting and a leading "+" so WhatsApp deep links can
// be built as https://wa.me/<digits>.
func normalizeE164(raw string) string {
	var digits strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	return digits.String()
}
