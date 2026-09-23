package app

// Channel smoke test: drives every chat channel's REAL webhook route with
// correctly-signed payloads through the REAL provider parsers and the REAL
// conversation FSM, and asserts that text, image (OCR), and voice (STT)
// inbound each resolve to a durable FSM action — not just a 200.
//
// Media bytes never touch the network: App.Download fans out to provider
// clients whose graph hosts are not env-redirectable, so the conversation
// service gets a recording ports.MediaDownloader that asserts the
// channel-correct media identity (channel + id/url) survived
// webhook → enqueue → FSM. The providers' real HTTP download paths are
// covered by their own client tests.
//
// TikTok note: its webhook parser maps image DMs only (by design), so the
// voice case is an explicit, documented skip rather than a fake pass.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/app/ -run TestChannelWebhookSmoke -v

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
	"sync"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/instagram"
	"whatsapp-payment-demo/internal/providers/telegram"
	"whatsapp-payment-demo/internal/providers/tiktok"
	"whatsapp-payment-demo/internal/providers/whatsapp"
	"whatsapp-payment-demo/internal/ratelimit"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// recordingDownloader captures the media identity each FSM extraction asks
// for and hands back fixed bytes, so no provider network is touched.
type recordingDownloader struct {
	mu    sync.Mutex
	asked []string
	bytes []byte
	mime  string
}

func (d *recordingDownloader) Download(_ context.Context, channel, mediaID, mediaURL string) ([]byte, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked = append(d.asked, channel+":"+mediaID+mediaURL)
	return d.bytes, d.mime, nil
}

func (d *recordingDownloader) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.asked))
	copy(out, d.asked)
	return out
}

// queueReader is an ImageReader that pops scripted OCR text per call, so a
// photographed slip can "contain" an account number or phone number. When no
// script is queued it falls back to the stubAI canned receipt text so the
// channel walks keep their fixed media behavior.
type queueReader struct {
	mu       sync.Mutex
	items    []string
	fallback func(prompt string) string
}

func (q *queueReader) ReadImage(_ context.Context, _ []byte, _, prompt string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) > 0 {
		item := q.items[0]
		q.items = q.items[1:]
		return item, nil
	}
	if q.fallback != nil {
		return q.fallback(prompt), nil
	}
	return "", nil
}

// queueSTT is a SpeechToText that pops scripted transcription text per call,
// so a voice note can "say" an amount or bank name. When no script is queued
// it falls back to the stubAI canned transcription.
type queueSTT struct {
	mu       sync.Mutex
	items    []string
	fallback string
}

func (q *queueSTT) Transcribe(context.Context, []byte, string, string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) > 0 {
		item := q.items[0]
		q.items = q.items[1:]
		return item, nil
	}
	return q.fallback, nil
}

// signedBody posts payload to url with the given signature header, asserting a 200.
func signedBody(t *testing.T, url, header, signature, payload string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(header, signature)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed POST to %s returned %d", url, resp.StatusCode)
	}
}

