// Package app assembles the HTTP server, background workers, and CLI commands.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	kafkabus "whatsapp-payment-demo/internal/bus/kafka"
	memorybus "whatsapp-payment-demo/internal/bus/memory"
	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/logging"
	"whatsapp-payment-demo/internal/ports"
	aiprovider "whatsapp-payment-demo/internal/providers/ai"
	dataprovider "whatsapp-payment-demo/internal/providers/data"
	emailprovider "whatsapp-payment-demo/internal/providers/email"
	identityprovider "whatsapp-payment-demo/internal/providers/identity"
	interswitchprovider "whatsapp-payment-demo/internal/providers/interswitch"
	screeningprovider "whatsapp-payment-demo/internal/providers/screening"
	"whatsapp-payment-demo/internal/providers/telegram"
	"whatsapp-payment-demo/internal/providers/vtpass"
	"whatsapp-payment-demo/internal/providers/whatsapp"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

const adminCookieName = "wpd_admin"
const merchantCookieName = "wpd_merchant"

// App is the fully assembled payment demo.
type App struct {
	cfg               config.Config
	logger            *slog.Logger
	store             *store.Store
	interswitch       *interswitchprovider.Client
	telegram          *telegram.Client
	whatsapp          *whatsapp.Client
	payments          *service.PaymentService
	data              *service.DataService
	conversation      *service.ConversationService
	templates         *template.Template
	limiter           *loginLimiter
	rateLimiter       ratelimit.Limiter
	rateClose         func() error
	totpKey           []byte
	sanctionsScreener ports.SanctionsScreener
	eventBus          ports.EventBus
	publisher         *service.EventPublisher
	merchantWebhooks  *service.MerchantWebhookDeliverer
	settlements       *service.SettlementService
	refunds           *service.RefundService
	disputes          *service.DisputeService
	workerWg          sync.WaitGroup

	// AI providers (nil when AI_ENABLED=false).
	imageReader  ports.ImageReader
	speechToText ports.SpeechToText
	chatAI       ports.ChatAI
}

