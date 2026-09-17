// This file adds a generic HTTP adapter for the SanctionsScreener port so a
// live screening vendor can be enabled without a bespoke integration. It POSTs
// the identity fields to "{SCREENING_API_BASE}/screen" and normalizes a small,
// documented JSON contract into the port's decision vocabulary.
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

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

// Sentinel errors mapped onto user-safe messages by Screen.
var (
	errUnauthorized = fmt.Errorf("unauthorized")
	errRateLimited  = fmt.Errorf("rate limited")
)

// HTTP calls a generic REST sanctions/PEP screening endpoint.
//
// Request (POST {baseURL}/screen, Content-Type: application/json):
//
//	{
//	  "legal_name":    "Ada Obi",
//	  "date_of_birth": "1990-01-02",
//	  "phone_number":  "+2348012345678",
//	  "address":       "12 Marina, Lagos",
//	  "country_code":  "NG"
//	}
//
// When an API key is configured it is sent as both "Authorization: Bearer ..."
// and "X-API-Key: ..." so common gateway schemes work unchanged.
//
// Response:
//
//	{
//	  "decision":      "clear|possible|strong|blocked",
//	  "matched_names": ["Ada Sanctioned"],
//	  "reference":     "vendor-ref-123",
//	  "message":       "no sanctions or PEP matches"
//	}
//
// "status" is accepted in place of "decision", and a boolean "matched" is used
// when neither is recognizable. Unknown decisions are rejected rather than
// silently treated as clear.
type HTTP struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewHTTP creates the generic screening adapter. An empty timeout defaults to
// 30 seconds.
func NewHTTP(baseURL, apiKey string, timeout time.Duration) *HTTP {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTP{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// ProviderName returns the adapter identifier for audit records.
func (h *HTTP) ProviderName() string { return "http" }

// httpScreenRequest is the wire body sent to the vendor.
type httpScreenRequest struct {
	LegalName   string `json:"legal_name"`
	DateOfBirth string `json:"date_of_birth,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Address     string `json:"address,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
}

// httpScreenResponse is the normalized response envelope we accept.
type httpScreenResponse struct {
	Decision     string   `json:"decision"`
	Status       string   `json:"status"`
	Matched      *bool    `json:"matched"`
	MatchedNames []string `json:"matched_names"`
	Reference    string   `json:"reference"`
	ProviderRef  string   `json:"provider_ref"`
	ReportID     string   `json:"report_id"`
	Message      string   `json:"message"`
}

// Screen performs one sanctions/PEP lookup against the configured vendor.
func (h *HTTP) Screen(ctx context.Context, request ports.ScreeningRequest) (ports.ScreeningDecision, error) {
	name := strings.TrimSpace(request.LegalName)
	if name == "" {
		return ports.ScreeningDecision{}, fmt.Errorf("legal name is required for screening")
	}
	if h.baseURL == "" {
		return ports.ScreeningDecision{}, fmt.Errorf("screening API base URL is not configured")
	}

	// --- call the screening endpoint ---
	resp, err := h.lookup(ctx, request)
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

	// --- normalize the decision ---
	decision, ok := normalizeDecision(resp)
	if !ok {
		return ports.ScreeningDecision{}, fmt.Errorf("unrecognized screening decision (decision=%q status=%q)", resp.Decision, resp.Status)
	}

	ref := firstNonEmpty(resp.Reference, resp.ProviderRef, resp.ReportID)
	if ref == "" {
		ref = "HTTP-SCR-" + shortHash(name)
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

// lookup performs the HTTP POST and decodes the JSON response.
func (h *HTTP) lookup(ctx context.Context, request ports.ScreeningRequest) (httpScreenResponse, error) {
	payload := httpScreenRequest{
		LegalName:   strings.TrimSpace(request.LegalName),
		DateOfBirth: strings.TrimSpace(request.DateOfBirth),
		PhoneNumber: strings.TrimSpace(request.PhoneNumber),
		Address:     strings.TrimSpace(request.Address),
		CountryCode: strings.TrimSpace(request.CountryCode),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return httpScreenResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/screen", bytes.NewReader(body))
	if err != nil {
		return httpScreenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if h.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
		req.Header.Set("X-API-Key", h.apiKey)
	}

	resp, err := h.httpClient.Do(req)
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

// normalizeDecision maps a vendor decision/status token (or a boolean matched
// flag) onto the kyc screening vocabulary.
func normalizeDecision(resp httpScreenResponse) (string, bool) {
	for _, token := range []string{resp.Decision, resp.Status} {
		if decision, ok := decisionToken(token); ok {
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

// decisionToken maps one token onto the screening vocabulary.
func decisionToken(raw string) (string, bool) {
	token := strings.ToLower(strings.TrimSpace(raw))
	token = strings.NewReplacer("-", "_", " ", "_").Replace(token)
	switch token {
	case "clear", "no_match", "not_matched", "no_hit", "clean", "pass":
		return kyc.ScreenClear, true
	case "possible", "potential", "potential_match", "pep", "review", "needs_review", "manual_review":
		return kyc.ScreenPossible, true
	case "strong", "match", "matched", "exact_match", "confirmed_match", "sanctions", "sanctioned", "blocked", "hit":
		return kyc.ScreenStrong, true
	}
	return "", false
}

// defaultDecisionMessage returns a neutral message when the vendor omits one.
func defaultDecisionMessage(decision string) string {
	switch decision {
	case kyc.ScreenClear:
		return "no sanctions or PEP matches"
	case kyc.ScreenPossible:
		return "potential PEP match detected"
	default:
		return "sanctions match detected"
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

// shortHash builds a stable, short hex-ish tag for a name when a vendor does
// not return its own reference, so audit records always carry a provider ref.
func shortHash(value string) string {
	sum := 0
	for _, r := range value {
		sum = (sum*31 + int(r)) % 1000000
	}
	return fmt.Sprintf("%06d", sum)
}
