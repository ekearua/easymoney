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

	// PaymentProvider selects the default payment gateway ("interswitch", "vtpass", etc.).
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

	LinkAccountsEnabled bool
	LinkDemoCodeInChat  bool
	LinkCodeTTL         time.Duration
	SMTPHost            string
	SMTPPort            int
	SMTPUsername        string
	SMTPPassword        string
	SMTPFrom            string

	InterswitchClientID             string
	InterswitchClientSecret         string
	InterswitchMerchantCode         string
	InterswitchPayItemID            string
	InterswitchBaseURL              string // API/requery host (gettransaction.json)
	InterswitchTokenURL             string // optional; OAuth token issuer (defaults to BaseURL's passport host)
	InterswitchCheckoutBaseURL      string // optional; hosted payment-page host (defaults per mode)
	InterswitchCheckoutMode         string
	InterswitchTransferBaseURL      string // optional; Quickteller Send Money v5 host for payouts/name-enquiry/query
	InterswitchSenderName           string // optional; name shown as the transfer sender
	InterswitchSenderPhone          string // optional; sender phone on the transfer
	InterswitchSenderEmail          string // optional; sender email on the transfer
	InterswitchInitiatingEntityCode string // optional; initiatingEntityCode on v5 TransferFunds (defaults to "PBL")
	InterswitchHostedFieldsSDKURL   string // optional; SDK script override for the Hosted Fields card page
	InterswitchWebhookSecret        string
	InterswitchTerminalID           string // fallback TerminalId for quickteller v5 calls
	InterswitchSourceAccount        string // funding account for NIP transfers and VTU
	InterswitchCheckoutRender       string // "hosted_fields" (default) or "legacy" redirect form

	// BankTransferMode selects how bank-transfer payments are collected:
	// "interswitch" (dynamic virtual accounts through the virtual accounts API).
	BankTransferMode string
	// PayoutProvider selects the settlement rail: "interswitch" (Quickteller
	// single transfer / NIP).
	PayoutProvider string
	// RefundProvider selects the reversal rail: "interswitch" (refund API).
	RefundProvider string

	// Fee configuration (kobo). These control the Faya platform fee
	// applied as a split on merchant collection payments.
	FeeCardBPS           int64
	FeeCardFixedKobo     int64
	FeeCardCapKobo       int64
	FeeDVABPS            int64
	FeeDVAFixedKobo      int64
	FeeDVACapKobo        int64
	FeeTransferBPS       int64
	FeeTransferFixedKobo int64
	FeeTransferCapKobo   int64
	FeeNIPPayoutFlatKobo int64

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

	// Instagram Messaging (Meta Graph API). Requires a professional Instagram
	// account linked to a Facebook Page and an app with instagram_manage_messages.
	InstagramEnabled      bool
	InstagramAppSecret    string
	InstagramAccessToken  string
	InstagramIGID         string // business IG account id (webhook recipient)
	InstagramPageID       string // linked Facebook Page id used to send replies (Messenger Send API)
	InstagramVerifyToken  string
	InstagramGraphVersion string

	// TikTok Business Messaging. Full-parity channel adapter: interactive
	// primitives degrade to numbered text menus (platform has no buttons).
	TikTokEnabled     bool
	TikTokAppSecret   string
	TikTokAccessToken string
	TikTokAPIBase     string

	// WebhookBodyLogEnabled logs the raw bytes of every inbound HTTPS webhook
	// before signature validation. Off by default; mainly a field-debug tool to
	// inspect the exact payload a provider (e.g. Meta's webhook tester) sends.
	WebhookBodyLogEnabled bool

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

	IdentityProvider    string
	NINBVNPortalKey     string
	NINBVNPortalURL     string
	NINBVNPortalTimeout time.Duration
	ScreeningProvider   string
	ScreeningAPIBase    string
	ScreeningAPIKey     string
	ScreeningTimeout    time.Duration
	KYCRescreenPeriod   time.Duration

	// Dojah is the primary screening/biometric vendor named in
	// AML_VENDOR_DECISION_RECORD.md §11. These credentials are shared by the
	// four lanes that accept the value "dojah" — onboarding sanctions/PEP
	// screening (SCREENING_PROVIDER), payment screening
	// (PAYMENT_SCREENING_PROVIDER), liveness (LIVENESS_PROVIDER) and document
	// authenticity (DOC_AUTH_PROVIDER) — and are only required when one of
	// them selects it. DOJAH_BASE_URL defaults to production
	// (https://api.dojah.io); the sandbox is https://sandbox.dojah.io.
	DojahAppID          string
	DojahSecretKey      string
	DojahBaseURL        string
	DojahMatchThreshold float64

	// G2 payment-time counterparty screening for money-out rails. Defaults
	// to the deterministic simulator until the screening vendor's payment-
	// screening adapter lands (AML_VENDOR_DECISION_RECORD.md).
	PaymentScreeningProvider string

	// G9 adverse-media + domestic-list screening. Domestic lists (NSC/NIGSAC)
	// are owned via the internal watchlist; this is the adverse-media vendor
	// lane, defaulting to the deterministic simulator until the Dojah adapter
	// (ADVERSE_MEDIA_PROVIDER=http) is configured. Validation is added by the
	// provider wiring step; an unknown provider hits the app.go switch default.
	AdverseMediaProvider       string
	AdverseMediaAPIBase        string
	AdverseMediaAppID          string
	AdverseMediaSecretKey      string
	AdverseMediaTimeout        time.Duration
	AdverseMediaMatchThreshold float64

	// G10 liveness / face verification for the Tier-3 EDD step. The live
	// biometric vendor is part of the identity purchase, so this defaults to
	// the deterministic simulator; there is no HTTP adapter yet.
	LivenessProvider string

	// Corporate-registry (CAC), tax-authority (FIRS) and location
	// verification. Unlike identity and screening these default to their
	// deterministic simulators, because their live vendors are optional: the
	// platform (and every test) boots without registry or geocoding
	// credentials. Set *_PROVIDER=http to use a live endpoint.
	CorporateRegistryProvider string
	CorporateRegistryAPIBase  string
	CorporateRegistryAPIKey   string
	CorporateRegistryTimeout  time.Duration
	TaxIDProvider             string
	TaxIDAPIBase              string
	TaxIDAPIKey               string
	TaxIDTimeout              time.Duration
	LocationProvider          string
	LocationAPIBase           string
	LocationAPIKey            string
	LocationTimeout           time.Duration
	LocationMaxDistanceM      float64

	// Document authenticity (G12): tamper detection and face-to-document
	// comparison over a submitted KYB/KYC reading. Like the registry and
	// location verifiers this defaults to its deterministic simulator, so the
	// platform boots without a document-analysis vendor; set
	// DOC_AUTH_PROVIDER=http to use a live endpoint. A reviewer-verified
	// document counts as evidence only when this check returns "genuine".
	DocAuthenticityProvider string
	DocAuthenticityAPIBase  string
	DocAuthenticityAPIKey   string
	DocAuthenticityTimeout  time.Duration

	// Bulk payments (location-aware disbursements).
	BulkLocationTTL       time.Duration
	BulkRadiusM           int
	BulkMaxAccuracyM      float64
	BulkChunkSize         int
	BulkWorkerConcurrency int
	BulkSendRatePerSec    int
	BulkMaxTargets        int
	BulkStage1Template    string

	// Database connection-pool sizing (requirements workbook #1): the store
	// pool was hardcoded at 25/5, so raising it for production needed a
	// rebuild. The knobs use the workbook's own names (sheet "Concurrency &
	// Pool Tuning", section 6): DB_POOL_MAX / DB_POOL_MIN, so a deployment
	// can scale the pool (e.g. 75 behind PgBouncer) from the environment.
	DBMaxConns int
	DBMinConns int

	// Background worker pool sizing (requirements workbook #3): concurrent
	// worker pools replace the single sequential tick loop where every
	// handler ran inline. Defaults follow the workbook's stated target (10
	// inbound, 5 outbox, 3 reconcilers) with the webhook, data and misc
	// classes sized alongside them; every value is floored at 1.
	WorkerInboundConcurrency   int
	WorkerOutboxConcurrency    int
	WorkerWebhookConcurrency   int
	WorkerDataConcurrency      int
	WorkerReconcileConcurrency int
	WorkerMiscConcurrency      int

	// AI / OCR / STT configuration (Phase 3-5).
	AIEnabled  bool
	AIProvider string
	AIAPIKey   string
	AIAIModel  string
	AITimeout  time.Duration
	AIMaxRPM   int

	MonitorVelocityWindow time.Duration

	// Release/CD (demo synthetic): the post-deploy check fails when the oldest
	// non-terminal outbox row is older than this, i.e. the message pipeline
	// stopped draining. Zero disables the backlog assertion.
	SyntheticOutboxMaxAge     time.Duration
	MonitorVelocityLimit      int
	MonitorStructuringWindow  time.Duration
	MonitorStructuringCount   int
	MonitorStructuringFloor   int64
	MonitorStructuringCeil    int64
	MonitorRoundAmountStep    int64
	MonitorRoundAmountMin     int64
	MonitorHighRiskCategories []string

	// Fraud engine (G13) knobs. A zero limit disables its rule; the window is
	// the lookback shared by the threshold rules. NewDestinationWindow is how
	// recently a payout destination account must have been created to count as
	// new, and NewDestinationMinKobo is the smallest such payout that flags.
	FraudWindow                time.Duration
	FraudFailedAttemptLimit    int
	FraudPaymentVelocityLimit  int
	FraudDisputeVelocityLimit  int
	FraudRefundVelocityLimit   int
	FraudPayoutVelocityLimit   int
	FraudNewDestinationWindow  time.Duration
	FraudNewDestinationMinKobo int64

	// Step-up re-auth (G13): when enabled, a merchant-initiated payout whose
	// owning merchant's risk band is StepUpBand requires a fresh MFA grant
	// within StepUpTTL. Worker dispatches are never gated.
	StepUpEnabled bool
	StepUpTTL     time.Duration
	StepUpBand    string

	// ReportCTRThresholdKobo is the amount above which a succeeded payment is
	// included in the currency transaction report (C15). Defaults to the
	// ₦10,000,000 CBN cash-reporting threshold.
	ReportCTRThresholdKobo int64

	// RiskHighSingleLimitKobo caps the single-transaction allowance for a
	// profile whose KYC risk band is high (M3 Trust Fabric step-up): the
	// decision layer makes the persisted band bite until the review clears.
	// Zero disables the cap.
	RiskHighSingleLimitKobo int64

	// Compliance-queue tuning suggestions (the M4 feedback loop made
	// actionable): a rule whose labelled false-positive share over the
	// window reaches TuningFPPercent with at least TuningMinLabelled
	// labelled outcomes gets a suggestion surfaced. Zero FPPercent keeps
	// the shipped default (60% over ≥10 labels).
	TuningFPPercent   float64
	TuningMinLabelled int64

	// RiskDecayHalfLife is how long one ML/FT risk observation's contribution
	// to the customer's risk score takes to fall by half. Zero or negative
	// disables decay, leaving every observation at full weight forever — which
	// is what ratchets a customer who collects dismissed alerts. The default
	// matches the 90-day KYC rescreen period, so a suspicion fades on the same
	// clock the profile is re-checked on.
	RiskDecayHalfLife time.Duration

	// RiskGeographyWeight (G14, spec §3.7) is the score weight added when the
	// customer's declared country of residence is a FATF high-risk / monitored
	// jurisdiction. Zero keeps the shipped default (kyc.RiskGeographyWeight).
	RiskGeographyWeight float64

	// AMLRecordRetention (G6, spec §5) is the MLPPA 2022 record-keeping
	// floor: five years. The retention purge clamps every AML-scope class
	// (payments, invoices, thrift, audit, merchant registrations) to this
	// boundary — financial classes are additionally archived before any
	// purge, so the ledger retains them indefinitely. Zero disables the
	// floor (dev/harness escape hatch).
	AMLRecordRetention time.Duration

	// AuditChainVerifyInterval (G7 residual, spec §5) is the cadence of the
	// scheduled audit-chain verification sweep. The sweep recomputes the
	// hash chain, records each run in audit_chain_checks and raises one
	// alert (audit_chain_alerts) on a clean→broken transition — so a
	// tamper event surfaces without waiting for an admin page view.
	AuditChainVerifyInterval time.Duration

	// Backup staleness thresholds (pre-deploy resilience prerequisites):
	// the admin metrics panel ages each backup_runs heartbeat and turns
	// red when the newest row of a kind is older than its threshold.
	// Defaults match the timer cadences in deploy/systemd: a daily base
	// backup (26h allows one missed run), a 15-min WAL ship (45m allows
	// three missed runs — beyond BACKUP_RPO=1h the RPO guarantee is
	// gone), and a weekly restore drill (8d allows one missed week).
	// Zero disables that alert.
	BackupBaseStaleAfter   time.Duration
	WALShipStaleAfter      time.Duration
	RestoreDrillStaleAfter time.Duration

	PaymentMinKobo  int64
	PaymentMaxKobo  int64
	RetentionPeriod time.Duration
	SessionTTL      time.Duration
	ReceiptTTL      time.Duration

	// Onboarding reminders (chat-side nudge for incomplete onboarding). The
	// worker sweeps stalled users every OnboardingReminderInterval; each
	// stage gets at most OnboardingReminderMax reminders spaced at least
	// OnboardingReminderGap apart, never inside OnboardingReminderQuietStart
	// to OnboardingReminderQuietEnd platform time. A WhatsApp reminder
	// outside the 24-hour service window needs an approved template: with
	// OnboardingReminderTemplate unset the attempt is recorded as
	// 'pending_template' and skipped instead of burning quality rating.
	OnboardingReminderInterval    time.Duration
	OnboardingReminderStartAfter  time.Duration
	OnboardingReminderStallAfter  time.Duration
	OnboardingReminderGap         time.Duration
	OnboardingReminderMax         int
	OnboardingReminderQuietStart  int
	OnboardingReminderQuietEnd    int
	OnboardingReminderTemplate    string
	OnboardingReminderTemplateLoc string
	// OnboardingReminderDeliveryGuardWindow is the trailing window (default
	// 1 hour) over which the reminder guard computes the recent WhatsApp
	// outbox failure/block ratio. When that ratio meets or exceeds
	// OnboardingReminderDeliveryGuardFailureRatio, the reminder sweep is
	// paused for the rest of the current tick so a degraded number's Meta
	// quality rating is not hammered by nudges. Zero disables the guard.
	OnboardingReminderDeliveryGuardWindow       time.Duration
	OnboardingReminderDeliveryGuardFailureRatio float64

	// WebFlowsEnabled moves transactional flows from WhatsApp chat into the
	// browser (migration 060): chat sends only the link message and the
	// completion confirmation, so a transaction costs at most two service
	// messages. WebFlowsDisabledFlows lists flow types that keep the chat FSM
	// (rollout override). Telegram and SMS always keep the chat FSM.
	WebFlowsEnabled       bool
	WebFlowsDisabledFlows []string

	// Message-cost metering: every outbound customer message is recorded in
	// message_log, and the admin messaging view prices it with these
	// configurable Meta Nigeria estimates (naira per message). Meta's USD rate
	// card is the source of truth; these values drive the display estimate
	// only, so they stay configurable rather than hard-coded.
	MessageLogEnabled       bool
	MessageCostServiceNGN   int64
	MessageCostMarketingNGN int64

	// ScreeningCostNGN is an optional display-only estimate per screening
	// check (customer screening, adverse media, payment screening) shown on
	// the admin "Onboarding & screening cost" tracking page.
	ScreeningCostNGN int64

	// AITokensPerNGN prices the AI extraction spend shown in the admin media
	// report so token totals read as an estimated naira cost. It mirrors the
	// message-cost fields: a display estimate driven by an env knob, not a
	// billing value.
	AITokensPerNGN int64

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

	// POS agent network (migration 103, POS_AGENT_NETWORK_PLAN.md §12). A local
	// agent fronts cash to a remittance recipient and Xego owes it on
	// 2400_agent_float. AgentNetworkEnabled is the feature kill switch;
	// RemittanceTTL/RemittanceOTPTTL are the pickup policy (72h window, short
	// OTP); AgentIDRequired forces a government ID on every payout (needed so a
	// recipient's cash-outs can be summed for the CBN per-customer ceiling). The
	// per-tier values seed an agent's limits and commission when an operator
	// activates it; the real enforcement is per-payout/per-customer at
	// redemption. Every number is bounded by the CBN ceilings (agent ₦1.2m/day;
	// customer ₦100k/day) in validateAgentNetwork.
	AgentNetworkEnabled bool
	RemittanceTTL       time.Duration
	RemittanceOTPTTL    time.Duration
	AgentIDRequired     bool
	AgentDefaultTier    string

	// AgentIDHashPepper keys the deterministic salted hash of a recipient's
	// government ID (receiver_id_hash): the stable per-customer cap key. It is
	// the same pepper at every agent so a recipient's cash-outs aggregate across
	// the network. Empty is acceptable in development (the hash is still
	// deterministic and cross-agent); production should set AGENT_ID_HASH_PEPPER
	// to a server-side secret.
	AgentIDHashPepper string

	AgentStandardPerPayoutKobo      int64
	AgentStandardDailyKobo          int64
	AgentStandardMaxOutstandingKobo int64
	AgentStandardCommissionBPS      int
	AgentSuperPerPayoutKobo         int64
	AgentSuperDailyKobo             int64
	AgentSuperMaxOutstandingKobo    int64
	AgentSuperCommissionBPS         int
	AgentMaxPerPayoutKobo           int64
	AgentMaxDailyKobo               int64
	AgentMaxMaxOutstandingKobo      int64
	AgentMaxCommissionBPS           int

	AuthSessionTTL time.Duration

	InvoiceAcceptedNumbers []string
}

