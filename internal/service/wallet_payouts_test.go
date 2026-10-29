package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
)

type stubNameEnquirer struct {
	name string
	err  error
	gotN string
	gotB string
}

func (s *stubNameEnquirer) NameEnquiry(_ context.Context, accountNumber, bankCode string) (string, error) {
	s.gotN, s.gotB = accountNumber, bankCode
	return s.name, s.err
}

func cashOutTestConfig() config.Config {
	return config.Config{
		FeeNIPPayoutFlatKobo: 10_000,
		PaymentMinKobo:       10_000,
		PaymentMaxKobo:       1_000_000_000,
	}
}

func TestWalletPayoutQuoteFeeAndName(t *testing.T) {
	enquirer := &stubNameEnquirer{name: "Ada Obi"}
	svc := NewWalletPayoutService(cashOutTestConfig(), nil, nil, enquirer, slog.Default())

	quote, err := svc.Quote(context.Background(), 50_000, "044", "0123 456 789")
	if err != nil {
		t.Fatal(err)
	}
	if quote.AccountName != "Ada Obi" {
		t.Fatalf("account name = %q", quote.AccountName)
	}
	if quote.FeeKobo != 10_000 {
		t.Fatalf("fee = %d, want 10000", quote.FeeKobo)
	}
	if quote.PayoutKobo != 40_000 {
		t.Fatalf("payout = %d, want 40000", quote.PayoutKobo)
	}
	if enquirer.gotN != "0123456789" || enquirer.gotB != "044" {
		t.Fatalf("enquiry called with %q/%q", enquirer.gotN, enquirer.gotB)
	}
}

func TestWalletPayoutQuoteFallsBackWithoutEnquirer(t *testing.T) {
	svc := NewWalletPayoutService(cashOutTestConfig(), nil, nil, nil, slog.Default())
	quote, err := svc.Quote(context.Background(), 50_000, "044", "0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if quote.AccountName != "Account ending 6789" {
		t.Fatalf("fallback name = %q", quote.AccountName)
	}
}

func TestWalletPayoutQuoteSurfacesEnquiryFailure(t *testing.T) {
	enquirer := &stubNameEnquirer{err: errors.New("switch timeout")}
	svc := NewWalletPayoutService(cashOutTestConfig(), nil, nil, enquirer, slog.Default())
	_, err := svc.Quote(context.Background(), 50_000, "044", "0123456789")
	if err == nil || !strings.Contains(err.Error(), "could not verify the account") {
		t.Fatalf("expected verification failure, got %v", err)
	}
}

func TestWalletPayoutQuoteValidation(t *testing.T) {
	svc := NewWalletPayoutService(cashOutTestConfig(), nil, nil, nil, slog.Default())
	cases := []struct {
		name   string
		amount int64
		bank   string
		acct   string
	}{
		{"zero amount", 0, "044", "0123456789"},
		{"below minimum", 5_000, "044", "0123456789"},
		{"above maximum", 2_000_000_000, "044", "0123456789"},
		{"short account", 50_000, "044", "012345678"},
		{"non-numeric account", 50_000, "044", "01234abcde"},
		{"empty bank", 50_000, " ", "0123456789"},
		{"amount below fee", 10_000, "044", "0123456789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Quote(context.Background(), tc.amount, tc.bank, tc.acct); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestWalletPayoutRequestWithoutProvider(t *testing.T) {
	svc := NewWalletPayoutService(cashOutTestConfig(), nil, nil, nil, slog.Default())
	_, err := svc.Request(context.Background(), uuid.Nil, WalletPayoutRequest{
		AmountKobo: 50_000, BankCode: "044", AccountNumber: "0123456789", AccountName: "Ada Obi",
	})
	if !errors.Is(err, ErrWalletCashOutUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}
