// This file wires the Dojah sanctions/PEP screening endpoint
// (POST /api/v1/aml/v2/screening) behind the SanctionsScreener port, per
// AML_VENDOR_DECISION_RECORD.md §11.1. Dojah authenticates with two headers on
// every call: "Authorization: <secret key>" (raw, no Bearer prefix) and
// "AppId: <app id>". The request/response contract is the same normalized
// envelope the generic HTTP adapter documents, so the port contract is
// unchanged; the §7 vendor trial confirms the live payload shape.
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
)

// Dojah screens identities against Dojah's AML screening API.
type Dojah struct {
	baseURL        string
	appID          string
	secretKey      string
	httpClient     *http.Client
	matchThreshold float64
}

// NewDojah creates the Dojah screening adapter. An empty baseURL defaults to
// production (https://api.dojah.io); an empty timeout defaults to 30 seconds.
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

// ProviderName returns the adapter identifier for audit records.
func (d *Dojah) ProviderName() string { return "dojah" }

// dojahScreenRequest is the wire body sent to Dojah.
type dojahScreenRequest struct {
	LegalName      string  `json:"legal_name"`
	DateOfBirth    string  `json:"date_of_birth,omitempty"`
	PhoneNumber    string  `json:"phone_number,omitempty"`
	Address        string  `json:"address,omitempty"`
	CountryCode    string  `json:"country_code,omitempty"`
	MatchThreshold float64 `json:"match_threshold"`
}

// Screen performs one sanctions/PEP lookup against Dojah.
func (d *Dojah) Screen(ctx context.Context, request ports.ScreeningRequest) (ports.ScreeningDecision, error) {
	name := strings.TrimSpace(request.LegalName)
	if name == "" {
		return ports.ScreeningDecision{}, fmt.Errorf("legal name is required for screening")
	}
	resp, err := d.lookup(ctx, request)
	if err != nil {
		switch {
		case err == errUnauthorized:
			return ports.ScreeningDecision{}, fmt.Errorf("screening API key rejected (401)")
		case err == errRateLimited:
			return ports.ScreeningDecision{}, fmt.Errorf("screening rate limit exceeded (429)")
		default:
			return ports.ScreeningDecision{}, fmt.Errorf("screening lookup: %w", err)
		}
	}
	decision, ok := normalizeDecision(resp)
	if !ok {
		return ports.ScreeningDecision{}, fmt.Errorf("unrecognized screening decision (decision=%q status=%q)", resp.Decision, resp.Status)
	}
	ref := firstNonEmpty(resp.Reference, resp.ProviderRef, resp.ReportID)
	if ref == "" {
		ref = "DOJAH-SCR-" + shortHash(name)
	}
	message := strings.TrimSpace(resp.Message)
	if message == "" {
		message = defaultDecisionMessage(decision)
	}
	return ports.ScreeningDecision{
		Decision:     decision,
		MatchedNames: resp.MatchedNames,
		ProviderRef:  ref,
		Message:      message,
	}, nil
}

// lookup performs the HTTP POST with Dojah's auth headers and decodes the
// normalized response envelope.
func (d *Dojah) lookup(ctx context.Context, request ports.ScreeningRequest) (httpScreenResponse, error) {
	payload := dojahScreenRequest{
		LegalName:      strings.TrimSpace(request.LegalName),
		DateOfBirth:    strings.TrimSpace(request.DateOfBirth),
		PhoneNumber:    strings.TrimSpace(request.PhoneNumber),
		Address:        strings.TrimSpace(request.Address),
		CountryCode:    strings.TrimSpace(request.CountryCode),
		MatchThreshold: d.matchThreshold,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return httpScreenResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/api/v1/aml/v2/screening", bytes.NewReader(body))
	if err != nil {
		return httpScreenResponse{}, err
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
		return httpScreenResponse{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return httpScreenResponse{}, fmt.Errorf("read response body: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusUnauthorized:
		return httpScreenResponse{}, errUnauthorized
	case http.StatusTooManyRequests:
		return httpScreenResponse{}, errRateLimited
	default:
		var errResp httpScreenResponse
		if json.Unmarshal(respBody, &errResp) == nil && strings.TrimSpace(errResp.Message) != "" {
			return httpScreenResponse{}, fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, errResp.Message)
		}
		return httpScreenResponse{}, fmt.Errorf("API error HTTP %d", resp.StatusCode)
	}

	var parsed httpScreenResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return httpScreenResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return parsed, nil
}
