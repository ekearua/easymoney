// This file wires Dojah payment-time counterparty screening behind the
// PaymentScreener port, per AML_VENDOR_DECISION_RECORD.md §11.1 (the same
// synchronous AML screening endpoint as onboarding). Dojah authenticates with
// "Authorization: <secret key>" (raw) and "AppId: <app id>". The response is
// normalized into the same decision vocabulary the simulator uses, so the G2
// hold/interdiction semantics are unchanged; an unrecognized reply is rejected
// rather than treated as clear (fail-closed).
package paymentscreening

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

// Dojah screens payment counterparties against Dojah's AML screening API.
type Dojah struct {
	baseURL        string
	appID          string
	secretKey      string
	httpClient     *http.Client
	matchThreshold float64
}

// NewDojah creates the Dojah payment-screening adapter. An empty baseURL
// defaults to production (https://api.dojah.io); an empty timeout defaults to
// 30 seconds.
func NewDojah(baseURL, appID, secretKey string, timeout time.Duration, matchThreshold float64) *Dojah {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.dojah.io"
	}
	return &Dojah{
		baseURL:        base,
		appID:          strings.TrimSpace(appID),
		secretKey:      strings.TrimSpace(secretKey),
		httpClient:     &http.Client{Timeout: timeout},
		matchThreshold: matchThreshold,
	}
}

// ProviderName names the vendor recorded on screening rows.
func (d *Dojah) ProviderName() string { return "dojah" }

type dojahPaymentRequest struct {
	LegalName      string  `json:"legal_name"`
	AccountNumber  string  `json:"account_number,omitempty"`
	BankCode       string  `json:"bank_code,omitempty"`
	Amount         int64   `json:"amount,omitempty"`
	Rail           string  `json:"rail,omitempty"`
	MatchThreshold float64 `json:"match_threshold"`
}

type dojahPaymentResponse struct {
	Decision     string   `json:"decision"`
	Status       string   `json:"status"`
	Matched      *bool    `json:"matched"`
	MatchedNames []string `json:"matched_names"`
	Reference    string   `json:"reference"`
	ProviderRef  string   `json:"provider_ref"`
	ReportID     string   `json:"report_id"`
	Message      string   `json:"message"`
}

// ScreenPayment performs one counterparty lookup against Dojah. An empty
// counterparty name is rejected as an error rather than cleared, because a
// movement without a screenable name must never auto-pass.
func (d *Dojah) ScreenPayment(ctx context.Context, req ports.PaymentScreenRequest) (ports.PaymentScreenDecision, error) {
	name := strings.TrimSpace(req.CounterpartyName)
	if name == "" {
		return ports.PaymentScreenDecision{}, fmt.Errorf("counterparty name is required for payment screening")
	}
	resp, err := d.lookup(ctx, req)
	if err != nil {
		return ports.PaymentScreenDecision{}, err
	}
	decision, ok := dojahPaymentDecision(resp)
	if !ok {
		return ports.PaymentScreenDecision{}, fmt.Errorf("unrecognized payment-screening decision (decision=%q status=%q)", resp.Decision, resp.Status)
	}
	ref := firstNonEmpty(resp.Reference, resp.ProviderRef, resp.ReportID)
	if ref == "" {
		ref = "DOJAH-PSCR-" + shortHash(name)
	}
	message := strings.TrimSpace(resp.Message)
	if message == "" {
		message = defaultPaymentMessage(decision)
	}
	return ports.PaymentScreenDecision{
		Decision:     decision,
		MatchedNames: resp.MatchedNames,
		ProviderRef:  ref,
		Message:      message,
	}, nil
}

// lookup performs the HTTP POST with Dojah's auth headers and decodes the
// normalized response envelope.
func (d *Dojah) lookup(ctx context.Context, req ports.PaymentScreenRequest) (dojahPaymentResponse, error) {
	payload := dojahPaymentRequest{
		LegalName:      strings.TrimSpace(req.CounterpartyName),
		AccountNumber:  strings.TrimSpace(req.AccountNumber),
		BankCode:       strings.TrimSpace(req.BankCode),
		Amount:         req.AmountKobo,
		Rail:           strings.TrimSpace(req.Rail),
		MatchThreshold: d.matchThreshold,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return dojahPaymentResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/api/v1/aml/v2/screening", bytes.NewReader(body))
	if err != nil {
		return dojahPaymentResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if d.secretKey != "" {
		httpReq.Header.Set("Authorization", d.secretKey)
	}
	if d.appID != "" {
		httpReq.Header.Set("AppId", d.appID)
	}

	resp, err := d.httpClient.Do(httpReq)
	if err != nil {
		return dojahPaymentResponse{}, fmt.Errorf("payment screening request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return dojahPaymentResponse{}, fmt.Errorf("read payment screening response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusUnauthorized:
		return dojahPaymentResponse{}, fmt.Errorf("payment screening API key rejected (401)")
	case http.StatusTooManyRequests:
		return dojahPaymentResponse{}, fmt.Errorf("payment screening rate limit exceeded (429)")
	default:
		var errResp dojahPaymentResponse
		if json.Unmarshal(respBody, &errResp) == nil && strings.TrimSpace(errResp.Message) != "" {
			return dojahPaymentResponse{}, fmt.Errorf("payment screening error (HTTP %d): %s", resp.StatusCode, errResp.Message)
		}
		return dojahPaymentResponse{}, fmt.Errorf("payment screening error HTTP %d", resp.StatusCode)
	}

	var parsed dojahPaymentResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return dojahPaymentResponse{}, fmt.Errorf("decode payment screening response: %w", err)
	}
	return parsed, nil
}

// dojahPaymentDecision maps a vendor decision/status token (or a boolean
// matched flag) onto the kyc screening vocabulary.
func dojahPaymentDecision(resp dojahPaymentResponse) (string, bool) {
	for _, token := range []string{resp.Decision, resp.Status} {
		if decision, ok := dojahPaymentToken(token); ok {
			return decision, true
		}
	}
	if resp.Matched != nil && resp.Decision == "" && resp.Status == "" {
		if *resp.Matched {
			return kyc.ScreenStrong, true
		}
		return kyc.ScreenClear, true
	}
	return "", false
}

// dojahPaymentToken maps one token onto the screening vocabulary.
func dojahPaymentToken(raw string) (string, bool) {
	token := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(raw)))
	switch token {
	case "clear", "no_match", "not_matched", "no_hit", "clean", "pass":
		return kyc.ScreenClear, true
	case "possible", "potential", "potential_match", "pep", "review", "needs_review", "manual_review":
		return kyc.ScreenPossible, true
	case "strong", "match", "matched", "exact_match", "confirmed_match", "hit", "hits":
		return kyc.ScreenStrong, true
	case "blocked", "block", "sanctioned", "sanctions", "prohibited", "confirmed", "interdict":
		return kyc.ScreenBlocked, true
	}
	return "", false
}

func defaultPaymentMessage(decision string) string {
	switch decision {
	case kyc.ScreenClear:
		return "no list matches"
	case kyc.ScreenPossible:
		return "potential counterparty match detected"
	case kyc.ScreenStrong:
		return "counterparty match detected; review required"
	default:
		return "counterparty interdiction"
	}
}

// firstNonEmpty returns the first trimmed non-empty value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// shortHash builds a stable, short tag for a name when a vendor does not return
// its own reference, so audit records always carry a provider ref.
func shortHash(value string) string {
	sum := 0
	for _, r := range value {
		sum = (sum*31 + int(r)) % 1000000
	}
	return fmt.Sprintf("%06d", sum)
}
