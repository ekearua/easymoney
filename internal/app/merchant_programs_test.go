package app

// Sponsor program console guards: CSRF on every mutating route, sponsor tools
// enabled by creating a program, and cross-sponsor scoping so one sponsor can
// never read or mutate another sponsor's program, allow-list, or enrollments.
//
// The scoping test drives the real router with three merchant sessions:
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestMerchantProgram -count=1 -v

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
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
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// TestMerchantProgramTemplateRenders executes the sponsor page with a fully
// populated dataset so an execution-time error (a missing field or a mismatched
// comparison) fails here rather than in the browser.
func TestMerchantProgramTemplateRenders(t *testing.T) {
	templates, err := template.New("").Funcs(template.FuncMap{
		"money":       domain.FormatNGN,
		"statusClass": func(status any) string { return strings.ReplaceAll(fmt.Sprint(status), "_", "-") },
		"date": func(v any) string {
			if value, ok := v.(time.Time); ok {
				return value.Local().Format("Jan 2, 2006 3:04 PM")
			}
			return ""
		},
	}).ParseFS(web.Assets, "templates/merchant_programs.html", "templates/merchant_nav.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	sponsorID := uuid.New()
	programID := uuid.New()
	userID := uuid.New()
	data := map[string]any{
		"AppName":    "Xego",
		"Title":      "Programs",
		"CSRF":       "csrf",
		"MerchantID": sponsorID,
		"IsSponsor":  true,
		"Programs": []store.MerchantProgram{{
			ID: programID, Code: "round-1", Name: "Round one", SponsorMerchantID: sponsorID,
			SponsorName: "Sponsor A", PurposeLabel: "Inputs", DefaultValidityDays: 30,
			Status: store.ProgramStatusActive,
		}},
		"Merchants": []store.Merchant{
			{ID: sponsorID, Name: "Sponsor A", Slug: "sponsor-a", Active: true},
			{ID: uuid.New(), Name: "Acceptor", Slug: "acceptor", Active: true},
		},
		"Program": store.MerchantProgram{
			ID: programID, Code: "round-1", Name: "Round one", SponsorMerchantID: sponsorID,
			PurposeLabel: "Inputs", Description: "Q1", Status: store.ProgramStatusActive,
		},
		"Summary": store.ProgramSummary{EnrolledCount: 1, ActivePurseCount: 1, ApprovedMerchants: 1, HeldKobo: 3_000},
		"Members": []store.ProgramMember{{
			ProgramID: programID, MerchantID: uuid.New(), MerchantName: "Acceptor",
			MerchantSlug: "acceptor", Status: "approved",
		}},
		"Enrollments": []store.ProgramEnrollment{{
			ID: uuid.New(), ProgramID: programID, UserID: userID, UserName: "Beneficiary",
			UserPhone: "+2348010000001", Status: "active", EnrolledAt: time.Now(),
		}},
		"Purses": []store.ProgramPurse{{
			ID: uuid.New(), ProgramID: programID, UserID: userID, UserName: "Beneficiary",
			UserPhone: "+2348010000001", Status: "active", OpenedAt: time.Now(), BalanceKobo: 3_000,
		}},
	}
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "merchant_programs.html", data); err != nil {
		t.Fatalf("execute merchant_programs.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Round one", "Acceptor", "Beneficiary", "/merchant/programs/" + programID.String() + "/status"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered page missing %q", want)
		}
	}
	// The sponsor must not be offered as an acceptor of its own program.
	if strings.Contains(out, `<option value="`+sponsorID.String()+`"`) {
		t.Fatalf("sponsor appears in its own allow-list dropdown")
	}
}

