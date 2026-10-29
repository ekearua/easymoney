package service

// Chat-level coverage for the wallet-to-bank cash-out flow: the "My wallet"
// submenu, the amount, bank, account and confirm ladder, the name-enquiry
// review, the debit on confirm, and the provider outcome surfaced back in chat.
//
// Run with: TEST_DATABASE_URL=postgres://... go test ./internal/service/ -run TestWalletCashOutChat -v

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func TestWalletCashOutChat(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	databaseURL := serviceTestDBURL(t, base)
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.Close() })
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	truncateServiceData(t, ctx, databaseURL)

	number := "+2348012349555"
	// Create the account, then reach L1 so the wallet is active and fund it.
	user, err := repository.GetOrCreateUser(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTier(ctx, user.ID, kyc.TierL1,
		[]string{kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails}, nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreditUserWallet(ctx, user.ID, store.LedgerAccountOperatingBank,
		1_000_000, "cashout-chat:credit", "wallet funding"); err != nil {
		t.Fatal(err)
	}
	wallet, err := repository.WalletByOwner(ctx, store.WalletOwnerUser, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	msg := &carryoverMessenger{}
	cfg := config.Config{
		WebFlowsEnabled:      false,
		AppName:              "Faya",
		SessionTTL:           24 * time.Hour,
		FeeNIPPayoutFlatKobo: 10_000,
		PaymentMinKobo:       10_000,
		PaymentMaxKobo:       1_000_000_000,
	}
	svc := NewConversationService(cfg, repository, nil, nil, map[string]ports.Messenger{
		ChannelWhatsApp: msg,
	}, nil, nil, nil)
	provider := stubPayoutProvider{now: time.Now}
	enquirer := &stubNameEnquirer{name: "Ada Obi"}
	svc.SetWalletPayoutService(NewWalletPayoutService(cfg, repository, provider, enquirer, slog.Default()))

	send := func(text string) {
		t.Helper()
		if err := svc.Handle(ctx, store.InboundMessage{
			Channel: ChannelWhatsApp, Sender: number, Recipient: number, Text: text,
		}); err != nil {
			t.Fatalf("Handle(%q): %v", text, err)
		}
	}
	lastText := func() string {
		texts := msg.allSentText()
		if len(texts) == 0 {
			return ""
		}
		return texts[len(texts)-1]
	}
	lastInteractive := func() string {
		items := msg.allSentInteractive()
		if len(items) == 0 {
			return ""
		}
		return items[len(items)-1]
	}

	send("my wallet")
	if body := lastInteractive(); !strings.Contains(body, "My wallet") || !strings.Contains(body, "Balance") {
		t.Fatalf("wallet menu = %q", body)
	}

	send("cash out")
	if body := lastText(); !strings.Contains(body, "Enter the amount") {
		t.Fatalf("amount prompt = %q", body)
	}

	send("500")
	if body := lastText(); !strings.Contains(body, "Enter the destination bank") {
		t.Fatalf("bank prompt = %q", body)
	}

	send("044")
	if body := lastText(); !strings.Contains(body, "10-digit account number") {
		t.Fatalf("account prompt = %q", body)
	}

	send("0123456789")
	if body := lastInteractive(); !strings.Contains(body, "Confirm cash-out") || !strings.Contains(body, "Ada Obi") {
		t.Fatalf("confirm prompt = %q", body)
	}
	if enquirer.gotN != "0123456789" || enquirer.gotB != "044" {
		t.Fatalf("name enquiry = %q/%q", enquirer.gotN, enquirer.gotB)
	}

	send("confirm_cashout")
	if body := lastText(); !strings.Contains(body, "Cash-out successful") {
		t.Fatalf("success message = %q", body)
	}

	balance, err := repository.WalletBalance(ctx, wallet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 950_000 {
		t.Fatalf("wallet balance = %d, want 950000 (N500 debited incl. fee)", balance)
	}
	payouts, err := repository.WalletPayoutsByUser(ctx, user.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(payouts) != 1 {
		t.Fatalf("payout rows = %d, want 1", len(payouts))
	}
	payout := payouts[0]
	if payout.Status != store.WalletPayoutCompleted {
		t.Fatalf("payout status = %s", payout.Status)
	}
	if payout.AccountName != "Ada Obi" || payout.BankCode != "044" || payout.AccountNumber != "0123456789" {
		t.Fatalf("payout destination = %+v", payout)
	}
	if payout.AmountKobo != 50_000 || payout.FeeKobo != 10_000 || payout.PayoutKobo != 40_000 {
		t.Fatalf("payout amounts = amount %d fee %d payout %d", payout.AmountKobo, payout.FeeKobo, payout.PayoutKobo)
	}
	if !strings.HasPrefix(payout.ExternalRef, "SIM-PAY-") {
		t.Fatalf("external ref = %q", payout.ExternalRef)
	}
}

func TestWalletCashOutFailsThenReverses(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	databaseURL := serviceTestDBURL(t, base)
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.Close() })
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	truncateServiceData(t, ctx, databaseURL)

	user, err := repository.GetOrCreateUser(ctx, "+2348012349556")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureKYCProfile(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTier(ctx, user.ID, kyc.TierL1,
		[]string{kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails}, nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreditUserWallet(ctx, user.ID, store.LedgerAccountOperatingBank,
		1_000_000, "cashout-reverse:credit", "wallet funding"); err != nil {
		t.Fatal(err)
	}
	wallet, err := repository.WalletByOwner(ctx, store.WalletOwnerUser, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		WebFlowsEnabled:      false,
		FeeNIPPayoutFlatKobo: 10_000,
		PaymentMinKobo:       10_000,
		PaymentMaxKobo:       1_000_000_000,
	}
	svc := NewWalletPayoutService(cfg, repository, stubPayoutProvider{now: time.Now},
		&stubNameEnquirer{name: "Ada Obi"}, slog.Default())

	// Bank code 011 is the stub rail's deterministic decline.
	payout, err := svc.Request(ctx, user.ID, WalletPayoutRequest{
		AmountKobo:    50_000,
		BankCode:      "011",
		BankName:      "Declining Bank",
		AccountNumber: "0123456789",
		AccountName:   "Ada Obi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if payout.Status != store.WalletPayoutFailed {
		t.Fatalf("after decline status = %s", payout.Status)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 950_000 {
		t.Fatalf("balance after decline = %d", balance)
	}

	// Two worker sweeps retry the transfer (attempts 2 and 3).
	for i := 0; i < 2; i++ {
		if err := svc.Maintain(ctx); err != nil {
			t.Fatal(err)
		}
		row, err := repository.WalletPayoutByID(ctx, payout.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != store.WalletPayoutFailed {
			t.Fatalf("sweep %d status = %s, want failed", i+1, row.Status)
		}
		if row.Attempts != i+2 {
			t.Fatalf("sweep %d attempts = %d, want %d", i+1, row.Attempts, i+2)
		}
	}

	// The third sweep exhausts the attempt budget and returns the money.
	if err := svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := repository.WalletPayoutByID(ctx, payout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.WalletPayoutReversed {
		t.Fatalf("exhausted status = %s", row.Status)
	}
	if balance, _ := repository.WalletBalance(ctx, wallet.ID); balance != 1_000_000 {
		t.Fatalf("balance after reversal = %d", balance)
	}
}
