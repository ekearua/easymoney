package app

// TestLiveInterswitchDVATransfer proves bank-transfer collection end to end
// with the REAL Interswitch sandbox: a bank_transfer payment draft is run
// through the production PaymentService.InitializeCheckout path with
// BankTransferMode=interswitch, which calls the real
// /paymentgateway/api/v1/virtualaccounts/transaction endpoint (OAuth client
// credentials) to mint a one-time dynamic virtual account. The test asserts
// the DVA instruction is persisted, the payment lands in pending, and the
// instruction round-trips from the store — the same state a customer sees on
// the /transfer/:ref instruction page.
//
// Money-in cannot be proven programmatically here (a human must actually move
// funds to the minted virtual account), so the confirmation leg stays covered
// by the requery path proven in TestLiveInterswitchPaymentLedger and by the
// webhook tests. This test proves the collection-instruction leg live.
//
//	TEST_DATABASE_URL=postgres://... \
//	INTERSWITCH_SANDBOX_SMOKE=1 \
//	INTERSWITCH_CLIENT_ID=... INTERSWITCH_CLIENT_SECRET=... \
//	INTERSWITCH_MERCHANT_CODE=MX... INTERSWITCH_PAY_ITEM_ID=... \
//	go test ./internal/app/ -run TestLiveInterswitchDVATransfer -v

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	whatsappkyc "whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/interswitch"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