// TestMerchantProgramMutationsRejectBadCSRF proves the token check is the first
// thing every mutating program handler does: the store is nil here, so a
// request that got past the guard would panic instead of returning 403.
func TestMerchantProgramMutationsRejectBadCSRF(t *testing.T) {
	a := &App{cfg: config.Config{AppName: "Xego"}, logger: slog.Default()}
	merchantID := uuid.New()
	programID := uuid.NewString()

	cases := []struct {
		name   string
		target string
		params map[string]string
		handle func(http.ResponseWriter, *http.Request)
	}{
		{"program create", "/merchant/programs", nil, a.merchantProgramCreate},
		{"program status", "/merchant/programs/{id}/status", map[string]string{"id": programID}, a.merchantProgramSetStatus},
		{"merchant approve", "/merchant/programs/{id}/merchants", map[string]string{"id": programID}, a.merchantProgramAddMerchant},
		{"merchant block", "/merchant/programs/{id}/merchants/{merchantID}/block", map[string]string{"id": programID, "merchantID": uuid.NewString()}, a.merchantProgramBlockMerchant},
		{"merchant remove", "/merchant/programs/{id}/merchants/{merchantID}/remove", map[string]string{"id": programID, "merchantID": uuid.NewString()}, a.merchantProgramRemoveMerchant},
		{"beneficiary enroll", "/merchant/programs/{id}/enroll", map[string]string{"id": programID}, a.merchantProgramEnroll},
		{"beneficiary remove", "/merchant/programs/{id}/enrollments/{userID}/remove", map[string]string{"id": programID, "userID": uuid.NewString()}, a.merchantProgramRemoveEnrollment},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, token := range []string{"wrong-token", ""} {
				form := url.Values{"csrf_token": {token}}
				rec := httptest.NewRecorder()
				tc.handle(rec, bulkMerchantRequest(http.MethodPost, tc.target, form, tc.params, merchantID, "session-csrf"))
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "invalid CSRF token") {
					t.Fatalf("token %q: status=%d body=%s, want 403 invalid CSRF token", token, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// TestMerchantProgramHandlersScopeToSponsor runs three merchants through the
// real router: creating a program enables sponsor tools, one sponsor can never
// read or mutate another's program, and a merchant with no program is refused
// sponsor tools but can still reach the list page and create its own.
func TestMerchantProgramHandlersScopeToSponsor(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the sponsor program handler test against a real PostgreSQL")
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
	a := &App{
		cfg: cfg, logger: logger, store: repository, templates: bulkMerchantTemplates(t),
		rateLimiter: ratelimit.NewMemory(),
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()

	sponsorA, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	sponsorB, err := repository.MerchantBySlug(ctx, "kora-books")
	if err != nil {
		t.Fatal(err)
	}
	plainC, err := repository.MerchantBySlug(ctx, "bright-fix-ng")
	if err != nil {
		t.Fatal(err)
	}
	ownerA, err := repository.MerchantOwnerID(ctx, sponsorA.ID)
	if err != nil {
		t.Fatal(err)
	}
	ownerB, err := repository.MerchantOwnerID(ctx, sponsorB.ID)
	if err != nil {
		ownerB = ownerA
	}
	ownerC, err := repository.MerchantOwnerID(ctx, plainC.ID)
	if err != nil {
		ownerC = ownerA
	}
	clientA, _, csrfA := bulkMerchantClient(t, srv.URL, repository, sponsorA, ownerA)
	clientB, _, csrfB := bulkMerchantClient(t, srv.URL, repository, sponsorB, ownerB)
	clientC, _, csrfC := bulkMerchantClient(t, srv.URL, repository, plainC, ownerC)

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
	programIDFrom := func(resp *http.Response) uuid.UUID {
		t.Helper()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("create status=%d, want 303 (body %s)", resp.StatusCode, body(resp))
		}
		parsed, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatalf("parse location: %v", err)
		}
		id, err := uuid.Parse(parsed.Query().Get("id"))
		if err != nil {
			t.Fatalf("program id from location %q: %v", resp.Header.Get("Location"), err)
		}
		return id
	}

	// Only an administrator can approve a sponsor. Approve A and B; leave C a
	// standard merchant.
	if err := repository.SetMerchantType(ctx, sponsorA.ID, store.MerchantTypeSponsor); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetMerchantType(ctx, sponsorB.ID, store.MerchantTypeSponsor); err != nil {
		t.Fatal(err)
	}

	// A creates a program.
	programA := programIDFrom(post(clientA, "/merchant/programs", url.Values{
		"csrf_token": {csrfA}, "code": {"sponsor-a-round"}, "name": {"Sponsor A round"},
	}))
	if got := body(get(clientA, "/merchant/programs?id="+programA.String())); !strings.Contains(got, "Sponsor A round") {
		t.Fatalf("owner cannot render its own program: %s", got)
	}

	// B is an approved sponsor too, so it clears requireSponsor and hits the
	// ownership guard: reading or writing A's program is a 404, never a success.
	if resp := get(clientB, "/merchant/programs?id="+programA.String()); resp.StatusCode != http.StatusNotFound {
		resp.Body.Close()
		t.Fatalf("cross-sponsor read: status=%d, want 404", resp.StatusCode)
	}
	for _, path := range []string{
		"/merchant/programs/" + programA.String() + "/status",
		"/merchant/programs/" + programA.String() + "/merchants",
		"/merchant/programs/" + programA.String() + "/merchants/" + plainC.ID.String() + "/block",
		"/merchant/programs/" + programA.String() + "/merchants/" + plainC.ID.String() + "/remove",
		"/merchant/programs/" + programA.String() + "/enroll",
		"/merchant/programs/" + programA.String() + "/enrollments/" + uuid.NewString() + "/remove",
	} {
		form := url.Values{
			"csrf_token": {csrfB}, "status": {"inactive"},
			"merchant_id": {plainC.ID.String()}, "phone": {"+2348010000099"},
		}
		resp := post(clientB, path, form)
		code := resp.StatusCode
		resp.Body.Close()
		if code != http.StatusNotFound {
			t.Fatalf("cross-sponsor write %s: status=%d, want 404", path, code)
		}
	}
	programB := programIDFrom(post(clientB, "/merchant/programs", url.Values{
		"csrf_token": {csrfB}, "code": {"sponsor-b-round"}, "name": {"Sponsor B round"},
	}))

	// A's program is untouched by B's attempts; both sponsors own exactly one.
	if program, err := repository.ProgramByID(ctx, programA); err != nil || program.Status != store.ProgramStatusActive {
		t.Fatalf("A's program status = %q (err %v), want active", program.Status, err)
	}
	ownedA, _ := repository.ProgramsForSponsor(ctx, sponsorA.ID)
	ownedB, _ := repository.ProgramsForSponsor(ctx, sponsorB.ID)
	if len(ownedA) != 1 || ownedA[0].ID != programA {
		t.Fatalf("A owns %d programs, want just its own", len(ownedA))
	}
	if len(ownedB) != 1 || ownedB[0].ID != programB {
		t.Fatalf("B owns %d programs, want just its own", len(ownedB))
	}

	// C is not a sponsor: the console refuses it. The list page is still served
	// so it can read what sponsor tools are, but every write is a 403.
	if resp := get(clientC, "/merchant/programs"); resp.StatusCode != http.StatusOK || !strings.Contains(body(resp), "Sponsor tools are not enabled") {
		t.Fatalf("non-sponsor list page: status=%d, want the enable notice", resp.StatusCode)
	}
	if resp := post(clientC, "/merchant/programs", url.Values{
		"csrf_token": {csrfC}, "code": {"sponsor-c-round"}, "name": {"Sponsor C round"}},
	); resp.StatusCode != http.StatusForbidden {
		resp.Body.Close()
		t.Fatalf("non-sponsor create: status=%d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := post(clientC, "/merchant/programs/"+programA.String()+"/enroll", url.Values{
		"csrf_token": {csrfC}, "phone": {"+2348010000099"}},
	); resp.StatusCode != http.StatusForbidden {
		resp.Body.Close()
		t.Fatalf("unrelated merchant write: status=%d, want 403", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
}

// TestAdminProgramSetSponsorRejectsBadCSRF locks the token check on the admin
// sponsor-approval route: the store is nil, so a request past the guard panics.
func TestAdminProgramSetSponsorRejectsBadCSRF(t *testing.T) {
	a := &App{cfg: config.Config{AppName: "Xego"}, logger: slog.Default()}
	for _, token := range []string{"wrong-token", ""} {
		form := url.Values{"csrf_token": {token}, "merchant_id": {uuid.NewString()}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/admin/programs/sponsor", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), csrfContextKey, "session-csrf"))
		a.adminProgramSetSponsor(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "invalid CSRF token") {
			t.Fatalf("token %q: status=%d body=%s, want 403 invalid CSRF token", token, rec.Code, rec.Body.String())
		}
	}
}
