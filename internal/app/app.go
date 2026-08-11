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
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/logging"
	"whatsapp-payment-demo/internal/ports"
	dataprovider "whatsapp-payment-demo/internal/providers/data"
	emailprovider "whatsapp-payment-demo/internal/providers/email"
	identityprovider "whatsapp-payment-demo/internal/providers/identity"
	"whatsapp-payment-demo/internal/providers/paystack"
	screeningprovider "whatsapp-payment-demo/internal/providers/screening"
	"whatsapp-payment-demo/internal/providers/telegram"
	"whatsapp-payment-demo/internal/providers/vtpass"
	"whatsapp-payment-demo/internal/providers/whatsapp"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/internal/totp"
	"whatsapp-payment-demo/web"
)

const adminCookieName = "wpd_admin"
const merchantCookieName = "wpd_merchant"

// App is the fully assembled payment demo.
type App struct {
	cfg               config.Config
	logger            *slog.Logger
	store             *store.Store
	paystack          *paystack.Client
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
}

// New creates all application dependencies.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	logger = slog.New(logging.WithRequestID(logger.Handler()))
	repository, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	repository.SetDataKey(cfg.DataEncryptionKey)
	paystackClient := paystack.New(cfg.PaystackSecretKey, cfg.PaystackBaseURL)
	whatsappClient := whatsapp.New(cfg.WhatsAppAppSecret, cfg.WhatsAppAccessToken, cfg.WhatsAppPhoneNumberID, cfg.WhatsAppGraphVersion, cfg.WhatsAppTemplateLocale)
	var telegramClient *telegram.Client
	messengers := map[string]ports.Messenger{service.ChannelWhatsApp: whatsappClient}
	if cfg.TelegramEnabled {
		telegramClient = telegram.New(cfg.TelegramBotToken, cfg.TelegramAPIBase, cfg.TelegramWebhookSecret)
		messengers[service.ChannelTelegram] = telegramClient
	}
	paymentService := service.NewPaymentService(cfg, repository, paystackClient, logger)
	var dataProvider ports.DataProvider = dataprovider.NewSimulator()
	if strings.EqualFold(cfg.DataProvider, "vtpass") {
		dataProvider = vtpass.NewWithTimeout(cfg.VTPassBaseURL, cfg.VTPassAPIKey, cfg.VTPassPublicKey, cfg.VTPassSecretKey, cfg.VTPassTimeout)
	}
	dataService := service.NewDataService(repository, paymentService, dataProvider)
	var identityVerifier ports.IdentityVerifier = identityprovider.NewSimulator()
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
	templates, err := template.New("").Funcs(template.FuncMap{
		"money":       domain.FormatNGN,
		"maskPII":     maskPII,
		"statusClass": func(status any) string { return strings.ReplaceAll(fmt.Sprint(status), "_", "-") },
		"percent":     func(value float64) string { return fmt.Sprintf("%.1f%%", value) },
		"sub":         func(a, b int64) int64 { return a - b },
		"inc":         func(i int) int { return i + 1 },
		"join":        func(items []string, sep string) string { return strings.Join(items, sep) },
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
	return &App{
		cfg: cfg, logger: logger, store: repository, paystack: paystackClient,
		telegram: telegramClient, whatsapp: whatsappClient, payments: paymentService,
		data:         dataService,
		conversation: service.NewConversationService(cfg, repository, paymentService, dataService, messengers, emailSender, identityVerifier, sanctionsScreener),
		templates:    templates, limiter: newLoginLimiter(), totpKey: totpKey,
		rateLimiter: rateLimiter, rateClose: rateClose, sanctionsScreener: sanctionsScreener,
	}, nil
}

// Close releases persistent resources.
func (a *App) Close() {
	if a.rateClose != nil {
		_ = a.rateClose()
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

// Seed refreshes the baseline merchant fixtures.
func (a *App) Seed(ctx context.Context) error {
	return a.store.Seed(ctx)
}

// Reconcile verifies unresolved Paystack transactions.
func (a *App) Reconcile(ctx context.Context) error {
	return a.payments.Reconcile(ctx)
}

// PurgeExpiredData enforces the configured retention period.
func (a *App) PurgeExpiredData(ctx context.Context) error {
	report, err := a.store.PurgeBefore(ctx, time.Now().Add(-a.cfg.RetentionPeriod))
	if err == nil {
		a.logger.InfoContext(ctx, "retention completed",
			"users_purged", report.UsersPurged,
			"payments_archived", report.PaymentsArchived,
			"payments_purged", report.PaymentsPurged,
			"invoices_archived", report.InvoicesArchived,
			"invoices_purged", report.InvoicesPurged,
			"thrift_purged", report.ThriftPurged,
			"operational_purged", report.OperationalPurged,
			"audit_archived", report.AuditArchived,
			"audit_purged", report.AuditPurged,
		)
	}
	return err
}

// RescreenDue re-runs sanctions/PEP screening for profiles whose last result
// is older than the configured period. A strong/blocked rescreen downgrades
// the profile below L2 and opens a manual review case.
func (a *App) RescreenDue(ctx context.Context) error {
	due, err := a.store.KYCProfilesDueForRescreen(ctx, time.Now().Add(-a.cfg.KYCRescreenPeriod), 50)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		a.logger.InfoContext(ctx, "no KYC profiles due for rescreen")
		return nil
	}
	screened, blocked := 0, 0
	for _, profile := range due {
		individual, err := a.store.IndividualProfileByUser(ctx, profile.UserID)
		if err != nil {
			a.logger.WarnContext(ctx, "rescreen skipped (no individual profile)", "user_id", profile.UserID.String())
			continue
		}
		decision, err := a.sanctionsScreener.Screen(ctx, ports.ScreeningRequest{LegalName: individual.LegalName})
		if err != nil {
			a.logger.WarnContext(ctx, "rescreen provider error", "user_id", profile.UserID.String(), "error", err)
			continue
		}
		if _, err := a.store.RecordScreeningResult(ctx, store.ScreeningResult{
			UserID: profile.UserID, Provider: "simulated", Decision: decision.Decision,
			MatchedNames: decision.MatchedNames,
		}, a.cfg.KYCRescreenPeriod); err != nil {
			a.logger.WarnContext(ctx, "rescreen record failed", "user_id", profile.UserID.String(), "error", err)
			continue
		}
		if _, err := a.store.RecomputeRiskScore(ctx, profile.UserID); err != nil {
			a.logger.WarnContext(ctx, "rescreen risk recompute failed", "user_id", profile.UserID.String(), "error", err)
		}
		screened++
		if kyc.BlockedByScreening(decision.Decision) {
			blocked++
			if _, err := a.store.DowngradeKYCTier(ctx, profile.UserID, kyc.TierL1, "rescreen "+decision.Decision, nil); err != nil {
				a.logger.WarnContext(ctx, "rescreen downgrade failed", "user_id", profile.UserID.String(), "error", err)
			}
			if _, err := a.store.CreateManualReviewCase(ctx, store.ManualReviewCase{
				UserID: profile.UserID, CaseType: "screening", Reason: "rescreen decision " + decision.Decision,
			}); err != nil {
				a.logger.WarnContext(ctx, "rescreen review case failed", "user_id", profile.UserID.String(), "error", err)
			}
		}
	}
	a.logger.InfoContext(ctx, "KYC rescreen completed", "due", len(due), "screened", screened, "blocked", blocked)
	return nil
}

// RecomputeAllRisk refreshes the ML/FT risk band for every KYC profile. Used
// by the demo CLI when risk events are backfilled or thresholds change.
func (a *App) RecomputeAllRisk(ctx context.Context) error {
	profiles, err := a.store.ListKYCProfiles(ctx, 500)
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		a.logger.InfoContext(ctx, "no KYC profiles to score")
		return nil
	}
	recomputed := 0
	for _, profile := range profiles {
		if _, err := a.store.RecomputeRiskScore(ctx, profile.UserID); err != nil {
			a.logger.WarnContext(ctx, "risk recompute failed", "user_id", profile.UserID.String(), "error", err)
			continue
		}
		recomputed++
	}
	a.logger.InfoContext(ctx, "risk recompute completed", "profiles", len(profiles), "recomputed", recomputed)
	return nil
}

// MonitorTransactions runs the C14 transaction-monitoring rules over the
// configured lookback window and returns how many alerts were raised.
func (a *App) MonitorTransactions(ctx context.Context) error {
	raised, err := a.store.RunTransactionMonitor(ctx, time.Now().Add(-a.cfg.MonitorVelocityWindow), kyc.MonitorConfig{
		VelocityWindow:     a.cfg.MonitorVelocityWindow,
		VelocityLimit:      a.cfg.MonitorVelocityLimit,
		StructuringWindow:  a.cfg.MonitorStructuringWindow,
		StructuringCount:   a.cfg.MonitorStructuringCount,
		StructuringFloor:   a.cfg.MonitorStructuringFloor,
		StructuringCeil:    a.cfg.MonitorStructuringCeil,
		RoundAmountStep:    a.cfg.MonitorRoundAmountStep,
		RoundAmountMin:     a.cfg.MonitorRoundAmountMin,
		HighRiskCategories: a.cfg.MonitorHighRiskCategories,
	})
	if err != nil {
		return err
	}
	a.logger.InfoContext(ctx, "transaction monitor completed", "alerts", raised, "window", a.cfg.MonitorVelocityWindow.String())
	return nil
}

// SyncVTPassDataPlans imports every current VTPass data variation into Xego's catalog.
func (a *App) SyncVTPassDataPlans(ctx context.Context) error {
	client := vtpass.NewWithTimeout(a.cfg.VTPassBaseURL, a.cfg.VTPassAPIKey, a.cfg.VTPassPublicKey, a.cfg.VTPassSecretKey, a.cfg.VTPassTimeout)
	networks := []string{"MTN", "AIRTEL", "GLO", "9MOBILE"}
	for _, network := range networks {
		serviceID := vtpass.ServiceIDForNetwork(network)
		variations, err := client.ListDataVariations(ctx, serviceID)
		if err != nil {
			return fmt.Errorf("sync %s variations: %w", network, err)
		}
		for index, variation := range variations {
			code := vtpass.PlanCodeFromVariation(network, variation.VariationCode)
			dataSize := extractDataSize(variation.Name)
			validity := extractValidity(variation.Name)
			if err := a.store.UpsertDataPlanFromProvider(ctx, network, code, variation.Name, dataSize, validity, variation.AmountKobo, variation.VariationCode, (index+1)*10); err != nil {
				return fmt.Errorf("upsert %s %s: %w", network, variation.VariationCode, err)
			}
		}
		a.logger.InfoContext(ctx, "synced VTPass data variations", "network", network, "count", len(variations))
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
	server := &http.Server{
		Addr:              a.cfg.HTTPAddr,
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go a.runWorkers(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	a.logger.InfoContext(ctx, "server listening", "addr", a.cfg.HTTPAddr, "base_url", a.cfg.BaseURL)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) routes() http.Handler {
	router := chi.NewRouter()
	router.Use(logging.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
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
	router.With(webhookLimit).Post("/webhooks/paystack", a.receivePaystackWebhook)
	router.With(webhookLimit).Post("/webhooks/vtpass", a.receiveVTPassWebhook)
	router.With(publicLimit).Get("/payments/return", a.paymentReturn)
	router.With(publicLimit).Get("/receipts/{token}", a.receipt)
	router.With(publicLimit).Get("/receipts/{token}/scan-qr.png", a.receiptScanQR)
	router.With(publicLimit).Get("/invoices/{reference}", a.invoice)
	router.With(publicLimit).Get("/thrift/{name}", a.thriftGroup)
	router.With(publicLimit).Get("/scan/{token}", a.scanLanding)
	router.With(scanLimit).Post("/api/readers/scan", a.readerScan)
	router.Handle("/static/*", http.FileServer(http.FS(web.Assets)))

	router.Get("/admin/login", a.loginPage)
	router.With(a.limitLogin).Post("/admin/login", a.login)
	router.Get("/admin/login/totp", a.totpPage)
	router.With(a.limitLogin).Post("/admin/login/totp", a.totpVerify)
	router.Group(func(admin chi.Router) {
		admin.Use(a.requireAdmin)
		admin.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/metrics", http.StatusSeeOther)
		})
		admin.Get("/admin/metrics", a.adminMetrics)
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
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Get("/admin/kyc", a.adminKYC)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/kyc/cases/{id}/review", a.adminKYCReview)
		admin.With(a.requireRole(store.RoleAdmin, store.RoleCompliance)).Post("/admin/monitoring/alerts/{id}/resolve", a.adminResolveTransactionAlert)
		admin.With(a.requireRole(store.RoleAdmin)).Get("/admin/admins", a.adminAdmins)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/admins", a.adminCreateAdmin)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/admins/{id}/role", a.adminUpdateAdminRole)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/admins/{id}/enabled", a.adminToggleAdminEnabled)
		admin.With(a.requireRole(store.RoleAdmin)).Post("/admin/admins/{id}/password", a.adminResetAdminPassword)
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
		m.Get("/merchant/profile", a.merchantProfile)
		m.Post("/merchant/profile", a.merchantUpdateProfile)
		m.Get("/merchant/services", a.merchantServicesList)
		m.Get("/merchant/services/new", a.merchantServiceNewForm)
		m.Post("/merchant/services/new", a.merchantServiceCreate)
		m.Get("/merchant/services/{id}/edit", a.merchantServiceEditForm)
		m.Post("/merchant/services/{id}/edit", a.merchantServiceUpdate)
		m.Post("/merchant/services/{id}/toggle", a.merchantServiceToggle)
		m.Get("/merchant/services/{id}/payments", a.merchantServicePayments)
		m.Post("/merchant/scanner/services/{id}/whitelist", a.merchantUpdateServiceWhitelist)
		m.Post("/merchant/totp/disable", func(w http.ResponseWriter, r *http.Request) {
			merchantID := merchantIDFromContext(r.Context())
			a.totpDisable(w, r, "merchant", &merchantID, "/merchant/settings")
		})
		m.Post("/merchant/logout", a.merchantLogout)
	})
	return router
}

func (a *App) runWorkers(ctx context.Context) {
	outboxTicker := time.NewTicker(2 * time.Second)
	reconcileTicker := time.NewTicker(1 * time.Minute)
	retentionTicker := time.NewTicker(24 * time.Hour)
	rescreenTicker := time.NewTicker(24 * time.Hour)
	monitorTicker := time.NewTicker(15 * time.Minute)
	defer outboxTicker.Stop()
	defer reconcileTicker.Stop()
	defer retentionTicker.Stop()
	defer rescreenTicker.Stop()
	defer monitorTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-outboxTicker.C:
			a.processInboundMessages(ctx)
			a.processPaystackWebhooks(ctx)
			a.processDataFulfilments(ctx)
			a.deliverOutbox(ctx)
		case <-reconcileTicker.C:
			if err := a.payments.Reconcile(ctx); err != nil {
				a.logger.WarnContext(ctx, "scheduled reconciliation failed", "error", err)
			}
		case <-retentionTicker.C:
			if err := a.PurgeExpiredData(ctx); err != nil {
				a.logger.WarnContext(ctx, "scheduled retention failed", "error", err)
			}
		case <-rescreenTicker.C:
			if err := a.RescreenDue(ctx); err != nil {
				a.logger.WarnContext(ctx, "scheduled KYC rescreen failed", "error", err)
			}
		case <-monitorTicker.C:
			if err := a.MonitorTransactions(ctx); err != nil {
				a.logger.WarnContext(ctx, "scheduled transaction monitor failed", "error", err)
			}
		}
	}
}

