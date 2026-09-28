// Package cac provides provider adapters for corporate-registry verification:
// a Corporate Affairs Commission registration number checked against the
// registered company name.
//
// The simulator stands in for a live CAC search (or a Smile ID / Youverify /
// Dojah corporate-KYC endpoint) while preserving the boundary a live vendor
// will use. Like the identity adapter, a check only ever proposes a
// disposition; a reviewer records the evidence.
package cac

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// simulatedCorporateNumber accepts the RC/CAC-prefixed or bare form of a
// Nigerian company registration number.
var simulatedCorporateNumber = regexp.MustCompile(`^(?i:RC|CAC)?[0-9]{6,10}$`)

// Simulated is the deterministic registry stand-in used when no live registry
// credentials are configured. Its contract is small and documented, mirroring
// the identity simulator:
//
//   - a 6-10 digit number (optionally prefixed RC or CAC) verifies;
//   - a number beginning with "0" is not on the register;
//   - a business name containing "mismatch" is a name mismatch, the signal a
//     reviewer should reject on.
type Simulated struct{}

// NewSimulated creates the deterministic corporate-registry simulator.
func NewSimulated() *Simulated { return &Simulated{} }

// ProviderName returns the adapter identifier for audit records.
func (s *Simulated) ProviderName() string { return "simulated" }

// VerifyCorporate applies the documented simulator contract.
func (s *Simulated) VerifyCorporate(_ context.Context, request ports.CorporateRegistryRequest) (ports.CorporateRegistryResult, error) {
	number := strings.TrimSpace(request.RegistrationNumber)
	name := strings.TrimSpace(request.BusinessName)
	if number == "" {
		return ports.CorporateRegistryResult{}, fmt.Errorf("registration number is required")
	}
	if !simulatedCorporateNumber.MatchString(number) {
		return ports.CorporateRegistryResult{
			Status:  ports.VerifyNotFound,
			Message: "no company matched that registration number",
		}, nil
	}
	digits := digitsOf(number)
	if strings.HasPrefix(digits, "0") {
		return ports.CorporateRegistryResult{
			Status:  ports.VerifyNotFound,
			Message: "no company matched that registration number",
		}, nil
	}
	if strings.Contains(strings.ToLower(name), "mismatch") {
		return ports.CorporateRegistryResult{
			Status:         ports.VerifyMismatch,
			RegisteredName: "NAME ON THE REGISTER",
			ProviderRef:    "SIM-CAC-" + digits,
			Message:        "the registered company name differs from the name supplied",
		}, nil
	}
	return ports.CorporateRegistryResult{
		Status:         ports.VerifyVerified,
		RegisteredName: name,
		RegisteredAt:   "2019-03-14",
		ProviderRef:    "SIM-CAC-" + digits,
		MatchName:      name != "",
		Message:        "company record found; the name matches",
	}, nil
}

// HTTP calls a generic REST corporate-registry endpoint.
//
// Request (POST {baseURL}/corporate-verification, Content-Type: application/json):
//
//	{"registration_number":"RC1234567","business_name":"Acme Ltd"}
//
// Response:
//
//	{"status":          "verified|mismatch|not_found",
//	 "registered_name": "Acme Ltd",
//	 "registration_date":"2019-03-14",
//	 "reference":       "vendor-ref-123",
//	 "message":         "company record found"}
//
// "name_matches" is accepted as a boolean fallback when no status token is
// recognizable. Unknown decisions are rejected rather than treated as clear.
type HTTP struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewHTTP creates the generic corporate-registry adapter. An empty timeout
// defaults to 30 seconds.
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

type httpCorporateRequest struct {
	RegistrationNumber string `json:"registration_number"`
	BusinessName       string `json:"business_name,omitempty"`
}

type httpCorporateResponse struct {
	Status           string `json:"status"`
	Decision         string `json:"decision"`
	NameMatches      *bool  `json:"name_matches"`
	RegisteredName   string `json:"registered_name"`
	RegistrationDate string `json:"registration_date"`
	Reference        string `json:"reference"`
	ProviderRef      string `json:"provider_ref"`
	Message          string `json:"message"`
}