// New creates all application dependencies.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	logger = slog.New(logging.WithRequestID(logger.Handler()))
	repository, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	repository.SetDataKey(cfg.DataEncryptionKey)
	// Interswitch Web Checkout is the sole card payment gateway.
	interswitchClient := interswitchprovider.New(interswitchprovider.Options{
		ClientID:        cfg.InterswitchClientID,
		ClientSecret:    cfg.InterswitchClientSecret,
		WebhookSecret:   cfg.InterswitchWebhookSecret,
		MerchantCode:    cfg.InterswitchMerchantCode,
		PayItemID:       cfg.InterswitchPayItemID,
		BaseURL:         cfg.InterswitchBaseURL,
		CheckoutBaseURL: cfg.InterswitchCheckoutBaseURL,
		Mode:            cfg.InterswitchCheckoutMode,
	})
	// Build the payment gateway registry. Interswitch is always registered so
	// card checkout resolves even in the backlog demo where no secret is set.
	// Interswitch is registered for both the card checkout rail and the
	// bank-transfer rail: bank-transfer payments are created as Interswitch
	// gateway transactions and verified through the same requery path.
	gateways := map[string]ports.PaymentGateway{
		service.ProviderInterswitch:  interswitchClient,
		service.ProviderBankTransfer: interswitchClient,
	}
	router := service.NewProviderRouter(gateways, logger)
	whatsappClient := whatsapp.New(cfg.WhatsAppAppSecret, cfg.WhatsAppAccessToken, cfg.WhatsAppPhoneNumberID, cfg.WhatsAppGraphVersion, cfg.WhatsAppTemplateLocale)
	var telegramClient *telegram.Client
	messengers := map[string]ports.Messenger{service.ChannelWhatsApp: whatsappClient}
	if cfg.TelegramEnabled {
		telegramClient = telegram.New(cfg.TelegramBotToken, cfg.TelegramAPIBase, cfg.TelegramWebhookSecret)
		messengers[service.ChannelTelegram] = telegramClient
	}
	paymentService := service.NewPaymentService(cfg, repository, gateways, router, logger)
	var dataProvider ports.DataProvider = dataprovider.NewSimulator()
	if strings.EqualFold(cfg.DataProvider, "vtpass") {
		dataProvider = vtpass.NewWithTimeout(cfg.VTPassBaseURL, cfg.VTPassAPIKey, cfg.VTPassPublicKey, cfg.VTPassSecretKey, cfg.VTPassTimeout)
	}
	dataService := service.NewDataService(repository, paymentService, dataProvider)
	var identityVerifier ports.IdentityVerifier = identityprovider.NewSimulator()
	identityProviderName := "simulated"
	switch strings.ToLower(cfg.IdentityProvider) {
	case "simulated", "":
		// default simulator
	case "ninbvnportal":
		if cfg.NINBVNPortalKey == "" {
			repository.Close()
			return nil, errors.New("NINBVNPORTAL_API_KEY is required when IDENTITY_PROVIDER=ninbvnportal")
		}
		p := identityprovider.NewNINBVNPortal(cfg.NINBVNPortalURL, cfg.NINBVNPortalKey, cfg.NINBVNPortalTimeout)
		identityVerifier = p
		identityProviderName = p.ProviderName()
	default:
		repository.Close()
		return nil, fmt.Errorf("unsupported IDENTITY_PROVIDER %q", cfg.IdentityProvider)
	}
	var sanctionsScreener ports.SanctionsScreener = screeningprovider.NewSimulator()
	switch strings.ToLower(cfg.ScreeningProvider) {
	case "simulated", "":
	default:
		repository.Close()
		return nil, fmt.Errorf("unsupported SCREENING_PROVIDER %q", cfg.ScreeningProvider)
	}
	var emailSender ports.EmailSender
	if cfg.SMTPHost != "" {
		emailSender = emailprovider.NewSMTP(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPFrom)
	}
	// AI providers: used for image-to-text, speech-to-text, and chat.
	var imageReader ports.ImageReader
	var speechToText ports.SpeechToText
	var chatAI ports.ChatAI
	if cfg.AIEnabled {
		switch strings.ToLower(cfg.AIProvider) {
		case "openai":
			if cfg.AIAPIKey == "" {
				repository.Close()
				return nil, errors.New("AI_API_KEY is required when AI_PROVIDER=openai")
			}
			openai := aiprovider.NewOpenAI(cfg.AIAPIKey, "", cfg.AIAIModel, "", cfg.AITimeout)
			imageReader = openai
			speechToText = openai
			chatAI = openai
		case "simulated", "":
			sim := aiprovider.NewSimulated()
			imageReader = sim
			speechToText = sim
			chatAI = sim
		default:
			repository.Close()
			return nil, fmt.Errorf("unsupported AI_PROVIDER %q", cfg.AIProvider)
		}
	}
	templates, err := template.New("").Funcs(template.FuncMap{
		"money":       domain.FormatNGN,
		"maskPII":     maskPII,
		"statusClass": func(status any) string { return strings.ReplaceAll(fmt.Sprint(status), "_", "-") },
		"percent":     func(value float64) string { return fmt.Sprintf("%.1f%%", value) },
		"sub":         func(a, b int64) int64 { return a - b },
		"add":         func(a, b int64) int64 { return a + b },
		"inc":         func(i int) int { return i + 1 },
		"collectionFeeKobo": func(p store.PaymentView) int64 {
			var meta struct {
				CollectionFeeKobo int64 `json:"collection_fee_kobo"`
			}
			if len(p.Metadata) > 0 {
				_ = json.Unmarshal(p.Metadata, &meta)
			}
			return meta.CollectionFeeKobo
		},
		"join": func(items []string, sep string) string { return strings.Join(items, sep) },
		"date": func(t any) string {
			switch v := t.(type) {
			case time.Time:
				return v.Local().Format("Jan 2, 2006 3:04 PM")
			case *time.Time:
				if v == nil {
					return ""
				}
				return v.Local().Format("Jan 2, 2006 3:04 PM")
			}
			return ""
		},
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		repository.Close()
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	var totpKey []byte
	if cfg.TOTPEnabled {
		totpKey, err = hex.DecodeString(cfg.TOTPEncryptionKey)
		if err != nil {
			repository.Close()
			return nil, fmt.Errorf("decode TOTP_ENCRYPTION_KEY: %w", err)
		}
	}
	var rateLimiter ratelimit.Limiter = ratelimit.NewMemory()
	var rateClose func() error
	if cfg.RedisURL != "" {
		redisLimiter, err := ratelimit.OpenRedis(ctx, cfg.RedisURL, "xego:rl", logger)
		if err != nil {
			logger.Warn("redis unavailable; falling back to in-memory rate limiting", "error", err)
		} else {
			rateLimiter = redisLimiter
			rateClose = redisLimiter.Close
		}
	}
	// Phase 3: the event backbone. "kafka" requires a broker; "memory" (the
	// default) is the Kafka-compatible in-memory bus used by the demo and tests.
	var eventBus ports.EventBus
	switch strings.ToLower(cfg.EventBus) {
	case "kafka":
		kafkaBus, err := kafkabus.New(cfg.KafkaBrokers, cfg.KafkaGroupID, logger)
		if err != nil {
			repository.Close()
			return nil, fmt.Errorf("open kafka event bus: %w", err)
		}
		eventBus = kafkaBus
	case "memory", "":
		eventBus = memorybus.New(cfg.EventBusPartitions, logger)
	}
	convo := service.NewConversationService(cfg, repository, paymentService, dataService, messengers, emailSender, identityVerifier, sanctionsScreener, identityProviderName)
	convo.SetMediaProviders(imageReader, speechToText, chatAI)
	return &App{
		cfg: cfg, logger: logger, store: repository, interswitch: interswitchClient,
		telegram: telegramClient, whatsapp: whatsappClient, payments: paymentService,
		data:         dataService,
		conversation: convo,
		templates:    templates, limiter: newLoginLimiter(), totpKey: totpKey,
		rateLimiter: rateLimiter, rateClose: rateClose, sanctionsScreener: sanctionsScreener,
		eventBus: eventBus, publisher: service.NewEventPublisher(repository, eventBus, logger),
		merchantWebhooks: service.NewMerchantWebhookDeliverer(repository, nil, logger),
		settlements:      service.NewSettlementService(repository, nil, logger, cfg.SettlementFeeBps, service.WithPayoutLimits(cfg.PayoutMinKobo, cfg.PayoutMaxKobo, cfg.PayoutDailyCapKobo, cfg.PayoutDailyCountLimit)),
		refunds:          service.NewRefundService(repository, nil, logger),
		disputes:         service.NewDisputeService(repository),
		imageReader:      imageReader,
		speechToText:     speechToText,
		chatAI:           chatAI,
	}, nil
}

// Close releases persistent resources.
func (a *App) Close() {
	if a.rateClose != nil {
		_ = a.rateClose()
	}
	if a.eventBus != nil {
		_ = a.eventBus.Close()
	}
	a.store.Close()
}

// Migrate applies the embedded PostgreSQL schema and ensures the bootstrap
// admin account exists.
func (a *App) Migrate(ctx context.Context) error {
	if err := a.store.Migrate(ctx); err != nil {
		return err
	}
	// After migration 024 moves payload columns to text, re-encrypt any rows
	// that predate DATA_ENCRYPTION_KEY so nothing stays plaintext at rest.
	if err := a.encryptLegacyAtRest(ctx); err != nil {
		return err
	}
	if a.cfg.AdminPasswordHash != "" {
		return a.store.EnsureAdminUser(ctx, a.cfg.AdminEmail, a.cfg.AdminPasswordHash)
	}
	return nil
}

// encryptLegacyAtRest seals plaintext chat payload rows and logs the count.
func (a *App) encryptLegacyAtRest(ctx context.Context) error {
	encrypted, err := a.store.EncryptLegacyAtRest(ctx)
	if err != nil {
		return fmt.Errorf("encrypt legacy rows: %w", err)
	}
	if encrypted > 0 {
		a.logger.InfoContext(ctx, "encrypted legacy rows at rest", "count", encrypted)
	}
	return nil
}

// Health checks whether the application can reach its database.
func (a *App) Health(ctx context.Context) error {
	return a.store.Ping(ctx)
}

// RunServer starts HTTP handling and bounded background workers.
func (a *App) RunServer(ctx context.Context) error {
	if a.cfg.AdminPasswordHash != "" {
		if err := a.store.EnsureAdminUser(ctx, a.cfg.AdminEmail, a.cfg.AdminPasswordHash); err != nil {
			a.logger.WarnContext(ctx, "bootstrap admin sync skipped (run migrate first)", "error", err)
		}
	}
	if err := a.encryptLegacyAtRest(ctx); err != nil {
		return fmt.Errorf("startup: %w", err)
	}
	server := a.newHTTPServer(a.routes())
	go a.runWorkers(ctx)
	go func() {
		<-ctx.Done()
		a.logger.InfoContext(context.Background(), "shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	a.logger.InfoContext(ctx, "server listening", "addr", a.cfg.HTTPAddr, "base_url", a.cfg.BaseURL)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		done := make(chan struct{})
		go func() {
			a.workerWg.Wait()
			close(done)
		}()
		select {
		case <-done:
			a.logger.InfoContext(context.Background(), "workers drained")
		case <-time.After(10 * time.Second):
			a.logger.WarnContext(context.Background(), "worker drain timed out after 10s")
		}
		return nil
	}
	return err
}

// newHTTPServer builds the hardened HTTP server (C19). The timeout and size
// bounds are locked by TestServerHardening so the ISO 8.6 posture cannot
// silently regress: header read 5s, full read 15s, write 45s, idle 60s, 1MB
// max headers, and a bounded connection shutdown.
func (a *App) newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              a.cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func (a *App) routes() http.Handler {
	router := chi.NewRouter()
	router.Use(logging.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
	router.Use(a.accessLog)
	router.Use(a.securityHeaders)
	router.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Get("/health/ready", a.handleReady)
	webhookLimit := a.rateLimit("webhook", a.cfg.RateLimitWebhooksPerMinute)
	publicLimit := a.rateLimit("public", a.cfg.RateLimitPublicPerMinute)
	scanLimit := a.rateLimit("scan", a.cfg.RateLimitScanPerMinute)
	router.Get("/webhooks/whatsapp", a.verifyWhatsAppWebhook)
	router.With(webhookLimit).Post("/webhooks/whatsapp", a.receiveWhatsAppWebhook)
	router.With(webhookLimit).Post("/webhooks/telegram", a.receiveTelegramWebhook)
	router.With(webhookLimit).Post("/webhooks/sms", a.receiveSMSWebhook)
	router.With(webhookLimit).Post("/webhooks/interswitch", a.receiveInterswitchWebhook)
	router.With(webhookLimit).Post("/webhooks/vtpass", a.receiveVTPassWebhook)
	router.With(publicLimit).Get("/payments/return", a.paymentReturn)
	router.With(publicLimit).Post("/payments/return", a.paymentReturn)
	router.With(publicLimit).Get("/checkout/{token}", a.hostedCheckout)
	router.With(publicLimit).Post("/checkout/{token}/pay", a.hostedCheckoutPay)
	router.With(publicLimit).Get("/checkout/interswitch/{reference}", a.interswitchCheckout)
	router.With(publicLimit).Get("/link/{token}", a.checkoutLink)
	router.With(publicLimit).Post("/link/{token}/resolve", a.checkoutLinkResolve)
	router.With(publicLimit).Get("/w/{token}", a.webFlowPage)
	router.With(publicLimit).Post("/w/{token}", a.webFlowSubmit)
	router.With(publicLimit).Get("/receipts/{token}", a.receipt)
	router.With(publicLimit).Get("/receipts/{token}/scan-qr.png", a.receiptScanQR)
	router.With(publicLimit).Get("/invoices/{reference}", a.invoice)
	router.With(publicLimit).Get("/thrift/{name}", a.thriftGroup)
	router.With(publicLimit).Get("/scan/{token}", a.scanLanding)
	router.With(scanLimit).Post("/api/readers/scan", a.readerScan)
	router.Handle("/static/*", http.FileServer(http.FS(web.Assets)))

	router.Route("/api/v1", func(api chi.Router) {
		api.Use(a.requireAPIKey)
		api.Post("/payments", a.apiCreatePayment)
		api.Get("/payments/{reference}", a.apiPaymentStatus)
		api.Post("/payments/{reference}/verify", a.apiVerifyPayment)
		api.Post("/invoices", a.apiCreateInvoice)
		api.Get("/invoices/{reference}", a.apiInvoiceStatus)
		api.Post("/checkouts", a.apiCreateCheckout)
		api.Get("/checkouts/{reference}", a.apiCheckoutStatus)
		api.Get("/balance", a.apiBalance)
		api.Post("/settlements", a.apiCreateSettlement)
		api.Get("/settlements/{batch_no}", a.apiSettlementStatus)
		api.Post("/settlements/{batch_no}/payout", a.apiRequestPayout)
		api.Post("/settlements/{batch_no}/payout/reverse", a.apiReversePayout)
		api.Get("/payouts/{reference}", a.apiPayoutStatus)
		api.Post("/settlement-accounts", a.apiCreateSettlementAccount)
		api.Get("/settlement-accounts", a.apiListSettlementAccounts)
		api.Post("/payments/{reference}/refund", a.apiRefundPayment)
		api.Get("/refunds/{id}", a.apiRefundStatus)
		api.Get("/disputes", a.apiListDisputes)
		api.Get("/disputes/{id}", a.apiDisputeDetail)
	})

	router.Get("/admin/login", a.loginPage)
	router.With(a.limitLogin).Post("/admin/login", a.login)
	router.Get("/admin/login/totp", a.totpPage)
	router.With(a.limitLogin).Post("/admin/login/totp", a.totpVerify)
	router.Group(func(admin chi.Router) {
		admin.Use(a.requireAdmin)
		admin.Use(a.rateLimit("admin", 120))
		admin.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/metrics", http.StatusSeeOther)
		})
		admin.Get("/admin/metrics", a.adminMetrics)
		admin.Get("/admin/messaging", a.adminMessaging)
		admin.Get("/admin/users", a.adminUsers)
		admin.Get("/admin/merchants", a.adminMerchants)
		admin.Get("/admin/payments", a.adminPayments)
		admin.Get("/admin/data-orders", a.adminDataOrders)
		admin.Get("/admin/thrift", a.adminThrift)
		admin.Get("/admin/accepted-numbers", a.adminAcceptedNumbers)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/merchant-registrations/{id}/approve", a.adminApproveMerchantRegistration)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleSupport)).Post("/admin/merchants/{id}/password", a.adminSetMerchantPassword)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/merchants/{id}/payment-terms", a.adminSetMerchantPaymentTerms)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/thrift/payouts/{id}/complete", a.adminCompleteThriftPayout)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/accepted-numbers", a.adminUpdateAcceptedNumbers)
		admin.With(a.requireRole(store.RoleAdmin)).Get("/admin/scanning", a.adminScanning)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/scanning/services", a.adminCreateScanningService)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/scanning/readers", a.adminCreateServiceReader)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/scanning/services/{id}/whitelist", a.adminSetPhoneWhitelist)
		admin.With(a.requireRole(store.RoleAdmin)).Get("/admin/webhooks", a.adminWebhooks)
		admin.With(a.requireRole(store.RoleAdmin)).Get("/admin/dead-letter", a.adminDeadLetter)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/dead-letter/{id}/replay", a.adminDeadLetterReplay)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/totp/disable", func(w http.ResponseWriter, r *http.Request) {
			adminID := adminIDFromContext(r.Context())
			a.totpDisable(w, r, "admin", &adminID, "/admin/metrics")
		})
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/audit", a.adminAuditLog)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/reports", a.adminReports)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/reports/{kind}/download", a.adminReportDownload)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/data-subjects", a.adminDataSubjects)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/data-subjects/export", a.adminDataSubjectExport)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/data-subjects/requests", a.adminDataSubjectRequest)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/data-subjects/requests/{id}/resolve", a.adminDataSubjectResolve)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/archive", a.adminArchive)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/legal-holds", a.adminLegalHolds)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/legal-holds", a.adminAddLegalHold)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/legal-holds/remove", a.adminRemoveLegalHold)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/ledger", a.adminLedger)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/ledger/reverse", a.adminLedgerReverse)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/reconciliation", a.adminReconciliation)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/reconciliation/run", a.adminReconciliationRun)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/chat-guard", a.adminChatGuard)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/kyc", a.adminKYC)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/kyc/cases/{id}/review", a.adminKYCReview)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/kyb", a.adminKYB)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/kyb/{id}/advance", a.adminKYBAdvance)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/kyb/{id}/advance-request/approve", a.adminKYBAdvanceRequestApprove)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/kyb/{id}/advance-request/clear", a.adminKYBAdvanceRequestClear)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/kyb/{id}/review", a.adminKYBReview)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/tier-limits/{id}", a.adminTierLimitUpdate)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/monitoring/alerts/{id}/resolve", a.adminResolveTransactionAlert)
		admin.With(a.requireRole(store.RoleAdmin)).Get("/admin/admins", a.adminAdmins)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/admins", a.adminCreateAdmin)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/admins/{id}/role", a.adminUpdateAdminRole)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/admins/{id}/enabled", a.adminToggleAdminEnabled)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/admins/{id}/password", a.adminResetAdminPassword)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/settlements", a.adminSettlements)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/settlements/accounts/{id}/approve", a.adminApproveSettlementAccount)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/settlements/accounts/{id}/disable", a.adminDisableSettlementAccount)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/settlements/payouts/{id}/retry", a.adminRetryPayout)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/settlements/payouts/{id}/reverse", a.adminReversePayout)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/refunds", a.adminRefunds)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/refunds/{id}/fail", a.adminRefundFail)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/refunds/{id}/approve", a.adminRefundApprove)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/refunds/{id}/reject", a.adminRefundReject)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/disputes", a.adminDisputes)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/disputes/{id}/resolve", a.adminResolveDispute)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/siem", a.adminSIEM)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/siem/export", a.adminSIEMExport)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/analytics", a.adminAnalytics)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/analytics/export", a.adminAnalyticsExport)
		admin.Post("/admin/logout", a.logout)
	})
	router.Get("/merchant/login", a.merchantLogin)
	router.With(a.limitLogin).Post("/merchant/login", a.merchantLoginPost)
	router.Get("/merchant/login/totp", a.totpPage)
	router.With(a.limitLogin).Post("/merchant/login/totp", a.totpVerify)
	router.Get("/merchant/set-password", a.merchantSetPasswordPage)
	router.Post("/merchant/set-password", a.merchantSetPasswordPost)
	router.Group(func(m chi.Router) {
		m.Use(a.requireMerchant)
		m.Get("/merchant", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/merchant/scanner", http.StatusSeeOther)
		})
		m.Get("/merchant/scanner", a.merchantScanner)
		m.Get("/merchant/scan", a.merchantScanPage)
		m.Post("/merchant/scan", a.merchantScanPost)
		m.Get("/merchant/invoices", a.merchantInvoices)
		m.Get("/merchant/payments", a.merchantPayments)
		m.Get("/merchant/settings", a.merchantSettings)
		m.Post("/merchant/settings", a.merchantUpdateSettings)
		m.Post("/merchant/api-keys", a.merchantCreateAPIKey)
		m.Post("/merchant/api-keys/{id}/revoke", a.merchantRevokeAPIKey)
		m.Post("/merchant/webhook", a.merchantUpdateWebhook)
		m.Get("/merchant/profile", a.merchantProfile)
		m.Post("/merchant/profile", a.merchantUpdateProfile)
		m.Get("/merchant/services", a.merchantServicesList)
		m.Get("/merchant/services/new", a.merchantServiceNewForm)
		m.Post("/merchant/services/new", a.merchantServiceCreate)
		m.Get("/merchant/services/{id}/edit", a.merchantServiceEditForm)
		m.Post("/merchant/services/{id}/edit", a.merchantServiceUpdate)
		m.Post("/merchant/services/{id}/toggle", a.merchantServiceToggle)
		m.Get("/merchant/services/{id}/payments", a.merchantServicePayments)
		m.Get("/merchant/events", a.merchantEventsList)
		m.Get("/merchant/events/new", a.merchantEventNewForm)
		m.Post("/merchant/events/new", a.merchantEventCreate)
		m.Get("/merchant/events/{id}/edit", a.merchantEventEditForm)
		m.Post("/merchant/events/{id}/edit", a.merchantEventUpdate)
		m.Post("/merchant/events/{id}/toggle", a.merchantEventToggle)
		m.Get("/merchant/events/{id}/tickets", a.merchantEventTickets)
		m.Post("/merchant/scanner/services/{id}/whitelist", a.merchantUpdateServiceWhitelist)
		m.Post("/merchant/totp/disable", func(w http.ResponseWriter, r *http.Request) {
			merchantID := merchantIDFromContext(r.Context())
			a.totpDisable(w, r, "merchant", &merchantID, "/merchant/settings")
		})
		m.Post("/merchant/logout", a.merchantLogout)
	})
	return router
}