func (a *App) processInboundMessages(ctx context.Context) {
	messages, err := a.store.ClaimInboundMessages(ctx, 20)
	if err != nil {
		a.logger.WarnContext(ctx, "claim inbound messages", "error", err)
		return
	}
	for _, message := range messages {
		if err := a.conversation.Handle(ctx, message); err != nil {
			a.logger.ErrorContext(ctx, "process inbound message", "message_id", message.ID, "channel", message.Channel, "error", err)
			_ = a.store.RetryInboundMessage(ctx, message.ID, message.Attempts, err.Error())
			continue
		}
		_ = a.store.CompleteInboundMessage(ctx, message.ID)
	}
}

func (a *App) processPaystackWebhooks(ctx context.Context) {
	events, err := a.store.ClaimPaystackWebhooks(ctx, 20)
	if err != nil {
		a.logger.WarnContext(ctx, "claim Paystack webhooks", "error", err)
		return
	}
	for _, event := range events {
		if event.Event != "charge.success" {
			_ = a.store.CompleteWebhook(ctx, event.ID, "ignored", "")
			continue
		}
		_, _, processErr := a.payments.VerifyAndApply(ctx, event.Reference, "paystack.webhook")
		if processErr != nil {
			a.logger.ErrorContext(ctx, "process Paystack webhook", "reference", event.Reference, "error", processErr)
			_ = a.store.RetryWebhook(ctx, event.ID, event.Attempts, processErr.Error())
			continue
		}
		_ = a.store.CompleteWebhook(ctx, event.ID, "processed", "")
	}
}

func (a *App) processDataFulfilments(ctx context.Context) {
	if err := a.data.ProcessFulfilments(ctx, 20); err != nil {
		a.logger.WarnContext(ctx, "process data fulfilments", "error", err)
	}
}

func (a *App) deliverOutbox(ctx context.Context) {
	messages, err := a.store.ClaimOutbox(ctx, 20)
	if err != nil {
		a.logger.WarnContext(ctx, "claim outbox", "error", err)
		return
	}
	for _, message := range messages {
		var sendErr error
		switch message.Kind {
		case "text":
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				sendErr = err
			} else {
				sendErr = a.sendOutboxText(ctx, message.Channel, message.Recipient, payload.Body)
			}
		case "image":
			var payload struct {
				ImageData string `json:"image_data"`
				Caption   string `json:"caption"`
			}
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				sendErr = err
			} else {
				sendErr = a.sendOutboxImage(ctx, message.Channel, message.Recipient, payload.ImageData, payload.Caption)
			}
		case "template":
			var payload struct {
				Name       string   `json:"name"`
				Parameters []string `json:"parameters"`
			}
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				sendErr = err
			} else if message.Channel == service.ChannelWhatsApp {
				sendErr = a.whatsapp.SendTemplate(ctx, message.Recipient, payload.Name, payload.Parameters)
			} else {
				sendErr = a.sendOutboxText(ctx, message.Channel, message.Recipient, strings.Join(payload.Parameters, "\n"))
			}
		default:
			sendErr = fmt.Errorf("unsupported outbox kind %q", message.Kind)
		}
		if sendErr == nil {
			_ = a.store.CompleteOutbox(ctx, message.ID)
		} else {
			_ = a.store.RetryOutbox(ctx, message.ID, message.Attempts, sendErr.Error())
		}
	}
}

