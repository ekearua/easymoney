package adversemedia

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

// Sentinel errors mapped onto user-safe messages by ScreenAdverseMedia.
var (
	errUnauthorized = fmt.Errorf("unauthorized")
	errRateLimited  = fmt.Errorf("rate limited")
)

// HTTP calls Dojah's adverse-media lane through the shared AML screening
// endpoint named in the vendor decision record.
//
// Request (POST {baseURL}/api/v2/aml/screening, Content-Type: application/json):
//
//	{
//	  "schema": "individual",
//	  "properties": {"names": "Ada Obi", "id_number": ""},
//	  "screening_options": {"adverse_media_check": true, "match_threshold": 0.85}
//	}
//
// Headers: "Authorization: <secret key>" (raw, no Bearer prefix) and
// "AppId: <app id>", per the vendor record.
//
// Response: the documented AML entity, whose ADVERSE_MEDIA groups carry
// categorised articles and whose match_status is one of "No Match",
// "Partially Matched", "Potential Match" or "Confirmed Match". Unknown
// statuses are rejected rather than silently treated as clear, so an
// unrecognized vendor reply can never auto-clear a customer.
type HTTP struct {
	baseURL        string
	appID          string
	secretKey      string
	httpClient     *http.Client
	matchThreshold float64
	listVersion    string
	provider       string
}

// NewHTTP creates the adverse-media adapter for an explicitly configured
// endpoint. The base URL, app id and secret key are the Dojah credentials; the
// provider name recorded on the row is "http".
func NewHTTP(baseURL, appID, secretKey string, timeout time.Duration, matchThreshold float64, listVersion string) *HTTP {
	return newHTTP(baseURL, appID, secretKey, timeout, matchThreshold, listVersion, "http")
}

// NewDojah creates the adverse-media adapter from the shared DOJAH_* settings,
// recording the vendor name as "dojah".
func NewDojah(baseURL, appID, secretKey string, timeout time.Duration, matchThreshold float64, listVersion string) *HTTP {
	return newHTTP(baseURL, appID, secretKey, timeout, matchThreshold, listVersion, "dojah")
}

func newHTTP(baseURL, appID, secretKey string, timeout time.Duration, matchThreshold float64, listVersion, provider string) *HTTP {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTP{
		baseURL:        strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		appID:          strings.TrimSpace(appID),
		secretKey:      strings.TrimSpace(secretKey),
		httpClient:     &http.Client{Timeout: timeout},
		matchThreshold: matchThreshold,
		listVersion:    strings.TrimSpace(listVersion),
		provider:       provider,
	}
}

// ProviderName returns the adapter identifier for audit records.
func (h *HTTP) ProviderName() string { return h.provider }

// ScreenAdverseMedia performs one adverse-media lookup against the configured
// vendor.
func (h *HTTP) ScreenAdverseMedia(ctx context.Context, request ports.AdverseMediaRequest) (ports.AdverseMediaDecision, error) {
	name := strings.TrimSpace(request.LegalName)
	if name == "" {
		return ports.AdverseMediaDecision{}, fmt.Errorf("legal name is required for adverse-media screening")
	}
	if h.baseURL == "" {
		return ports.AdverseMediaDecision{}, fmt.Errorf("adverse-media API base URL is not configured")
	}

	entity, err := h.lookup(ctx, name)
	if err != nil {
		switch {
		case err == errUnauthorized:
			return ports.AdverseMediaDecision{}, fmt.Errorf("adverse-media API key rejected (401)")
		case err == errRateLimited:
			return ports.AdverseMediaDecision{}, fmt.Errorf("adverse-media rate limit exceeded (429)")
		default:
			return ports.AdverseMediaDecision{}, fmt.Errorf("adverse-media lookup: %w", err)
		}
	}

	decision, ok := dojahaml.Decision(entity.MatchStatus)
	if !ok {
		return ports.AdverseMediaDecision{}, fmt.Errorf("unrecognized adverse-media match status %q", entity.MatchStatus)
	}

	hits := make([]ports.AdverseMediaHit, 0, len(entity.Results))
	for _, result := range entity.Results {
		if !strings.EqualFold(result.SourceType, "ADVERSE_MEDIA") {
			continue
		}
		for _, article := range result.Articles {
			hits = append(hits, ports.AdverseMediaHit{
				Source:      strings.TrimSpace(article.Source),
				Title:       strings.TrimSpace(article.Headline),
				PublishedAt: strings.TrimSpace(article.Timestamp),
				ProviderRef: strings.TrimSpace(entity.EntityID),
				ListVersion: h.listVersion,
			})
		}
	}

	ref := strings.TrimSpace(entity.EntityID)
	if ref == "" && len(hits) > 0 {
		ref = hits[0].ProviderRef
	}
	if ref == "" {
		ref = "ADVERSE-" + shortHash(name)
	}
	return ports.AdverseMediaDecision{
		Decision:    decision,
		MatchedHits: hits,
		ProviderRef: ref,
		ListVersion: h.listVersion,
		Message:     defaultAdverseMessage(decision),
	}, nil
}

// lookup performs the HTTP POST and decodes the documented AML response.
func (h *HTTP) lookup(ctx context.Context, name string) (*dojahaml.Entity, error) {
	payload := dojahaml.Request{
		Schema: "individual",
		Properties: dojahaml.Properties{
			Names:    name,
			IDNumber: "",
		},
		ScreeningOptions: dojahaml.Options{
			AdverseMediaCheck: true,
			MatchThreshold:    h.matchThreshold,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+dojahaml.Path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if h.secretKey != "" {
		req.Header.Set("Authorization", h.secretKey)
	}
	if h.appID != "" {
		req.Header.Set("AppId", h.appID)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusUnauthorized:
		return nil, errUnauthorized
	case http.StatusPaymentRequired:
		return nil, fmt.Errorf("Dojah wallet balance is too low (402)")
	case http.StatusTooManyRequests:
		return nil, errRateLimited
	case http.StatusFailedDependency:
		return nil, fmt.Errorf("Dojah upstream source failed (424)")
	default:
		var errResp struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil {
			if msg := firstNonEmpty(errResp.Message, errResp.Error); msg != "" {
				return nil, fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, msg)
			}
		}
		return nil, fmt.Errorf("API error HTTP %d", resp.StatusCode)
	}

	var parsed dojahaml.Response
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if parsed.Entity == nil {
		return nil, fmt.Errorf("Dojah returned no adverse-media entity")
	}
	return parsed.Entity, nil
}

// defaultAdverseMessage returns a neutral message when the vendor omits one.
func defaultAdverseMessage(decision string) string {
	switch decision {
	case kyc.ScreenClear:
		return "no adverse-media matches"
	case kyc.ScreenPossible:
		return "potential adverse-media match detected"
	case kyc.ScreenStrong:
		return "adverse-media match detected"
	default:
		return "adverse-media interdiction"
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
