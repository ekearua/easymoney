// Package docauth provides provider adapters for document-authenticity
// verification: tamper detection and face-to-document comparison over the
// extracted reading a KYB/KYC upload kept (never the raw media).
//
// The simulator stands in for a live document-analysis endpoint (Smile ID /
// Youverify / Dojah, whose document-analysis path the vendor record names) while
// preserving the boundary a live vendor will use. Like the registry and
// location adapters, a check only ever proposes a verdict; a reviewer records
// the disposition, and the corporate hard gate counts a verified key only when
// the verdict is genuine.
package docauth

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

// Simulated is the deterministic authenticity stand-in used when no live
// document-analysis credentials are configured. Its contract is small and
// documented, mirroring the registry and location simulators:
//
//   - a blank reading is inconclusive (nothing to examine);
//   - a reading containing "tampered" or "forged" is tampered;
//   - a reading containing "blur" or "illegible" is inconclusive;
//   - anything else is genuine, at confidence 0.97;
//   - a face-to-document reference makes the result report FaceMatched.
type Simulated struct{}

// NewSimulated creates the deterministic document-authenticity simulator.
func NewSimulated() *Simulated { return &Simulated{} }

// ProviderName returns the adapter identifier for audit records.
func (s *Simulated) ProviderName() string { return "simulated" }

// VerifyDocumentAuthenticity applies the documented simulator contract.
func (s *Simulated) VerifyDocumentAuthenticity(_ context.Context, request ports.DocumentAuthenticityRequest) (ports.DocumentAuthenticityResult, error) {
	reading := strings.TrimSpace(request.Document)
	if reading == "" {
		return ports.DocumentAuthenticityResult{
			Status:  ports.AuthenticityInconclusive,
			Message: "no document text to examine",
		}, nil
	}
	lower := strings.ToLower(reading)
	switch {
	case strings.Contains(lower, "tampered") || strings.Contains(lower, "forged"):
		return ports.DocumentAuthenticityResult{
			Status:      ports.AuthenticityTampered,
			Confidence:  0.99,
			ProviderRef: refFor(request),
			Message:     "the document shows signs of alteration",
		}, nil
	case strings.Contains(lower, "blur") || strings.Contains(lower, "illegible"):
		return ports.DocumentAuthenticityResult{
			Status:      ports.AuthenticityInconclusive,
			ProviderRef: refFor(request),
			Message:     "the reading is too illegible to judge; a human must review the document",
		}, nil
	}
	return ports.DocumentAuthenticityResult{
		Status:      ports.AuthenticityGenuine,
		Confidence:  0.97,
		FaceMatched: strings.TrimSpace(request.FaceDocumentRef) != "",
		ProviderRef: refFor(request),
		Message:     "no signs of alteration",
	}, nil
}

// HTTP calls a generic REST document-analysis endpoint.
//
// Request (POST {baseURL}/document/analysis, Content-Type: application/json):
//
//	{"evidence_key":"cac_verified","document":"<extracted reading>",
//	 "legal_name":"Acme Ltd","face_document_ref":"selfie-ref"}
//
// Response:
//
//	{"status":          "genuine|tampered|inconclusive",
//	 "confidence":      0.97,
//	 "face_matched":    true,
//	 "reference":       "vendor-ref-123",
//	 "message":         "no signs of alteration"}
//
// "decision" is accepted as a fallback status token. Unknown decisions are
// rejected rather than treated as genuine, so an unrecognized vendor reply can
// never clear a document.
type HTTP struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewHTTP creates the generic document-analysis adapter. An empty timeout
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

type httpAuthenticityRequest struct {
	EvidenceKey     string `json:"evidence_key"`
	Document        string `json:"document"`
	LegalName       string `json:"legal_name,omitempty"`
	FaceDocumentRef string `json:"face_document_ref,omitempty"`
}

type httpAuthenticityResponse struct {
	Status      string  `json:"status"`
	Decision    string  `json:"decision"`
	Confidence  float64 `json:"confidence"`
	FaceMatched *bool   `json:"face_matched"`
	Reference   string  `json:"reference"`
	ProviderRef string  `json:"provider_ref"`
	Message     string  `json:"message"`
}

