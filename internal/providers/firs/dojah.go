// This file wires Dojah's FIRS TIN lookup (GET /api/v1/kyc/tin) behind the
// TaxIDVerifier port, per docs/compliance/AML_VENDOR_DECISION_RECORD.md
// (section 11). Dojah authenticates with "Authorization: <secret key>" (raw, no
// Bearer prefix) and "AppId: <app id>".
//
// The vendor returns the taxpayer record rather than a verdict, so this adapter
// compares the registered taxpayer name against the declared business name
// (MatchName). A missing record (404) is reported as not_found so the reviewer
// sees a clean outcome; transport and vendor failures remain errors.
package firs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// Dojah verifies Tax Identification Numbers against Dojah's FIRS lookup.
type Dojah struct {
	baseURL    string
	appID      string
	secretKey  string
	httpClient *http.Client
}

// NewDojah creates the Dojah TIN adapter. An empty baseURL defaults to
// production (https://api.dojah.io); an empty timeout defaults to 30 seconds.
func NewDojah(baseURL, appID, secretKey string, timeout time.Duration) *Dojah {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.dojah.io"
	}
	return &Dojah{
		baseURL:    base,
		appID:      strings.TrimSpace(appID),
		secretKey:  strings.TrimSpace(secretKey),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// ProviderName returns the adapter identifier for audit records.
func (d *Dojah) ProviderName() string { return "dojah" }

// dojahTINEntity is the documented /kyc/tin entity.
type dojahTINEntity struct {
	Search       string `json:"search"`
	TaxpayerName string `json:"taxpayer_name"`
	CACRegNumber string `json:"cac_reg_number"`
	FirstTIN     string `json:"firstin"`
	JITTin       string `json:"jittin"`
	TaxOffice    string `json:"tax_office"`
	PhoneNumber  string `json:"phone_number"`
	Email        string `json:"email"`
}

type dojahTINResponse struct {
	Entity *dojahTINEntity `json:"entity"`
}

// VerifyTaxID performs one FIRS TIN lookup against Dojah.
func (d *Dojah) VerifyTaxID(ctx context.Context, request ports.TaxIDRequest) (ports.TaxIDResult, error) {
	tin := normalizeTIN(request.TIN)
	if tin == "" {
		return ports.TaxIDResult{}, fmt.Errorf("TIN is required")
	}
	query := url.Values{}
	query.Set("tin", tin)
	endpoint := d.baseURL + "/api/v1/kyc/tin?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ports.TaxIDResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	if d.secretKey != "" {
		req.Header.Set("Authorization", d.secretKey)
	}
	if d.appID != "" {
		req.Header.Set("AppId", d.appID)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return ports.TaxIDResult{}, fmt.Errorf("tax authority request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ports.TaxIDResult{}, fmt.Errorf("read tax authority response: %w", err)
	}

	ref := "DOJAH-TIN-" + tin
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusNotFound:
		return ports.TaxIDResult{
			Status:      ports.VerifyNotFound,
			ProviderRef: ref,
			Message:     "no taxpayer matched that TIN",
		}, nil
	case http.StatusUnauthorized:
		return ports.TaxIDResult{}, fmt.Errorf("tax authority API key rejected (401)")
	case http.StatusBadRequest:
		return ports.TaxIDResult{}, fmt.Errorf("tax authority rejected the request (400): %s", vendorMessage(body, "check the TIN format"))
	case http.StatusPaymentRequired:
		return ports.TaxIDResult{}, fmt.Errorf("tax authority unavailable: Dojah wallet balance is too low (402)")
	case http.StatusTooManyRequests:
		return ports.TaxIDResult{}, fmt.Errorf("tax authority rate limit exceeded (429)")
	case http.StatusFailedDependency:
		return ports.TaxIDResult{}, fmt.Errorf("tax authority unavailable: Dojah upstream source failed (424)")
	default:
		return ports.TaxIDResult{}, fmt.Errorf("tax authority error HTTP %d: %s", resp.StatusCode, vendorMessage(body, "no message"))
	}

	var parsed dojahTINResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ports.TaxIDResult{}, fmt.Errorf("decode tax authority response: %w", err)
	}
	if parsed.Entity == nil || strings.TrimSpace(parsed.Entity.TaxpayerName) == "" {
		return ports.TaxIDResult{}, fmt.Errorf("Dojah returned no FIRS taxpayer record")
	}
	matchName := true
	if declared := strings.TrimSpace(request.BusinessName); declared != "" {
		matchName = namesMatch(declared, parsed.Entity.TaxpayerName)
	}
	message := "taxpayer record found"
	if !matchName {
		message = "FIRS taxpayer name differs from the declared business name"
	}
	return ports.TaxIDResult{
		Status:         ports.VerifyVerified,
		RegisteredName: strings.TrimSpace(parsed.Entity.TaxpayerName),
		ProviderRef:    ref,
		MatchName:      matchName,
		Message:        message,
	}, nil
}

// namesMatch compares a declared business name with the registered taxpayer
// name after normalising punctuation, case and the common Ltd/Limited and
// Co/Company aliases.
func namesMatch(declared, registered string) bool {
	return normalizeCompanyName(declared) == normalizeCompanyName(registered)
}

func normalizeCompanyName(value string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	fields := strings.Fields(b.String())
	for i, field := range fields {
		switch field {
		case "limited":
			fields[i] = "ltd"
		case "company":
			fields[i] = "co"
		case "incorporated":
			fields[i] = "inc"
		}
	}
	return strings.Join(fields, " ")
}

func vendorMessage(body []byte, fallback string) string {
	var parsed struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		if msg := firstNonEmpty(parsed.Message, parsed.Error); msg != "" {
			return msg
		}
	}
	return fallback
}