// CBN agent-banking ceilings (POS_AGENT_NETWORK_PLAN.md §12): a POS agent may
// cash out at most ₦1,200,000 per day cumulatively, and a single recipient at
// most ₦100,000 per day (₦500,000 per week). Tier configuration may not exceed
// these; redemption enforces the real per-payout, per-agent and per-customer
// limits at runtime.
const (
	CBNMaxAgentDailyCashOutKobo      int64 = 120_000_000
	CBNMaxCustomerDailyCashOutKobo   int64 = 10_000_000
	CBNMaxCustomerWeeklyCashOutKobo  int64 = 50_000_000
)

// AgentTierDefaults resolves an agent tier's limit and commission defaults from
// configuration. An unknown tier falls back to standard.
func (c Config) AgentTierDefaults(tier string) (maxOutstandingKobo, perPayoutKobo, dailyKobo int64, commissionBps int) {
	switch tier {
	case "super":
		return c.AgentSuperMaxOutstandingKobo, c.AgentSuperPerPayoutKobo, c.AgentSuperDailyKobo, c.AgentSuperCommissionBPS
	case "max":
		return c.AgentMaxMaxOutstandingKobo, c.AgentMaxPerPayoutKobo, c.AgentMaxDailyKobo, c.AgentMaxCommissionBPS
	default:
		return c.AgentStandardMaxOutstandingKobo, c.AgentStandardPerPayoutKobo, c.AgentStandardDailyKobo, c.AgentStandardCommissionBPS
	}
}