func (a *App) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]string{"database": "ok"}
	ready := true
	if err := a.store.Ping(ctx); err != nil {
		checks["database"] = err.Error()
		ready = false
	}
	if a.eventBus != nil {
		checks["event_bus"] = "ok"
	} else {
		checks["event_bus"] = "not_configured"
	}
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"status": checks, "ready": ready})
}

type contextKey string

const csrfContextKey contextKey = "csrf"
const adminIDContextKey contextKey = "admin_id"
const adminRoleContextKey contextKey = "admin_role"
const adminEmailContextKey contextKey = "admin_email"
const merchantIDContextKey contextKey = "merchant_id"
const merchantCSRFContextKey contextKey = "merchant_csrf"

func (a *App) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(adminCookieName)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		adminID, role, email, csrf, err := a.store.ValidateAdminSession(r.Context(), cookie.Value)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), csrfContextKey, csrf)
		ctx = context.WithValue(ctx, adminIDContextKey, adminID)
		ctx = context.WithValue(ctx, adminRoleContextKey, role)
		ctx = context.WithValue(ctx, adminEmailContextKey, email)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// audit records a privileged action with actor identity and IP into the
// tamper-evident chain. A failed write is logged loudly but does not abort the
// action, so a storage hiccup can never freeze payments or admin operations.
func (a *App) audit(r *http.Request, action, resourceType, resourceID string, details map[string]any) {
	ctx := r.Context()
	entry := store.AuditLog{
		ActorType:  "system",
		ActorEmail: sql.NullString{},
		Action:     action,
	}
	if id := merchantIDFromContext(ctx); id != uuid.Nil {
		entry.ActorType = "merchant"
		entry.ActorID = uuid.NullUUID{UUID: id, Valid: true}
	} else if id := adminIDFromContext(ctx); id != uuid.Nil {
		entry.ActorType = "admin"
		entry.ActorID = uuid.NullUUID{UUID: id, Valid: true}
		entry.ActorEmail = sql.NullString{String: adminEmailFromContext(ctx), Valid: true}
	}
	entry.IP = sql.NullString{String: clientIP(r), Valid: true}
	entry.ResourceType = sql.NullString{String: resourceType, Valid: resourceType != ""}
	entry.ResourceID = sql.NullString{String: resourceID, Valid: resourceID != ""}
	entry.Details = details
	_, err := a.store.AppendAuditLog(ctx, entry)
	if err != nil {
		a.logger.Error("audit log write failed", "action", action, "resource_type", resourceType, "resource_id", resourceID, "error", err)
	}
}

