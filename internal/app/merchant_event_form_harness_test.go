package app

// Merchant event-form harness boots the real app, mints a merchant session
// for a seeded merchant, and prints the new-event form URL plus session
// cookie so a Playwright script can drive the form end to end (tier rows,
// custom fields, Active toggle, submit) with screenshots.
//
//	TEST_DATABASE_URL=postgres://... XEGO_EVENT_FORM=1 \
//	go test ./internal/app/ -run TestMerchantEventFormHarness -v

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

func TestMerchantEventFormHarness(t *testing.T) {
	if os.Getenv("XEGO_EVENT_FORM") != "1" {
		t.Skip("event-form harness only")
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

	// Bind the listener before building cfg so BaseURL is real from the start.
	srv := httptest.NewUnstartedServer(nil)
	defer srv.Close()

	cfg := config.Config{
		AppName:                  "Xego",
		BaseURL:                  srv.URL,
		AuthSessionTTL:           12 * time.Hour,
		Environment:              "test", // non-production → session cookie not Secure
		SessionTTL:               30 * time.Minute,
		RateLimitPublicPerMinute: 6000,
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

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	ownerID, err := repository.MerchantOwnerID(ctx, merchant.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := randomToken(24)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateMerchantSession(ctx, merchant.ID, ownerID, token, csrf, time.Now().Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}

	fmt.Printf("EVENTFORM base=%s\n", srv.URL)
	fmt.Printf("EVENTFORM cookie=%s\n", token)
	fmt.Printf("EVENTFORM csrf=%s\n", csrf)
	if out := os.Getenv("EVENTFORM_META_FILE"); out != "" {
		meta := fmt.Sprintf("%s\n%s\n%s\n", srv.URL, token, csrf)
		if err := os.WriteFile(out, []byte(meta), 0o644); err != nil {
			t.Logf("write meta file: %v", err)
		}
	}
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

func atoi(s string) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n > 0 || s == "0"
}
