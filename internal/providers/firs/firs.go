// Package firs provides provider adapters for tax-identification verification:
// a business Tax Identification Number checked against the registered taxpayer.
//
// The simulator stands in for a live FIRS lookup while preserving the boundary
// a live vendor will use. A check proposes a disposition; a reviewer records
// the evidence.
package firs

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

// simulatedTIN accepts a 10-13 digit taxpayer number, optionally hyphenated.
var simulatedTIN = regexp.MustCompile(`^[0-9]{10,13}$`)

// Simulated is the deterministic tax-authority stand-in used when no live
// credentials are configured. Its contract mirrors the identity simulator:
//
//   - a 10-13 digit TIN verifies;
//   - a TIN beginning with "0" is not on the register;
//   - a business name containing "mismatch" is a name mismatch.
type Simulated struct{}

// NewSimulated creates the deterministic TIN simulator.
func NewSimulated() *Simulated { return &Simulated{} }

// ProviderName returns the adapter identifier for audit records.
func (s *Simulated) ProviderName() string { return "simulated" }

// VerifyTaxID applies the documented simulator contract.
func (s *Simulated) VerifyTaxID(_ context.Context, request ports.TaxIDRequest) (ports.TaxIDResult, error) {
	tin := normalizeTIN(request.TIN)
	name := strings.TrimSpace(request.BusinessName)
	if tin == "" {
		return ports.TaxIDResult{}, fmt.Errorf("TIN is required")
	}
	if !simulatedTIN.MatchString(tin) {
		return ports.TaxIDResult{
			Status:  ports.VerifyNotFound,
			Message: "no taxpayer matched that TIN",
		}, nil
	}
	if strings.HasPrefix(tin, "0") {
		return ports.TaxIDResult{
			Status:  ports.VerifyNotFound,
			Message: "no taxpayer matched that TIN",
		}, nil
	}
	if strings.Contains(strings.ToLower(name), "mismatch") {
		return ports.TaxIDResult{
			Status:         ports.VerifyMismatch,
			RegisteredName: "TAXPAYER ON THE REGISTER",
			ProviderRef:    "SIM-TIN-" + tin,
			Message:        "the registered taxpayer name differs from the name supplied",
		}, nil
	}
	return ports.TaxIDResult{
		Status:         ports.VerifyVerified,
		RegisteredName: name,
		ProviderRef:    "SIM-TIN-" + tin,
		MatchName:      name != "",
		Message:        "taxpayer record found; the name matches",
	}, nil
}

// HTTP calls a generic REST tax-authority endpoint.
//
// Request (POST {baseURL}/tin-verification, Content-Type: application/json):
//
//	{"tin":"1234567890","business_name":"Acme Ltd"}
//
// Response:
//
//	{"status":"verified|mismatch|not_found","registered_name":"Acme Ltd",
//	 "reference":"vendor-ref-123","message":"taxpayer record found"}
//
// "name_matches" is accepted as a boolean fallback when no status token is
// recognizable.
type HTTP struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewHTTP creates the generic tax-authority adapter. An empty timeout defaults
// to 30 seconds.
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

type httpTINRequest struct {
	TIN          string `json:"tin"`
	BusinessName string `json:"business_name,omitempty"`
}

type httpTINResponse struct {
	Status         string `json:"status"`
	Decision       string `json:"decision"`
	NameMatches    *bool  `json:"name_matches"`
	RegisteredName string `json:"registered_name"`
	Reference      string `json:"reference"`
	ProviderRef    string `json:"provider_ref"`
	Message        string `json:"message"`
}

// VerifyTaxID performs one TIN lookup against the configured vendor.
func (h *HTTP) VerifyTaxID(ctx context.Context, request ports.TaxIDRequest) (ports.TaxIDResult, error) {
	tin := normalizeTIN(request.TIN)
	if tin == "" {
		return ports.TaxIDResult{}, fmt.Errorf("TIN is required")
	}
	if h.baseURL == "" {
		return ports.TaxIDResult{}, fmt.Errorf("tax authority API base URL is not configured")
	}
	payload, err := json.Marshal(httpTINRequest{TIN: tin, BusinessName: strings.TrimSpace(request.BusinessName)})
	if err != nil {
		return ports.TaxIDResult{}, err
	}
	body, err := h.post(ctx, "/tin-verification", payload)
	if err != nil {
		return ports.TaxIDResult{}, err
	}
	var resp httpTINResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ports.TaxIDResult{}, fmt.Errorf("decode tax authority response: %w", err)
	}
	status, ok := normalizeTINStatus(resp)
	if !ok {
		return ports.TaxIDResult{}, fmt.Errorf("unrecognized tax authority status (status=%q decision=%q)", resp.Status, resp.Decision)
	}
	ref := firstNonEmpty(resp.Reference, resp.ProviderRef)
	if ref == "" {
		ref = "TIN-" + tin
	}
	message := strings.TrimSpace(resp.Message)
	if message == "" {
		message = defaultTINMessage(status)
	}
	matchName := status == ports.VerifyVerified
	if resp.NameMatches != nil && (resp.Status != "" || resp.Decision != "") {
		matchName = *resp.NameMatches
	}
	return ports.TaxIDResult{
		Status:         status,
		RegisteredName: strings.TrimSpace(resp.RegisteredName),
		ProviderRef:    ref,
		MatchName:      matchName,
		Message:        message,
	}, nil
}

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
		return nil, fmt.Errorf("tax authority request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read tax authority response: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return body, nil
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("tax authority API key rejected (401)")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("tax authority rate limit exceeded (429)")
	}
	var errResp httpTINResponse
	if json.Unmarshal(body, &errResp) == nil && strings.TrimSpace(errResp.Message) != "" {
		return nil, fmt.Errorf("tax authority error (HTTP %d): %s", resp.StatusCode, errResp.Message)
	}
	return nil, fmt.Errorf("tax authority error HTTP %d", resp.StatusCode)
}

// normalizeTINStatus maps a vendor status token (or a boolean name_matches)
// onto the shared verification vocabulary.
func normalizeTINStatus(resp httpTINResponse) (string, bool) {
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

func defaultTINMessage(status string) string {
	switch status {
	case ports.VerifyVerified:
		return "taxpayer record found; the name matches"
	case ports.VerifyMismatch:
		return "the registered taxpayer name differs from the name supplied"
	}
	return "no taxpayer matched that TIN"
}

// normalizeTIN strips separators so a hyphenated TIN compares to the bare form.
func normalizeTIN(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
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