// requireRole gates a route to admins holding any of the given roles.
func (a *App) requireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles))
	for _, role := range roles {
		allowed[role] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !allowed[adminRoleFromContext(r.Context())] {
				http.Error(w, "you do not have permission to do this", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func adminIDFromContext(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(adminIDContextKey).(uuid.UUID)
	return id
}

func adminRoleFromContext(ctx context.Context) string {
	role, _ := ctx.Value(adminRoleContextKey).(string)
	return role
}

func adminEmailFromContext(ctx context.Context) string {
	email, _ := ctx.Value(adminEmailContextKey).(string)
	return email
}

func csrfFromContext(ctx context.Context) string {
	value, _ := ctx.Value(csrfContextKey).(string)
	return value
}

func merchantIDFromContext(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(merchantIDContextKey).(uuid.UUID)
	return id
}

func merchantCSRFFromContext(ctx context.Context) string {
	value, _ := ctx.Value(merchantCSRFContextKey).(string)
	return value
}

func (a *App) requireMerchant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(merchantCookieName)
		if err != nil {
			http.Redirect(w, r, "/merchant/login", http.StatusSeeOther)
			return
		}
		merchantID, _, csrf, err := a.store.ValidateMerchantSession(r.Context(), cookie.Value)
		if err != nil {
			http.Redirect(w, r, "/merchant/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), merchantIDContextKey, merchantID)
		ctx = context.WithValue(ctx, merchantCSRFContextKey, csrf)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) renderMerchant(w http.ResponseWriter, name string, r *http.Request, title string, data map[string]any) {
	data["AppName"] = a.cfg.AppName
	data["Title"] = title
	data["CSRF"] = merchantCSRFFromContext(r.Context())
	data["MerchantID"] = merchantIDFromContext(r.Context())
	a.render(w, name, data)
}

func extractDataSize(name string) string {
	match := regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?\s*(?:MB|GB|TB))`).FindString(name)
	if strings.TrimSpace(match) == "" {
		return "Data bundle"
	}
	return strings.ToUpper(strings.ReplaceAll(match, " ", ""))
}

func extractValidity(name string) string {
	match := regexp.MustCompile(`(?i)([0-9]+\s*(?:day|days|week|weeks|month|months|hour|hours))`).FindString(name)
	if strings.TrimSpace(match) == "" {
		return "Validity varies"
	}
	return strings.TrimSpace(match)
}

func (a *App) limitLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retryAfter := a.limiter.retryAfter(clientIP(r)); retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			http.Error(w, "too many login attempts", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimit enforces a fixed-window cap per client IP. The key names the
// route class (webhook, public, scan) so limits are isolated per class.
func (a *App) rateLimit(class string, limit int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := class + ":" + clientIP(r)
			allowed, retryAfter := a.rateLimiter.Allow(r.Context(), key, limit, time.Minute)
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *App) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		a.logger.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data: blob:; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) renderAdmin(w http.ResponseWriter, name string, r *http.Request, title string, data map[string]any) {
	data["AppName"] = a.cfg.AppName
	data["Title"] = title
	data["CSRF"] = csrfFromContext(r.Context())
	data["AdminRole"] = adminRoleFromContext(r.Context())
	a.render(w, name, data)
}

func (a *App) render(w http.ResponseWriter, name string, data any) {
	a.renderStatus(w, name, data, http.StatusOK)
}

func (a *App) renderStatus(w http.ResponseWriter, name string, data any, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		a.logger.Error("render template", "template", name, "error", err)
	}
}

func readBody(r *http.Request, limit int64) ([]byte, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("request body is too large")
	}
	return body, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func randomToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func maskPII(value string) string {
	if strings.Contains(value, "@") {
		parts := strings.SplitN(value, "@", 2)
		if len(parts[0]) <= 2 {
			return "**@" + parts[1]
		}
		return parts[0][:2] + "***@" + parts[1]
	}
	if len(value) > 7 {
		return value[:4] + "****" + value[len(value)-3:]
	}
	return "****"
}