// VerifyDocumentAuthenticity performs one document-analysis call against the
// configured vendor.
func (h *HTTP) VerifyDocumentAuthenticity(ctx context.Context, request ports.DocumentAuthenticityRequest) (ports.DocumentAuthenticityResult, error) {
	reading := strings.TrimSpace(request.Document)
	if reading == "" {
		return ports.DocumentAuthenticityResult{}, fmt.Errorf("document text is required")
	}
	if h.baseURL == "" {
		return ports.DocumentAuthenticityResult{}, fmt.Errorf("document authenticity API base URL is not configured")
	}
	payload, err := json.Marshal(httpAuthenticityRequest{
		EvidenceKey:     strings.TrimSpace(request.EvidenceKey),
		Document:        reading,
		LegalName:       strings.TrimSpace(request.LegalName),
		FaceDocumentRef: strings.TrimSpace(request.FaceDocumentRef),
	})
	if err != nil {
		return ports.DocumentAuthenticityResult{}, err
	}
	body, err := h.post(ctx, "/document/analysis", payload)
	if err != nil {
		return ports.DocumentAuthenticityResult{}, err
	}
	var resp httpAuthenticityResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ports.DocumentAuthenticityResult{}, fmt.Errorf("decode document authenticity response: %w", err)
	}
	status, ok := normalizeAuthenticityStatus(resp)
	if !ok {
		return ports.DocumentAuthenticityResult{}, fmt.Errorf("unrecognized document authenticity status (status=%q decision=%q)", resp.Status, resp.Decision)
	}
	ref := firstNonEmpty(resp.Reference, resp.ProviderRef)
	if ref == "" {
		ref = "DOCAUTH-" + strings.TrimSpace(request.EvidenceKey)
	}
	message := strings.TrimSpace(resp.Message)
	if message == "" {
		message = defaultAuthenticityMessage(status)
	}
	faceMatched := false
	if resp.FaceMatched != nil {
		faceMatched = *resp.FaceMatched
	}
	return ports.DocumentAuthenticityResult{
		Status:      status,
		Confidence:  resp.Confidence,
		FaceMatched: faceMatched,
		ProviderRef: ref,
		Message:     message,
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
		return nil, fmt.Errorf("document authenticity request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read document authenticity response: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return body, nil
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("document authenticity API key rejected (401)")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("document authenticity rate limit exceeded (429)")
	}
	var errResp httpAuthenticityResponse
	if json.Unmarshal(body, &errResp) == nil && strings.TrimSpace(errResp.Message) != "" {
		return nil, fmt.Errorf("document authenticity error (HTTP %d): %s", resp.StatusCode, errResp.Message)
	}
	return nil, fmt.Errorf("document authenticity error HTTP %d", resp.StatusCode)
}

// normalizeAuthenticityStatus maps a vendor status token onto the shared
// authenticity vocabulary.
func normalizeAuthenticityStatus(resp httpAuthenticityResponse) (string, bool) {
	for _, token := range []string{resp.Status, resp.Decision} {
		normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(token)))
		switch normalized {
		case "genuine", "authentic", "pass", "passed", "valid", "ok", "clean":
			return ports.AuthenticityGenuine, true
		case "tampered", "forged", "altered", "fake", "fraudulent", "fail", "failed":
			return ports.AuthenticityTampered, true
		case "inconclusive", "unknown", "undetermined", "unreadable", "review", "manual":
			return ports.AuthenticityInconclusive, true
		}
	}
	return "", false
}

func defaultAuthenticityMessage(status string) string {
	switch status {
	case ports.AuthenticityGenuine:
		return "no signs of alteration"
	case ports.AuthenticityTampered:
		return "the document shows signs of alteration"
	}
	return "the provider could not judge the document"
}

// refFor builds a stable simulator provider reference from the evidence key.
func refFor(request ports.DocumentAuthenticityRequest) string {
	key := strings.TrimSpace(request.EvidenceKey)
	if key == "" {
		key = "document"
	}
	return "SIM-DOCAUTH-" + strings.ToUpper(key)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
