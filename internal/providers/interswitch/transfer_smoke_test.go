package interswitch

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestSandboxTransferRailSmoke exercises the Quickteller Send Money v5 bank
// directory endpoint (read-only, no money movement) against the configured
// sandbox/QA credentials, proving the Bearer-token host split for the transfer
// rail works. Gated like the other smoke tests so a plain `go test ./...`
// never touches the network:
//
//	INTERSWITCH_SANDBOX_SMOKE=1 \
//	INTERSWITCH_CLIENT_ID=... INTERSWITCH_CLIENT_SECRET=... \
//	go test ./internal/providers/interswitch/ -run TestSandboxTransferRailSmoke -v
func TestSandboxTransferRailSmoke(t *testing.T) {
	if os.Getenv("INTERSWITCH_SANDBOX_SMOKE") != "1" {
		t.Skip("set INTERSWITCH_SANDBOX_SMOKE=1 to hit the Quickteller Send Money v5 host")
	}
	clientID := strings.TrimSpace(os.Getenv("INTERSWITCH_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("INTERSWITCH_CLIENT_SECRET"))
	if clientID == "" || clientSecret == "" {
		t.Skip("set INTERSWITCH_CLIENT_ID and INTERSWITCH_CLIENT_SECRET to probe the transfer rail")
	}
	banks, err := clientForSmoke(clientID, clientSecret).GetBankCodes(context.Background())
	if err != nil {
		t.Fatalf("GetBankCodes rejected: %v\n\nInterswitch's sandbox API host stops serving the v5 transfer rails with HTTP 503, and the QA host rejects passport-sandbox tokens with 401. A live transfer-rail probe therefore needs QA-provisioned credentials AND the QA token host, which is NOT qa.interswitchng.com/passport/oauth/token (404). Set Options.TokenURL / INTERSWITCH_TOKEN_URL when one is known.", err)
	}
	if len(banks) == 0 {
		t.Fatalf("GetBankCodes returned an empty directory")
	}
	t.Logf("PASS: fundstransferbanks returned %d banks (first: %s %s %s)", len(banks), banks[0].CBNCode, banks[0].BankName, banks[0].BankCode)
}

// TestSandboxNameEnquirySmoke validates an account number through the v5
// DoAccountNameInquiry endpoint. Uses the same gate as the other smokes plus an
// account that is expected to exist. Skipped unless the destination account and
// bank are provided, since they are env-specific.
func TestSandboxNameEnquirySmoke(t *testing.T) {
	if os.Getenv("INTERSWITCH_SANDBOX_SMOKE") != "1" {
		t.Skip("set INTERSWITCH_SANDBOX_SMOKE=1 to hit name enquiry")
	}
	account := strings.TrimSpace(os.Getenv("INTERSWITCH_ENQUIRY_ACCOUNT"))
	bank := strings.TrimSpace(os.Getenv("INTERSWITCH_ENQUIRY_BANK"))
	clientID := strings.TrimSpace(os.Getenv("INTERSWITCH_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("INTERSWITCH_CLIENT_SECRET"))
	if account == "" || bank == "" || clientID == "" || clientSecret == "" {
		t.Skip("set INTERSWITCH_ENQUIRY_ACCOUNT, INTERSWITCH_ENQUIRY_BANK, INTERSWITCH_CLIENT_ID, and INTERSWITCH_CLIENT_SECRET to probe name enquiry")
	}
	name, err := clientForSmoke(clientID, clientSecret).NameEnquiry(context.Background(), account, bank)
	if err != nil {
		t.Fatalf("name enquiry rejected: %v", err)
	}
	t.Logf("PASS: account %s @ bank %s resolves to %q", account, bank, name)
}

func clientForSmoke(clientID, clientSecret string) *Client {
	return New(Options{
		ClientID:        clientID,
		ClientSecret:    clientSecret,
		BaseURL:         "https://sandbox.interswitchng.com",
		TransferBaseURL: os.Getenv("INTERSWITCH_TRANSFER_BASE_URL"),
		Mode:            "TEST",
	})
}