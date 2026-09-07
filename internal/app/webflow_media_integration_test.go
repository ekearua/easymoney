package app

// TestPostgresWebFlowMediaUpload drives the web-flow media endpoint through
// the real HTTP routes against a live PostgreSQL: an image upload (OCR) and a
// voice upload (STT) with the simulated AI provider, asserting the extracted
// text lands in the flow payload and that only the text is kept (the raw file
// is never stored). Run with:
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestPostgresWebFlowMediaUpload -v

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	aiprovider "whatsapp-payment-demo/internal/providers/ai"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

func TestPostgresWebFlowMediaUpload(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the media upload integration test")
	}
	ctx := context.Background()

	// Dedicated <database>_simulation database (same convention as the flow
	// simulations) so the store package's truncating suites can never wipe it.
	repository, err := store.Open(ctx, simTestDBURL(t, base))
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
	key, err := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key)

	user, err := repository.GetOrCreateUser(ctx, "+2348096660001")
	if err != nil {
		t.Fatal(err)
	}
	flow, err := repository.MintWebFlow(ctx, user.ID, "whatsapp", "individual_upgrade",
		map[string]string{}, "profile", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	templates, err := template.New("").ParseFS(web.Assets, "templates/webflow.html")
	if err != nil {
		t.Fatalf("parse webflow template: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sim := aiprovider.NewSimulated()
	a := &App{
		cfg: configForMediaTest(), logger: logger, store: repository,
		templates: templates, rateLimiter: ratelimit.NewMemory(),
		imageReader: sim, speechToText: sim,
	}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()

	// --- image upload: OCR extracts the identity number from the slip -------
	resp := mediaUpload(t, srv.URL, flow.Token, "id_slip",
		"Extract the 11-digit NIN or BVN number from this identity slip. Reply with only the digits.",
		"slip.png", "image/png", []byte("\x89PNG fake slip bytes"))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("image upload: status=%d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/w/"+flow.Token {
		t.Fatalf("image upload: Location=%q, want /w/%s", loc, flow.Token)
	}
	resp.Body.Close()

	got, err := repository.WebFlowByToken(ctx, flow.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Payload["id_slip"] != "NIN: 12345678901" {
		t.Fatalf("OCR text not in payload: got %q", got.Payload["id_slip"])
	}

	// The re-rendered step shows what was read so the customer confirms it
	// before continuing.
	pageResp, err := http.Get(srv.URL + "/w/" + flow.Token)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(pageResp.Body)
	pageResp.Body.Close()
	if !strings.Contains(string(body), "NIN: 12345678901") {
		t.Fatalf("profile step should surface the read slip text, page lacks it")
	}

	// --- voice upload: STT transcription lands under the field name ---------
	resp = mediaUpload(t, srv.URL, flow.Token, "id_voice",
		"", "note.m4a", "audio/mp4", []byte("fake audio bytes"))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("voice upload: status=%d, want 303", resp.StatusCode)
	}
	resp.Body.Close()

	got, err = repository.WebFlowByToken(ctx, flow.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Payload["id_voice"] != "pay 5000 to jumia" {
		t.Fatalf("transcribed text not in payload: got %q", got.Payload["id_voice"])
	}

	// --- non-image/audio upload is rejected without touching the payload ----
	resp = mediaUpload(t, srv.URL, flow.Token, "id_slip",
		"Extract the 11-digit NIN or BVN number from this identity slip. Reply with only the digits.",
		"slip.pdf", "application/pdf", []byte("%PDF-1.4 fake"))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("pdf upload: status=%d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	got, err = repository.WebFlowByToken(ctx, flow.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Payload["id_slip"] != "NIN: 12345678901" {
		t.Fatalf("rejected upload must not overwrite the payload, got %q", got.Payload["id_slip"])
	}

	// --- an invalid field name is rejected outright -------------------------
	resp = mediaUpload(t, srv.URL, flow.Token, "id slip;drop",
		"", "x.png", "image/png", []byte("x"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad field name: status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

// configForMediaTest returns the minimum config the web-flow pages need to
// render (app name, deep link, base URL, public rate limit).
func configForMediaTest() config.Config {
	return config.Config{
		AppName:                    "Xego",
		BaseURL:                    "https://demo.xego.ng",
		WhatsAppPhoneNumber:        "2348000000000",
		RateLimitPublicPerMinute:   600,
		RateLimitScanPerMinute:     600,
		RateLimitWebhooksPerMinute: 600,
		RateLimitAPIKeysPerMinute:  600,
	}
}

// mediaUpload posts one multipart file to /w/{token}/media and returns the
// response (caller closes the body).
func mediaUpload(t *testing.T, baseURL, token, field, prompt, filename, mime string, data []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("field", field)
	if prompt != "" {
		_ = mw.WriteField("prompt", prompt)
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	h.Set("Content-Type", mime)
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/w/"+token+"/media", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
