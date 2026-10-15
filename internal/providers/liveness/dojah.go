// This file wires the Dojah biometric liveness endpoint
// (POST /api/v1/ml/liveness) behind the LivenessVerifier port, per
// docs/compliance/AML_VENDOR_DECISION_RECORD.md §11.1 (G10). Dojah authenticates with
// "Authorization: <secret key>" (raw) and "AppId: <app id>". The response is
// normalized into the passed/failed/review vocabulary, so the Tier-3 EDD gate
// grants EvEDDCompleted only on a provider pass.
package liveness

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

// Dojah is the biometric liveness adapter for the Tier-3 EDD step.
type Dojah struct {
	baseURL    string
	appID      string
	secretKey  string
	httpClient *http.Client
}

// NewDojah creates the Dojah liveness adapter. An empty baseURL defaults to
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

// ProviderName labels the outcome in the verification's provider column.
func (d *Dojah) ProviderName() string { return "dojah" }

type dojahLivenessRequest struct {
	UserID       string `json:"user_id,omitempty"`
	LegalName    string `json:"legal_name,omitempty"`
	PhoneNumber  string `json:"phone_number,omitempty"`
	SelfieRef    string `json:"selfie_ref"`
	BrowserImage string `json:"image_ref,omitempty"`
}

type dojahLivenessResponse struct {
	Status      string  `json:"status"`
	Decision    string  `json:"decision"`
	Live        *bool   `json:"live"`
	Passed      *bool   `json:"passed"`
	Confidence  float64 `json:"confidence"`
	Liveness    float64 `json:"liveness_score"`
	Reference   string  `json:"reference"`
	ProviderRef string  `json:"provider_ref"`
	Message     string  `json:"message"`
}

// VerifyLiveness performs one face-liveness check against Dojah. A missing
// capture is an error rather than a pass, because the gate exists precisely so
// an absent selfie cannot grant EDD.
func (d *Dojah) VerifyLiveness(ctx context.Context, request ports.LivenessRequest) (ports.LivenessResult, error) {
	capture := strings.TrimSpace(request.CaptureRef)
	if capture == "" {
		return ports.LivenessResult{}, fmt.Errorf("a liveness capture is required")
	}
	resp, err := d.lookup(ctx, request)
	if err != nil {
		return ports.LivenessResult{}, err
	}
	status, ok := dojahLivenessStatus(resp)
	if !ok {
		return ports.LivenessResult{}, fmt.Errorf("unrecognized liveness status (status=%q decision=%q)", resp.Status, resp.Decision)
	}
	ref := firstNonEmpty(resp.Reference, resp.ProviderRef)
	if ref == "" {
		ref = "DOJAH-LIVE-" + shortHash(capture)
	}
	confidence := resp.Confidence
	if confidence <= 0 {
		confidence = resp.Liveness
	}
	message := strings.TrimSpace(resp.Message)
	if message == "" {
		message = defaultLivenessMessage(status)
	}
	return ports.LivenessResult{
		Status:      status,
		ProviderRef: ref,
		Confidence:  confidence,
		Message:     message,
	}, nil
}

// lookup performs the HTTP POST with Dojah's auth headers and decodes the
// normalized response envelope.
func (d *Dojah) lookup(ctx context.Context, request ports.LivenessRequest) (dojahLivenessResponse, error) {
	payload := dojahLivenessRequest{
		UserID:       strings.TrimSpace(request.UserID),
		LegalName:    strings.TrimSpace(request.LegalName),
		PhoneNumber:  strings.TrimSpace(request.PhoneNumber),
		SelfieRef:    strings.TrimSpace(request.CaptureRef),
		BrowserImage: strings.TrimSpace(request.CaptureRef),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return dojahLivenessResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/api/v1/ml/liveness", bytes.NewReader(body))
	if err != nil {
		return dojahLivenessResponse{}, err
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
		return dojahLivenessResponse{}, fmt.Errorf("liveness request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return dojahLivenessResponse{}, fmt.Errorf("read liveness response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusUnauthorized:
		return dojahLivenessResponse{}, fmt.Errorf("liveness API key rejected (401)")
	case http.StatusTooManyRequests:
		return dojahLivenessResponse{}, fmt.Errorf("liveness rate limit exceeded (429)")
	default:
		var errResp dojahLivenessResponse
		if json.Unmarshal(respBody, &errResp) == nil && strings.TrimSpace(errResp.Message) != "" {
			return dojahLivenessResponse{}, fmt.Errorf("liveness error (HTTP %d): %s", resp.StatusCode, errResp.Message)
		}
		return dojahLivenessResponse{}, fmt.Errorf("liveness error HTTP %d", resp.StatusCode)
	}

	var parsed dojahLivenessResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return dojahLivenessResponse{}, fmt.Errorf("decode liveness response: %w", err)
	}
	return parsed, nil
}

// dojahLivenessStatus maps a vendor status token (or boolean live/passed flag)
// onto the shared liveness vocabulary.
func dojahLivenessStatus(resp dojahLivenessResponse) (string, bool) {
	for _, token := range []string{resp.Status, resp.Decision} {
		normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(token)))
		switch normalized {
		case "passed", "pass", "live", "genuine", "success", "ok", "cleared":
			return ports.LivenessPassed, true
		case "failed", "fail", "rejected", "spoof", "no_liveness", "not_live":
			return ports.LivenessFailed, true
		case "review", "manual", "inconclusive", "unknown", "undetermined", "pending":
			return ports.LivenessReview, true
		}
	}
	if resp.Live != nil && resp.Status == "" && resp.Decision == "" {
		if *resp.Live {
			return ports.LivenessPassed, true
		}
		return ports.LivenessFailed, true
	}
	if resp.Passed != nil && resp.Status == "" && resp.Decision == "" {
		if *resp.Passed {
			return ports.LivenessPassed, true
		}
		return ports.LivenessFailed, true
	}
	return "", false
}

func defaultLivenessMessage(status string) string {
	switch status {
	case ports.LivenessPassed:
		return "live capture verified"
	case ports.LivenessFailed:
		return "the liveness check did not pass"
	default:
		return "the liveness check needs a manual review"
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func shortHash(value string) string {
	sum := 0
	for _, r := range value {
		sum = (sum*31 + int(r)) % 1000000
	}
	return fmt.Sprintf("%06d", sum)
}
