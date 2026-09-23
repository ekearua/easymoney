package app

// Admin media-report harness: boots the real app, seeds representative
// channel-media extraction rows (both rails) across several days, and mints
// an admin session so a Playwright script can screenshot the live
// /admin/media-report page including the per-day token trend sparkline.
//
//	TEST_DATABASE_URL=host=... port=... user=postgres dbname=... \
//	MEDIA_REPORT_HARNESS=1 PARK_MINUTES=8 \
//	go test ./internal/app/ -run TestMediaReportHarness -v

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

func TestMediaReportHarness(t *testing.T) {
	if os.Getenv("MEDIA_REPORT_HARNESS") != "1" {
		t.Skip("media-report harness only")
	}
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	databaseURL := simTestDBURL(t, base)
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	// Seed a week of extraction activity across channels on both rails so
	// the trend shows a visible spike (day -2) and quiet days. Rows go in
	// through the real enqueue path (payload sealed at rest), then get their
	// outcomes stamped and backdated so the 7-day window shows a spike.
	if _, err := repository.RawExec(ctx, `TRUNCATE inbound_messages, media_usage_log CASCADE`); err != nil {
		t.Fatal(err)
	}
	seedDay := func(daysAgo int, id, channel, mediaType string, ok bool, tokens int) {
		t.Helper()
		if _, err := repository.EnqueueInboundMessage(ctx, store.InboundMessage{
			ID: id, Channel: channel, Sender: "+" + id, MediaType: mediaType, MediaID: "media-" + id,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.RawExec(ctx, fmt.Sprintf(`
			UPDATE inbound_messages
			SET status='processed', media_ok=%t, media_ai_tokens=%d,
			    received_at = now() - interval '%d days'
			WHERE provider_message_id='%s'`, ok, tokens, daysAgo, id)); err != nil {
			t.Fatal(err)
		}
	}
	seedDay(6, "h-wa-1", "whatsapp", "image", true, 210)
	seedDay(5, "h-wa-2", "whatsapp", "voice", true, 320)
	seedDay(5, "h-tg-1", "telegram", "image", false, 150)
	seedDay(3, "h-ig-1", "instagram", "image", true, 95)
	seedDay(3, "h-tt-1", "tiktok", "image", true, 140)
	seedDay(2, "h-wa-3", "whatsapp", "image", true, 2450) // spike day
	seedDay(2, "h-wa-4", "whatsapp", "voice", true, 980)  // spike day
	seedDay(2, "h-tg-2", "telegram", "voice", false, 60)  // spike day
	seedDay(0, "h-wa-5", "whatsapp", "image", false, 0)
	if err := repository.RecordMediaUsage(ctx, store.MediaUsageRecord{Channel: "", MediaKind: "voice", Success: true, AITokens: 88}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(nil)
	defer srv.Close()

	cfg := config.Config{
		AppName:                  "Xego",
		BaseURL:                  srv.URL,
		AuthSessionTTL:           12 * time.Hour,
		Environment:              "test", // non-production → session cookie not Secure
		SessionTTL:               30 * time.Minute,
		RateLimitPublicPerMinute: 6000,
		AITokensPerNGN:           config.EnvInt64("AI_TOKENS_PER_NGN", 250), // naira estimate on the trend hover labels
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	templates, err := template.New("").Funcs(template.FuncMap{
		"money":       domain.FormatNGN,
		"maskPII":     func(s string) string { return s },
		"statusClass": func(status any) string { return strings.ReplaceAll(fmt.Sprint(status), "_", "-") },
		"percent":     func(value float64) string { return fmt.Sprintf("%.1f%%", value) },
		"sub":         func(a, b int64) int64 { return a - b },
		"add":         func(a, b int64) int64 { return a + b },
		"inc":         func(i int64) int64 { return i + 1 },
		"join":        func(items []string, sep string) string { return strings.Join(items, sep) },
		"date": func(v any) string {
			if t, ok := v.(time.Time); ok {
				return t.Local().Format("Jan 2, 2006 3:04 PM")
			}
			return ""
		},
		"collectionFeeKobo": func(p store.PaymentView) int64 { return 0 },
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	a := &App{
		cfg: cfg, logger: logger, store: repository,
		templates: templates, rateLimiter: ratelimit.NewMemory(),
	}
	srv.Config.Handler = a.routes()
	srv.Start()

	// Mint a real admin user + session exactly as the login flow would.
	csrf, err := randomToken(24)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureAdminUser(ctx, "media-report-admin@xego.test",
		"$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0G1S3kQvJmZ1OTZq0nRvLmZvZ6W"); err != nil { // bcrypt hash placeholder; session minted directly
		t.Fatal(err)
	}
	admin, err := repository.AdminUserByEmail(ctx, "media-report-admin@xego.test")
	if err != nil {
		t.Fatal(err)
	}
	if admin == nil {
		t.Fatal("admin user missing after EnsureAdminUser")
	}
	adminID := admin.ID
	if err != nil {
		t.Fatal(err)
	}
	sessionToken := make([]byte, 32)
	if _, err := rand.Read(sessionToken); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAdminSession(ctx, adminID, hex.EncodeToString(sessionToken), csrf, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	fmt.Printf("MEDIAREPORT base=%s\n", srv.URL)
	fmt.Printf("MEDIAREPORT cookie=%s\n", hex.EncodeToString(sessionToken))
	meta := fmt.Sprintf("%s\n%s\n", srv.URL, hex.EncodeToString(sessionToken))
	if out := os.Getenv("MEDIA_REPORT_META_FILE"); out != "" {
		if err := os.WriteFile(out, []byte(meta), 0o644); err != nil {
			t.Logf("write meta file: %v", err)
		}
	}
	_ = middleware.Logger // keep the import aligned with sibling harnesses
	// Park while the browser drives. PARK_MINUTES caps the stay because
	// `go test` panics any test still running at 10 minutes.
	park := 5 * time.Minute
	if v := os.Getenv("PARK_MINUTES"); v != "" {
		if mins, ok := atoi(v); ok && mins > 0 && mins < 9 {
			park = time.Duration(mins) * time.Minute
		}
	}
	time.Sleep(park)
}
