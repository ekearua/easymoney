// Package identity provides provider adapters for NIN/BVN identity
// verification. The simulator stands in for Smile ID / Youverify / Dojah /
// Prembly while preserving the same boundary a live vendor will use later.
//
// The NINBVNPortal adapter calls the live ninbvnportal.com.ng lookup API and
// maps the returned record onto the IdentityVerifier port semantics.
package identity

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
	"unicode"

	"whatsapp-payment-demo/internal/ports"
)

// ninbvnPortalDigits validates the 11-digit ID number format.
var ninbvnPortalDigits = regexp.MustCompile(`^\d{11}$`)

// NINBVNPortal calls the live ninbvnportal.com.ng lookup API. The API is
// read-only: it returns the full record for a NIN or BVN. The adapter then
// compares the returned name/DOB against the request to map onto the
// IdentityVerifier port (verified / mismatch / not_found).
type NINBVNPortal struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewNINBVNPortal creates the live identity verification adapter.
func NewNINBVNPortal(baseURL, apiKey string, timeout time.Duration) *NINBVNPortal {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &NINBVNPortal{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// ProviderName returns the adapter identifier for audit records.
func (p *NINBVNPortal) ProviderName() string {
	return "ninbvnportal"
}

// VerifyIdentity calls the NINBVNPORTAL lookup endpoint and compares the
// returned record against the supplied name and date of birth.
func (p *NINBVNPortal) VerifyIdentity(ctx context.Context, request ports.IdentityVerificationRequest) (ports.IdentityVerificationResult, error) {
	number := strings.TrimSpace(request.IDNumber)
	idType := strings.ToUpper(strings.TrimSpace(request.IDType))
	if idType != "NIN" && idType != "BVN" {
		return ports.IdentityVerificationResult{}, fmt.Errorf("unsupported id type %q", request.IDType)
	}
	if !ninbvnPortalDigits.MatchString(number) {
		return ports.IdentityVerificationResult{}, fmt.Errorf("%s must be exactly 11 digits", idType)
	}

	// --- call the lookup API ---
	record, err := p.lookup(ctx, idType, number)
	if err != nil {
		// Map HTTP-level errors to port semantics.
		switch {
		case err == errNotFound:
			return ports.IdentityVerificationResult{
				Status:  "not_found",
				Message: fmt.Sprintf("no %s record matched", idType),
			}, nil
		case err == errUnauthorized:
			return ports.IdentityVerificationResult{}, fmt.Errorf("NINBVNPORTAL API key rejected (401)")
		case err == errRateLimited:
			return ports.IdentityVerificationResult{}, fmt.Errorf("NINBVNPORTAL rate limit exceeded (429)")
		default:
			return ports.IdentityVerificationResult{}, fmt.Errorf("NINBVNPORTAL lookup: %w", err)
		}
	}

	// --- compare returned record against the request ---
	matchName := false
	matchDOB := false
	var nameParts []string
	if strings.TrimSpace(record.Firstname) != "" {
		nameParts = append(nameParts, strings.TrimSpace(record.Firstname))
	}
	if strings.TrimSpace(record.Middlename) != "" {
		nameParts = append(nameParts, strings.TrimSpace(record.Middlename))
	}
	if strings.TrimSpace(record.Surname) != "" {
		nameParts = append(nameParts, strings.TrimSpace(record.Surname))
	}
	returnedName := strings.Join(nameParts, " ")

	if request.LegalName != "" && returnedName != "" {
		matchName = normalizeName(request.LegalName) == normalizeName(returnedName)
	}
	if request.DateOfBirth != "" && record.Birthdate != "" {
		matchDOB = normalizeDate(request.DateOfBirth) == normalizeDate(record.Birthdate)
	}

	status := "verified"
	if !matchName || !matchDOB {
		status = "mismatch"
	}

	return ports.IdentityVerificationResult{
		Status:      status,
		ProviderRef: record.ReportID,
		MatchName:   matchName,
		MatchDOB:    matchDOB,
		Message:     fmt.Sprintf("%s record found", idType),
	}, nil
}

// --- internal API types and helpers ---

// ninbvnRecord holds the fields we extract from the NINBVNPORTAL response.
type ninbvnRecord struct {
	ReportID   string `json:"reportID"`
	Firstname  string `json:"firstname"`
	Middlename string `json:"middlename"`
	Surname    string `json:"surname"`
	Birthdate  string `json:"birthdate"`
	Gender     string `json:"gender"`
}

// ninbvnAPIResponse is the envelope returned by all NINBVNPORTAL endpoints.
type ninbvnAPIResponse struct {
	Status  string         `json:"status"`
	ReportID string        `json:"reportID"`
	Message string         `json:"message"`
	Data    ninbvnRecord   `json:"data"`
}

var errNotFound = fmt.Errorf("record not found")
var errUnauthorized = fmt.Errorf("unauthorized")
var errRateLimited = fmt.Errorf("rate limited")

// lookup calls the appropriate NINBVNPORTAL endpoint for the given ID type.
func (p *NINBVNPortal) lookup(ctx context.Context, idType, number string) (ninbvnRecord, error) {
	endpoint := "/api/nin-verification"
	payload := map[string]string{"nin": number, "consent": "true"}
	if idType == "BVN" {
		endpoint = "/api/bvn-verification"
		payload = map[string]string{"bvn": number, "consent": "true"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return ninbvnRecord{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return ninbvnRecord{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return ninbvnRecord{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return ninbvnRecord{}, fmt.Errorf("read response body: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusUnauthorized:
		return ninbvnRecord{}, errUnauthorized
	case http.StatusTooManyRequests:
		return ninbvnRecord{}, errRateLimited
	default:
		// Try to extract an error message from the JSON body.
		var errResp ninbvnAPIResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return ninbvnRecord{}, fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, errResp.Message)
		}
		return ninbvnRecord{}, fmt.Errorf("API error HTTP %d", resp.StatusCode)
	}

	var apiResp ninbvnAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return ninbvnRecord{}, fmt.Errorf("decode response: %w", err)
	}
	if strings.ToLower(apiResp.Status) != "success" {
		if strings.Contains(strings.ToLower(apiResp.Message), "not found") || strings.Contains(strings.ToLower(apiResp.Message), "no record") {
			return ninbvnRecord{}, errNotFound
		}
		return ninbvnRecord{}, fmt.Errorf("API status %q: %s", apiResp.Status, apiResp.Message)
	}

	return apiResp.Data, nil
}

// normalizeName lowercases and collapses whitespace, stripping non-letter
// characters except spaces, for comparison purposes.
func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Collapse multiple spaces.
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return s
}

// normalizeDate tries to parse a date string in common Nigerian formats and
// returns it in YYYY-MM-DD for comparison.
func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	// Already YYYY-MM-DD.
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("2006-01-02")
	}
	// DD/MM/YYYY or DD-MM-YYYY.
	for _, sep := range []string{"/", "-"} {
		parts := strings.Split(s, sep)
		if len(parts) == 3 {
			if t, err := time.Parse("02/01/2006", parts[0]+"/"+parts[1]+"/"+parts[2]); err == nil {
				return t.Format("2006-01-02")
			}
		}
	}
	// Strip non-digit characters and try YYYYMMDD.
	digits := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
	if len(digits) == 8 {
		if t, err := time.Parse("20060102", digits); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return strings.ToLower(s)
}
