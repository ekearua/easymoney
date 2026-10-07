package app

// Merchant bulk handler guards: CSRF on every mutating route, coordinate
// validation before the store is reachable, and cross-merchant scoping so one
// merchant can never read, mutate, or delete another merchant's locations,
// recipients, or batches.
//
// The scoping test drives the real router with two merchant sessions:
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestMerchantBulk -count=1 -v

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// bulkMerchantRequest builds a request as requireMerchant would: the merchant
// id and CSRF token already resolved from the session, plus any route params.
func bulkMerchantRequest(method, target string, form url.Values, params map[string]string, merchantID uuid.UUID, csrf string) *http.Request {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	ctx := req.Context()
	if len(params) > 0 {
		rc := chi.NewRouteContext()
		for key, value := range params {
			rc.URLParams.Add(key, value)
		}
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rc)
	}
	ctx = context.WithValue(ctx, merchantIDContextKey, merchantID)
	ctx = context.WithValue(ctx, merchantCSRFContextKey, csrf)
	return req.WithContext(ctx)
}

// TestMerchantBulkMutationsRejectBadCSRF proves the token check is the first
// thing every mutating bulk handler does: the store is nil here, so a request
// that got past the guard would panic instead of returning 403.
func TestMerchantBulkMutationsRejectBadCSRF(t *testing.T) {
	a := &App{cfg: config.Config{AppName: "Xego", BulkRadiusM: 50}, logger: slog.Default()}
	merchantID := uuid.New()

	cases := []struct {
		name   string
		method string
		target string
		params map[string]string
		handle func(http.ResponseWriter, *http.Request)
	}{
		{"center create", http.MethodPost, "/merchant/bulk/centers", nil, a.merchantBulkCenterCreate},
		{"center delete", http.MethodPost, "/merchant/bulk/centers/{id}/delete", map[string]string{"id": uuid.NewString()}, a.merchantBulkCenterDelete},
		{"recipient add", http.MethodPost, "/merchant/bulk/centers/{id}/recipients", map[string]string{"id": uuid.NewString()}, a.merchantBulkRecipientAdd},
		{"recipient upload", http.MethodPost, "/merchant/bulk/centers/{id}/recipients/upload", map[string]string{"id": uuid.NewString()}, a.merchantBulkRecipientUpload},
		{"recipient delete", http.MethodPost, "/merchant/bulk/centers/{id}/recipients/{recipientID}/delete", map[string]string{"id": uuid.NewString(), "recipientID": uuid.NewString()}, a.merchantBulkRecipientDelete},
		{"batch create", http.MethodPost, "/merchant/bulk/batches", nil, a.merchantBulkBatchCreate},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, token := range []string{"wrong-token", ""} {
				form := url.Values{"csrf_token": {token}, "name": {"Corner shop"}, "latitude": {"6.5"}, "longitude": {"3.4"}}
				rec := httptest.NewRecorder()
				tc.handle(rec, bulkMerchantRequest(tc.method, tc.target, form, tc.params, merchantID, "session-csrf"))
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "invalid CSRF token") {
					t.Fatalf("token %q: status=%d body=%s, want 403 invalid CSRF token", token, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// TestMerchantBulkCenterCreateValidatesCoordinates locks the input guards on
// center creation: an empty name or an out-of-range coordinate is rejected
// before CreateMerchantCenter is called (the store is nil).
func TestMerchantBulkCenterCreateValidatesCoordinates(t *testing.T) {
	a := &App{cfg: config.Config{AppName: "Xego", BulkRadiusM: 50}, logger: slog.Default()}

	cases := []struct {
		name string
		form url.Values
	}{
		{"missing name", url.Values{"csrf_token": {"ok"}, "latitude": {"6.5"}, "longitude": {"3.4"}}},
		{"non-numeric latitude", url.Values{"csrf_token": {"ok"}, "name": {"Shop"}, "latitude": {"north"}, "longitude": {"3.4"}}},
		{"latitude above 90", url.Values{"csrf_token": {"ok"}, "name": {"Shop"}, "latitude": {"95"}, "longitude": {"3.4"}}},
		{"longitude below -180", url.Values{"csrf_token": {"ok"}, "name": {"Shop"}, "latitude": {"6.5"}, "longitude": {"-200"}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			a.merchantBulkCenterCreate(rec, bulkMerchantRequest(http.MethodPost, "/merchant/bulk/centers", tc.form, nil, uuid.New(), "ok"))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "provide a name and valid coordinates") {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
}

// bulkMerchantTemplates parses the whole template set so merchant pages render
// exactly as they do in production.
func bulkMerchantTemplates(t *testing.T) *template.Template {
	t.Helper()
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
			if value, ok := v.(time.Time); ok {
				return value.Local().Format("Jan 2, 2006 3:04 PM")
			}
			return ""
		},
		"collectionFeeKobo": func(p store.PaymentView) int64 { return 0 },
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	return templates
}

// bulkMerchantClient mints a merchant session and returns a client that
// carries it. Redirects are not followed so a test can assert the target.
func bulkMerchantClient(t *testing.T, baseURL string, repository *store.Store, merchant store.Merchant, ownerID uuid.UUID) (*http.Client, string, string) {
	t.Helper()
	ctx := context.Background()
	token, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := randomToken(24)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateMerchantSession(ctx, merchant.ID, ownerID, token, csrf, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(parsed, []*http.Cookie{{Name: merchantCookieName, Value: token}})
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, token, csrf
}

// TestMerchantBulkHandlersScopeToMerchant runs two merchants through the real
// router: B can never see, mutate, or delete A's location, recipients, or
// batch, and every batch-create failure surfaces as the message the merchant
// is meant to read rather than a raw error.
func TestMerchantBulkHandlersScopeToMerchant(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the merchant bulk handler test against a real PostgreSQL")
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
		Environment: "test", RateLimitPublicPerMinute: 6000,
		AuthSessionTTL: 12 * time.Hour, SessionTTL: 30 * time.Minute,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bulk := service.NewBulkPaymentsService(repository,
		map[string]ports.Messenger{service.ChannelWhatsApp: &simMessenger{}}, logger,
		service.BulkPaymentsConfig{BaseURL: cfg.BaseURL, RadiusM: cfg.BulkRadiusM, SendRatePerSec: 1000})
	a := &App{
		cfg: cfg, logger: logger, store: repository, templates: bulkMerchantTemplates(t),
		rateLimiter: ratelimit.NewMemory(), bulkPayments: bulk,
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()

	merchantA, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	merchantB, err := repository.MerchantBySlug(ctx, "kora-books")
	if err != nil {
		t.Fatal(err)
	}
	ownerA, err := repository.MerchantOwnerID(ctx, merchantA.ID)
	if err != nil {
		t.Fatal(err)
	}
	ownerB, err := repository.MerchantOwnerID(ctx, merchantB.ID)
	if err != nil {
		// A seeded merchant without its own owner still gets a session bound
		// to its own merchant id, which is all the middleware checks.
		ownerB = ownerA
	}
	clientA, _, csrfA := bulkMerchantClient(t, srv.URL, repository, merchantA, ownerA)
	clientB, _, csrfB := bulkMerchantClient(t, srv.URL, repository, merchantB, ownerB)

	// Anonymous traffic never reaches a handler.
	anonymous, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Get(srv.URL + "/merchant/bulk/centers")
	if err != nil {
		t.Fatal(err)
	}
	defer anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusSeeOther || anonymous.Header.Get("Location") != "/merchant/login" {
		t.Fatalf("anonymous: status=%d location=%q, want 303 /merchant/login", anonymous.StatusCode, anonymous.Header.Get("Location"))
	}

	post := func(client *http.Client, path string, form url.Values) *http.Response {
		t.Helper()
		resp, err := client.PostForm(srv.URL+path, form)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	get := func(client *http.Client, path string) *http.Response {
		t.Helper()
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	body := func(resp *http.Response) string {
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	// A creates a location; the radius defaults to the configured one.
	create := post(clientA, "/merchant/bulk/centers", url.Values{
		"csrf_token": {csrfA}, "name": {"Scoping spot"},
		"latitude": {"6.5244"}, "longitude": {"3.3792"},
	})
	if create.StatusCode != http.StatusSeeOther || !strings.Contains(create.Header.Get("Location"), "created=1") {
		t.Fatalf("center create: status=%d location=%q", create.StatusCode, create.Header.Get("Location"))
	}
	create.Body.Close()
	centers, err := repository.ListMerchantCenters(ctx, merchantA.ID)
	if err != nil || len(centers) == 0 {
		t.Fatalf("centers: %d (err %v)", len(centers), err)
	}
	center := centers[0]
	if center.RadiusM != cfg.BulkRadiusM {
		t.Fatalf("radius = %d, want the configured default %d", center.RadiusM, cfg.BulkRadiusM)
	}
	centerPath := "/merchant/bulk/centers/" + center.ID.String()

	if got := body(get(clientA, centerPath)); !strings.Contains(got, "Scoping spot") {
		t.Fatalf("owner cannot render its own location: %s", got)
	}
	if resp := get(clientB, centerPath); resp.StatusCode != http.StatusNotFound || !strings.Contains(body(resp), "location not found") {
		t.Fatalf("cross-merchant read: status=%d, want 404 location not found", resp.StatusCode)
	}

	// B cannot add to, or delete, A's location.
	addB := post(clientB, centerPath+"/recipients", url.Values{
		"csrf_token": {csrfB}, "phone": {"+2348010000001"}, "name": {"Stolen"}},
	)
	addB.Body.Close()
	if addB.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-merchant recipient add: status=%d, want 404", addB.StatusCode)
	}
	deleteB := post(clientB, centerPath+"/delete", url.Values{"csrf_token": {csrfB}})
	deleteB.Body.Close()
	if deleteB.StatusCode != http.StatusSeeOther {
		t.Fatalf("cross-merchant delete: status=%d, want 303", deleteB.StatusCode)
	}
	if survivors, err := repository.ListMerchantCenters(ctx, merchantA.ID); err != nil || len(survivors) == 0 {
		t.Fatalf("cross-merchant delete removed the location: %d (err %v)", len(survivors), err)
	}
	users, err := repository.ListCenterUsers(ctx, center.ID)
	if err != nil || len(users) != 0 {
		t.Fatalf("recipient leaked in: %d rows (err %v)", len(users), err)
	}

	// A adds a recipient and renders it back.
	addA := post(clientA, centerPath+"/recipients", url.Values{
		"csrf_token": {csrfA}, "phone": {"+2348010000002"}, "name": {"Loyal customer"}},
	)
	if addA.StatusCode != http.StatusSeeOther || !strings.Contains(addA.Header.Get("Location"), "saved=1") {
		t.Fatalf("recipient add: status=%d location=%q", addA.StatusCode, addA.Header.Get("Location"))
	}
	addA.Body.Close()
	if got := body(get(clientA, centerPath)); !strings.Contains(got, "2348010000002") {
		t.Fatalf("recipient missing from the location page: %s", got)
	}

	// redirectError asserts a 303 and returns the message the location page
	// will render from ?error=, decoded exactly as a browser would.
	redirectError := func(resp *http.Response, want string) {
		t.Helper()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("status=%d, want 303 (location %q)", resp.StatusCode, resp.Header.Get("Location"))
		}
		parsed, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatalf("parse location: %v", err)
		}
		if got := parsed.Query().Get("error"); got != want {
			t.Fatalf("error message = %q, want %q", got, want)
		}
	}

	// Batch creation: bad input, an unaffordable batch, and a center B owns.
	redirectError(post(clientA, "/merchant/bulk/batches", url.Values{
		"csrf_token": {csrfA}, "center_id": {center.ID.String()}, "amount_kobo": {"lots"}},
	), "Enter a valid amount")

	// A large amount keeps this unaffordable even against a wallet a previous
	// run funded.
	redirectError(post(clientA, "/merchant/bulk/batches", url.Values{
		"csrf_token": {csrfA}, "center_id": {center.ID.String()}, "amount_kobo": {"1000000000000"}},
	), "Business wallet balance is too low for this batch")

	redirectError(post(clientB, "/merchant/bulk/batches", url.Values{
		"csrf_token": {csrfB}, "center_id": {center.ID.String()}, "amount_kobo": {"100000"}},
	), "Could not start the batch")

	// A batch exists (seeded directly so no wallet funding is needed): only A
	// may read it.
	batch, err := repository.CreateBulkBatch(ctx, store.NewBulkBatch{
		MerchantID: merchantA.ID, CenterID: center.ID, AmountKobo: 50_000,
		Currency: "NGN", TTLSeconds: 300, CreatedBy: ownerA,
	})
	if err != nil {
		t.Fatal(err)
	}
	batchPath := "/merchant/bulk/batches/" + batch.ID.String()
	if got := body(get(clientA, batchPath)); !strings.Contains(got, batch.BatchNo) {
		t.Fatalf("owner cannot render its own batch: %s", got)
	}
	if resp := get(clientB, batchPath); resp.StatusCode != http.StatusNotFound || !strings.Contains(body(resp), "batch not found") {
		t.Fatalf("cross-merchant batch read: status=%d, want 404 batch not found", resp.StatusCode)
	}
	if resp := get(clientA, "/merchant/bulk/batches"); resp.StatusCode != http.StatusOK || !strings.Contains(body(resp), batch.BatchNo) {
		t.Fatalf("batch list: status=%d, want the batch rendered", resp.StatusCode)
	}
}
