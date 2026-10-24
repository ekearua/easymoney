// This file wires Dojah's AML screening endpoint
// (POST /api/v2/aml/screening) behind the SanctionsScreener port, per
// docs/compliance/AML_VENDOR_DECISION_RECORD.md. Dojah authenticates with two
// headers on every call: "Authorization: <secret key>" (raw, no Bearer prefix)
// and "AppId: <app id>". The vendor reports a match status rather than a
// decision, so this adapter maps "No Match" / "Partially Matched" /
// "Potential Match" / "Confirmed Match" onto the shared clear / possible /
// blocked vocabulary and rejects anything else instead of clearing it.
//
// Dojah does not publish a list-version identifier in the response. The
// operator-supplied DOJAH_LIST_VERSION stamp is recorded instead (empty when
// unset), so the stale-list re-evaluation only fires when that stamp moves.
package screening

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/dojahaml"
)

// Dojah screens identities against Dojah's AML screening API.
type Dojah struct {
	baseURL        string
	appID          string
	secretKey      string
	httpClient     *http.Client
	matchThreshold float64
	listVersion    string
}

// NewDojah creates the Dojah screening adapter. An empty baseURL defaults to
// production (https://api.dojah.io); an empty timeout defaults to 30 seconds.
// listVersion is the operator-supplied list stamp recorded with each decision.
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

// ProviderName returns the adapter identifier for audit records.
func (d *Dojah) ProviderName() string { return "dojah" }

// Screen performs one sanctions/PEP lookup against Dojah.
func (d *Dojah) Screen(ctx context.Context, request ports.ScreeningRequest) (ports.ScreeningDecision, error) {
	name := strings.TrimSpace(request.LegalName)
	if name == "" {
		return ports.ScreeningDecision{}, fmt.Errorf("legal name is required for screening")
	}
	entity, err := d.lookup(ctx, request)
	if err != nil {
		return ports.ScreeningDecision{}, fmt.Errorf("screening lookup: %w", err)
	}
	decision, ok := dojahaml.Decision(entity.MatchStatus)
	if !ok {
		return ports.ScreeningDecision{}, fmt.Errorf("unrecognized screening match status %q", entity.MatchStatus)
	}
	ref := strings.TrimSpace(entity.EntityID)
	if ref == "" {
		ref = "DOJAH-SCR-" + shortHash(name)
	}
	return ports.ScreeningDecision{
		Decision:     decision,
		MatchedNames: dojahaml.MatchedNames(entity.Results),
		ProviderRef:  ref,
		ListVersion:  d.listVersion,
		Message:      defaultDecisionMessage(decision),
	}, nil
}

// lookup performs the HTTP POST with Dojah's auth headers and decodes the
// documented AML response.
func (d *Dojah) lookup(ctx context.Context, request ports.ScreeningRequest) (*dojahaml.Entity, error) {
	payload := dojahaml.Request{
		Schema: "individual",
		Properties: dojahaml.Properties{
			Names:       strings.TrimSpace(request.LegalName),
			DateOfBirth: strings.TrimSpace(request.DateOfBirth),
			Nationality: nationality(request.CountryCode),
			IDNumber:    "",
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+dojahaml.Path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if d.secretKey != "" {
		req.Header.Set("Authorization", d.secretKey)
	}
	if d.appID != "" {
		req.Header.Set("AppId", d.appID)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if err := dojahHTTPError(resp.StatusCode, respBody); err != nil {
		return nil, err
	}
	var parsed dojahaml.Response
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if parsed.Entity == nil {
		return nil, fmt.Errorf("Dojah returned no screening entity")
	}
	return parsed.Entity, nil
}

// dojahHTTPError maps a non-2xx reply onto an operator-safe error. A 402 means
// the Dojah wallet is empty and a 424 that an upstream source is down; both are
// returned as errors so the caller fails closed (holds) rather than clearing.
func dojahHTTPError(status int, body []byte) error {
	switch status {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("screening API key rejected (401)")
	case http.StatusPaymentRequired:
		return fmt.Errorf("screening unavailable: Dojah wallet balance is too low (402)")
	case http.StatusTooManyRequests:
		return fmt.Errorf("screening rate limit exceeded (429)")
	case http.StatusFailedDependency:
		return fmt.Errorf("screening unavailable: Dojah upstream source failed (424)")
	}
	var errResp struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil {
		if msg := firstNonEmpty(errResp.Message, errResp.Error); msg != "" {
			return fmt.Errorf("API error (HTTP %d): %s", status, msg)
		}
	}
	return fmt.Errorf("API error HTTP %d", status)
}

// nationality converts the platform's two-letter country code into the country
// name Dojah's match scoring expects; other values pass through unchanged.
func nationality(code string) string {
	trimmed := strings.TrimSpace(code)
	if strings.EqualFold(trimmed, "NG") {
		return "Nigerian"
	}
	return trimmed
}