func (a *App) sendOutboxText(ctx context.Context, channel, recipient, body string) error {
	switch channel {
	case service.ChannelSMS:
		// The SMS MVP returns replies synchronously from /webhooks/sms. A live SMS
		// sender can be wired here later without changing order fulfilment logic.
		return nil
	case service.ChannelTelegram:
		if a.telegram == nil {
			return errors.New("Telegram is not configured")
		}
		return a.telegram.SendText(ctx, recipient, body)
	default:
		return a.whatsapp.SendText(ctx, recipient, body)
	}
}

func (a *App) sendOutboxImage(ctx context.Context, channel, recipient, imageDataB64, caption string) error {
	imageData, err := base64.StdEncoding.DecodeString(imageDataB64)
	if err != nil {
		return fmt.Errorf("decode image data: %w", err)
	}
	switch channel {
	case service.ChannelSMS:
		return nil
	case service.ChannelTelegram:
		if a.telegram == nil {
			return errors.New("Telegram is not configured")
		}
		return a.telegram.SendImage(ctx, recipient, imageData, caption)
	default:
		return a.whatsapp.SendImage(ctx, recipient, imageData, caption)
	}
}

func (a *App) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *App) verifyWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("hub.mode") != "subscribe" ||
		r.URL.Query().Get("hub.verify_token") != a.cfg.WhatsAppVerifyToken {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(r.URL.Query().Get("hub.challenge")))
}

func (a *App) receiveWhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	eventKey := digest(body)
	validationErr := a.whatsapp.ValidateSignature(body, r.Header.Get("X-Hub-Signature-256"))
	deliveryID, _, storeErr := a.store.RecordWebhook(r.Context(), "whatsapp", eventKey, validationErr == nil, json.RawMessage(`{}`))
	if storeErr != nil {
		http.Error(w, "storage error", http.StatusServiceUnavailable)
		return
	}
	if validationErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "rejected", "invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	messages, err := whatsapp.ParseInbound(body)
	if err != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", err.Error())
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	for _, message := range messages {
		if _, err := a.store.EnqueueInboundMessage(r.Context(), store.InboundMessage{
			ID: message.ID, Channel: service.ChannelWhatsApp, Sender: message.From, Recipient: message.From, Text: message.Text, Interactive: message.Interactive,
		}); err != nil {
			_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", err.Error())
			http.Error(w, "storage error", http.StatusServiceUnavailable)
			return
		}
	}
	_ = a.store.CompleteWebhook(r.Context(), deliveryID, "accepted", "")
	w.WriteHeader(http.StatusOK)
}

func (a *App) receiveTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.TelegramEnabled || a.telegram == nil {
		http.Error(w, "telegram disabled", http.StatusNotFound)
		return
	}
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	validationErr := a.telegram.ValidateSecret(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))
	updates, parseErr := telegram.ParseInbound(body)
	eventKey := digest(body)
	if len(updates) > 0 {
		eventKey = "update:" + strconv.FormatInt(updates[0].UpdateID, 10)
	}
	deliveryID, fresh, storeErr := a.store.RecordWebhook(r.Context(), service.ChannelTelegram, eventKey, validationErr == nil && parseErr == nil, json.RawMessage(`{}`))
	if storeErr != nil {
		http.Error(w, "storage error", http.StatusServiceUnavailable)
		return
	}
	if validationErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "rejected", "invalid secret")
		http.Error(w, "invalid secret", http.StatusUnauthorized)
		return
	}
	if parseErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", parseErr.Error())
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	for _, update := range updates {
		if update.CallbackQueryID != "" {
			_ = a.telegram.AnswerCallback(r.Context(), update.CallbackQueryID)
		}
		if _, err := a.store.EnqueueInboundMessage(r.Context(), store.InboundMessage{
			ID:          "telegram:" + strconv.FormatInt(update.UpdateID, 10),
			Channel:     service.ChannelTelegram,
			Sender:      update.UserID,
			Recipient:   update.ChatID,
			Text:        update.Text,
			Interactive: update.Interactive,
			Username:    update.Username,
		}); err != nil {
			_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", err.Error())
			http.Error(w, "storage error", http.StatusServiceUnavailable)
			return
		}
	}
	if fresh {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "accepted", "")
	}
	w.WriteHeader(http.StatusOK)
}

func (a *App) receiveSMSWebhook(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.SMSEnabled {
		http.Error(w, "sms disabled", http.StatusNotFound)
		return
	}
	secret := r.Header.Get("X-SMS-Webhook-Secret")
	if secret == "" {
		secret = r.Header.Get("X-Xego-SMS-Secret")
	}
	if a.cfg.SMSWebhookSecret == "" || secret != a.cfg.SMSWebhookSecret {
		http.Error(w, "invalid sms secret", http.StatusUnauthorized)
		return
	}
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	messageID, sender, text := parseSMSWebhookPayload(r, body)
	if messageID == "" {
		messageID = "sms:" + digest(body)
	}
	if sender == "" || text == "" {
		http.Error(w, "missing sender or text", http.StatusBadRequest)
		return
	}
	reply, err := a.data.HandleSMS(r.Context(), messageID, sender, text)
	if err != nil {
		a.logger.WarnContext(r.Context(), "process SMS webhook", "message_id", messageID, "error", err)
		http.Error(w, "sms processing failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reply": reply})
}

func (a *App) receivePaystackWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	event, validationErr := a.paystack.ValidateWebhook(body, r.Header.Get("X-Paystack-Signature"))
	eventKey := digest(body)
	if event.Reference != "" {
		eventKey = event.Event + ":" + event.Reference
	}
	payload := json.RawMessage(`{}`)
	if validationErr == nil {
		payload, _ = json.Marshal(store.GatewayEvent{Event: event.Event, Reference: event.Reference})
	}
	deliveryID, fresh, err := a.store.RecordWebhook(r.Context(), "paystack", eventKey, validationErr == nil, payload)
	if err != nil {
		http.Error(w, "storage error", http.StatusServiceUnavailable)
		return
	}
	if validationErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "rejected", "invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
	if !fresh || event.Event != "charge.success" {
		if fresh {
			_ = a.store.CompleteWebhook(context.Background(), deliveryID, "ignored", "")
		}
		return
	}
}

func (a *App) receiveVTPassWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	secretValid := a.cfg.VTPassWebhookSecret == "" ||
		r.Header.Get("X-VTPass-Webhook-Secret") == a.cfg.VTPassWebhookSecret
	event, parseErr := vtpass.ParseWebhook(body)
	eventKey := digest(body)
	if event.Reference != "" {
		eventKey = "vtpass:" + event.Reference
	}
	payload := json.RawMessage(`{}`)
	if parseErr == nil {
		payload, _ = json.Marshal(map[string]string{"reference": event.Reference, "status": event.Status, "message": event.Message})
	}
	deliveryID, fresh, storeErr := a.store.RecordWebhook(r.Context(), "vtpass", eventKey, secretValid && parseErr == nil, payload)
	if storeErr != nil {
		http.Error(w, "storage error", http.StatusServiceUnavailable)
		return
	}
	if !secretValid {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "rejected", "invalid secret")
		http.Error(w, "invalid secret", http.StatusUnauthorized)
		return
	}
	if parseErr != nil {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", parseErr.Error())
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if !fresh {
		writeJSON(w, http.StatusOK, map[string]string{"response": "success"})
		return
	}
	if _, changed, err := a.data.ApplyProviderResult(r.Context(), event.Reference, event.Status, event.Message); err != nil {
		a.logger.WarnContext(r.Context(), "process VTPass webhook", "reference", event.Reference, "error", err)
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "failed", err.Error())
		http.Error(w, "processing failed", http.StatusInternalServerError)
		return
	} else if changed {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "processed", "")
	} else {
		_ = a.store.CompleteWebhook(r.Context(), deliveryID, "ignored", "")
	}
	writeJSON(w, http.StatusOK, map[string]string{"response": "success"})
}

func (a *App) paymentReturn(w http.ResponseWriter, r *http.Request) {
	reference := strings.TrimSpace(r.URL.Query().Get("reference"))
	if reference == "" {
		http.Error(w, "missing reference", http.StatusBadRequest)
		return
	}
	payment, _, err := a.payments.VerifyAndApply(r.Context(), reference, "paystack.callback")
	if err != nil {
		a.logger.WarnContext(r.Context(), "callback verification failed", "reference", reference, "error", err)
		http.Error(w, "Payment is still being verified. Return to WhatsApp or refresh your receipt shortly.", http.StatusAccepted)
		return
	}
	http.Redirect(w, r, "/receipts/"+payment.ReceiptToken, http.StatusSeeOther)
}