// waSignature is the Meta X-Hub-Signature-256 for body under secret.
func waSignature(secret string, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// lastTextReply asserts a non-empty text reply was sent and returns it.
func lastTextReply(t *testing.T, m *simMessenger) string {
	t.Helper()
	sent := m.snapshot()
	if len(sent) == 0 {
		t.Fatal("expected a reply, got none")
	}
	last := sent[len(sent)-1]
	if last.kind != "text" || strings.TrimSpace(last.body) == "" {
		t.Fatalf("expected a non-empty text reply, got kind=%q body=%q", last.kind, last.body)
	}
	return last.body
}

// menuReply asserts a reply was sent and returns the most recent kind.
func menuReply(t *testing.T, m *simMessenger, label string) string {
	t.Helper()
	sent := m.snapshot()
	if len(sent) == 0 {
		t.Fatalf("%s: expected at least one reply, got none", label)
	}
	return sent[len(sent)-1].kind
}

// TestChannelWebhookSmoke walks WhatsApp, Telegram, Instagram, and TikTok
// end to end: signed webhook POST → real parser → enqueued inbound →
// processInboundMessages → real FSM. Each channel proves a text case
// (menu), an image case (OCR text resolves to the pay intent), and a voice
// case (STT text resolves to the pay intent). TikTok voice is skipped:
// TikTok DMs carry images only.
func TestChannelWebhookSmoke(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run the channel smoke test against a real PostgreSQL")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated channel smoke test in short mode")
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

	const waSecret = "test-wa-secret"
	const tgSecret = "test-tg-secret"
	const igSecret = "test-ig-secret"
	const ttSecret = "test-tt-secret"

	cfg := config.Config{
		AppName:                    "Xego",
		BaseURL:                    "https://demo.xego.ng",
		WebFlowsEnabled:            false,
		SessionTTL:                 30 * time.Minute,
		PaymentMinKobo:             10_000,
		PaymentMaxKobo:             10_000_000,
		AIEnabled:                  true,
		WhatsAppAppSecret:          waSecret,
		WhatsAppAccessToken:        "test-wa-token",
		WhatsAppPhoneNumberID:      "wa-phone-1",
		WhatsAppVerifyToken:        "test-wa-verify",
		TelegramEnabled:            true,
		TelegramBotToken:           "test-tg-token",
		TelegramWebhookSecret:      tgSecret,
		InstagramEnabled:           true,
		InstagramAppSecret:         igSecret,
		InstagramAccessToken:       "test-ig-token",
		InstagramIGID:              "ig-business-1",
		InstagramVerifyToken:       "test-ig-verify",
		TikTokEnabled:              true,
		TikTokAppSecret:            ttSecret,
		TikTokAccessToken:          "test-tt-token",
		RateLimitWebhooksPerMinute: 600,
		RateLimitPublicPerMinute:   600,
	}

	ocrQueue := &queueReader{items: []string{}, fallback: func(prompt string) string {
		if strings.Contains(strings.ToLower(prompt), "nin") || strings.Contains(strings.ToLower(prompt), "bvn") {
			return "NIN: 12345678901"
		}
		return "Payment reference: REF-12345678 Amount: NGN 5,000 Date: 2026-01-15"
	}}
	sttQueue := &queueSTT{items: []string{}, fallback: "pay 5000 to jumia"}

	waMessenger := &simMessenger{}
	tgMessenger := &simMessenger{}
	igMessenger := &simMessenger{}
	ttMessenger := &simMessenger{}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	payments := service.NewPaymentService(cfg, repository, map[string]ports.PaymentGateway{
		service.ProviderInterswitch:  &simGateway{store: repository},
		service.ProviderBankTransfer: &simGateway{store: repository},
	}, service.NewProviderRouter(nil, logger), logger)
	data := service.NewDataService(repository, payments, stubDataProvider{})
	convo := service.NewConversationService(cfg, repository, payments, data, map[string]ports.Messenger{
		service.ChannelWhatsApp:  waMessenger,
		service.ChannelTelegram:  tgMessenger,
		service.ChannelInstagram: igMessenger,
		service.ChannelTikTok:    ttMessenger,
	}, nil, stubIdentityVerifier{}, stubSanctionsScreener{})
	downloader := &recordingDownloader{bytes: []byte("fake-media-bytes"), mime: "image/jpeg"}
	convo.SetMediaDownloader(downloader)
	// The AI chat classifier stays stubAI (its "pay …" intents drive the
	// merchant-pay cases); OCR/STT use scripted queues so photographed slips
	// and voice notes can carry per-step content (phone, amount, bank, account).
	convo.SetMediaProviders(ocrQueue, sttQueue, stubAI{})

	a := &App{
		cfg: cfg, logger: logger, store: repository,
		payments: payments, data: data, conversation: convo,
		templates: nil, rateLimiter: ratelimit.NewMemory(),
	}
	// The webhook handlers guard on the channel client being configured, and
	// App.Download (not exercised here) fans out through these clients.
	a.whatsapp = whatsapp.New(cfg.WhatsAppAppSecret, cfg.WhatsAppAccessToken, cfg.WhatsAppPhoneNumberID, "v23.0", "en")
	a.telegram = telegram.New(cfg.TelegramBotToken, cfg.TelegramAPIBase, cfg.TelegramWebhookSecret)
	a.instagram = instagram.New(cfg.InstagramAppSecret, cfg.InstagramAccessToken, cfg.InstagramIGID, "v23.0")
	a.tiktok = tiktok.New(cfg.TikTokAppSecret, cfg.TikTokAccessToken, cfg.TikTokAPIBase)

	srv := httptest.NewServer(a.routes())
	defer srv.Close()

	// -----------------------------------------------------------------
	// WhatsApp: HMAC-signed webhook. WhatsApp users are auto-onboarded by
	// GetOrCreateUser on first contact (onboarding_complete=true at insert),
	// so the text case doubles as the auto-onboarding proof; then the media
	// cases run against the confirmed account.
	// -----------------------------------------------------------------
	t.Run("whatsapp", func(t *testing.T) {
		waPost := func(payload string) {
			signedBody(t, srv.URL+"/webhooks/whatsapp", "X-Hub-Signature-256", waSignature(waSecret, payload), payload)
		}
		waText := func(id, text string) {
			t.Helper()
			waPost(`{"entry":[{"changes":[{"value":{"messages":[{"id":"` + id + `","from":"2348010000042","type":"text","text":{"body":"` + text + `"},"timestamp":"1700000000"}]}}]}]}`)
			a.processInboundMessages(ctx)
		}
		waInteractive := func(id string) {
			t.Helper()
			waPost(`{"entry":[{"changes":[{"value":{"messages":[{"id":"` + id + `","from":"2348010000042","type":"interactive","interactive":{"type":"list_reply","list_reply":{"id":"menu_pay_individual","title":"Pay an individual"}},"timestamp":"1700000000"}]}}]}]}`)
			a.processInboundMessages(ctx)
		}
		waImagePost := func(id, mediaID string) {
			t.Helper()
			waPost(`{"entry":[{"changes":[{"value":{"messages":[{"id":"` + id + `","from":"2348010000042","type":"image","image":{"id":"` + mediaID + `","mime_type":"image/jpeg","caption":""},"timestamp":"1700000001"}]}}]}]}`)
			a.processInboundMessages(ctx)
		}
		waVoicePost := func(id, mediaID string) {
			t.Helper()
			waPost(`{"entry":[{"changes":[{"value":{"messages":[{"id":"` + id + `","from":"2348010000042","type":"audio","audio":{"id":"` + mediaID + `","mime_type":"audio/ogg"},"timestamp":"1700000002"}]}}]}]}`)
			a.processInboundMessages(ctx)
		}

		// Webhook verification handshake.
		verifyResp, err := http.Get(srv.URL + "/webhooks/whatsapp?hub.mode=subscribe&hub.verify_token=" + cfg.WhatsAppVerifyToken + "&hub.challenge=777")
		if err != nil {
			t.Fatal(err)
		}
		verifyBody, _ := io.ReadAll(verifyResp.Body)
		verifyResp.Body.Close()
		if verifyResp.StatusCode != http.StatusOK || string(verifyBody) != "777" {
			t.Fatalf("WhatsApp verification handshake failed: status=%d body=%q", verifyResp.StatusCode, string(verifyBody))
		}

		// Text case: the very first message gets the menu, and the user was
		// auto-created fully onboarded (no onboarding gate on WhatsApp).
		waText("sm-wa-1", "menu")
		if kind := menuReply(t, waMessenger, "WhatsApp menu"); kind != "interactive" {
			t.Fatalf("menu should send an interactive message, got %q", kind)
		}
		waUser, err := repository.FindUserByChannelHandle(ctx, "whatsapp", "+2348010000042")
		if err != nil {
			t.Fatal(err)
		}
		if !waUser.OnboardingComplete || !waUser.NumberConfirmedAt.Valid {
			t.Fatal("WhatsApp first contact should auto-onboard the account")
		}

		// Mirror the real individual-onboarding sequence (ApplyIndividualProfile):
		// identity profile (sets account_level='individual'), clean screening,
		// then the ladder promotion to L2 — so the payer may send money to
		// other individuals.
		if _, err := repository.UpsertIndividualProfile(ctx, waUser.ID, "Ada Smoke",
			time.Date(1992, 5, 24, 0, 0, 0, 0, time.UTC), "14 Admiralty Way, Lekki, Lagos", "Product designer"); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
			UserID: waUser.ID, Provider: "simulated", Decision: kyc.ScreenClear,
		}, 30*24*time.Hour); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.AdvanceKYCTierTo(ctx, waUser.ID, kyc.TierL2,
			[]string{kyc.EvChannelConfirmed, kyc.EvIdentityOnFile}, nil); err != nil {
			t.Fatal(err)
		}
		waMessenger.reset()

		// Image case: OCR-extracted text resolves to the pay intent.
		waMessenger.reset()
		waPost(`{"entry":[{"changes":[{"value":{"messages":[{"id":"sm-wa-6","from":"2348010000042","type":"image","image":{"id":"wa-img-1","mime_type":"image/jpeg","caption":""},"timestamp":"1700000001"}]}}]}]}`)
		a.processInboundMessages(ctx)
		if sent := waMessenger.snapshot(); len(sent) == 0 {
			t.Fatal("WhatsApp image case produced no reply")
		}
		waSession, err := repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "select_merchant" {
			t.Fatalf("OCR'd text should resolve to merchant selection, session state is %q", waSession.State)
		}
		if got := downloader.requests(); len(got) == 0 || got[len(got)-1] != "whatsapp:wa-img-1" {
			t.Fatalf("download should carry the WhatsApp media id, got %v", got)
		}

		// Voice case: transcribed speech resolves to the pay intent.
		waMessenger.reset()
		waPost(`{"entry":[{"changes":[{"value":{"messages":[{"id":"sm-wa-7","from":"2348010000042","type":"audio","audio":{"id":"wa-voice-1","mime_type":"audio/ogg"},"timestamp":"1700000002"}]}}]}]}`)
		a.processInboundMessages(ctx)
		if sent := waMessenger.snapshot(); len(sent) == 0 {
			t.Fatal("WhatsApp voice case produced no reply")
		}
		waSession, err = repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "select_merchant" {
			t.Fatalf("transcribed text should resolve to merchant selection, session state is %q", waSession.State)
		}
		if got := downloader.requests(); got[len(got)-1] != "whatsapp:wa-voice-1" {
			t.Fatalf("voice download should carry the WhatsApp media id, got %v", got)
		}

		// -------------------------------------------------------------
		// Individual pay: media feeds every step of the pay-an-individual
		// journey. The chat FSM collects recipient phone, amount, bank, and
		// account number (no First Name — identity is the phone number);
		// the image cases photograph the phone/account, the voice case says
		// the amount and bank name.
		// -------------------------------------------------------------
		waMessenger.reset()
		waText("sm-wa-8", "menu")
		waInteractive("menu_pay_individual")
		waSession, err = repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "pay_individual_phone" {
			t.Fatalf("individual pay should start at the phone step, state is %q", waSession.State)
		}

		// Phone step: the recipient's phone arrives as a photographed slip.
		ocrQueue.mu.Lock()
		ocrQueue.items = []string{"08039999900"}
		ocrQueue.mu.Unlock()
		waImagePost("sm-wa-9", "wa-img-2")
		a.processInboundMessages(ctx)
		waSession, err = repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "pay_individual_amount" || waSession.Data["recipient_phone"] != "+2348039999900" {
			t.Fatalf("OCR'd phone should advance to the amount step, state=%q data=%v", waSession.State, waSession.Data)
		}

		// Amount + bank steps: the voice note states the amount; the bank name
		// arrives as a second voice note. A misheard bank name first produces
		// the FSM's friendly re-ask — the trigram directory fuzzy-matches, so
		// the customer gets the searchable bank picker echoing their words —
		// then the corrected name resolves through the alias directory.
		sttQueue.mu.Lock()
		sttQueue.items = []string{"5000", "moonbeam trust", "gtbank"}
		sttQueue.mu.Unlock()
		waVoicePost("sm-wa-10", "wa-voice-2")
		a.processInboundMessages(ctx) // amount step consumes 5000
		waVoicePost("sm-wa-10b", "wa-voice-2b")
		a.processInboundMessages(ctx) // unknown bank: friendly picker re-ask
		waSession, err = repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "pay_individual_bank_code" {
			t.Fatalf("unknown bank should stay on the bank step, state is %q", waSession.State)
		}
		sent := waMessenger.snapshot()
		if len(sent) == 0 || sent[len(sent)-1].kind != "interactive" || !strings.Contains(sent[len(sent)-1].body, "moonbeam trust") {
			unknown := "none"
			if len(sent) > 0 {
				unknown = sent[len(sent)-1].kind + ": " + sent[len(sent)-1].body
			}
			t.Fatalf("unknown bank should produce the searchable picker re-ask, got %q", unknown)
		}
		waVoicePost("sm-wa-11", "wa-voice-3")
		a.processInboundMessages(ctx) // corrected bank: gtbank resolves
		waSession, err = repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "pay_individual_account" || waSession.Data["amount_kobo"] != "500000" || waSession.Data["bank_code"] != "058" {
			t.Fatalf("voice amount+bank should reach the account step, state=%q data=%v", waSession.State, waSession.Data)
		}

		// Account step: a garbled photograph first produces the friendly
		// re-ask (not an error), then the clean slip reads correctly.
		ocrQueue.mu.Lock()
		ocrQueue.items = []string{"01X3#5678 9", "0123456789"}
		ocrQueue.mu.Unlock()
		waImagePost("sm-wa-12", "wa-img-3")
		a.processInboundMessages(ctx) // garbled account: friendly re-ask
		waSession, err = repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "pay_individual_account" {
			t.Fatalf("garbled account should stay on the account step, state is %q", waSession.State)
		}
		if body := lastTextReply(t, waMessenger); !strings.Contains(body, "exactly 10 digits") {
			t.Fatalf("garbled account should produce the friendly re-ask, got %q", body)
		}
		waImagePost("sm-wa-12b", "wa-img-4")
		a.processInboundMessages(ctx) // clean account: advances
		waSession, err = repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "pay_individual_confirm" || waSession.Data["account_number"] != "0123456789" {
			t.Fatalf("OCR'd account number should reach the confirm step, state=%q data=%v", waSession.State, waSession.Data)
		}

		// Confirm: bank-transfer choice mints the payment and returns the
		// hosted-checkout link.
		waMessenger.reset()
		waText("sm-wa-13", "1")
		lastKind, lastBody := "", ""
		for _, m := range waMessenger.snapshot() {
			lastKind, lastBody = m.kind, m.body
			if m.kind == "checkout" {
				break
			}
		}
		if lastKind != "checkout" {
			t.Fatalf("confirm should send the checkout link, last message kind=%q body=%q", lastKind, lastBody)
		}
		if !strings.Contains(lastBody, "0123456789") || !strings.Contains(lastBody, "Guaranty Trust Bank") {
			t.Fatalf("checkout message should echo the recipient details, got %q", lastBody)
		}

		// Payout side: the draft persisted the media-collected details, and
		// the recipient user plus payout destination rows already exist so
		// the post-success hook can settle without session data.
		recent, err := repository.ListPayments(ctx, 5)
		if err != nil {
			t.Fatal(err)
		}
		var draft *store.PaymentView
		for i := range recent {
			var meta struct {
				IndividualPay *struct {
					RecipientPhone string `json:"recipient_phone"`
					BankCode       string `json:"bank_code"`
					BankName       string `json:"bank_name"`
					AccountNumber  string `json:"account_number"`
				} `json:"individual_pay"`
			}
			if json.Unmarshal(recent[i].Metadata, &meta) == nil && meta.IndividualPay != nil {
				draft = &recent[i]
				if meta.IndividualPay.RecipientPhone != "+2348039999900" ||
					meta.IndividualPay.BankCode != "058" ||
					meta.IndividualPay.AccountNumber != "0123456789" {
					t.Fatalf("payment metadata should carry the media-collected details, got %+v", meta.IndividualPay)
				}
				break
			}
		}
		if draft == nil {
			t.Fatal("the individual-pay draft payment should exist")
		}
		if draft.Status != domain.StatusAwaitingConfirmation {
			t.Fatalf("draft should await confirmation, status is %q", draft.Status)
		}
		recipientUser, err := repository.FindUserByChannelHandle(ctx, "whatsapp", "+2348039999900")
		if err != nil {
			t.Fatal(err)
		}
		dests, err := repository.UserPayoutDestinations(ctx, recipientUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(dests) == 0 {
			t.Fatal("the recipient payout destination should be saved at draft time")
		}
		dest := dests[0]
		if dest.BankCode != "058" || dest.BankName != "Guaranty Trust Bank" || dest.AccountNumber != "0123456789" {
			t.Fatalf("payout destination should match the media-collected details, got bank=%s/%s account=%s", dest.BankCode, dest.BankName, dest.AccountNumber)
		}

		// -------------------------------------------------------------
		// Deterministic instruction parsing: free text (typed or OCR'd from a
		// photo) routes without the AI classifier. A merchant instruction lands
		// on the merchant picker pre-searched by name; a full individual
		// instruction seeds the chat FSM straight at the confirm step.
		// -------------------------------------------------------------
		resetChatSession(t, ctx, repository, waUser.ID)
		waMessenger.reset()
		waText("sm-wa-14", "send 2000 to Kora Books")
		waSession, err = repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "select_merchant" {
			t.Fatalf("a parsed merchant instruction should land on merchant selection, state is %q", waSession.State)
		}
		lastSent := waMessenger.snapshot()
		if len(lastSent) == 0 || lastSent[len(lastSent)-1].kind != "interactive" || !strings.Contains(lastSent[len(lastSent)-1].body, "Kora Books") {
			got := "none"
			if len(lastSent) > 0 {
				got = lastSent[len(lastSent)-1].kind + ": " + lastSent[len(lastSent)-1].body
			}
			t.Fatalf("the merchant picker should pre-search the parsed merchant, got %q", got)
		}

		resetChatSession(t, ctx, repository, waUser.ID)
		waMessenger.reset()
		ocrQueue.mu.Lock()
		ocrQueue.items = []string{"send 5000 to 08039999900 gtbank 0123456789"}
		ocrQueue.mu.Unlock()
		waImagePost("sm-wa-15", "wa-img-5")
		a.processInboundMessages(ctx)
		waSession, err = repository.LoadSession(ctx, waUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waSession.State != "pay_individual_confirm" {
			t.Fatalf("a full individual instruction should prefill straight to confirm, state is %q", waSession.State)
		}
		if waSession.Data["recipient_phone"] != "+2348039999900" || waSession.Data["amount_kobo"] != "500000" ||
			waSession.Data["bank_code"] != "058" || waSession.Data["account_number"] != "0123456789" {
			t.Fatalf("parsed individual instruction should prefill recipient, amount, bank, and account, data=%v", waSession.Data)
		}
	})

	// -----------------------------------------------------------------
	// Telegram: secret-header webhook. The fresh user is confirmed through
	// the same store setter the account-confirmation handler uses, so the
	// walk focuses on media → FSM.
	// -----------------------------------------------------------------
	t.Run("telegram", func(t *testing.T) {
		tgPost := func(payload string) {
			signedBody(t, srv.URL+"/webhooks/telegram", "X-Telegram-Bot-Api-Secret-Token", tgSecret, payload)
		}

		// Text case: menu (creates the user).
		tgPost(`{"update_id":9101,"message":{"message_id":1,"text":"menu","chat":{"id":552001},"from":{"id":552001,"username":"smoke_tg"}}}`)
		a.processInboundMessages(ctx)
		if kind := menuReply(t, tgMessenger, "Telegram menu"); kind != "interactive" {
			t.Fatalf("Telegram menu should send an interactive message, got %q", kind)
		}
		tgUser, err := repository.FindUserByChannelHandle(ctx, "telegram", "552001")
		if err != nil {
			t.Fatal(err)
		}
		if !tgUser.TelegramVerifiedAt.Valid {
			t.Fatal("Telegram verified timestamp should be set on first contact")
		}
		// Complete onboarding for the channel exactly as the confirm handler does.
		if err := repository.ConfirmTelegramAccount(ctx, tgUser.ID); err != nil {
			t.Fatal(err)
		}

		// Image case: photo OCR resolves to the pay intent.
		tgMessenger.reset()
		tgPost(`{"update_id":9102,"message":{"message_id":2,"chat":{"id":552001},"from":{"id":552001,"username":"smoke_tg"},"photo":[{"file_id":"tg-photo-1","width":100,"height":100}]}}`)
		a.processInboundMessages(ctx)
		if sent := tgMessenger.snapshot(); len(sent) == 0 {
			t.Fatal("Telegram image case produced no reply")
		}
		tgSession, err := repository.LoadSession(ctx, tgUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if tgSession.State != "select_merchant" {
			t.Fatalf("Telegram OCR text should resolve to merchant selection, state is %q", tgSession.State)
		}
		if got := downloader.requests(); got[len(got)-1] != "telegram:tg-photo-1" {
			t.Fatalf("Telegram download should carry the file id, got %v", got)
		}

		// Voice case: STT resolves to the pay intent.
		tgMessenger.reset()
		tgPost(`{"update_id":9103,"message":{"message_id":3,"chat":{"id":552001},"from":{"id":552001,"username":"smoke_tg"},"voice":{"file_id":"tg-voice-1","mime_type":"audio/ogg"}}}`)
		a.processInboundMessages(ctx)
		if sent := tgMessenger.snapshot(); len(sent) == 0 {
			t.Fatal("Telegram voice case produced no reply")
		}
		tgSession, err = repository.LoadSession(ctx, tgUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if tgSession.State != "select_merchant" {
			t.Fatalf("Telegram transcribed text should resolve to merchant selection, state is %q", tgSession.State)
		}
		if got := downloader.requests(); got[len(got)-1] != "telegram:tg-voice-1" {
			t.Fatalf("Telegram voice download should carry the file id, got %v", got)
		}

		// Deterministic parsing still routes on Telegram, but a not-yet-L2
		// sender meets the same individual-pay KYC gate as the typed flow —
		// proof the fresh image instruction was understood as "pay an
		// individual" and gated, not silently reclassified.
		resetChatSession(t, ctx, repository, tgUser.ID)
		tgMessenger.reset()
		ocrQueue.mu.Lock()
		ocrQueue.items = []string{"send 5000 to 08039999900 gtbank 0123456789"}
		ocrQueue.mu.Unlock()
		tgPost(`{"update_id":9104,"message":{"message_id":4,"chat":{"id":552001},"from":{"id":552001,"username":"smoke_tg"},"photo":[{"file_id":"tg-photo-2","width":100,"height":100}]}}`)
		a.processInboundMessages(ctx)
		if body := lastTextReply(t, tgMessenger); !strings.Contains(body, "Level 2") {
			t.Fatalf("an individual instruction for an unapproved Telegram user should hit the L2 gate, got %q", body)
		}
	})

	// -----------------------------------------------------------------
	// Instagram: HMAC-signed webhook with attachment media.
	// -----------------------------------------------------------------
	t.Run("instagram", func(t *testing.T) {
		igPost := func(payload string) {
			signedBody(t, srv.URL+"/webhooks/instagram", "X-Hub-Signature-256", instagramSignature(igSecret, []byte(payload)), payload)
		}

		// Text case: menu (creates the user).
		igPost(`{"object":"instagram","entry":[{"id":"ig-business-1","messaging":[{"sender":{"id":"igsid-smoke-1"},"recipient":{"id":"ig-business-1"},"timestamp":1700000000,"message":{"mid":"sm-ig-1","text":"menu"}}]}]}`)
		a.processInboundMessages(ctx)
		if kind := menuReply(t, igMessenger, "Instagram menu"); kind != "interactive" {
			t.Fatalf("Instagram menu should send an interactive message, got %q", kind)
		}
		igUser, err := repository.FindUserByChannelHandle(ctx, "instagram", "igsid-smoke-1")
		if err != nil {
			t.Fatal(err)
		}
		if !igUser.InstagramVerifiedAt.Valid {
			t.Fatal("Instagram verified timestamp should be set on first contact")
		}
		if err := repository.ConfirmInstagramAccount(ctx, igUser.ID); err != nil {
			t.Fatal(err)
		}

		// Image case: attachment OCR resolves to the pay intent.
		igMessenger.reset()
		igPost(`{"object":"instagram","entry":[{"id":"ig-business-1","messaging":[{"sender":{"id":"igsid-smoke-1"},"recipient":{"id":"ig-business-1"},"timestamp":1700000001,"message":{"mid":"sm-ig-2","attachments":[{"type":"image","payload":{"url":"https://media.example/ig-img-1"}}]}}]}]}`)
		a.processInboundMessages(ctx)
		if sent := igMessenger.snapshot(); len(sent) == 0 {
			t.Fatal("Instagram image case produced no reply")
		}
		igSession, err := repository.LoadSession(ctx, igUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if igSession.State != "select_merchant" {
			t.Fatalf("Instagram OCR text should resolve to merchant selection, state is %q", igSession.State)
		}
		if got := downloader.requests(); got[len(got)-1] != "instagram:https://media.example/ig-img-1" {
			t.Fatalf("Instagram download should carry the attachment URL, got %v", got)
		}

		// Voice case: audio attachment STT resolves to the pay intent.
		igMessenger.reset()
		igPost(`{"object":"instagram","entry":[{"id":"ig-business-1","messaging":[{"sender":{"id":"igsid-smoke-1"},"recipient":{"id":"ig-business-1"},"timestamp":1700000002,"message":{"mid":"sm-ig-3","attachments":[{"type":"audio","payload":{"url":"https://media.example/ig-voice-1"}}]}}]}]}`)
		a.processInboundMessages(ctx)
		if sent := igMessenger.snapshot(); len(sent) == 0 {
			t.Fatal("Instagram voice case produced no reply")
		}
		igSession, err = repository.LoadSession(ctx, igUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if igSession.State != "select_merchant" {
			t.Fatalf("Instagram transcribed text should resolve to merchant selection, state is %q", igSession.State)
		}
		if got := downloader.requests(); got[len(got)-1] != "instagram:https://media.example/ig-voice-1" {
			t.Fatalf("Instagram voice download should carry the attachment URL, got %v", got)
		}

		// Deterministic parsing still routes on Instagram, but the unapproved
		// sender meets the individual-pay L2 gate like any other channel.
		resetChatSession(t, ctx, repository, igUser.ID)
		igMessenger.reset()
		ocrQueue.mu.Lock()
		ocrQueue.items = []string{"send 5000 to 08039999900 gtbank 0123456789"}
		ocrQueue.mu.Unlock()
		igPost(`{"object":"instagram","entry":[{"id":"ig-business-1","messaging":[{"sender":{"id":"igsid-smoke-1"},"recipient":{"id":"ig-business-1"},"timestamp":1700000003,"message":{"mid":"sm-ig-4","attachments":[{"type":"image","payload":{"url":"https://media.example/ig-img-2"}}]}}]}]}`)
		a.processInboundMessages(ctx)
		if body := lastTextReply(t, igMessenger); !strings.Contains(body, "Level 2") {
			t.Fatalf("an individual instruction for an unapproved Instagram user should hit the L2 gate, got %q", body)
		}
	})

	// -----------------------------------------------------------------
	// TikTok: timestamped-signature webhook. Image DMs only — the voice
	// case is a documented capability skip.
	// -----------------------------------------------------------------
	t.Run("tiktok", func(t *testing.T) {
		event := func(eventID, openID, text, mediaURL string) string {
			inner := `{"event_id":"` + eventID + `","event_type":"message.received","data":{"conversation_id":"conv-smoke-1","sender":{"open_id":"` + openID + `","union_id":"union-smoke-1"},"content":{"text":"` + text + `"`
			if mediaURL != "" {
				inner += `,"media":[{"url":"` + mediaURL + `","mime_type":"image/jpeg"}]`
			}
			inner += `}}}`
			return `{"event":` + mustQuoteJSON(t, inner) + `}`
		}
		ttPost := func(payload string) {
			timestamp := strconv.FormatInt(time.Now().Unix(), 10)
			mac := hmac.New(sha256.New, []byte(ttSecret))
			_, _ = mac.Write([]byte(timestamp + "." + payload))
			sig := "t=" + timestamp + ",s=" + hex.EncodeToString(mac.Sum(nil))
			signedBody(t, srv.URL+"/webhooks/tiktok", "TikTok-Signature", sig, payload)
		}

		// Text case: menu (creates the user).
		ttPost(event("sm-tt-1", "open-smoke-1", "menu", ""))
		a.processInboundMessages(ctx)
		if sent := ttMessenger.snapshot(); len(sent) == 0 {
			t.Fatal("TikTok text case produced no reply")
		}
		ttUser, err := repository.FindUserByChannelHandle(ctx, "tiktok", "open-smoke-1")
		if err != nil {
			t.Fatal(err)
		}
		if !ttUser.TikTokVerifiedAt.Valid {
			t.Fatal("TikTok verified timestamp should be set on first contact")
		}
		if err := repository.ConfirmTikTokAccount(ctx, ttUser.ID); err != nil {
			t.Fatal(err)
		}

		// Image case: media OCR resolves to the pay intent.
		ttMessenger.reset()
		ttPost(event("sm-tt-2", "open-smoke-1", "", "https://media.example/tt-img-1"))
		a.processInboundMessages(ctx)
		if sent := ttMessenger.snapshot(); len(sent) == 0 {
			t.Fatal("TikTok image case produced no reply")
		}
		ttSession, err := repository.LoadSession(ctx, ttUser.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ttSession.State != "select_merchant" {
			t.Fatalf("TikTok OCR text should resolve to merchant selection, state is %q", ttSession.State)
		}
		if got := downloader.requests(); got[len(got)-1] != "tiktok:https://media.example/tt-img-1" {
			t.Fatalf("TikTok download should carry the media URL, got %v", got)
		}

		// Voice case: TikTok DMs carry images only; there is no audio media
		// type to send. Skip explicitly so coverage is honest.
		t.Skip("TikTok DMs carry images only — no voice media type exists on this channel")
	})

	// All media extraction must have gone through the one downloader, carrying
	// the channel-correct identity (id for WhatsApp/Telegram, URL for IG/TikTok).
	if got := downloader.requests(); len(got) != 16 {
		t.Fatalf("expected 16 media downloads (2 WA walk + 6 WA individual + 1 WA parse + 2 TG + 1 TG parse + 2 IG + 1 IG parse + 1 TT), got %d: %v", len(got), got)
	}
}
