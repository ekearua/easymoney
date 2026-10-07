package app

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// bulkLocationJSON decodes the JSON body an endpoint returned so a test can
// assert the machine-readable error instead of a substring of the response.
func bulkLocationJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return payload
}

// TestBulkLocationCaptureValidatesPayload locks the input guards on the
// unauthenticated capture endpoint. The orchestrator is deliberately nil here:
// every malformed request must be rejected by validation alone, so a crafted
// body can never reach a dependency or panic the handler.
func TestBulkLocationCaptureValidatesPayload(t *testing.T) {
	a := &App{cfg: config.Config{AppName: "Xego"}, logger: slog.Default()}

	cases := []struct {
		name        string
		contentType string
		body        string
		wantError   string
	}{
		{"malformed json", "application/json", "{not json", "invalid payload"},
		{"non-uuid id", "application/json", `{"id":"nope","latitude":6.5,"longitude":3.3}`, "invalid request"},
		{"missing form id", "application/x-www-form-urlencoded", "latitude=6.5&longitude=3.3", "invalid request"},
		{"garbage form id", "application/x-www-form-urlencoded", "id=%7Bnope%7D&latitude=6.5", "invalid request"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/bulk/loc", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()

			a.publicBulkLocationCapture(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("content type = %q, want application/json", got)
			}
			if got := bulkLocationJSON(t, rec)["error"]; got != tc.wantError {
				t.Fatalf("error = %v, want %q", got, tc.wantError)
			}
		})
	}
}