func (a *App) receipt(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if len(token) < 32 {
		http.NotFound(w, r)
		return
	}
	payment, err := a.store.PaymentByReceiptToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var dataOrder *store.DataOrderView
	if order, err := a.store.DataOrderByPaymentID(r.Context(), payment.ID); err == nil {
		dataOrder = &order
	}
	var invoice *store.InvoiceView
	if view, err := a.store.InvoiceByPaymentID(r.Context(), payment.ID); err == nil {
		invoice = &view
	}
	var thrift *store.ThriftContributionView
	if view, err := a.store.ThriftContributionByPaymentID(r.Context(), payment.ID); err == nil {
		thrift = &view
	}
	var scanToken *store.ReceiptScanTokenView
	if view, err := a.store.ReceiptScanTokenByPaymentID(r.Context(), payment.ID); err == nil {
		scanToken = &view
	}
	a.render(w, "receipt.html", map[string]any{
		"AppName": a.cfg.AppName, "Payment": payment, "DataOrder": dataOrder,
		"Invoice": invoice, "Thrift": thrift, "ScanToken": scanToken, "BaseURL": a.cfg.BaseURL,
	})
}

func (a *App) receiptScanQR(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	payment, err := a.store.PaymentByReceiptToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	scanToken, err := a.store.ReceiptScanTokenByPaymentID(r.Context(), payment.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// The QR payload contains only the opaque scan URL. Readers call the
	// authenticated API to get safe receipt details and consume the token.
	png, err := qrcode.Encode(a.cfg.BaseURL+"/scan/"+scanToken.Token, qrcode.Medium, 240)
	if err != nil {
		http.Error(w, "qr unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (a *App) scanLanding(w http.ResponseWriter, r *http.Request) {
	token := store.ExtractScanToken(chi.URLParam(r, "token"))
	if token == "" {
		http.NotFound(w, r)
		return
	}
	a.render(w, "scan.html", map[string]any{"AppName": a.cfg.AppName, "Token": token})
}

func (a *App) readerScan(w http.ResponseWriter, r *http.Request) {
	apiKey := strings.TrimSpace(r.Header.Get("X-Xego-Reader-Key"))
	if apiKey == "" {
		apiKey = strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	}
	var body struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	} else if err := r.ParseForm(); err == nil {
		body.Token = r.FormValue("token")
		body.URL = r.FormValue("url")
	}
	tokenOrURL := body.Token
	if tokenOrURL == "" {
		tokenOrURL = body.URL
	}
	result, err := a.store.ValidateAndConsumeReceiptScan(r.Context(), apiKey, tokenOrURL, clientIP(r))
	if err != nil {
		http.Error(w, "scan unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	if result.Status != "valid_consumed" {
		status = http.StatusConflict
		if result.Status == "reader_not_authorized" {
			status = http.StatusUnauthorized
		}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

func (a *App) invoice(w http.ResponseWriter, r *http.Request) {
	reference := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "reference")))
	if !strings.HasPrefix(reference, "XG-INV-") {
		http.NotFound(w, r)
		return
	}
	invoice, err := a.store.InvoiceByReference(r.Context(), reference)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.render(w, "invoice.html", map[string]any{"AppName": a.cfg.AppName, "Invoice": invoice, "BaseURL": a.cfg.BaseURL})
}

func (a *App) thriftGroup(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if name == "" {
		http.NotFound(w, r)
		return
	}
	group, err := a.store.ThriftGroupByName(r.Context(), name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	members, _ := a.store.ThriftMembers(r.Context(), group.ID)
	progress, _ := a.store.ThriftCycleProgressForGroup(r.Context(), group.ID)
	a.render(w, "thrift_group.html", map[string]any{
		"AppName":  a.cfg.AppName,
		"Group":    group,
		"Members":  members,
		"Progress": progress,
	})
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, "login.html", map[string]any{"AppName": a.cfg.AppName})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	admin, err := a.store.AdminUserByEmail(r.Context(), email)
	if err != nil || admin == nil || !admin.Enabled ||
		bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password)) != nil {
		a.limiter.fail(clientIP(r))
		a.renderStatus(w, "login.html", map[string]any{"AppName": a.cfg.AppName, "Error": "Invalid email or password."}, http.StatusUnauthorized)
		return
	}
	if a.cfg.TOTPEnabled {
		a.beginTOTPLogin(w, r, "admin", &admin.ID, admin.Email, "/admin/login")
		return
	}
	a.completeAdminLogin(w, r, admin.ID)
}

// beginTOTPLogin starts the second factor of a login. When no TOTP secret is
// enrolled for the scope/subject it starts enrollment instead. The redirect
// carries an opaque single-use token; the pending row holds the enrollment
// secret cipher so the QR page can render after a refresh.
func (a *App) beginTOTPLogin(w http.ResponseWriter, r *http.Request, scope string, subjectID *uuid.UUID, account, fallbackPath string) {
	pending, err := a.newTOTPPending(r.Context(), scope, subjectID)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "start totp login", "scope", scope, "error", err)
		http.Error(w, "security setup error", http.StatusInternalServerError)
		return
	}
	next := "/admin/login/totp"
	if scope == "merchant" {
		next = "/merchant/login/totp"
	}
	q := url.Values{"token": {pending.Token}, "scope": {scope}, "account": {account}}
	http.Redirect(w, r, next+"?"+q.Encode(), http.StatusSeeOther)
}