// Load reads settings from the process environment and applies safe local defaults.
func Load() (Config, error) {
	cfg := Config{
		Environment:                     env("APP_ENV", "development"),
		AppName:                         env("APP_NAME", "Xego"),
		BaseURL:                         strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		HTTPAddr:                        env("HTTP_ADDR", ":8080"),
		DatabaseURL:                     os.Getenv("DATABASE_URL"),
		PaymentProvider:                 env("PAYMENT_PROVIDER", "interswitch"),
		AdminEmail:                      strings.ToLower(strings.TrimSpace(env("ADMIN_EMAIL", "admin@example.com"))),
		AdminPasswordHash:               os.Getenv("ADMIN_PASSWORD_HASH"),
		TOTPEnabled:                     envBool("TOTP_ENABLED", env("APP_ENV", "development") == "production"),
		TOTPEncryptionKey:               strings.TrimSpace(os.Getenv("TOTP_ENCRYPTION_KEY")),
		EmailConfirmationEnabled:        envBool("EMAIL_CONFIRMATION_ENABLED", true),
		EmailDemoCodeInChat:             envBool("EMAIL_DEMO_CODE_IN_CHAT", false),
		EmailVerificationTTL:            envDuration("EMAIL_VERIFICATION_TTL", 10*time.Minute),
		LinkAccountsEnabled:             envBool("LINK_ACCOUNTS_ENABLED", false),
		LinkDemoCodeInChat:              envBool("LINK_DEMO_CODE_IN_CHAT", false),
		LinkCodeTTL:                     envDuration("LINK_CODE_TTL", 10*time.Minute),
		SMTPHost:                        strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:                        int(EnvInt64("SMTP_PORT", 587)),
		SMTPUsername:                    os.Getenv("SMTP_USERNAME"),
		SMTPPassword:                    os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:                        strings.TrimSpace(os.Getenv("SMTP_FROM")),
		InterswitchClientID:             strings.TrimSpace(os.Getenv("INTERSWITCH_CLIENT_ID")),
		InterswitchClientSecret:         os.Getenv("INTERSWITCH_CLIENT_SECRET"),
		InterswitchMerchantCode:         strings.TrimSpace(os.Getenv("INTERSWITCH_MERCHANT_CODE")),
		InterswitchPayItemID:            strings.TrimSpace(os.Getenv("INTERSWITCH_PAY_ITEM_ID")),
		InterswitchBaseURL:              strings.TrimRight(env("INTERSWITCH_BASE_URL", "https://sandbox.interswitchng.com"), "/"),
		InterswitchTokenURL:             strings.TrimRight(os.Getenv("INTERSWITCH_TOKEN_URL"), "/"),
		InterswitchCheckoutBaseURL:      strings.TrimRight(os.Getenv("INTERSWITCH_CHECKOUT_BASE_URL"), "/"),
		InterswitchCheckoutMode:         strings.ToUpper(strings.TrimSpace(os.Getenv("INTERSWITCH_CHECKOUT_MODE"))),
		InterswitchTransferBaseURL:      strings.TrimRight(os.Getenv("INTERSWITCH_TRANSFER_BASE_URL"), "/"),
		InterswitchSenderName:           strings.TrimSpace(os.Getenv("INTERSWITCH_SENDER_NAME")),
		InterswitchSenderPhone:          strings.TrimSpace(os.Getenv("INTERSWITCH_SENDER_PHONE")),
		InterswitchSenderEmail:          strings.TrimSpace(os.Getenv("INTERSWITCH_SENDER_EMAIL")),
		InterswitchInitiatingEntityCode: strings.TrimSpace(os.Getenv("INTERSWITCH_INITIATING_ENTITY_CODE")),
		InterswitchHostedFieldsSDKURL:   strings.TrimSpace(os.Getenv("INTERSWITCH_HOSTED_FIELDS_SDK_URL")),
		InterswitchWebhookSecret:        os.Getenv("INTERSWITCH_WEBHOOK_SECRET"),
		InterswitchTerminalID:           strings.TrimSpace(os.Getenv("INTERSWITCH_TERMINAL_ID")),
		InterswitchSourceAccount:        strings.TrimSpace(os.Getenv("INTERSWITCH_SOURCE_ACCOUNT")),
		InterswitchCheckoutRender:       strings.ToLower(env("INTERSWITCH_CHECKOUT_RENDER", "hosted_fields")),
		BankTransferMode:                strings.ToLower(env("BANK_TRANSFER_MODE", "interswitch")),
		PayoutProvider:                  strings.ToLower(env("PAYOUT_PROVIDER", "interswitch")),
		RefundProvider:                  strings.ToLower(env("REFUND_PROVIDER", "interswitch")),
		FeeCardBPS:                      EnvInt64("XEGO_FEE_CARD_BPS", 200),
		FeeCardFixedKobo:                EnvInt64("XEGO_FEE_CARD_FIXED_KOBO", 10000),
		FeeCardCapKobo:                  EnvInt64("XEGO_FEE_CARD_CAP_KOBO", 350000),
		FeeDVABPS:                       EnvInt64("XEGO_FEE_DVA_BPS", 150),
		FeeDVAFixedKobo:                 EnvInt64("XEGO_FEE_DVA_FIXED_KOBO", 0),
		FeeDVACapKobo:                   EnvInt64("XEGO_FEE_DVA_CAP_KOBO", 150000),
		FeeTransferBPS:                  EnvInt64("XEGO_FEE_TRANSFER_BPS", 180),
		FeeTransferFixedKobo:            EnvInt64("XEGO_FEE_TRANSFER_FIXED_KOBO", 0),
		FeeTransferCapKobo:              EnvInt64("XEGO_FEE_TRANSFER_CAP_KOBO", 250000),
		FeeNIPPayoutFlatKobo:            EnvInt64("XEGO_FEE_NIP_PAYOUT_FLAT_KOBO", 10000),
		WhatsAppVerifyToken:             os.Getenv("WHATSAPP_VERIFY_TOKEN"),
		WhatsAppAppSecret:               os.Getenv("WHATSAPP_APP_SECRET"),
		WhatsAppAccessToken:             os.Getenv("WHATSAPP_ACCESS_TOKEN"),
		WhatsAppPhoneNumberID:           os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		WhatsAppPhoneNumber:             normalizeE164(os.Getenv("WHATSAPP_PHONE_NUMBER")),
		WhatsAppGraphVersion:            strings.TrimSpace(os.Getenv("WHATSAPP_GRAPH_VERSION")),
		WhatsAppTemplateName:            env("WHATSAPP_STATUS_TEMPLATE", "payment_status_update"),
		WhatsAppTemplateLocale:          env("WHATSAPP_TEMPLATE_LOCALE", "en"),
		TelegramEnabled:                 envBool("TELEGRAM_ENABLED", false),
		TelegramBotToken:                os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramWebhookSecret:           os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		TelegramAPIBase:                 strings.TrimRight(env("TELEGRAM_API_BASE", "https://api.telegram.org"), "/"),
		InstagramEnabled:                envBool("INSTAGRAM_ENABLED", false),
		InstagramAppSecret:              os.Getenv("INSTAGRAM_APP_SECRET"),
		InstagramAccessToken:            os.Getenv("INSTAGRAM_ACCESS_TOKEN"),
		InstagramIGID:                   strings.TrimSpace(os.Getenv("INSTAGRAM_IG_ID")),
		InstagramPageID:                 strings.TrimSpace(os.Getenv("INSTAGRAM_PAGE_ID")),
		InstagramVerifyToken:            os.Getenv("INSTAGRAM_VERIFY_TOKEN"),
		InstagramGraphVersion:           strings.TrimSpace(os.Getenv("INSTAGRAM_GRAPH_VERSION")),
		TikTokEnabled:                   envBool("TIKTOK_ENABLED", false),
		TikTokAppSecret:                 os.Getenv("TIKTOK_APP_SECRET"),
		TikTokAccessToken:               os.Getenv("TIKTOK_ACCESS_TOKEN"),
		TikTokAPIBase:                   strings.TrimRight(env("TIKTOK_API_BASE", "https://open.tiktokapis.com"), "/"),
		WebhookBodyLogEnabled:           envBool("WEBHOOK_BODY_LOG_ENABLED", false),
		SMSEnabled:                      envBool("SMS_ENABLED", false),
		SMSProvider:                     env("SMS_PROVIDER", "webhook"),
		SMSWebhookSecret:                os.Getenv("SMS_WEBHOOK_SECRET"),
		SMSSenderID:                     env("SMS_SENDER_ID", "Xego"),
		SMSAPIBase:                      strings.TrimRight(os.Getenv("SMS_API_BASE"), "/"),
		SMSAPIKey:                       os.Getenv("SMS_API_KEY"),
		DataProvider:                    strings.ToLower(env("DATA_PROVIDER", "vtpass")),
		IdentityProvider:                strings.ToLower(env("IDENTITY_PROVIDER", "ninbvnportal")),
		NINBVNPortalKey:                 os.Getenv("NINBVNPORTAL_API_KEY"),
		NINBVNPortalURL:                 strings.TrimRight(env("NINBVNPORTAL_BASE_URL", "https://ninbvnportal.com/api"), "/"),
		NINBVNPortalTimeout:             envDuration("NINBVNPORTAL_TIMEOUT", 30*time.Second),
		ScreeningProvider:               strings.ToLower(env("SCREENING_PROVIDER", "http")),
		ScreeningAPIBase:                strings.TrimRight(os.Getenv("SCREENING_API_BASE"), "/"),
		ScreeningAPIKey:                 os.Getenv("SCREENING_API_KEY"),
		ScreeningTimeout:                envDuration("SCREENING_TIMEOUT", 30*time.Second),
		CorporateRegistryProvider:       strings.ToLower(env("CAC_PROVIDER", "simulated")),
		CorporateRegistryAPIBase:        strings.TrimRight(os.Getenv("CAC_API_BASE"), "/"),
		CorporateRegistryAPIKey:         os.Getenv("CAC_API_KEY"),
		CorporateRegistryTimeout:        envDuration("CAC_TIMEOUT", 30*time.Second),
		TaxIDProvider:                   strings.ToLower(env("FIRS_PROVIDER", "simulated")),
		TaxIDAPIBase:                    strings.TrimRight(os.Getenv("FIRS_API_BASE"), "/"),
		TaxIDAPIKey:                     os.Getenv("FIRS_API_KEY"),
		TaxIDTimeout:                    envDuration("FIRS_TIMEOUT", 30*time.Second),
		LocationProvider:                strings.ToLower(env("LOCATION_PROVIDER", "simulated")),
		LocationAPIBase:                 strings.TrimRight(os.Getenv("LOCATION_API_BASE"), "/"),
		LocationAPIKey:                  os.Getenv("LOCATION_API_KEY"),
		LocationTimeout:                 envDuration("LOCATION_TIMEOUT", 30*time.Second),
		LocationMaxDistanceM:            float64(EnvInt64("LOCATION_MAX_DISTANCE_M", 5000)),
		DocAuthenticityProvider:         strings.ToLower(env("DOC_AUTH_PROVIDER", "simulated")),
		DocAuthenticityAPIBase:          strings.TrimRight(os.Getenv("DOC_AUTH_API_BASE"), "/"),
		DocAuthenticityAPIKey:           os.Getenv("DOC_AUTH_API_KEY"),
		DocAuthenticityTimeout:          envDuration("DOC_AUTH_TIMEOUT", 30*time.Second),
		DojahAppID:                      strings.TrimSpace(os.Getenv("DOJAH_APP_ID")),
		DojahSecretKey:                  strings.TrimSpace(os.Getenv("DOJAH_SECRET_KEY")),
		DojahBaseURL:                    strings.TrimRight(env("DOJAH_BASE_URL", "https://api.dojah.io"), "/"),
		DojahMatchThreshold:             envFloat("DOJAH_MATCH_THRESHOLD", 0.85),

		BulkLocationTTL:       envDuration("BULK_LOCATION_TTL", 5*time.Minute),
		BulkRadiusM:           int(EnvInt64("BULK_RADIUS_M", 50)),
		BulkMaxAccuracyM:      float64(EnvInt64("BULK_MAX_ACCURACY_M", 50)),
		BulkChunkSize:         int(EnvInt64("BULK_CHUNK_SIZE", 500)),
		BulkWorkerConcurrency: int(EnvInt64("BULK_WORKER_CONCURRENCY", 8)),
		BulkSendRatePerSec:    int(EnvInt64("BULK_SEND_RATE_PER_SEC", 20)),
		BulkMaxTargets:        int(EnvInt64("BULK_MAX_TARGETS", 100000)),
		BulkStage1Template:    env("BULK_STAGE1_TEMPLATE", ""), DBMaxConns: int(EnvInt64("DB_POOL_MAX", 25)),
		DBMinConns:                 int(EnvInt64("DB_POOL_MIN", 5)),
		WorkerInboundConcurrency:   int(EnvInt64("WORKER_INBOUND_CONCURRENCY", 10)),
		WorkerOutboxConcurrency:    int(EnvInt64("WORKER_OUTBOX_CONCURRENCY", 5)),
		WorkerWebhookConcurrency:   int(EnvInt64("WORKER_WEBHOOK_CONCURRENCY", 4)),
		WorkerDataConcurrency:      int(EnvInt64("WORKER_DATA_CONCURRENCY", 4)),
		WorkerReconcileConcurrency: int(EnvInt64("WORKER_RECONCILE_CONCURRENCY", 3)),
		WorkerMiscConcurrency:      int(EnvInt64("WORKER_MISC_CONCURRENCY", 3)),
		AIEnabled:                  envBool("AI_ENABLED", false),
		AIProvider:                 strings.ToLower(env("AI_PROVIDER", "openai")),
		AIAPIKey:                   os.Getenv("AI_API_KEY"),
		AIAIModel:                  env("AI_MODEL", ""),
		AITimeout:                  envDuration("AI_TIMEOUT", 30*time.Second),
		AIMaxRPM:                   int(EnvInt64("AI_MAX_REQUESTS_PER_MINUTE", 30)),
		VTPassBaseURL:              strings.TrimRight(env("VTPASS_BASE_URL", "https://sandbox.vtpass.com/api"), "/"),
		VTPassAPIKey:               os.Getenv("VTPASS_API_KEY"),
		VTPassPublicKey:            os.Getenv("VTPASS_PUBLIC_KEY"),
		VTPassSecretKey:            os.Getenv("VTPASS_SECRET_KEY"),
		VTPassWebhookSecret:        os.Getenv("VTPASS_WEBHOOK_SECRET"),
		VTPassTimeout:              envDuration("VTPASS_TIMEOUT", 45*time.Second),
		PaymentMinKobo:             EnvInt64("PAYMENT_MIN_KOBO", 10_000),
		PaymentMaxKobo:             EnvInt64("PAYMENT_MAX_KOBO", 1_000_000_000),
		RetentionPeriod:            envDuration("RETENTION_PERIOD", 90*24*time.Hour),
		// G6 (AML spec §5): hard-deleting AML-scope records younger than
		// five years violates the MLPPA record-keeping floor, so the
		// retention clamp refuses any configured period below it.
		AMLRecordRetention:         envDuration("AML_RECORD_RETENTION", 5*365*24*time.Hour),
		AuditChainVerifyInterval:   envDuration("AUDIT_CHAIN_VERIFY_INTERVAL", time.Hour),
		BackupBaseStaleAfter:       envDuration("BACKUP_BASE_STALE_AFTER", 26*time.Hour),
		WALShipStaleAfter:          envDuration("WAL_SHIP_STALE_AFTER", 45*time.Minute),
		RestoreDrillStaleAfter:     envDuration("RESTORE_DRILL_STALE_AFTER", 8*24*time.Hour),
		KYCRescreenPeriod:          envDuration("KYC_RESCREEN_PERIOD", 90*24*time.Hour),
		MonitorVelocityWindow:      envDuration("MONITOR_VELOCITY_WINDOW", 24*time.Hour),
		SyntheticOutboxMaxAge:      envDuration("SYNTHETIC_OUTBOX_MAX_AGE", 30*time.Minute),
		MonitorVelocityLimit:       int(EnvInt64("MONITOR_VELOCITY_LIMIT", 10)),
		MonitorStructuringWindow:   envDuration("MONITOR_STRUCTURING_WINDOW", 24*time.Hour),
		MonitorStructuringCount:    int(EnvInt64("MONITOR_STRUCTURING_COUNT", 3)),
		MonitorStructuringFloor:    EnvInt64("MONITOR_STRUCTURING_FLOOR_KOBO", 4_000_000),
		MonitorStructuringCeil:     EnvInt64("MONITOR_STRUCTURING_CEIL_KOBO", 10_000_000),
		MonitorRoundAmountStep:     EnvInt64("MONITOR_ROUND_AMOUNT_STEP_KOBO", 1_000_000),
		MonitorRoundAmountMin:      EnvInt64("MONITOR_ROUND_AMOUNT_MIN_KOBO", 1_000_000),
		ReportCTRThresholdKobo:     EnvInt64("REPORT_CTR_THRESHOLD_KOBO", 1_000_000_000),
		FraudWindow:                envDuration("FRAUD_WINDOW", 24*time.Hour),
		FraudFailedAttemptLimit:    int(EnvInt64("FRAUD_FAILED_ATTEMPT_LIMIT", 5)),
		FraudPaymentVelocityLimit:  int(EnvInt64("FRAUD_PAYMENT_VELOCITY_LIMIT", 10)),
		FraudDisputeVelocityLimit:  int(EnvInt64("FRAUD_DISPUTE_VELOCITY_LIMIT", 3)),
		FraudRefundVelocityLimit:   int(EnvInt64("FRAUD_REFUND_VELOCITY_LIMIT", 5)),
		FraudPayoutVelocityLimit:   int(EnvInt64("FRAUD_PAYOUT_VELOCITY_LIMIT", 5)),
		FraudNewDestinationWindow:  envDuration("FRAUD_NEW_DESTINATION_WINDOW", 24*time.Hour),
		FraudNewDestinationMinKobo: EnvInt64("FRAUD_NEW_DESTINATION_MIN_KOBO", 100_000),
		StepUpEnabled:              envBool("STEP_UP_ENABLED", false),
		StepUpTTL:                  envDuration("STEP_UP_TTL", 10*time.Minute),
		StepUpBand:                 env("STEP_UP_BAND", "high"),
		// M3 Trust Fabric step-up: a high-risk-band profile has its
		// single-transaction allowance capped at this ceiling until the
		// review clears. Zero disables the step-up.
		RiskHighSingleLimitKobo:                     EnvInt64("RISK_HIGH_SINGLE_LIMIT_KOBO", 50_000),
		TuningFPPercent:                             envFloat("TUNING_FP_PERCENT", 60),
		TuningMinLabelled:                           EnvInt64("TUNING_MIN_LABELLED", 10),
		RiskDecayHalfLife:                           envDuration("RISK_DECAY_HALF_LIFE", 90*24*time.Hour),
		RiskGeographyWeight:                         envFloat("RISK_GEOGRAPHY_WEIGHT", 15),
		SessionTTL:                                  envDuration("CONVERSATION_TTL", 30*time.Minute),
		ReceiptTTL:                                  envDuration("RECEIPT_TTL", 90*24*time.Hour),
		OnboardingReminderInterval:                  envDuration("ONBOARDING_REMINDER_INTERVAL", 6*time.Hour),
		OnboardingReminderStartAfter:                envDuration("ONBOARDING_REMINDER_START_AFTER", 24*time.Hour),
		OnboardingReminderStallAfter:                envDuration("ONBOARDING_REMINDER_STALL_AFTER", 12*time.Hour),
		OnboardingReminderGap:                       envDuration("ONBOARDING_REMINDER_GAP", 48*time.Hour),
		OnboardingReminderMax:                       int(EnvInt64("ONBOARDING_REMINDER_MAX", 3)),
		OnboardingReminderQuietStart:                int(EnvInt64("ONBOARDING_REMINDER_QUIET_START", 20)),
		OnboardingReminderQuietEnd:                  int(EnvInt64("ONBOARDING_REMINDER_QUIET_END", 8)),
		OnboardingReminderTemplate:                  strings.TrimSpace(os.Getenv("ONBOARDING_REMINDER_TEMPLATE")),
		OnboardingReminderTemplateLoc:               env("ONBOARDING_REMINDER_TEMPLATE_LOCALE", "en"),
		OnboardingReminderDeliveryGuardWindow:       envDuration("ONBOARDING_REMINDER_DELIVERY_GUARD_WINDOW", time.Hour),
		OnboardingReminderDeliveryGuardFailureRatio: envFloat("ONBOARDING_REMINDER_DELIVERY_GUARD_FAILURE_RATIO", 0.5),
		WebFlowsEnabled:                             envBool("WEB_FLOWS_ENABLED", true),
		MessageLogEnabled:                           envBool("MESSAGE_LOG_ENABLED", true),
		MessageCostServiceNGN:                       EnvInt64("MESSAGE_COST_SERVICE_NGN", 14),
		MessageCostMarketingNGN:                     EnvInt64("MESSAGE_COST_MARKETING_NGN", 84),
		ScreeningCostNGN:                            EnvInt64("SCREENING_COST_NGN", 0),
		AITokensPerNGN:                              EnvInt64("AI_TOKENS_PER_NGN", 250),
		RedisURL:                                    strings.TrimSpace(os.Getenv("REDIS_URL")),
		EventBus:                                    strings.ToLower(env("EVENT_BUS", "memory")),
		EventBusPartitions:                          int(EnvInt64("EVENT_BUS_PARTITIONS", 4)),
		KafkaGroupID:                                env("KAFKA_GROUP_ID", "xego"),
		RateLimitWebhooksPerMinute:                  int(EnvInt64("RATE_LIMIT_WEBHOOKS_PER_MINUTE", 120)),
		RateLimitPublicPerMinute:                    int(EnvInt64("RATE_LIMIT_PUBLIC_PER_MINUTE", 60)),
		RateLimitScanPerMinute:                      int(EnvInt64("RATE_LIMIT_SCAN_PER_MINUTE", 30)),
		RateLimitAPIKeysPerMinute:                   int(EnvInt64("RATE_LIMIT_API_KEYS_PER_MINUTE", 300)),

		SettlementFeeBps:           int(EnvInt64("SETTLEMENT_FEE_BPS", 250)),
		PaymentScreeningProvider:   strings.ToLower(env("PAYMENT_SCREENING_PROVIDER", "simulated")),
		LivenessProvider:           strings.ToLower(env("LIVENESS_PROVIDER", "simulated")),
		AdverseMediaProvider:       strings.ToLower(env("ADVERSE_MEDIA_PROVIDER", "simulated")),
		AdverseMediaAPIBase:        strings.TrimRight(os.Getenv("ADVERSE_MEDIA_API_BASE"), "/"),
		AdverseMediaAppID:          strings.TrimSpace(os.Getenv("ADVERSE_MEDIA_APP_ID")),
		AdverseMediaSecretKey:      strings.TrimSpace(os.Getenv("ADVERSE_MEDIA_SECRET_KEY")),
		AdverseMediaTimeout:        envDuration("ADVERSE_MEDIA_TIMEOUT", 30*time.Second),
		AdverseMediaMatchThreshold: envFloat("ADVERSE_MEDIA_MATCH_THRESHOLD", 0.85),

		PayoutMinKobo:         EnvInt64("PAYOUT_MIN_KOBO", 100_00),
		PayoutMaxKobo:         EnvInt64("PAYOUT_MAX_KOBO", 10_000_00),
		PayoutDailyCapKobo:    EnvInt64("PAYOUT_DAILY_CAP_KOBO", 50_000_00),
		PayoutDailyCountLimit: int(EnvInt64("PAYOUT_DAILY_COUNT_LIMIT", 10)),

		AgentNetworkEnabled:             envBool("AGENT_NETWORK_ENABLED", true),
		RemittanceTTL:                   envDuration("REMITTANCE_TTL", 72*time.Hour),
		RemittanceOTPTTL:                envDuration("REMITTANCE_OTP_TTL", 10*time.Minute),
		AgentIDRequired:                 envBool("AGENT_ID_REQUIRED", true),
		AgentDefaultTier:                env("AGENT_DEFAULT_TIER", "standard"),
		AgentIDHashPepper:               strings.TrimSpace(os.Getenv("AGENT_ID_HASH_PEPPER")),
		AgentStandardPerPayoutKobo:      EnvInt64("AGENT_STANDARD_PER_PAYOUT_KOBO", 2_000_000),
		AgentStandardDailyKobo:          EnvInt64("AGENT_STANDARD_DAILY_KOBO", 20_000_000),
		AgentStandardMaxOutstandingKobo: EnvInt64("AGENT_STANDARD_MAX_OUTSTANDING_KOBO", 50_000_000),
		AgentStandardCommissionBPS:      int(EnvInt64("AGENT_STANDARD_COMMISSION_BPS", 50)),
		AgentSuperPerPayoutKobo:         EnvInt64("AGENT_SUPER_PER_PAYOUT_KOBO", 5_000_000),
		AgentSuperDailyKobo:             EnvInt64("AGENT_SUPER_DAILY_KOBO", 60_000_000),
		AgentSuperMaxOutstandingKobo:    EnvInt64("AGENT_SUPER_MAX_OUTSTANDING_KOBO", 150_000_000),
		AgentSuperCommissionBPS:         int(EnvInt64("AGENT_SUPER_COMMISSION_BPS", 40)),
		AgentMaxPerPayoutKobo:           EnvInt64("AGENT_MAX_PER_PAYOUT_KOBO", 10_000_000),
		AgentMaxDailyKobo:               EnvInt64("AGENT_MAX_DAILY_KOBO", 120_000_000),
		AgentMaxMaxOutstandingKobo:      EnvInt64("AGENT_MAX_MAX_OUTSTANDING_KOBO", 250_000_000),
		AgentMaxCommissionBPS:           int(EnvInt64("AGENT_MAX_COMMISSION_BPS", 30)),

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
	if raw := strings.TrimSpace(os.Getenv("WEB_FLOWS_DISABLED_FLOWS")); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.WebFlowsDisabledFlows = append(cfg.WebFlowsDisabledFlows, s)
			}
		}
	}
	if cfg.EventBus != "memory" && cfg.EventBus != "kafka" {
		return Config{}, fmt.Errorf("EVENT_BUS must be \"memory\" or \"kafka\", got %q", cfg.EventBus)
	}
	if cfg.EventBus == "kafka" && len(cfg.KafkaBrokers) == 0 {
		return Config{}, errors.New("KAFKA_BROKERS is required when EVENT_BUS=kafka")
	}
	switch cfg.InterswitchCheckoutRender {
	case "", "hosted_fields", "legacy":
	default:
		return Config{}, fmt.Errorf("INTERSWITCH_CHECKOUT_RENDER must be \"hosted_fields\" or \"legacy\", got %q", cfg.InterswitchCheckoutRender)
	}
	switch cfg.InterswitchCheckoutMode {
	case "", "TEST", "LIVE":
	default:
		return Config{}, fmt.Errorf("INTERSWITCH_CHECKOUT_MODE must be \"TEST\" or \"LIVE\", got %q", cfg.InterswitchCheckoutMode)
	}
	switch cfg.ScreeningProvider {
	case "", "http", "dojah":
	default:
		return Config{}, fmt.Errorf("SCREENING_PROVIDER must be \"http\" or \"dojah\", got %q", cfg.ScreeningProvider)
	}
	switch cfg.PaymentScreeningProvider {
	case "", "simulated", "dojah":
	default:
		return Config{}, fmt.Errorf("PAYMENT_SCREENING_PROVIDER must be \"simulated\" or \"dojah\" (the live screening-vendor adapter lands with the AML_VENDOR_DECISION_RECORD.md §11 Dojah contract), got %q", cfg.PaymentScreeningProvider)
	}
	switch cfg.LivenessProvider {
	case "", "simulated", "dojah":
	default:
		return Config{}, fmt.Errorf("LIVENESS_PROVIDER must be \"simulated\" or \"dojah\", got %q", cfg.LivenessProvider)
	}
	if cfg.OnboardingReminderMax < 1 || cfg.OnboardingReminderMax > 10 {
		return Config{}, fmt.Errorf("ONBOARDING_REMINDER_MAX must be between 1 and 10, got %d", cfg.OnboardingReminderMax)
	}
	for name, hour := range map[string]int{
		"ONBOARDING_REMINDER_QUIET_START": cfg.OnboardingReminderQuietStart,
		"ONBOARDING_REMINDER_QUIET_END":   cfg.OnboardingReminderQuietEnd,
	} {
		if hour < 0 || hour > 23 {
			return Config{}, fmt.Errorf("%s must be an hour 0-23, got %d", name, hour)
		}
	}
	if cfg.OnboardingReminderDeliveryGuardFailureRatio < 0 || cfg.OnboardingReminderDeliveryGuardFailureRatio > 1 {
		return Config{}, fmt.Errorf("ONBOARDING_REMINDER_DELIVERY_GUARD_FAILURE_RATIO must be between 0 and 1, got %g", cfg.OnboardingReminderDeliveryGuardFailureRatio)
	}
	switch cfg.DataProvider {
	case "", "vtpass", "interswitch":
	default:
		return Config{}, fmt.Errorf("DATA_PROVIDER must be \"vtpass\" or \"interswitch\", got %q", cfg.DataProvider)
	}
	switch cfg.IdentityProvider {
	case "", "ninbvnportal":
	default:
		return Config{}, fmt.Errorf("IDENTITY_PROVIDER must be \"ninbvnportal\", got %q", cfg.IdentityProvider)
	}
	switch cfg.CorporateRegistryProvider {
	case "", "simulated", "http":
	default:
		return Config{}, fmt.Errorf("CAC_PROVIDER must be \"simulated\" or \"http\", got %q", cfg.CorporateRegistryProvider)
	}
	switch cfg.TaxIDProvider {
	case "", "simulated", "http":
	default:
		return Config{}, fmt.Errorf("FIRS_PROVIDER must be \"simulated\" or \"http\", got %q", cfg.TaxIDProvider)
	}
	switch cfg.LocationProvider {
	case "", "simulated", "http":
	default:
		return Config{}, fmt.Errorf("LOCATION_PROVIDER must be \"simulated\" or \"http\", got %q", cfg.LocationProvider)
	}
	switch cfg.DocAuthenticityProvider {
	case "", "simulated", "http", "dojah":
	default:
		return Config{}, fmt.Errorf("DOC_AUTH_PROVIDER must be \"simulated\", \"http\" or \"dojah\", got %q", cfg.DocAuthenticityProvider)
	}
	switch cfg.SMSProvider {
	case "", "webhook", "http":
	default:
		return Config{}, fmt.Errorf("SMS_PROVIDER must be \"webhook\" or \"http\", got %q", cfg.SMSProvider)
	}
	if strings.EqualFold(cfg.SMSProvider, "http") && cfg.SMSAPIBase == "" {
		return Config{}, errors.New("SMS_API_BASE is required when SMS_PROVIDER=http")
	}
	switch cfg.AIProvider {
	case "", "openai":
	default:
		return Config{}, fmt.Errorf("AI_PROVIDER must be \"openai\", got %q", cfg.AIProvider)
	}
	if cfg.AIMaxRPM < 0 {
		return Config{}, errors.New("AI_MAX_REQUESTS_PER_MINUTE cannot be negative")
	}
	switch cfg.BankTransferMode {
	case "", "interswitch":
	default:
		return Config{}, fmt.Errorf("BANK_TRANSFER_MODE must be \"interswitch\", got %q", cfg.BankTransferMode)
	}
	switch cfg.PayoutProvider {
	case "", "interswitch":
	default:
		return Config{}, fmt.Errorf("PAYOUT_PROVIDER must be \"interswitch\", got %q", cfg.PayoutProvider)
	}
	switch cfg.RefundProvider {
	case "", "interswitch":
	default:
		return Config{}, fmt.Errorf("REFUND_PROVIDER must be \"interswitch\", got %q", cfg.RefundProvider)
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
	if cfg.AITokensPerNGN < 0 {
		return Config{}, fmt.Errorf("AI_TOKENS_PER_NGN must not be negative")
	}
	if cfg.RiskDecayHalfLife < 0 {
		return Config{}, fmt.Errorf("RISK_DECAY_HALF_LIFE must not be negative (0 disables decay)")
	}
	if cfg.RiskGeographyWeight < 0 {
		return Config{}, fmt.Errorf("RISK_GEOGRAPHY_WEIGHT must not be negative (0 keeps the shipped default)")
	}
	if cfg.AMLRecordRetention < 0 {
		return Config{}, fmt.Errorf("AML_RECORD_RETENTION must not be negative (0 disables the AML floor)")
	}
	if cfg.AMLRecordRetention != 0 && cfg.AMLRecordRetention < 5*365*24*time.Hour {
		return Config{}, fmt.Errorf("AML_RECORD_RETENTION must be at least 5 years (MLPPA 2022 record-keeping floor); got %s", cfg.AMLRecordRetention)
	}
	if cfg.AMLRecordRetention != 0 && cfg.RetentionPeriod > cfg.AMLRecordRetention {
		return Config{}, fmt.Errorf("RETENTION_PERIOD (%s) cannot exceed AML_RECORD_RETENTION (%s)", cfg.RetentionPeriod, cfg.AMLRecordRetention)
	}
	if err := cfg.validateAgentNetwork(); err != nil {
		return Config{}, err
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
		// Require keys for configured payment providers.
		hasAnyProvider := false
		if strings.EqualFold(cfg.PaymentProvider, "interswitch") || cfg.InterswitchClientSecret != "" {
			if cfg.InterswitchClientID == "" {
				return Config{}, errors.New("INTERSWITCH_CLIENT_ID is required in production when interswitch is enabled")
			}
			if cfg.InterswitchMerchantCode == "" {
				return Config{}, errors.New("INTERSWITCH_MERCHANT_CODE is required in production when interswitch is enabled")
			}
			if cfg.InterswitchWebhookSecret == "" {
				return Config{}, errors.New("INTERSWITCH_WEBHOOK_SECRET is required in production when interswitch is enabled")
			}
			hasAnyProvider = true
		}
		if !hasAnyProvider {
			return Config{}, errors.New("at least one payment provider (INTERSWITCH_CLIENT_ID) is required in production")
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
		if cfg.InstagramEnabled {
			for name, value := range map[string]string{
				"INSTAGRAM_APP_SECRET":   cfg.InstagramAppSecret,
				"INSTAGRAM_ACCESS_TOKEN": cfg.InstagramAccessToken,
				"INSTAGRAM_IG_ID":        cfg.InstagramIGID,
				"INSTAGRAM_VERIFY_TOKEN": cfg.InstagramVerifyToken,
			} {
				if value == "" {
					return Config{}, fmt.Errorf("%s is required when INSTAGRAM_ENABLED=true", name)
				}
			}
		}
		if cfg.TikTokEnabled {
			for name, value := range map[string]string{
				"TIKTOK_APP_SECRET":   cfg.TikTokAppSecret,
				"TIKTOK_ACCESS_TOKEN": cfg.TikTokAccessToken,
			} {
				if value == "" {
					return Config{}, fmt.Errorf("%s is required when TIKTOK_ENABLED=true", name)
				}
			}
		}
		if cfg.EmailDemoCodeInChat {
			return Config{}, errors.New("EMAIL_DEMO_CODE_IN_CHAT is not allowed in production")
		}
		if cfg.LinkDemoCodeInChat {
			return Config{}, errors.New("LINK_DEMO_CODE_IN_CHAT is not allowed in production")
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
		if cfg.AIEnabled {
			if cfg.AIMaxRPM <= 0 {
				return Config{}, errors.New("AI_MAX_REQUESTS_PER_MINUTE must be > 0 in production when AI_ENABLED=true")
			}
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
		if err := validateProductionRails(cfg); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// validateProductionRails requires every money and identity rail in production
// to be a live, fully-credentialed integration. It is only valid for the real
// rails supported by the platform and is always enforced in APP_ENV=production.
func validateProductionRails(cfg Config) error {
	if strings.EqualFold(cfg.PaymentProvider, "interswitch") && cfg.InterswitchCheckoutMode != "LIVE" {
		return errors.New("INTERSWITCH_CHECKOUT_MODE=LIVE is required in production")
	}
	if cfg.IdentityProvider == "ninbvnportal" && cfg.NINBVNPortalKey == "" {
		return errors.New("NINBVNPORTAL_API_KEY is required when IDENTITY_PROVIDER=ninbvnportal")
	}
	if cfg.ScreeningProvider == "http" && cfg.ScreeningAPIBase == "" {
		return errors.New("SCREENING_API_BASE is required when SCREENING_PROVIDER=http")
	}
	if cfg.CorporateRegistryProvider == "http" && cfg.CorporateRegistryAPIBase == "" {
		return errors.New("CAC_API_BASE is required when CAC_PROVIDER=http")
	}
	if cfg.TaxIDProvider == "http" && cfg.TaxIDAPIBase == "" {
		return errors.New("FIRS_API_BASE is required when FIRS_PROVIDER=http")
	}
	if cfg.LocationProvider == "http" && cfg.LocationAPIBase == "" {
		return errors.New("LOCATION_API_BASE is required when LOCATION_PROVIDER=http")
	}
	if cfg.DocAuthenticityProvider == "http" && cfg.DocAuthenticityAPIBase == "" {
		return errors.New("DOC_AUTH_API_BASE is required when DOC_AUTH_PROVIDER=http")
	}
	if cfg.UsesDojah() {
		if cfg.DojahAppID == "" {
			return errors.New("DOJAH_APP_ID is required when a *_PROVIDER is set to \"dojah\"")
		}
		if cfg.DojahSecretKey == "" {
			return errors.New("DOJAH_SECRET_KEY is required when a *_PROVIDER is set to \"dojah\"")
		}
		if cfg.DojahBaseURL == "" {
			return errors.New("DOJAH_BASE_URL must not be empty (defaults to https://api.dojah.io)")
		}
	}
	if cfg.DojahMatchThreshold < 0 || cfg.DojahMatchThreshold > 1 {
		return fmt.Errorf("DOJAH_MATCH_THRESHOLD must be between 0 and 1, got %g", cfg.DojahMatchThreshold)
	}
	if strings.EqualFold(cfg.PayoutProvider, "interswitch") || strings.EqualFold(cfg.RefundProvider, "interswitch") {
		if cfg.InterswitchTransferBaseURL == "" {
			return errors.New("INTERSWITCH_TRANSFER_BASE_URL is required when payout or refund uses interswitch in production")
		}
		if cfg.InterswitchSourceAccount == "" {
			return errors.New("INTERSWITCH_SOURCE_ACCOUNT is required when payout or refund uses interswitch in production")
		}
	}
	return nil
}

// UsesDojah reports whether any provider lane selects the Dojah adapter, so the
// shared DOJAH_* credentials are required.
func (c Config) UsesDojah() bool {
	for _, provider := range []string{
		c.ScreeningProvider,
		c.PaymentScreeningProvider,
		c.LivenessProvider,
		c.DocAuthenticityProvider,
	} {
		if provider == "dojah" {
			return true
		}
	}
	return false
}

// validateAgentNetwork bounds the POS agent knobs. The pickup and OTP windows
// must be positive, the default tier must be one of the three, and no tier's
// per-payout or daily cap may exceed the CBN ceilings (POS_AGENT_NETWORK_PLAN.md
// §12) — an operator can widen a specific agent's limits, but it is a
// configuration error for the shipped tier defaults to advertise a cap the
// regulator does not allow.
func (c Config) validateAgentNetwork() error {
	nonNegative := map[string]int64{
		"AGENT_STANDARD_PER_PAYOUT_KOBO":      c.AgentStandardPerPayoutKobo,
		"AGENT_STANDARD_DAILY_KOBO":           c.AgentStandardDailyKobo,
		"AGENT_STANDARD_MAX_OUTSTANDING_KOBO": c.AgentStandardMaxOutstandingKobo,
		"AGENT_STANDARD_COMMISSION_BPS":       int64(c.AgentStandardCommissionBPS),
		"AGENT_SUPER_PER_PAYOUT_KOBO":         c.AgentSuperPerPayoutKobo,
		"AGENT_SUPER_DAILY_KOBO":              c.AgentSuperDailyKobo,
		"AGENT_SUPER_MAX_OUTSTANDING_KOBO":    c.AgentSuperMaxOutstandingKobo,
		"AGENT_SUPER_COMMISSION_BPS":          int64(c.AgentSuperCommissionBPS),
		"AGENT_MAX_PER_PAYOUT_KOBO":           c.AgentMaxPerPayoutKobo,
		"AGENT_MAX_DAILY_KOBO":                c.AgentMaxDailyKobo,
		"AGENT_MAX_MAX_OUTSTANDING_KOBO":      c.AgentMaxMaxOutstandingKobo,
		"AGENT_MAX_COMMISSION_BPS":            int64(c.AgentMaxCommissionBPS),
	}
	for name, value := range nonNegative {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	if c.RemittanceTTL <= 0 {
		return errors.New("REMITTANCE_TTL must be positive")
	}
	if c.RemittanceOTPTTL <= 0 {
		return errors.New("REMITTANCE_OTP_TTL must be positive")
	}
	tiers := []struct {
		name             string
		perPayout, daily int64
	}{
		{"STANDARD", c.AgentStandardPerPayoutKobo, c.AgentStandardDailyKobo},
		{"SUPER", c.AgentSuperPerPayoutKobo, c.AgentSuperDailyKobo},
		{"MAX", c.AgentMaxPerPayoutKobo, c.AgentMaxDailyKobo},
	}
	for _, t := range tiers {
		if t.perPayout > CBNMaxCustomerDailyCashOutKobo {
			return fmt.Errorf("AGENT_%s_PER_PAYOUT_KOBO exceeds the CBN ₦100,000 per-customer daily ceiling", t.name)
		}
		if t.daily > CBNMaxAgentDailyCashOutKobo {
			return fmt.Errorf("AGENT_%s_DAILY_KOBO exceeds the CBN ₦1,200,000 agent daily ceiling", t.name)
		}
		if t.daily > 0 && t.perPayout > t.daily {
			return fmt.Errorf("AGENT_%s_PER_PAYOUT_KOBO cannot exceed its daily cap", t.name)
		}
	}
	switch c.AgentDefaultTier {
	case "standard", "super", "max":
	default:
		return fmt.Errorf("AGENT_DEFAULT_TIER must be \"standard\", \"super\" or \"max\", got %q", c.AgentDefaultTier)
	}
	return nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// EnvInt64 reads an integer env var with a fallback; exported so test
// harnesses that build Config literally honor the same env knobs.
// envFloat reads a float env var with a fallback; empty or malformed values
// fall back silently (config template truthfulness guards the documented ones).
func envFloat(name string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return v
}

func EnvInt64(name string, fallback int64) int64 {
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