// TestBulkLocationPageGrantsGeolocation locks the fallback page contract: it
// re-enables geolocation for this origin (the app-wide security header denies
// it), renders the item id for the capture script, and serves the script
// itself from the same origin required by the CSP.
func TestBulkLocationPageGrantsGeolocation(t *testing.T) {
	templates, err := template.New("").ParseFS(web.Assets, "templates/bulk_location.html")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: config.Config{AppName: "Xego"}, logger: slog.Default(), templates: templates}
	itemID := uuid.NewString()

	rec := httptest.NewRecorder()
	a.publicBulkLocationPage(rec, httptest.NewRequest(http.MethodGet, "/bulk/loc?id="+itemID, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Permissions-Policy"); got != "geolocation=(self)" {
		t.Fatalf("Permissions-Policy = %q, want %q", got, "geolocation=(self)")
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-item="` + itemID + `"`,
		"/static/bulk-location.js",
		"Confirm your location",
		"Xego",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page is missing %q", want)
		}
	}

	// A request without an id still renders the panel so the script can report
	// the dead link instead of showing a blank page.
	rec = httptest.NewRecorder()
	a.publicBulkLocationPage(rec, httptest.NewRequest(http.MethodGet, "/bulk/loc", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-item=""`) {
		t.Fatalf("id-less page: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestBulkLocationCaptureEndToEnd drives the browser fallback through the real
// router: the page is served with geolocation re-enabled over the global
// security header, a form submission for an open request is recorded and
// scored against the center, and a replay or a dead id gets 410 instead of a
// second silent write.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestBulkLocationCaptureEndToEnd -v
func TestBulkLocationCaptureEndToEnd(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the location-capture test against a real PostgreSQL")
	}
	ctx := context.Background()
	repository, err := store.Open(ctx, bulkTestDBURL(t, base))
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

	cfg := config.Config{
		AppName: "Xego", BaseURL: "https://xego.test", BulkRadiusM: 50,
		RateLimitPublicPerMinute: 600,
	}
	logger := slog.Default()
	bulk := service.NewBulkPaymentsService(repository,
		map[string]ports.Messenger{service.ChannelWhatsApp: &simMessenger{}}, logger,
		service.BulkPaymentsConfig{BaseURL: cfg.BaseURL, RadiusM: cfg.BulkRadiusM, SendRatePerSec: 1000})
	templates, err := template.New("").ParseFS(web.Assets, "templates/bulk_location.html")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{
		cfg: cfg, logger: logger, store: repository, templates: templates,
		rateLimiter: ratelimit.NewMemory(), bulkPayments: bulk,
	}
	handler := a.routes()

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	run := fmt.Sprintf("%05d", time.Now().UnixNano()%100_000)
	phone := "+234809" + run + "01"
	center, err := repository.CreateMerchantCenter(ctx, merchant.ID, "Bulk loc "+run, 6.5244, 3.3792, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertCenterUser(ctx, merchant.ID, center.ID, phone, "Loc Tester", "manual"); err != nil {
		t.Fatal(err)
	}
	user, err := repository.GetOrCreateUser(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	// CreateBulkBatch (not the service) is used so this endpoint test never
	// depends on a funded wallet: the readiness gate is the store's own.
	batch, err := repository.CreateBulkBatch(ctx, store.NewBulkBatch{
		MerchantID: merchant.ID, CenterID: center.ID, AmountKobo: 100_000,
		Currency: "NGN", TTLSeconds: 300, CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if err := bulk.RunStage1(ctx, batch.ID); err != nil {
		t.Fatalf("stage 1: %v", err)
	}
	items, err := repository.ListBulkBatchItems(ctx, batch.ID, "", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("batch items: %d (err %v)", len(items), err)
	}
	item := items[0]
	if item.Status != store.BulkItemLocationSent {
		t.Fatalf("item status = %s, want %s", item.Status, store.BulkItemLocationSent)
	}

	// The page must win over the global "geolocation=()" security header.
	pageRec := httptest.NewRecorder()
	handler.ServeHTTP(pageRec, httptest.NewRequest(http.MethodGet, "/bulk/loc?id="+item.ID.String(), nil))
	if pageRec.Code != http.StatusOK {
		t.Fatalf("page status = %d, want 200", pageRec.Code)
	}
	if got := pageRec.Header().Get("Permissions-Policy"); got != "geolocation=(self)" {
		t.Fatalf("Permissions-Policy = %q, want %q", got, "geolocation=(self)")
	}

	form := url.Values{
		"id": {item.ID.String()}, "latitude": {"6.5245"},
		"longitude": {"3.3792"}, "accuracy": {"12"},
	}
	req := httptest.NewRequest(http.MethodPost, "/bulk/loc", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("capture status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := bulkLocationJSON(t, rec)["status"]; got != "recorded" {
		t.Fatalf("capture status = %v, want %q", got, "recorded")
	}

	// A replay and a dead id are both "no longer open", never a second write.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"replay", `{"id":"` + item.ID.String() + `","latitude":6.5245,"longitude":3.3792,"accuracy":12}`},
		{"dead id", `{"id":"` + uuid.NewString() + `","latitude":6.5245,"longitude":3.3792,"accuracy":12}`},
	} {
		req := httptest.NewRequest(http.MethodPost, "/bulk/loc", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusGone {
			t.Fatalf("%s: status = %d, want 410 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		if got := bulkLocationJSON(t, rec)["error"]; got != "this location request is no longer open" {
			t.Fatalf("%s: error = %v", tc.name, got)
		}
	}

	updated, err := repository.ListBulkBatchItems(ctx, batch.ID, "", 10)
	if err != nil || len(updated) != 1 {
		t.Fatalf("reload items: %d (err %v)", len(updated), err)
	}
	scored := updated[0]
	if scored.Status != store.BulkItemLocationReceived {
		t.Fatalf("item status = %s, want %s", scored.Status, store.BulkItemLocationReceived)
	}
	if scored.Latitude == nil || scored.Longitude == nil || scored.DistanceM == nil {
		t.Fatalf("capture was not stored: %+v", scored)
	}
	if *scored.DistanceM > float64(center.RadiusM) {
		t.Fatalf("distance = %v m, want <= the %d m radius", *scored.DistanceM, center.RadiusM)
	}
}
