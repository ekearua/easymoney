package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/ports"
	dataprovider "whatsapp-payment-demo/internal/providers/data"
	identityprovider "whatsapp-payment-demo/internal/providers/identity"
	"whatsapp-payment-demo/internal/providers/instagram"
	screeningprovider "whatsapp-payment-demo/internal/providers/screening"
	"whatsapp-payment-demo/internal/providers/tiktok"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// newChannelWebhookApp builds a minimal App wired for Instagram/TikTok webhook
// tests: real routes, real store, fake messengers so outbound sends are
// captured without network access.
func newChannelWebhookApp(t *testing.T, ctx context.Context, repository *store.Store, messengers map[string]ports.Messenger, cfg config.Config) (*App, *httptest.Server) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	payments := service.NewPaymentService(cfg, repository, map[string]ports.PaymentGateway{
		service.ProviderInterswitch:  &simGateway{store: repository},
		service.ProviderBankTransfer: &simGateway{store: repository},
	}, service.NewProviderRouter(nil, logger), logger)
	data := service.NewDataService(repository, payments, dataprovider.NewSimulator())
	convo := service.NewConversationService(cfg, repository, payments, data, messengers,
		nil, identityprovider.NewSimulator(), screeningprovider.NewSimulator())
	a := &App{
		cfg: cfg, logger: logger, store: repository,
		payments: payments, data: data, conversation: convo,
		templates: nil, rateLimiter: ratelimit.NewMemory(),
	}
	// The webhook handlers guard on the channel client being configured.
	if cfg.InstagramEnabled {
		a.instagram = instagram.New(cfg.InstagramAppSecret, cfg.InstagramAccessToken, cfg.InstagramIGID, cfg.InstagramGraphVersion)
	}
	if cfg.TikTokEnabled {
		a.tiktok = tiktok.New(cfg.TikTokAppSecret, cfg.TikTokAccessToken, cfg.TikTokAPIBase)
	}
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	return a, srv
}

func instagramSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestInstagramWebhookEndToEnd(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the Instagram webhook test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated webhook test in short mode")
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

	const appSecret = "test-app-secret"
	const verifyToken = "test-verify-token"
	cfg := config.Config{
		AppName:                    "Xego",
		BaseURL:                    "https://demo.xego.ng",
		WebFlowsEnabled:            false,
		MessageLogEnabled:          true,
		SessionTTL:                 30 * time.Minute,
		PaymentMinKobo:             10_000,
		PaymentMaxKobo:             10_000_000,
		InstagramEnabled:           true,
		InstagramAppSecret:         appSecret,
		InstagramAccessToken:       "test-token",
		InstagramIGID:              "ig-business-1",
		InstagramVerifyToken:       verifyToken,
		RateLimitWebhooksPerMinute: 600,
		RateLimitPublicPerMinute:   600,
	}
	messenger := &simMessenger{}
	a, srv := newChannelWebhookApp(t, ctx, repository, map[string]ports.Messenger{
		service.ChannelInstagram: messenger,
	}, cfg)

	// 1. Webhook verification handshake.
	verifyResp, err := http.Get(srv.URL + "/webhooks/instagram?hub.mode=subscribe&hub.verify_token=" + verifyToken + "&hub.challenge=12345")
	if err != nil {
		t.Fatal(err)
	}
	verifyBody, _ := io.ReadAll(verifyResp.Body)
	verifyResp.Body.Close()
	if verifyResp.StatusCode != http.StatusOK || string(verifyBody) != "12345" {
		t.Fatalf("verification handshake failed: status=%d body=%q", verifyResp.StatusCode, string(verifyBody))
	}

	// 2. Signed message POST: "menu" from a new Instagram user.
	payload := []byte(`{"object":"instagram","entry":[{"id":"ig-business-1","messaging":[{"sender":{"id":"igsid-e2e-1"},"recipient":{"id":"ig-business-1"},"timestamp":1700000000,"message":{"mid":"m-e2e-1","text":"menu"}}]}]}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/instagram", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hub-Signature-256", instagramSignature(appSecret, payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed message POST returned %d", resp.StatusCode)
	}

	// The inbound message is enqueued; process it synchronously.
	// The user is auto-created by resolveUser on first Handle.
	a.processInboundMessages(ctx)

	// 3. The user exists with the IGSID identity and a session was created.
	user, err := repository.FindUserByChannelHandle(ctx, "instagram", "igsid-e2e-1")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == uuid.Nil {
		t.Fatal("Instagram user was not created by the webhook flow")
	}
	if !user.InstagramVerifiedAt.Valid {
		t.Fatal("Instagram verified timestamp should be set on first contact")
	}

	// 4. A bad signature is rejected with 401 and recorded as rejected.
	badPayload := []byte(`{"object":"instagram","entry":[{"id":"ig-business-1","messaging":[{"sender":{"id":"igsid-e2e-2"},"timestamp":1700000001,"message":{"mid":"m-e2e-2","text":"menu"}}]}]}`)
	badReq, err := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/instagram", strings.NewReader(string(badPayload)))
	if err != nil {
		t.Fatal(err)
	}
	badReq.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature should be rejected with 401, got %d", badResp.StatusCode)
	}
}

func TestTikTokWebhookEndToEnd(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the TikTok webhook test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated webhook test in short mode")
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

	const appSecret = "test-tiktok-secret"
	cfg := config.Config{
		AppName:                    "Xego",
		BaseURL:                    "https://demo.xego.ng",
		WebFlowsEnabled:            false,
		MessageLogEnabled:          true,
		SessionTTL:                 30 * time.Minute,
		PaymentMinKobo:             10_000,
		PaymentMaxKobo:             10_000_000,
		TikTokEnabled:              true,
		TikTokAppSecret:            appSecret,
		TikTokAccessToken:          "test-token",
		RateLimitWebhooksPerMinute: 600,
		RateLimitPublicPerMinute:   600,
	}
	messenger := &simMessenger{}
	a, srv := newChannelWebhookApp(t, ctx, repository, map[string]ports.Messenger{
		service.ChannelTikTok: messenger,
	}, cfg)

	// Signed message POST from a new TikTok user. The signature timestamp must
	// be fresh — the client rejects stale timestamps (5-minute window).
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	eventJSON := `{"event_id":"evt-e2e-1","event_type":"message.received","timestamp":` + timestamp + `000,"data":{"conversation_id":"conv-e2e-1","sender":{"open_id":"open-e2e-1","union_id":"union-e2e-1"},"content":{"text":"menu"}}}`
	payload := []byte(`{"event":` + mustQuoteJSON(t, eventJSON) + `}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/tiktok", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	_, _ = mac.Write([]byte(timestamp + "." + string(payload)))
	req.Header.Set("TikTok-Signature", "t="+timestamp+",s="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed TikTok POST returned %d", resp.StatusCode)
	}

	a.processInboundMessages(ctx)

	// Outbound replies are addressed to the provider conversation id (the
	// TikTok Business Messaging reply-to address), not the open_id.
	sent := messenger.snapshot()
	if len(sent) == 0 {
		t.Fatal("the conversation should have sent a reply to the TikTok user")
	}
	if sent[0].to != "conv-e2e-1" {
		t.Fatalf("TikTok reply should address conversation_id conv-e2e-1, got %q", sent[0].to)
	}

	user, err := repository.FindUserByChannelHandle(ctx, "tiktok", "open-e2e-1")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == uuid.Nil {
		t.Fatal("TikTok user was not created by the webhook flow")
	}
	if !user.TikTokVerifiedAt.Valid {
		t.Fatal("TikTok verified timestamp should be set on first contact")
	}
	if !user.TikTokUnionID.Valid || user.TikTokUnionID.String != "union-e2e-1" {
		t.Fatalf("union id should be stored, got %#v", user.TikTokUnionID)
	}

	// A bad signature is rejected with 401.
	badPayload := []byte(`{"event":"{\"event_id\":\"evt-e2e-2\",\"data\":{\"sender\":{\"open_id\":\"open-e2e-2\"},\"content\":{\"text\":\"menu\"}}}"}`)
	badReq, err := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/tiktok", strings.NewReader(string(badPayload)))
	if err != nil {
		t.Fatal(err)
	}
	badReq.Header.Set("TikTok-Signature", "t=1700000000,s=deadbeef")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature should be rejected with 401, got %d", badResp.StatusCode)
	}
}

func mustQuoteJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