// completeAdminLogin issues the admin session cookie.
func (a *App) completeAdminLogin(w http.ResponseWriter, r *http.Request, adminID uuid.UUID) {
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if err := a.store.CreateAdminSession(r.Context(), adminID, token, csrf, time.Now().Add(12*time.Hour)); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.limiter.success(clientIP(r))
	http.SetCookie(w, &http.Cookie{
		Name: adminCookieName, Value: token, Path: "/admin", HttpOnly: true,
		Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/admin/metrics", http.StatusSeeOther)
}

// newTOTPPending creates a short-lived second-factor step. The secret cipher
// is stored in the pending row only when enrolling (no secret exists yet);
// otherwise verification loads the persisted secret by scope/subject.
func (a *App) newTOTPPending(ctx context.Context, scope string, subjectID *uuid.UUID) (totpPending, error) {
	existing, err := a.store.GetTOTPSecret(ctx, scope, subjectID)
	if err != nil {
		return totpPending{}, err
	}
	var secretCipher []byte
	if existing == nil {
		key, err := totp.Generate(a.cfg.AppName, "login")
		if err != nil {
			return totpPending{}, err
		}
		secretCipher, err = totp.EncryptSecret(a.totpKey, key.Secret())
		if err != nil {
			return totpPending{}, err
		}
	}
	token, err := randomToken(32)
	if err != nil {
		return totpPending{}, err
	}
	hash := sha256.Sum256([]byte(token))
	if err := a.store.CreateTOTPPendingLogin(ctx, hash[:], scope, subjectID, secretCipher, time.Now().Add(10*time.Minute)); err != nil {
		return totpPending{}, err
	}
	return totpPending{Token: token, Scope: scope, SubjectID: subjectID, SecretCipher: secretCipher}, nil
}

type totpPending struct {
	Token        string
	Scope        string
	SubjectID    *uuid.UUID
	SecretCipher []byte
}

// totpPage renders the second-factor page: QR + secret during enrollment,
// code field only when a secret is already enrolled.
func (a *App) totpPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	account := strings.TrimSpace(r.URL.Query().Get("account"))
	loginPath := "/admin/login"
	if token == "" {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	hash := sha256.Sum256([]byte(token))
	scope, _, secretCipher, found, err := a.store.GetTOTPPendingLogin(r.Context(), hash[:])
	if err != nil || !found {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	if scope == "merchant" {
		loginPath = "/merchant/login"
	}
	data := map[string]any{
		"AppName":    a.cfg.AppName,
		"Title":      "Two-step login",
		"Token":      token,
		"Scope":      scope,
		"Account":    account,
		"VerifyPath": "/admin/login/totp",
		"CancelPath": loginPath,
	}
	if secretCipher != nil {
		secret, err := totp.DecryptSecret(a.totpKey, secretCipher)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "decrypt totp secret", "error", err)
			http.Redirect(w, r, loginPath, http.StatusSeeOther)
			return
		}
		keyURI := totp.KeyURI(a.cfg.AppName, account, secret)
		png, err := qrcode.Encode(keyURI, qrcode.Medium, 220)
		if err != nil {
			a.logger.ErrorContext(r.Context(), "encode totp qr", "error", err)
			http.Redirect(w, r, loginPath, http.StatusSeeOther)
			return
		}
		data["Secret"] = secret
		data["QR"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	if scope == "merchant" {
		data["VerifyPath"] = "/merchant/login/totp"
		a.render(w, "merchant_totp.html", data)
		return
	}
	a.render(w, "admin_totp.html", data)
}

// totpVerify consumes the pending login and issues the session after a valid code.
func (a *App) totpVerify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	code := strings.TrimSpace(r.FormValue("code"))
	if token == "" || code == "" {
		http.Error(w, "missing token or code", http.StatusBadRequest)
		return
	}
	hash := sha256.Sum256([]byte(token))
	scope, subjectID, secretCipher, found, err := a.store.ConsumeTOTPPendingLogin(r.Context(), hash[:])
	loginTemplate := "login.html"
	if scope == "merchant" {
		loginTemplate = "merchant_login.html"
	}
	if err != nil || !found {
		a.renderStatus(w, loginTemplate, map[string]any{"AppName": a.cfg.AppName, "Error": "This login step has expired. Please sign in again."}, http.StatusUnauthorized)
		return
	}
	secret := ""
	if secretCipher != nil {
		secret, err = totp.DecryptSecret(a.totpKey, secretCipher)
	} else {
		var cipher []byte
		cipher, err = a.store.GetTOTPSecret(r.Context(), scope, subjectID)
		if err == nil && cipher == nil {
			err = errors.New("totp secret missing")
		}
		if err == nil {
			secret, err = totp.DecryptSecret(a.totpKey, cipher)
		}
	}
	if err != nil {
		a.logger.ErrorContext(r.Context(), "load totp secret", "scope", scope, "error", err)
		a.renderStatus(w, loginTemplate, map[string]any{"AppName": a.cfg.AppName, "Error": "Security error. Please sign in again."}, http.StatusInternalServerError)
		return
	}
	if !totp.Validate(code, secret) {
		a.limiter.fail(clientIP(r))
		a.renderStatus(w, loginTemplate, map[string]any{"AppName": a.cfg.AppName, "Error": "Invalid authentication code."}, http.StatusUnauthorized)
		return
	}
	if secretCipher != nil {
		if err := a.store.SetTOTPSecret(r.Context(), scope, subjectID, secretCipher); err != nil {
			a.logger.ErrorContext(r.Context(), "persist totp secret", "scope", scope, "error", err)
			a.renderStatus(w, loginTemplate, map[string]any{"AppName": a.cfg.AppName, "Error": "Security error. Please sign in again."}, http.StatusInternalServerError)
			return
		}
		a.auditTOTPEnrollment(r, scope, subjectID)
	}
	if scope == "merchant" {
		a.completeMerchantLoginWithTOTP(w, r, subjectID)
		return
	}
	if subjectID == nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.completeAdminLogin(w, r, *subjectID)
}

// completeMerchantLoginWithTOTP issues the merchant session cookie after the
// second factor passed for the given merchant subject.
func (a *App) completeMerchantLoginWithTOTP(w http.ResponseWriter, r *http.Request, merchantID *uuid.UUID) {
	if merchantID == nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	ownerID, err := a.store.MerchantOwnerID(r.Context(), *merchantID)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if err := a.store.CreateMerchantSession(r.Context(), *merchantID, ownerID, token, csrf, time.Now().Add(12*time.Hour)); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.limiter.success(clientIP(r))
	http.SetCookie(w, &http.Cookie{
		Name: merchantCookieName, Value: token, Path: "/merchant", HttpOnly: true,
		Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/merchant/scanner", http.StatusSeeOther)
}

// totpDisable removes the enrolled secret, turning TOTP off for the subject.
func (a *App) totpDisable(w http.ResponseWriter, r *http.Request, scope string, subjectID *uuid.UUID, redirectTo string) {
	if scope == "admin" && r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if scope == "merchant" && r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := a.store.DeleteTOTPSecret(r.Context(), scope, subjectID); err != nil {
		http.Error(w, "security error", http.StatusInternalServerError)
		return
	}
	a.audit(r, scope+".totp_disabled", scope, subjectID.String(), nil)
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

// auditTOTPEnrollment records a first-time TOTP enrollment. It runs during the
// login flow, before any session exists, so the subject is taken from the
// pending login rather than the request context.
func (a *App) auditTOTPEnrollment(r *http.Request, scope string, subjectID *uuid.UUID) {
	if subjectID == nil {
		return
	}
	entry := store.AuditLog{
		ActorType:  scope,
		ActorID:    uuid.NullUUID{UUID: *subjectID, Valid: true},
		Action:     scope + ".totp_enrolled",
		ResourceID: sql.NullString{String: subjectID.String(), Valid: true},
		IP:         sql.NullString{String: clientIP(r), Valid: true},
		Details:    map[string]any{},
	}
	if scope == "admin" {
		if admin, err := a.store.AdminUserByID(r.Context(), *subjectID); err == nil && admin != nil {
			entry.ActorEmail = sql.NullString{String: admin.Email, Valid: true}
		}
	}
	if _, err := a.store.AppendAuditLog(r.Context(), entry); err != nil {
		a.logger.ErrorContext(r.Context(), "audit log write failed", "action", entry.Action, "error", err)
	}
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie(adminCookieName); err == nil {
		_ = a.store.DeleteAdminSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Path: "/admin", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (a *App) adminMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := a.store.Metrics(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "metrics.html", r, "Metrics", map[string]any{"Metrics": metrics, "TOTPEnabled": a.cfg.TOTPEnabled})
}

func (a *App) adminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.ListUsers(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "users.html", r, "Users", map[string]any{"Users": users})
}

func (a *App) adminMerchants(w http.ResponseWriter, r *http.Request) {
	merchants, err := a.store.ListMerchants(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	registrations, err := a.store.ListMerchantRegistrations(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "merchants.html", r, "Merchants", map[string]any{"Merchants": merchants, "Registrations": registrations})
}

func (a *App) adminAdmins(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.ListAdminUsers(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_users.html", r, "Admin accounts", map[string]any{"Admins": users, "CurrentAdminID": adminIDFromContext(r.Context())})
}

// adminAuditLog renders the tamper-evident audit trail with chain status.
func (a *App) adminAuditLog(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.ListAuditLogs(r.Context(), 200)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	count, broken, err := a.store.VerifyAuditChain(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_audit.html", r, "Audit log", map[string]any{
		"Entries":     entries,
		"ChainCount":  count,
		"ChainBroken": broken,
	})
}

// adminArchive renders the append-only archive of records removed by retention.
func (a *App) adminArchive(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.ListArchiveLedger(r.Context(), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list archive ledger", "error", err)
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_archive.html", r, "Retention archive", map[string]any{"Entries": entries})
}

// adminLegalHolds lists retention-exempt subjects (C29).
func (a *App) adminLegalHolds(w http.ResponseWriter, r *http.Request) {
	holds, err := a.store.ListLegalHolds(r.Context())
	if err != nil {
		a.logger.WarnContext(r.Context(), "list legal holds", "error", err)
		http.Error(w, "legal holds unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_legal_holds.html", r, "Legal holds", map[string]any{"Holds": holds})
}

// adminAddLegalHold freezes a subject so retention never purges it.
func (a *App) adminAddLegalHold(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	subjectType := strings.TrimSpace(r.FormValue("subject_type"))
	subjectID := strings.TrimSpace(r.FormValue("subject_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	validTypes := map[string]bool{"user": true, "payment": true, "invoice": true, "thrift_group": true}
	if !validTypes[subjectType] || subjectID == "" || reason == "" {
		http.Error(w, "subject type, subject id and reason are required", http.StatusBadRequest)
		return
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(r.FormValue("expires_at")); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			http.Error(w, "expiry must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		expiresAt = &parsed
	}
	adminEmail := adminEmailFromContext(r.Context())
	if err := a.store.AddLegalHold(r.Context(), subjectType, subjectID, reason, adminEmail, expiresAt); err != nil {
		a.logger.WarnContext(r.Context(), "add legal hold failed", "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.legal_holds.created", subjectType, subjectID, map[string]any{"reason": reason, "expires_at": expiresAt})
	a.logger.InfoContext(r.Context(), "legal hold added", "subject_type", subjectType, "subject_id", subjectID)
	http.Redirect(w, r, "/admin/legal-holds", http.StatusSeeOther)
}

// adminRemoveLegalHold clears a freeze.
func (a *App) adminRemoveLegalHold(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	subjectType := strings.TrimSpace(r.FormValue("subject_type"))
	subjectID := strings.TrimSpace(r.FormValue("subject_id"))
	if subjectType == "" || subjectID == "" {
		http.Error(w, "subject type and id are required", http.StatusBadRequest)
		return
	}
	if err := a.store.RemoveLegalHold(r.Context(), subjectType, subjectID); err != nil {
		a.logger.WarnContext(r.Context(), "remove legal hold failed", "error", err)
		http.Error(w, "remove failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.legal_holds.removed", subjectType, subjectID, map[string]any{})
	http.Redirect(w, r, "/admin/legal-holds", http.StatusSeeOther)
}

// adminKYC renders the customer identity ladder and the pending review queue.
func (a *App) adminKYC(w http.ResponseWriter, r *http.Request) {
	profiles, err := a.store.ListKYCProfiles(r.Context(), 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list kyc profiles", "error", err)
		http.Error(w, "kyc dashboard unavailable", http.StatusInternalServerError)
		return
	}
	cases, err := a.store.ListManualReviewCases(r.Context(), "", 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list review cases", "error", err)
		http.Error(w, "kyc dashboard unavailable", http.StatusInternalServerError)
		return
	}
	alerts, err := a.store.ListTransactionAlerts(r.Context(), "", 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list transaction alerts", "error", err)
		http.Error(w, "kyc dashboard unavailable", http.StatusInternalServerError)
		return
	}
	byTier := map[string]int{"L0": 0, "L1": 0, "L2": 0, "L3": 0, "L4": 0}
	for _, p := range profiles {
		byTier[p.Tier]++
	}
	a.renderAdmin(w, "admin_kyc.html", r, "KYC ladder", map[string]any{
		"Profiles": profiles,
		"Cases":    cases,
		"Alerts":   alerts,
		"ByTier":   byTier,
	})
}

// adminResolveTransactionAlert moves a monitoring alert to a terminal status
// (acknowledged / escalated / resolved) and records the reviewer.
func (a *App) adminResolveTransactionAlert(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	alertID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || alertID <= 0 {
		http.Error(w, "invalid alert id", http.StatusBadRequest)
		return
	}
	status := strings.TrimSpace(r.FormValue("status"))
	adminEmail := adminEmailFromContext(r.Context())
	alert, err := a.store.ResolveTransactionAlert(r.Context(), alertID, status, adminEmail)
	if err != nil {
		a.logger.WarnContext(r.Context(), "resolve transaction alert failed", "alert_id", alertID, "error", err)
		http.Error(w, "resolve failed", http.StatusInternalServerError)
		return
	}
	a.logger.InfoContext(r.Context(), "transaction alert resolved", "alert_id", alert.ID, "status", alert.Status)
	http.Redirect(w, r, "/admin/kyc", http.StatusSeeOther)
}

// adminKYCReview approves or rejects a manual review case.
func (a *App) adminKYCReview(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || caseID <= 0 {
		http.Error(w, "invalid case id", http.StatusBadRequest)
		return
	}
	approve := strings.TrimSpace(r.FormValue("decision")) == "approve"
	note := strings.TrimSpace(r.FormValue("note"))
	adminID := adminIDFromContext(r.Context())
	adminEmail := adminEmailFromContext(r.Context())
	actor := &store.AuditLog{
		ActorType:  "admin",
		ActorID:    uuid.NullUUID{UUID: adminID, Valid: true},
		ActorEmail: sql.NullString{String: adminEmail, Valid: true},
		IP:         sql.NullString{String: clientIP(r), Valid: true},
	}
	c, err := a.store.ReviewManualReviewCase(r.Context(), caseID, approve, adminEmail, note, actor)
	if err != nil {
		a.logger.WarnContext(r.Context(), "review kyc case failed", "case_id", caseID, "error", err)
		http.Error(w, "review failed", http.StatusInternalServerError)
		return
	}
	a.logger.InfoContext(r.Context(), "kyc case reviewed", "case_id", c.ID, "status", c.Status, "user_id", c.UserID.String())
	http.Redirect(w, r, "/admin/kyc", http.StatusSeeOther)
}

func (a *App) adminCreateAdmin(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	role := strings.TrimSpace(r.FormValue("role"))
	password := r.FormValue("password")
	if email == "" || password == "" || !validAdminRole(role) {
		http.Error(w, "email, password and role are required", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if err := a.store.CreateAdminUser(r.Context(), email, string(hash), role); err != nil {
		a.logger.WarnContext(r.Context(), "create admin user failed", "email", email, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.admins.created", "admin_user", email, map[string]any{"role": role})
	a.logger.InfoContext(r.Context(), "admin user created", "email", email, "role", role)
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

func (a *App) adminUpdateAdminRole(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid admin id", http.StatusBadRequest)
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	if !validAdminRole(role) {
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}
	if id == adminIDFromContext(r.Context()) {
		http.Error(w, "you cannot change your own role", http.StatusForbidden)
		return
	}
	if err := a.store.UpdateAdminUserRole(r.Context(), id, role); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.admins.role_changed", "admin_user", id.String(), map[string]any{"new_role": role})
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

func (a *App) adminToggleAdminEnabled(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid admin id", http.StatusBadRequest)
		return
	}
	if id == adminIDFromContext(r.Context()) {
		http.Error(w, "you cannot disable your own account", http.StatusForbidden)
		return
	}
	enabled := r.FormValue("enabled") == "1"
	if err := a.store.SetAdminUserEnabled(r.Context(), id, enabled); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.admins.enabled_toggled", "admin_user", id.String(), map[string]any{"enabled": enabled})
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

func (a *App) adminResetAdminPassword(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid admin id", http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	if password == "" {
		http.Error(w, "password required", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if err := a.store.UpdateAdminUserPassword(r.Context(), id, string(hash)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.admins.password_reset", "admin_user", id.String(), nil)
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

func validAdminRole(role string) bool {
	switch role {
	case store.RoleAdmin, store.RoleCompliance, store.RoleSupport, store.RoleReadOnly:
		return true
	}
	return false
}

func (a *App) adminApproveMerchantRegistration(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid registration id", http.StatusBadRequest)
		return
	}
	merchant, err := a.store.ApproveMerchantRegistration(r.Context(), id)
	if err != nil {
		a.logger.WarnContext(r.Context(), "approve merchant registration failed", "registration_id", id, "error", err)
		http.Error(w, "approval failed", http.StatusInternalServerError)
		return
	}
	owner, err := a.store.MerchantOwnerByRegistrationID(r.Context(), id)
	if err == nil {
		var setPasswordURL string
		token, err := randomToken(32)
		if err == nil {
			if err := a.store.CreateMerchantPasswordResetToken(r.Context(), merchant.ID, owner.ID, token, time.Now().Add(72*time.Hour)); err == nil {
				setPasswordURL = a.cfg.BaseURL + "/merchant/set-password?token=" + token
			}
		}
		a.conversation.NotifyMerchantApproved(r.Context(), owner, merchant.Name, setPasswordURL)
	}
	a.audit(r, "admin.merchants.approved", "merchant", merchant.ID.String(), map[string]any{"name": merchant.Name, "registration_id": id.String()})
	http.Redirect(w, r, "/admin/merchants", http.StatusSeeOther)
}

func (a *App) adminSetMerchantPassword(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	password := strings.TrimSpace(r.FormValue("password"))
	if password == "" {
		http.Error(w, "password required", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if err := a.store.UpdateMerchantPassword(r.Context(), id, string(hash)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.password_reset", "merchant", id.String(), nil)
	http.Redirect(w, r, "/admin/merchants?password_set=1", http.StatusSeeOther)
}

func (a *App) adminSetMerchantPaymentTerms(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	allowPartial := r.FormValue("allow_partial_payments") == "on"
	minInvoiceKobo, _ := strconv.ParseInt(r.FormValue("min_invoice_amount_kobo"), 10, 64)
	upfrontPct, _ := strconv.Atoi(r.FormValue("upfront_percent"))
	minInstallPct, _ := strconv.Atoi(r.FormValue("min_installment_percent"))
	maxInstallments, _ := strconv.Atoi(r.FormValue("max_installments"))
	allowFullAlways := r.FormValue("allow_full_pay_always") == "on"
	if err := a.store.UpdateMerchantPaymentTerms(r.Context(), id, allowPartial, minInvoiceKobo, upfrontPct, minInstallPct, maxInstallments, allowFullAlways); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.merchants.payment_terms_updated", "merchant", id.String(), map[string]any{
		"allow_partial": allowPartial, "min_invoice_kobo": minInvoiceKobo, "upfront_percent": upfrontPct,
		"min_installment_percent": minInstallPct, "max_installments": maxInstallments, "allow_full_always": allowFullAlways,
	})
	http.Redirect(w, r, "/admin/merchants?terms_set=1", http.StatusSeeOther)
}

func (a *App) adminSetPhoneWhitelist(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	whitelist := strings.TrimSpace(r.FormValue("phone_whitelist"))
	if err := a.store.UpdateServicePhoneWhitelist(r.Context(), id, whitelist); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.scanning.whitelist_updated", "service", id.String(), map[string]any{"whitelist": whitelist})
	http.Redirect(w, r, "/admin/scanning?whitelist_set=1", http.StatusSeeOther)
}

func (a *App) adminPayments(w http.ResponseWriter, r *http.Request) {
	payments, err := a.store.ListPayments(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "payments.html", r, "Payments", map[string]any{"Payments": payments})
}

func (a *App) adminDataOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := a.store.ListDataOrders(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	smsRequests, err := a.store.ListSMSRequests(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "data_orders.html", r, "Data Orders", map[string]any{"Orders": orders, "SMSRequests": smsRequests})
}

func (a *App) adminThrift(w http.ResponseWriter, r *http.Request) {
	groups, err := a.store.ListThriftGroups(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	contributions, err := a.store.ListThriftContributions(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	payouts, err := a.store.ListThriftPayouts(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "thrift.html", r, "Thrift", map[string]any{"Groups": groups, "Contributions": contributions, "Payouts": payouts})
}

func (a *App) adminCompleteThriftPayout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid payout id", http.StatusBadRequest)
		return
	}
	if err := a.store.MarkThriftPayoutCompleted(r.Context(), id); err != nil {
		a.logger.WarnContext(r.Context(), "complete thrift payout failed", "payout_id", id, "error", err)
		http.Error(w, "payout completion failed", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.thrift.payout_completed", "thrift_payout", id.String(), nil)
	http.Redirect(w, r, "/admin/thrift", http.StatusSeeOther)
}

func (a *App) adminAcceptedNumbers(w http.ResponseWriter, r *http.Request) {
	numbers := a.conversation.AcceptedInvoiceNumbers()
	a.renderAdmin(w, "accepted_numbers.html", r, "Accepted Invoice Numbers", map[string]any{"Numbers": numbers})
}

func (a *App) adminUpdateAcceptedNumbers(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	raw := r.FormValue("numbers")
	var numbers []string
	for _, s := range strings.Split(raw, "\n") {
		s = strings.TrimSpace(s)
		if s != "" {
			numbers = append(numbers, s)
		}
	}
	a.conversation.SetAcceptedInvoiceNumbers(numbers)
	a.audit(r, "admin.scanning.accepted_numbers_updated", "config", "accepted_invoice_numbers", map[string]any{"count": len(numbers)})
	http.Redirect(w, r, "/admin/accepted-numbers", http.StatusSeeOther)
}

func (a *App) adminScanning(w http.ResponseWriter, r *http.Request) {
	a.renderScanningAdmin(w, r, nil)
}

func (a *App) adminCreateScanningService(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ttlHours, _ := strconv.Atoi(r.FormValue("ttl_hours"))
	if ttlHours <= 0 {
		ttlHours = 24
	}
	var merchantID uuid.NullUUID
	if raw := strings.TrimSpace(r.FormValue("merchant_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "invalid merchant", http.StatusBadRequest)
			return
		}
		merchantID = uuid.NullUUID{UUID: id, Valid: true}
	}
	_, err := a.store.CreateRegisteredService(
		r.Context(),
		r.FormValue("name"),
		r.FormValue("service_type"),
		merchantID,
		r.FormValue("accepted_receipt_types"),
		ttlHours*3600,
		r.FormValue("active") == "on",
	)
	if err != nil {
		a.renderScanningAdmin(w, r, map[string]any{"Error": "Could not save registered service."})
		return
	}
	a.audit(r, "admin.scanning.service_created", "service", r.FormValue("name"), map[string]any{"service_type": r.FormValue("service_type")})
	http.Redirect(w, r, "/admin/scanning", http.StatusSeeOther)
}

func (a *App) adminCreateServiceReader(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	serviceID, err := uuid.Parse(strings.TrimSpace(r.FormValue("service_id")))
	if err != nil {
		http.Error(w, "invalid service", http.StatusBadRequest)
		return
	}
	reader, key, err := a.store.CreateServiceReader(r.Context(), serviceID, r.FormValue("name"))
	if err != nil {
		a.renderScanningAdmin(w, r, map[string]any{"Error": "Could not create reader."})
		return
	}
	a.audit(r, "admin.scanning.reader_created", "service_reader", reader.ID.String(), map[string]any{"name": r.FormValue("name")})
	a.renderScanningAdmin(w, r, map[string]any{"NewReader": reader, "NewReaderKey": key})
}

func (a *App) renderScanningAdmin(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	services, err := a.store.ListRegisteredServices(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	readers, err := a.store.ListServiceReaders(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	tokens, err := a.store.ListReceiptScanTokens(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	attempts, err := a.store.ListReceiptScanAttempts(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	merchants, err := a.store.ListMerchants(r.Context())
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Services": services, "Readers": readers, "Tokens": tokens,
		"Attempts": attempts, "Merchants": merchants,
	}
	for key, value := range extra {
		data[key] = value
	}
	a.renderAdmin(w, "scanning.html", r, "Receipt scanning", data)
}

func (a *App) adminWebhooks(w http.ResponseWriter, r *http.Request) {
	webhooks, err := a.store.ListWebhooks(r.Context(), 100)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "webhooks.html", r, "Webhooks", map[string]any{"Webhooks": webhooks})
}

func (a *App) merchantScanner(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	services, err := a.store.ServicesByMerchantID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Services": services}
	a.renderMerchant(w, "merchant_scanner.html", r, "Receipt scanner", data)
}

func (a *App) merchantScanPage(w http.ResponseWriter, r *http.Request) {
	a.renderMerchant(w, "merchant_scan.html", r, "Manual scan", map[string]any{})
}

func (a *App) merchantScanPost(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		a.renderMerchant(w, "merchant_scan.html", r, "Manual scan", map[string]any{"Error": "Enter a scan token, manual code, or scan URL."})
		return
	}
	result, err := a.store.MerchantValidateScanToken(r.Context(), merchantID, token, "")
	if err != nil {
		a.renderMerchant(w, "merchant_scan.html", r, "Manual scan", map[string]any{"Error": "Scan validation failed."})
		return
	}
	a.renderMerchant(w, "merchant_scan.html", r, "Manual scan", map[string]any{"Result": result})
}

func (a *App) merchantUpdateServiceWhitelist(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	services, err := a.store.ServicesByMerchantID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	owned := false
	for _, svc := range services {
		if svc.ID == serviceID {
			owned = true
			break
		}
	}
	if !owned {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	whitelist := strings.TrimSpace(r.FormValue("phone_whitelist"))
	if err := a.store.UpdateServicePhoneWhitelist(r.Context(), serviceID, whitelist); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/scanner?whitelist_saved=1", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Merchant services/events handlers
// ---------------------------------------------------------------------------

func (a *App) merchantServicesList(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	services, err := a.store.ListMerchantServices(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	purchases, err := a.store.ServicePurchasesByMerchantID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_services_list.html", r, "Services", map[string]any{
		"Services": services, "Purchases": purchases,
	})
}

func (a *App) merchantServiceNewForm(w http.ResponseWriter, r *http.Request) {
	a.renderMerchant(w, "merchant_service_form.html", r, "New service", map[string]any{"Edit": false, "CustomFields": []store.ServiceCustomField{}})
}

func (a *App) merchantServiceCreate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	unitPrice, _ := strconv.ParseInt(r.FormValue("unit_price_kobo"), 10, 64)
	qtyAvailable, _ := strconv.Atoi(r.FormValue("quantity_available"))
	if name == "" || unitPrice <= 0 {
		http.Error(w, "name and unit price required", http.StatusBadRequest)
		return
	}
	if qtyAvailable < -1 {
		qtyAvailable = -1
	}
	var expiresAt *time.Time
	if expiryStr := strings.TrimSpace(r.FormValue("expires_at")); expiryStr != "" {
		if t, err := time.Parse("2006-01-02", expiryStr); err == nil {
			expiresAt = &t
		}
	}
	svc, err := a.store.CreateMerchantService(r.Context(), merchantID, name, description, unitPrice, qtyAvailable, expiresAt)
	if err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if err := a.saveCustomFields(r, svc.ID); err != nil {
		a.logger.ErrorContext(r.Context(), "save custom fields", "error", err)
	}
	http.Redirect(w, r, "/merchant/services?created=1", http.StatusSeeOther)
}

func (a *App) saveCustomFields(r *http.Request, serviceID uuid.UUID) error {
	names := r.Form["cf_name[]"]
	types := r.Form["cf_type[]"]
	reqStrs := r.Form["cf_required[]"]
	options := r.Form["cf_options[]"]
	var fields []store.CustomFieldSpec
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		ft := "text"
		if i < len(types) {
			ft = types[i]
		}
		isReq := false
		if i < len(reqStrs) && reqStrs[i] == "1" {
			isReq = true
		}
		opts := ""
		if i < len(options) {
			opts = options[i]
		}
		fields = append(fields, store.CustomFieldSpec{
			FieldName:    n,
			FieldType:    ft,
			FieldOptions: opts,
			IsRequired:   isReq,
			SortOrder:    i,
		})
	}
	return a.store.SetServiceCustomFields(r.Context(), serviceID, fields)
}

func (a *App) merchantServiceEditForm(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	svc, err := a.store.MerchantServiceByID(r.Context(), serviceID)
	if err != nil || svc.MerchantID != merchantID {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	customFields, _ := a.store.ListServiceCustomFields(r.Context(), serviceID)
	a.renderMerchant(w, "merchant_service_form.html", r, "Edit service", map[string]any{
		"Service": svc, "Edit": true, "CustomFields": customFields,
	})
}

func (a *App) merchantServiceUpdate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	svc, err := a.store.MerchantServiceByID(r.Context(), serviceID)
	if err != nil || svc.MerchantID != merchantID {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	unitPrice, _ := strconv.ParseInt(r.FormValue("unit_price_kobo"), 10, 64)
	qtyAvailable, _ := strconv.Atoi(r.FormValue("quantity_available"))
	if name == "" || unitPrice <= 0 {
		http.Error(w, "name and unit price required", http.StatusBadRequest)
		return
	}
	if qtyAvailable < -1 {
		qtyAvailable = -1
	}
	var expiresAt *time.Time
	if expiryStr := strings.TrimSpace(r.FormValue("expires_at")); expiryStr != "" {
		if t, err := time.Parse("2006-01-02", expiryStr); err == nil {
			expiresAt = &t
		}
	}
	if err := a.store.UpdateMerchantService(r.Context(), serviceID, name, description, unitPrice, qtyAvailable, expiresAt); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if err := a.saveCustomFields(r, serviceID); err != nil {
		a.logger.ErrorContext(r.Context(), "save custom fields", "error", err)
	}
	http.Redirect(w, r, "/merchant/services?saved=1", http.StatusSeeOther)
}

func (a *App) merchantServiceToggle(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	if err := a.store.ToggleMerchantService(r.Context(), serviceID, merchantID); err != nil {
		http.Error(w, "toggle failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/services?toggled=1", http.StatusSeeOther)
}

func (a *App) merchantServicePayments(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid service id", http.StatusBadRequest)
		return
	}
	svc, err := a.store.MerchantServiceByID(r.Context(), serviceID)
	if err != nil || svc.MerchantID != merchantID {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	purchases, err := a.store.ServicePurchasesByServiceID(r.Context(), serviceID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	purchaseIDs := make([]uuid.UUID, len(purchases))
	for i, p := range purchases {
		purchaseIDs[i] = p.ID
	}
	customData, _ := a.store.PurchaseCustomDataByPurchaseIDs(r.Context(), purchaseIDs)
	a.renderMerchant(w, "merchant_service_payments.html", r, "Service payments", map[string]any{
		"Service": svc, "Purchases": purchases, "CustomData": customData,
	})
}

func (a *App) merchantInvoices(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	invoices, err := a.store.InvoicesByMerchantID(r.Context(), merchantID, 50, 0)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_invoices.html", r, "Invoices", map[string]any{"Invoices": invoices})
}

func (a *App) merchantPayments(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	payments, err := a.store.PaymentsByMerchantID(r.Context(), merchantID, 50, 0)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_payments.html", r, "Payments", map[string]any{"Payments": payments})
}

func (a *App) merchantSettings(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	merchant, err := a.store.MerchantByID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	services, err := a.store.ServicesByMerchantID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	qrTTL, err := a.store.MerchantServiceTTL(r.Context(), merchantID)
	if err != nil {
		qrTTL = 86400
	}
	a.renderMerchant(w, "merchant_settings.html", r, "Payment settings", map[string]any{
		"Merchant": merchant, "Services": services, "QRValidityHours": qrTTL / 3600, "TOTPEnabled": a.cfg.TOTPEnabled,
	})
}

func (a *App) merchantUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	allowPartial := r.FormValue("allow_partial_payments") == "on"
	minInvoiceKobo, _ := strconv.ParseInt(r.FormValue("min_invoice_amount_kobo"), 10, 64)
	upfrontPct, _ := strconv.Atoi(r.FormValue("upfront_percent"))
	minInstallPct, _ := strconv.Atoi(r.FormValue("min_installment_percent"))
	maxInstallments, _ := strconv.Atoi(r.FormValue("max_installments"))
	allowFullAlways := r.FormValue("allow_full_pay_always") == "on"
	if err := a.store.UpdateMerchantPaymentTerms(r.Context(), merchantID, allowPartial, minInvoiceKobo, upfrontPct, minInstallPct, maxInstallments, allowFullAlways); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	if hours, err := strconv.Atoi(r.FormValue("qr_validity_hours")); err == nil && hours >= 1 && hours <= 720 {
		_ = a.store.UpdateMerchantServiceTTL(r.Context(), merchantID, hours*3600)
	}
	http.Redirect(w, r, "/merchant/settings?saved=1", http.StatusSeeOther)
}

func (a *App) merchantProfile(w http.ResponseWriter, r *http.Request) {
	merchantID := merchantIDFromContext(r.Context())
	merchant, err := a.store.MerchantByID(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	a.renderMerchant(w, "merchant_profile.html", r, "Business profile", map[string]any{"Merchant": merchant})
}

func (a *App) merchantUpdateProfile(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	merchantID := merchantIDFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	category := strings.TrimSpace(r.FormValue("category"))
	description := strings.TrimSpace(r.FormValue("description"))
	logoURL := strings.TrimSpace(r.FormValue("logo_url"))
	if name == "" {
		name = "Untitled"
	}
	if err := a.store.UpdateMerchantProfile(r.Context(), merchantID, name, category, description, logoURL); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/merchant/profile?saved=1", http.StatusSeeOther)
}

func (a *App) merchantSetPasswordPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		http.Error(w, "invalid link", http.StatusBadRequest)
		return
	}
	merchantID, _, err := a.store.ValidateMerchantPasswordResetToken(r.Context(), token)
	if err != nil {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Error": "This link has expired or has already been used.", "Invalid": true})
		return
	}
	merchant, err := a.store.MerchantByID(r.Context(), merchantID)
	if err != nil {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Error": "Invalid link.", "Invalid": true})
		return
	}
	a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Merchant": merchant, "Token": token})
}

func (a *App) merchantSetPasswordPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")
	if token == "" || password == "" {
		http.Error(w, "token and password required", http.StatusBadRequest)
		return
	}
	if password != confirm {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Token": token, "Error": "Passwords do not match."})
		return
	}
	if len(password) < 8 {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Token": token, "Error": "Password must be at least 8 characters."})
		return
	}
	merchantID, _, err := a.store.ValidateMerchantPasswordResetToken(r.Context(), token)
	if err != nil {
		a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Error": "This link has expired or has already been used.", "Invalid": true})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash error", http.StatusInternalServerError)
		return
	}
	if err := a.store.UpdateMerchantPassword(r.Context(), merchantID, string(hash)); err != nil {
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	_ = a.store.UseMerchantPasswordResetToken(r.Context(), token)
	a.render(w, "merchant_set_password.html", map[string]any{"AppName": a.cfg.AppName, "Done": true})
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

func (a *App) merchantLogin(w http.ResponseWriter, r *http.Request) {
	a.render(w, "merchant_login.html", map[string]any{"AppName": a.cfg.AppName})
}

func (a *App) merchantLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	merchant, err := a.store.MerchantByEmail(r.Context(), email)
	if err != nil || merchant.PasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(merchant.PasswordHash), []byte(password)) != nil {
		a.limiter.fail(clientIP(r))
		a.renderStatus(w, "merchant_login.html", map[string]any{"AppName": a.cfg.AppName, "Error": "Invalid email or password."}, http.StatusUnauthorized)
		return
	}
	if a.cfg.TOTPEnabled {
		a.beginTOTPLogin(w, r, "merchant", &merchant.ID, email, "/merchant/login")
		return
	}
	token, err := randomToken(32)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	ownerID, err := a.store.MerchantOwnerID(r.Context(), merchant.ID)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if err := a.store.CreateMerchantSession(r.Context(), merchant.ID, ownerID, token, csrf, time.Now().Add(12*time.Hour)); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	a.limiter.success(clientIP(r))
	http.SetCookie(w, &http.Cookie{
		Name: merchantCookieName, Value: token, Path: "/merchant", HttpOnly: true,
		Secure: a.cfg.Environment == "production", SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/merchant/scanner", http.StatusSeeOther)
}

func (a *App) merchantLogout(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie(merchantCookieName); err == nil {
		_ = a.store.DeleteMerchantSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: merchantCookieName, Path: "/merchant", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/merchant/login", http.StatusSeeOther)
}

func (a *App) renderMerchant(w http.ResponseWriter, name string, r *http.Request, title string, data map[string]any) {
	data["AppName"] = a.cfg.AppName
	data["Title"] = title
	data["CSRF"] = merchantCSRFFromContext(r.Context())
	data["MerchantID"] = merchantIDFromContext(r.Context())
	a.render(w, name, data)
}

func parseSMSWebhookPayload(r *http.Request, body []byte) (string, string, string) {
	var payload struct {
		ID      string `json:"id"`
		Message string `json:"message_id"`
		From    string `json:"from"`
		Sender  string `json:"sender"`
		Body    string `json:"body"`
		Text    string `json:"text"`
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(body, &payload)
	} else if values, err := url.ParseQuery(string(body)); err == nil {
		payload.ID = values.Get("id")
		payload.Message = values.Get("message_id")
		payload.From = values.Get("from")
		payload.Sender = values.Get("sender")
		payload.Body = values.Get("body")
		payload.Text = values.Get("text")
	}
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		id = strings.TrimSpace(payload.Message)
	}
	sender := strings.TrimSpace(payload.From)
	if sender == "" {
		sender = strings.TrimSpace(payload.Sender)
	}
	text := strings.TrimSpace(payload.Body)
	if text == "" {
		text = strings.TrimSpace(payload.Text)
	}
	return id, sender, text
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

type loginAttempt struct {
	failures int
	until    time.Time
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string]loginAttempt{}}
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.attempts[ip]
	attempt.failures++
	if attempt.failures >= 5 {
		attempt.until = time.Now().Add(15 * time.Minute)
	}
	l.attempts[ip] = attempt
}

func (l *loginLimiter) success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

func (l *loginLimiter) retryAfter(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.attempts[ip]
	if time.Now().After(attempt.until) {
		if !attempt.until.IsZero() {
			delete(l.attempts, ip)
		}
		return 0
	}
	return time.Until(attempt.until)
}
