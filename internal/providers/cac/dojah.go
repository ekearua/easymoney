// This file wires Dojah's CAC lookup (GET /api/v1/kyc/cac/basic) behind the
// CorporateRegistryVerifier port, per docs/compliance/AML_VENDOR_DECISION_RECORD.md
// (section 11). Dojah authenticates with "Authorization: <secret key>" (raw, no
// Bearer prefix) and "AppId: <app id>", and requires both rc_number and
// company_type.
//
// The vendor returns the registered company record rather than a verdict, so
// this adapter compares the registered name against the declared business name
// (MatchName) and refuses a company that CAC marks inactive. An empty or
// unrecognized company status is an error, never a clear.
package cac

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

// Dojah verifies CAC registration numbers against Dojah's registry lookup.
type Dojah struct {
	baseURL     string
	appID       string
	secretKey   string
	httpClient  *http.Client
	companyType string
}

// NewDojah creates the Dojah CAC adapter. An empty baseURL defaults to
// production (https://api.dojah.io); an empty timeout defaults to 30 seconds;
// an empty companyType defaults to COMPANY.
func NewDojah(baseURL, appID, secretKey string, timeout time.Duration, companyType string) *Dojah {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.dojah.io"
	}
	kind := strings.ToUpper(strings.TrimSpace(companyType))
	if kind == "" {
		kind = "COMPANY"
	}
	return &Dojah{
		baseURL:     base,
		appID:       strings.TrimSpace(appID),
		secretKey:   strings.TrimSpace(secretKey),
		httpClient:  &http.Client{Timeout: timeout},
		companyType: kind,
	}
}

// ProviderName returns the adapter identifier for audit records.
func (d *Dojah) ProviderName() string { return "dojah" }

// dojahCACEntity is the documented /cac/basic entity.
type dojahCACEntity struct {
	CompanyName        string `json:"company_name"`
	TypeOfCompany      string `json:"type_of_company"`
	Status             string `json:"status"`
	RCNumber           string `json:"rc_number"`
	BusinessNumber     string `json:"business_number"`
	State              string `json:"state"`
	City               string `json:"city"`
	LGA                string `json:"lga"`
	DateOfRegistration string `json:"date_of_registration"`
}

type dojahCACResponse struct {
	Entity *dojahCACEntity `json:"entity"`
}

// VerifyCorporate performs one CAC lookup against Dojah.
func (d *Dojah) VerifyCorporate(ctx context.Context, request ports.CorporateRegistryRequest) (ports.CorporateRegistryResult, error) {
	number := strings.TrimSpace(request.RegistrationNumber)
	if number == "" {
		return ports.CorporateRegistryResult{}, fmt.Errorf("registration number is required")
	}
	query := url.Values{}
	query.Set("rc_number", number)
	query.Set("company_type", d.companyType)
	endpoint := d.baseURL + "/api/v1/kyc/cac/basic?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ports.CorporateRegistryResult{}, err
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
		return ports.CorporateRegistryResult{}, fmt.Errorf("corporate registry request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ports.CorporateRegistryResult{}, fmt.Errorf("read corporate registry response: %w", err)
	}

	ref := "DOJAH-CAC-" + digitsOf(number)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusNotFound:
		return ports.CorporateRegistryResult{
			Status:      ports.VerifyNotFound,
			ProviderRef: ref,
			Message:     "no company matched that registration number",
		}, nil
	case http.StatusUnauthorized:
		return ports.CorporateRegistryResult{}, fmt.Errorf("corporate registry API key rejected (401)")
	case http.StatusBadRequest:
		return ports.CorporateRegistryResult{}, fmt.Errorf("corporate registry rejected the request (400): %s", vendorMessage(body, "check the registration number and CAC company type"))
	case http.StatusPaymentRequired:
		return ports.CorporateRegistryResult{}, fmt.Errorf("corporate registry unavailable: Dojah wallet balance is too low (402)")
	case http.StatusTooManyRequests:
		return ports.CorporateRegistryResult{}, fmt.Errorf("corporate registry rate limit exceeded (429)")
	case http.StatusFailedDependency:
		return ports.CorporateRegistryResult{}, fmt.Errorf("corporate registry unavailable: Dojah upstream source failed (424)")
	default:
		return ports.CorporateRegistryResult{}, fmt.Errorf("corporate registry error HTTP %d: %s", resp.StatusCode, vendorMessage(body, "no message"))
	}

	var parsed dojahCACResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ports.CorporateRegistryResult{}, fmt.Errorf("decode corporate registry response: %w", err)
	}
	if parsed.Entity == nil || strings.TrimSpace(parsed.Entity.CompanyName) == "" {
		return ports.CorporateRegistryResult{}, fmt.Errorf("Dojah returned no CAC company record")
	}
	status, ok := dojahCACStatus(parsed.Entity.Status)
	if !ok {
		return ports.CorporateRegistryResult{}, fmt.Errorf("unrecognized CAC company status %q", parsed.Entity.Status)
	}
	matchName := status == ports.VerifyVerified
	if declared := strings.TrimSpace(request.BusinessName); declared != "" {
		matchName = matchName && namesMatch(declared, parsed.Entity.CompanyName)
	}
	message := defaultCorporateMessage(status)
	if status == ports.VerifyMismatch {
		message = fmt.Sprintf("CAC lists the company as %q", parsed.Entity.Status)
	}
	if matchName && strings.TrimSpace(request.BusinessName) != "" {
		message = "registered name matches the declared business name"
	}
	return ports.CorporateRegistryResult{
		Status:         status,
		RegisteredName: strings.TrimSpace(parsed.Entity.CompanyName),
		RegisteredAt:   strings.TrimSpace(parsed.Entity.DateOfRegistration),
		ProviderRef:    ref,
		MatchName:      matchName,
		Message:        message,
	}, nil
}

// dojahCACStatus maps CAC's company status onto the shared vocabulary. A
// company CAC reports as inactive/dissolved is a mismatch (the register holds
// the number but the entity is not usable); anything unrecognized is rejected.
func dojahCACStatus(raw string) (string, bool) {
	token := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(raw)))
	switch token {
	case "", "active", "registered":
		return ports.VerifyVerified, true
	case "not_active", "inactive", "dissolved", "struck_off", "struck_off_the_register", "cease", "ceased":
		return ports.VerifyMismatch, true
	}
	return "", false
}

// namesMatch compares a declared business name with the registered name after
// normalising punctuation, case and the common Ltd/Limited and Co/Company
// aliases, so "Acme Ltd" matches "ACME LIMITED" without accepting a different
// company.
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
