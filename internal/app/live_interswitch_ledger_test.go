package app

// TestLiveInterswitchPaymentLedger proves the money-in ledger path with a REAL
// Interswitch sandbox payment: a payment draft whose provider reference is a
// transaction that was actually completed at the gateway (form render → test
// card → PIN/OTP → approve) is run through the production
// PaymentService.VerifyAndApply path, which requeries the real
// gettransaction.json endpoint and posts the ledger. The test asserts the
// payment lands in succeeded and the double-entry book nets to zero.
//
// The card payment itself is completed with a browser against
// newwebpay-sandbox.interswitchng.com (the hosted checkout) — this test only
// replays the confirmation, exactly as the app does on the gateway return hop:
//
//	TEST_DATABASE_URL=postgres://... \
//	INTERSWITCH_SANDBOX_SMOKE=1 \
//	INTERSWITCH_CLIENT_ID=... INTERSWITCH_CLIENT_SECRET=... \
//	INTERSWITCH_MERCHANT_CODE=MX... INTERSWITCH_PAY_ITEM_ID=... \
//	ISW_PAID_REF=<txn reference actually paid at the gateway> \
//	go test ./internal/app/ -run TestLiveInterswitchPaymentLedger -v

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/interswitch"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

func TestLiveInterswitchPaymentLedger(t *testing.T) {
	if os.Getenv("INTERSWITCH_SANDBOX_SMOKE") != "1" {
		t.Skip("set INTERSWITCH_SANDBOX_SMOKE=1 to post a real payment confirmation through the ledger")
	}
	base := os.Getenv("TEST_DATABASE_URL")
	clientID := os.Getenv("INTERSWITCH_CLIENT_ID")
	clientSecret := os.Getenv("INTERSWITCH_CLIENT_SECRET")
	merchantCode := os.Getenv("INTERSWITCH_MERCHANT_CODE")
	payItemID := os.Getenv("INTERSWITCH_PAY_ITEM_ID")
	paidRef := os.Getenv("ISW_PAID_REF")
	if base == "" || clientID == "" || clientSecret == "" || merchantCode == "" || payItemID == "" || paidRef == "" {
		t.Skip("set TEST_DATABASE_URL plus INTERSWITCH_CLIENT_ID/SECRET/MERCHANT_CODE/PAY_ITEM_ID and ISW_PAID_REF (a reference actually paid at the sandbox)")
	}
	mode := "TEST"
	paidAmount := int64(50_000) // kobo; matches the real ₦500 gateway transaction we requery

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

	cfg := config.Config{
		AppName:              "Xego",
		BaseURL:              "https://demo.xego.ng",
		PaymentMinKobo:       10_000,
		PaymentMaxKobo:       10_000_000,
		FeeCardBPS:           200,
		FeeCardFixedKobo:     10_000,
		FeeCardCapKobo:       350_000,
		FeeDVABPS:            150,
		FeeDVAFixedKobo:      0,
		FeeDVACapKobo:        150_000,
		FeeTransferBPS:       180,
		FeeTransferFixedKobo: 0,
		FeeTransferCapKobo:   250_000,
		FeeNIPPayoutFlatKobo: 10_000,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	gateway := interswitch.New(interswitch.Options{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		MerchantCode: merchantCode,
		PayItemID:    payItemID,
		BaseURL:      "https://sandbox.interswitchng.com",
		Mode:         mode,
	})
	gateways := map[string]ports.PaymentGateway{
		service.ProviderInterswitch: gateway,
	}
	payments := service.NewPaymentService(cfg, repository, gateways, service.NewProviderRouter(gateways, logger), logger)

	// Payer mirroring the real individual-onboarding sequence (L2, clear file).
	payer, err := repository.GetOrCreateUser(ctx, "+2348012340777")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, payer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertIndividualProfile(ctx, payer.ID, "Ada Obi",
		time.Date(1992, 5, 24, 0, 0, 0, 0, time.UTC), "14 Admiralty Way, Lekki, Lagos", "Product designer"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID: payer.ID, Provider: "interswitch-live", Decision: kyc.ScreenClear,
	}, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTierTo(ctx, payer.ID, kyc.TierL2,
		[]string{kyc.EvChannelConfirmed, kyc.EvIdentityOnFile}, nil); err != nil {
		t.Fatal(err)
	}
	profile, err := repository.KYCProfileByUser(ctx, payer.ID)
	if err != nil {
		t.Fatal(err)
	}

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}

	// Draft semantics identical to CreateCollectionDraft: charge base+fee and
	// pick the base so the total equals the real gateway amount we requery.
	baseKobo := int64(39_216)
	fee := service.XegoCollectionFee(cfg, "card", baseKobo).FeeKobo
	if baseKobo+fee != paidAmount {
		t.Fatalf("base %d + fee %d = %d, want the paid amount %d", baseKobo, fee, baseKobo+fee, paidAmount)
	}
	meta, err := json.Marshal(map[string]any{
		"base_amount_kobo":    baseKobo,
		"collection_fee_kobo": fee,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := domain.NewReceiptToken()
	if err != nil {
		t.Fatal(err)
	}
	checkoutToken, err := domain.NewCheckoutToken()
	if err != nil {
		t.Fatal(err)
	}
	payment := domain.Payment{
		ID:                uuid.New(),
		UserID:            payer.ID,
		MerchantID:        merchant.ID,
		AmountKobo:        paidAmount,
		Currency:          "NGN",
		Status:            domain.StatusDraft,
		Provider:          service.ProviderInterswitch,
		ProviderReference: paidRef,
		Channel:           service.ChannelWhatsApp,
		Recipient:         "wa:" + payer.WhatsAppNumber,
		ReceiptToken:      token,
		CheckoutToken:     checkoutToken,
		Metadata:          meta,
	}
	if err := repository.ReserveAllowance(ctx, store.AllowanceReservation{
		AccountType: store.AccountIndividual,
		SubjectID:   payer.ID,
		Direction:   kyc.DirIn,
		Tier:        profile.Tier,
		AmountKobo:  paidAmount,
		Ref:         payment.ProviderReference,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreatePayment(ctx, payment); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionPayment(ctx, payment.ID, domain.StatusAwaitingConfirmation, "live-test",
		map[string]any{"merchant": merchant.Slug}); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("\n========== LIVE INTERSWITCH LEDGER ==========\n")
	fmt.Printf("  Real gateway transaction %s (%s) confirmed via signed requery.\n", paidRef, domain.FormatNGN(paidAmount))
	fmt.Printf("  Draft %s… awaiting_confirmation → initialized → VerifyAndApply (production path)…\n", payment.ID.String()[:8])

	// Mirror the hosted-checkout sequence exactly: initializing the gateway
	// checkout moves the payment awaiting_confirmation → initialized
	// (SetCheckout), which is the state from which a verified success lands.
	draft, err := repository.PaymentByID(ctx, payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payments.InitializeCheckout(ctx, draft); err != nil {
		t.Fatalf("InitializeCheckout (real gateway): %v", err)
	}

	// The production money-in path: requery the REAL gateway, then transition
	// and apply post-success hooks (collection splits) when approved.
	updated, changed, err := payments.VerifyAndApply(ctx, paidRef, "payment-return")
	if err != nil {
		t.Fatalf("VerifyAndApply against the real gateway failed: %v", err)
	}
	if !changed {
		t.Fatalf("VerifyAndApply did not change the payment")
	}
	if updated.Status != domain.StatusSucceeded {
		t.Fatalf("payment status = %q, want %q", updated.Status, domain.StatusSucceeded)
	}
	fmt.Printf("  ✅ Payment %s… → %s\n", payment.ID.String()[:8], updated.Status)

	verifyLedgerForPayment(t, ctx, repository, payment.ID, "Live Interswitch make payment")
	fmt.Printf("  Ref: %s | amount: %s | channel: card | merchant: %s\n", paidRef, domain.FormatNGN(updated.AmountKobo), merchant.Slug)
}