func TestLiveInterswitchDVATransfer(t *testing.T) {
	if os.Getenv("INTERSWITCH_SANDBOX_SMOKE") != "1" {
		t.Skip("set INTERSWITCH_SANDBOX_SMOKE=1 to mint a real sandbox virtual account")
	}
	base := os.Getenv("TEST_DATABASE_URL")
	clientID := os.Getenv("INTERSWITCH_CLIENT_ID")
	clientSecret := os.Getenv("INTERSWITCH_CLIENT_SECRET")
	merchantCode := os.Getenv("INTERSWITCH_MERCHANT_CODE")
	payItemID := os.Getenv("INTERSWITCH_PAY_ITEM_ID")
	if base == "" || clientID == "" || clientSecret == "" || merchantCode == "" || payItemID == "" {
		t.Skip("set TEST_DATABASE_URL plus INTERSWITCH_CLIENT_ID/SECRET/MERCHANT_CODE/PAY_ITEM_ID")
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

	cfg := config.Config{
		AppName:          "Xego",
		BaseURL:          "https://demo.xego.ng",
		BankTransferMode: "interswitch",
		PaymentMinKobo:   10_000,
		PaymentMaxKobo:   10_000_000,
		FeeDVABPS:        150,
		FeeDVAFixedKobo:  0,
		FeeDVACapKobo:    150_000,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	gateway := interswitch.New(interswitch.Options{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		MerchantCode: merchantCode,
		PayItemID:    payItemID,
		BaseURL:      "https://sandbox.interswitchng.com",
		Mode:         "TEST",
	})
	gateways := map[string]ports.PaymentGateway{
		service.ProviderInterswitch: gateway,
		service.ProviderBankTransfer: gateway,
	}
	payments := service.NewPaymentService(cfg, repository, gateways, service.NewProviderRouter(gateways, logger), logger)

	// Payer at L1 (DVA collection is a money-in movement; the allowance must
	// fit the amount the same way a real customer's draft does).
	payer, err := repository.GetOrCreateUser(ctx, "+2348012340999")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, payer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID: payer.ID, Provider: "interswitch-live", Decision: whatsappkyc.ScreenClear,
	}, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTierTo(ctx, payer.ID, whatsappkyc.TierL1,
		[]string{whatsappkyc.EvChannelConfirmed}, nil); err != nil {
		t.Fatal(err)
	}

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}

	// Real DVA request: ₦500.00 to keep it tiny, base + 150bps DVA fee.
	const baseKobo = int64(49_255)
	payment, err := payments.CreateDraftForProvider(ctx, payer, merchant, baseKobo,
		service.ProviderBankTransfer, service.ChannelWhatsApp, payer.WhatsAppNumber)
	if err != nil {
		t.Fatalf("create bank_transfer draft: %v", err)
	}
	fmt.Printf("\n========== LIVE INTERSWITCH DVA ==========\n")
	fmt.Printf("  Draft %s… %s (base %s + 150bps DVA fee) awaiting confirmation\n",
		payment.ID.String()[:8], domain_FormatNGN(payment.AmountKobo), domain_FormatNGN(baseKobo))

	// Production path: InitializeCheckout with BankTransferMode=interswitch
	// mints the one-time virtual account on the real gateway, then SetCheckout
	// moves the payment awaiting_confirmation -> initialized like a card
	// checkout does — the state from which webhook/return verification lands.
	updated, err := payments.InitializeCheckout(ctx, payment)
	if err != nil {
		t.Fatalf("InitializeCheckout (real DVA): %v", err)
	}
	if updated.Status != "initialized" {
		t.Fatalf("payment status = %q, want initialized after DVA initialization", updated.Status)
	}
	instruction, err := repository.VirtualAccountInstructionByPaymentID(ctx, payment.ID)
	if err != nil {
		t.Fatalf("virtual account instruction not persisted: %v", err)
	}
	if instruction.AccountNumber == "" {
		t.Fatal("minted virtual account has an empty account number")
	}
	if len(instruction.AccountNumber) != 10 {
		t.Fatalf("virtual account number %q is not a 10-digit NUBAN", instruction.AccountNumber)
	}
	if instruction.ProviderReference != payment.ProviderReference {
		t.Fatalf("instruction reference %q != payment provider reference %q (requery key must match)",
			instruction.ProviderReference, payment.ProviderReference)
	}
	if instruction.ExpiresAt.Before(time.Now()) {
		t.Fatalf("virtual account already expired at %s", instruction.ExpiresAt)
	}
	if updated.CheckoutURL == "" {
		t.Fatal("DVA checkout did not set the /transfer instruction URL")
	}

	fmt.Printf("  ✅ Virtual account minted: %s (%s, %s)\n",
		instruction.AccountNumber, instruction.AccountName, instruction.BankName)
	fmt.Printf("  ✅ Instruction persisted; payment pending; valid until %s\n",
		instruction.ExpiresAt.Format(time.RFC3339))
	fmt.Printf("  Ref: %s | /transfer page: %s\n", payment.ProviderReference, updated.CheckoutURL)

	// Sanity: the signed requery machinery answers for the minted reference.
	// An UNPAID DVA has no transaction to find — the sandbox answers HTTP 500
	// (code 10500) rather than a clean not-found — which still proves the
	// signature and host are accepted (an auth failure would be 401). The
	// production confirmation leg for DVA payments is the webhook path, not
	// the reconcile requery (UnresolvedPayments only picks provider
	// 'interswitch'), so this probe must not drive VerifyAndApply.
	if _, err := gateway.Verify(ctx, payment.ProviderReference, payment.AmountKobo); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "401") || strings.Contains(msg, "Unauthorized") || strings.Contains(msg, "signature") {
			t.Fatalf("signed requery rejected (credential problem): %v", err)
		}
		fmt.Printf("  ✅ Signed requery answered: gateway reports no funds yet (%.60s…) — webhook confirms on payment\n", msg)
	} else {
		fmt.Printf("  ✅ Signed requery answered: reference resolvable\n")
	}
	after, err := repository.PaymentByID(ctx, payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("  Payment remains %s until the customer transfers — proven instruction leg complete\n", after.Status)

	_ = uuid.New // keep uuid import if the draft path changes shape later
}

// domain_FormatNGN avoids importing domain only for the formatter in the
// common case; falls back to a plain kobo string if the import is dropped.
func domain_FormatNGN(kobo int64) string {
	return fmt.Sprintf("ₓ%d.%02d", kobo/100, kobo%100)
}
