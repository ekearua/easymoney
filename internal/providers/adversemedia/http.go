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
)

// Sentinel errors mapped onto user-safe messages by ScreenAdverseMedia.
var (
	errUnauthorized = fmt.Errorf("unauthorized")
	errRateLimited  = fmt.Errorf("rate limited")
)

// HTTP calls the Dojah adverse-media endpoint named in the vendor decision
// record §11.
//
// Request (POST {baseURL}/api/v1/aml/v2/screening, Content-Type: application/json):
//
//	{
//	  "legal_name":      "Ada Obi",
//	  "phone":           "+2348012345678",
//	  "match_threshold": 0.85
//	}
//
// Headers: "Authorization: <secret key>" (raw, no Bearer prefix) and
// "AppId: <app id>", per the vendor record §11.1.
//
// Response:
//
//	{
//	  "decision":     "clear|possible|strong|blocked",
//	  "hits":         [{"source":"...","title":"...","published_at":"...",
//	                    "reference":"...","list_version":"..."}],
//	  "reference":    "vendor-ref-123",
//	  "list_version": "2026-10",
//	  "message":      "no adverse-media matches"
//	}
//
// "status" is accepted in place of "decision". Unknown decisions are rejected
// rather than silently treated as clear, so an unrecognized vendor reply can
// never auto-clear a customer.
type HTTP struct {
	baseURL        string
	appID          string
	secretKey      string
	httpClient     *http.Client
	matchThreshold float64
}

// NewHTTP creates the Dojah adverse-media adapter. An empty timeout defaults
// to 30 seconds.
func NewHTTP(baseURL, appID, secretKey string, timeout time.Duration, matchThreshold float64) *HTTP {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTP{
		baseURL:        strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		appID:          strings.TrimSpace(appID),
		secretKey:      strings.TrimSpace(secretKey),
		httpClient:     &http.Client{Timeout: timeout},
		matchThreshold: matchThreshold,
	}
}

// ProviderName returns the adapter identifier for audit records.
func (h *HTTP) ProviderName() string { return "http" }

// httpAdverseRequest is the wire body sent to the vendor.
type httpAdverseRequest struct {
	LegalName      string  `json:"legal_name"`
	Phone          string  `json:"phone,omitempty"`
	MatchThreshold float64 `json:"match_threshold"`
}

// httpAdverseResponse is the normalized response envelope we accept.
type httpAdverseResponse struct {
	Decision    string           `json:"decision"`
	Status      string           `json:"status"`
	Hits        []httpAdverseHit `json:"hits"`
	Reference   string           `json:"reference"`
	ProviderRef string           `json:"provider_ref"`
	ListVersion string           `json:"list_version"`
	Message     string           `json:"message"`
}

// httpAdverseHit is one hit entry in the vendor envelope.
type httpAdverseHit struct {
	Source      string `json:"source"`
	Title       string `json:"title"`
	PublishedAt string `json:"published_at"`
	Reference   string `json:"reference"`
	ListVersion string `json:"list_version"`
}

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

	resp, err := h.lookup(ctx, request)
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

	decision, ok := normalizeAdverseDecision(resp)
	if !ok {
		return ports.AdverseMediaDecision{}, fmt.Errorf("unrecognized adverse-media decision (decision=%q status=%q)", resp.Decision, resp.Status)
	}

	hits := make([]ports.AdverseMediaHit, 0, len(resp.Hits))
	listVersion := strings.TrimSpace(resp.ListVersion)
	for _, hit := range resp.Hits {
		hits = append(hits, ports.AdverseMediaHit{
			Source:      strings.TrimSpace(hit.Source),
			Title:       strings.TrimSpace(hit.Title),
			PublishedAt: strings.TrimSpace(hit.PublishedAt),
			ProviderRef: strings.TrimSpace(hit.Reference),
			ListVersion: strings.TrimSpace(hit.ListVersion),
		})
		if listVersion == "" {
			listVersion = strings.TrimSpace(hit.ListVersion)
		}
	}

	ref := firstNonEmpty(resp.Reference, resp.ProviderRef)
	if ref == "" && len(hits) > 0 {
		ref = hits[0].ProviderRef
	}
	if ref == "" {
		ref = "HTTP-AMS-" + shortHash(name)
	}
	message := strings.TrimSpace(resp.Message)
	if message == "" {
		message = defaultAdverseMessage(decision)
	}
	return ports.AdverseMediaDecision{
		Decision:    decision,
		MatchedHits: hits,
		ProviderRef: ref,
		ListVersion: listVersion,
		Message:     message,
	}, nil
}

// lookup performs the HTTP POST and decodes the JSON response.
func (h *HTTP) lookup(ctx context.Context, request ports.AdverseMediaRequest) (httpAdverseResponse, error) {
	payload := httpAdverseRequest{
		LegalName:      strings.TrimSpace(request.LegalName),
		Phone:          strings.TrimSpace(request.PhoneNumber),
		MatchThreshold: h.matchThreshold,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return httpAdverseResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/api/v1/aml/v2/screening", bytes.NewReader(body))
	if err != nil {
		return httpAdverseResponse{}, err
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
		return httpAdverseResponse{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return httpAdverseResponse{}, fmt.Errorf("read response body: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusUnauthorized:
		return httpAdverseResponse{}, errUnauthorized
	case http.StatusTooManyRequests:
		return httpAdverseResponse{}, errRateLimited
	default:
		var errResp httpAdverseResponse
		if json.Unmarshal(respBody, &errResp) == nil && strings.TrimSpace(errResp.Message) != "" {
			return httpAdverseResponse{}, fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, errResp.Message)
		}
		return httpAdverseResponse{}, fmt.Errorf("API error HTTP %d", resp.StatusCode)
	}

	var parsed httpAdverseResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return httpAdverseResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return parsed, nil
}

// normalizeAdverseDecision maps a vendor decision/status token onto the kyc
// screening vocabulary.
func normalizeAdverseDecision(resp httpAdverseResponse) (string, bool) {
	for _, token := range []string{resp.Decision, resp.Status} {
		if decision, ok := adverseDecisionToken(token); ok {
			return decision, true
		}
	}
	return "", false
}

// adverseDecisionToken maps one token onto the screening vocabulary.
func adverseDecisionToken(raw string) (string, bool) {
	token := strings.ToLower(strings.TrimSpace(raw))
	token = strings.NewReplacer("-", "_", " ", "_").Replace(token)
	switch token {
	case "clear", "no_match", "not_matched", "no_hit", "clean", "pass",
		"no_adverse_media", "no_adverse", "no_alerts":
		return kyc.ScreenClear, true
	case "possible", "potential", "potential_match", "pep", "review", "needs_review",
		"manual_review", "alerts", "alert", "adverse", "adverse_media", "media_match":
		return kyc.ScreenPossible, true
	case "strong", "match", "matched", "exact_match", "confirmed_match", "hit", "hits":
		return kyc.ScreenStrong, true
	case "blocked", "block", "sanctioned", "prohibited", "confirmed", "interdict":
		return kyc.ScreenBlocked, true
	}
	return "", false
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
