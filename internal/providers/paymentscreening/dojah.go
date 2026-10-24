// This file wires Dojah payment-time counterparty screening behind the
// PaymentScreener port, per docs/compliance/AML_VENDOR_DECISION_RECORD.md. It
// calls the same documented AML endpoint as onboarding
// (POST /api/v2/aml/screening) with "Authorization: <secret key>" (raw) and
// "AppId: <app id>". The vendor's match status is mapped onto the shared
// decision vocabulary, so the G2 hold/interdiction semantics are unchanged; an
// unrecognized reply is rejected rather than treated as clear (fail-closed).
//
// Dojah does not publish a list-version identifier; the operator-supplied
// DOJAH_LIST_VERSION stamp is recorded instead so stale-list re-evaluation
// only fires when that stamp moves.
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
	"whatsapp-payment-demo/internal/providers/dojahaml"
)

// Dojah screens payment counterparties against Dojah's AML screening API.
type Dojah struct {
	baseURL        string
	appID          string
	secretKey      string
	httpClient     *http.Client
	matchThreshold float64
	listVersion    string
}

// NewDojah creates the Dojah payment-screening adapter. An empty baseURL
// defaults to production (https://api.dojah.io); an empty timeout defaults to
// 30 seconds. listVersion is the operator-supplied list stamp recorded with
// each decision.
func NewDojah(baseURL, appID, secretKey string, timeout time.Duration, matchThreshold float64, listVersion string) *Dojah {
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
		listVersion:    strings.TrimSpace(listVersion),
	}
}

// ProviderName names the vendor recorded on screening rows.
func (d *Dojah) ProviderName() string { return "dojah" }

// ScreenPayment performs one counterparty lookup against Dojah. An empty
// counterparty name is rejected as an error rather than cleared, because a
// movement without a screenable name must never auto-pass.
func (d *Dojah) ScreenPayment(ctx context.Context, req ports.PaymentScreenRequest) (ports.PaymentScreenDecision, error) {
	name := strings.TrimSpace(req.CounterpartyName)
	if name == "" {
		return ports.PaymentScreenDecision{}, fmt.Errorf("counterparty name is required for payment screening")
	}
	entity, err := d.lookup(ctx, req)
	if err != nil {
		return ports.PaymentScreenDecision{}, err
	}
	decision, ok := dojahaml.Decision(entity.MatchStatus)
	if !ok {
		return ports.PaymentScreenDecision{}, fmt.Errorf("unrecognized payment-screening match status %q", entity.MatchStatus)
	}
	ref := strings.TrimSpace(entity.EntityID)
	if ref == "" {
		ref = "DOJAH-PSCR-" + shortHash(name)
	}
	return ports.PaymentScreenDecision{
		Decision:     decision,
		MatchedNames: dojahaml.MatchedNames(entity.Results),
		ProviderRef:  ref,
		ListVersion:  d.listVersion,
		Message:      defaultPaymentMessage(decision),
	}, nil
}

// lookup performs the HTTP POST with Dojah's auth headers and decodes the
// documented AML response.
func (d *Dojah) lookup(ctx context.Context, req ports.PaymentScreenRequest) (*dojahaml.Entity, error) {
	payload := dojahaml.Request{
		Schema:          "individual",
		UniqueReference: uniqueReference(req),
		Properties: dojahaml.Properties{
			Names:    strings.TrimSpace(req.CounterpartyName),
			IDNumber: "",
		},
		ScreeningOptions: dojahaml.Options{
			PEPCheck:          true,
			Sanction:          true,
			AdverseMediaCheck: false,
			MatchThreshold:    d.matchThreshold,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+dojahaml.Path, bytes.NewReader(body))
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("payment screening request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read payment screening response: %w", err)
	}
	if err := paymentHTTPError(resp.StatusCode, respBody); err != nil {
		return nil, err
	}
	var parsed dojahaml.Response
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode payment screening response: %w", err)
	}
	if parsed.Entity == nil {
		return nil, fmt.Errorf("Dojah returned no payment-screening entity")
	}
	return parsed.Entity, nil
}

// uniqueReference gives Dojah an echoed reconciliation key without sending
// more counterparty data than the name. It is omitted when neither value is
// available.
func uniqueReference(req ports.PaymentScreenRequest) string {
	rail := strings.TrimSpace(req.Rail)
	account := strings.TrimSpace(req.AccountNumber)
	switch {
	case rail != "" && account != "":
		return rail + ":" + account
	case account != "":
		return account
	default:
		return ""
	}
}

// paymentHTTPError maps a non-2xx reply onto an operator-safe error. A 402
// means the Dojah wallet is empty and a 424 that an upstream source is down;
// both are returned as errors so the movement is held, never cleared.
func paymentHTTPError(status int, body []byte) error {
	switch status {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("payment screening API key rejected (401)")
	case http.StatusPaymentRequired:
		return fmt.Errorf("payment screening unavailable: Dojah wallet balance is too low (402)")
	case http.StatusTooManyRequests:
		return fmt.Errorf("payment screening rate limit exceeded (429)")
	case http.StatusFailedDependency:
		return fmt.Errorf("payment screening unavailable: Dojah upstream source failed (424)")
	}
	var errResp struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil {
		if msg := firstNonEmpty(errResp.Message, errResp.Error); msg != "" {
			return fmt.Errorf("payment screening error (HTTP %d): %s", status, msg)
		}
	}
	return fmt.Errorf("payment screening error HTTP %d", status)
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