// VerifyCorporate performs one registry lookup against the configured vendor.
func (h *HTTP) VerifyCorporate(ctx context.Context, request ports.CorporateRegistryRequest) (ports.CorporateRegistryResult, error) {
	number := strings.TrimSpace(request.RegistrationNumber)
	if number == "" {
		return ports.CorporateRegistryResult{}, fmt.Errorf("registration number is required")
	}
	if h.baseURL == "" {
		return ports.CorporateRegistryResult{}, fmt.Errorf("corporate registry API base URL is not configured")
	}
	payload, err := json.Marshal(httpCorporateRequest{
		RegistrationNumber: number,
		BusinessName:       strings.TrimSpace(request.BusinessName),
	})
	if err != nil {
		return ports.CorporateRegistryResult{}, err
	}
	body, err := h.post(ctx, "/corporate-verification", payload)
	if err != nil {
		return ports.CorporateRegistryResult{}, err
	}
	var resp httpCorporateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ports.CorporateRegistryResult{}, fmt.Errorf("decode corporate registry response: %w", err)
	}
	status, ok := normalizeCorporateStatus(resp)
	if !ok {
		return ports.CorporateRegistryResult{}, fmt.Errorf("unrecognized corporate registry status (status=%q decision=%q)", resp.Status, resp.Decision)
	}
	ref := firstNonEmpty(resp.Reference, resp.ProviderRef)
	if ref == "" {
		ref = "CAC-" + digitsOf(number)
	}
	message := strings.TrimSpace(resp.Message)
	if message == "" {
		message = defaultCorporateMessage(status)
	}
	matchName := status == ports.VerifyVerified
	if resp.NameMatches != nil && (resp.Status != "" || resp.Decision != "") {
		matchName = *resp.NameMatches
	}
	return ports.CorporateRegistryResult{
		Status:         status,
		RegisteredName: strings.TrimSpace(resp.RegisteredName),
		RegisteredAt:   strings.TrimSpace(resp.RegistrationDate),
		ProviderRef:    ref,
		MatchName:      matchName,
		Message:        message,
	}, nil
}

// post performs the HTTP POST and returns the body, mapping transport-level
// failures to operator-safe errors.
func (h *HTTP) post(ctx context.Context, path string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if h.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
		req.Header.Set("X-API-Key", h.apiKey)
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("corporate registry request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read corporate registry response: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return body, nil
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("corporate registry API key rejected (401)")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("corporate registry rate limit exceeded (429)")
	}
	var errResp httpCorporateResponse
	if json.Unmarshal(body, &errResp) == nil && strings.TrimSpace(errResp.Message) != "" {
		return nil, fmt.Errorf("corporate registry error (HTTP %d): %s", resp.StatusCode, errResp.Message)
	}
	return nil, fmt.Errorf("corporate registry error HTTP %d", resp.StatusCode)
}

// normalizeCorporateStatus maps a vendor status token (or a boolean
// name_matches) onto the shared verification vocabulary.
func normalizeCorporateStatus(resp httpCorporateResponse) (string, bool) {
	for _, token := range []string{resp.Status, resp.Decision} {
		normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(token)))
		switch normalized {
		case "verified", "valid", "active", "exists", "found", "registered", "match", "matched", "ok":
			return ports.VerifyVerified, true
		case "mismatch", "name_mismatch", "different", "discrepancy":
			return ports.VerifyMismatch, true
		case "not_found", "no_match", "unknown", "invalid":
			return ports.VerifyNotFound, true
		}
	}
	if resp.NameMatches != nil && resp.Status == "" && resp.Decision == "" {
		if *resp.NameMatches {
			return ports.VerifyVerified, true
		}
		return ports.VerifyMismatch, true
	}
	return "", false
}

func defaultCorporateMessage(status string) string {
	switch status {
	case ports.VerifyVerified:
		return "company record found; the name matches"
	case ports.VerifyMismatch:
		return "the registered company name differs from the name supplied"
	}
	return "no company matched that registration number"
}

// digitsOf strips every non-digit from a registration number so the provider
// reference is stable across the RC-prefixed and bare forms.
func digitsOf(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
