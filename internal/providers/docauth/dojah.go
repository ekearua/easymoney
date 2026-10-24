// This file wires Dojah's document-analysis endpoint
// (POST /api/v1/document/analysis) behind the DocumentAuthenticityVerifier
// port, per docs/compliance/AML_VENDOR_DECISION_RECORD.md (G12). Dojah
// authenticates with "Authorization: <secret key>" (raw) and "AppId: <app id>".
//
// The endpoint analyses an image (Base64 or a URL) and answers with
// entity.status.overall_status: 1 is VALID, 0 is INVALID. That maps onto the
// shared genuine / tampered vocabulary; a reply without a status is rejected,
// so a reviewer-verified key counts as evidence only on a real "valid" verdict.
//
// The platform stores extracted document text, not the uploaded file
// ("never a raw uploaded file" in store.KYBDocuments). When the value on file
// is text rather than an image or URL, the adapter returns an error instead of
// sending unanalysable text to the document engine.
package docauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"whatsapp-payment-demo/internal/ports"
)

// Dojah is the document-authenticity adapter for the KYB/KYC reviewer panel.
type Dojah struct {
	baseURL    string
	appID      string
	secretKey  string
	httpClient *http.Client
}

// NewDojah creates the Dojah document-analysis adapter. An empty baseURL
// defaults to production (https://api.dojah.io); an empty timeout defaults to
// 30 seconds.
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

// dojahDocumentRequest is the documented wire body.
type dojahDocumentRequest struct {
	InputType      string `json:"input_type"`
	ImageFrontSide string `json:"imagefrontside"`
}

// dojahDocumentEntity is the documented reply body. It is reached through a
// pointer so an empty body is distinguishable from an invalid document.
type dojahDocumentEntity struct {
	Status struct {
		OverallStatus  int    `json:"overall_status"`
		Reason         string `json:"reason"`
		DocumentType   string `json:"document_type"`
		DocumentImages string `json:"document_images"`
		Text           string `json:"text"`
		Expiry         string `json:"expiry"`
	} `json:"status"`
	DocumentType struct {
		DocumentName        string `json:"document_name"`
		DocumentCountryName string `json:"document_country_name"`
		DocumentCountryCode string `json:"document_country_code"`
	} `json:"document_type"`
}

type dojahDocumentResponse struct {
	Entity *dojahDocumentEntity `json:"entity"`
}

// VerifyDocumentAuthenticity performs one document-analysis call against Dojah.
func (d *Dojah) VerifyDocumentAuthenticity(ctx context.Context, request ports.DocumentAuthenticityRequest) (ports.DocumentAuthenticityResult, error) {
	inputType, image, ok := documentImage(request.Document)
	if !ok {
		return ports.DocumentAuthenticityResult{}, fmt.Errorf("Dojah document analysis requires the document image (Base64 or URL); only extracted text is on file")
	}
	payload, err := json.Marshal(dojahDocumentRequest{
		InputType:      inputType,
		ImageFrontSide: image,
	})
	if err != nil {
		return ports.DocumentAuthenticityResult{}, err
	}
	entity, err := d.post(ctx, payload)
	if err != nil {
		return ports.DocumentAuthenticityResult{}, err
	}
	status, ok := dojahAuthenticityStatus(entity.Status.OverallStatus)
	if !ok {
		return ports.DocumentAuthenticityResult{}, fmt.Errorf("unrecognized document authenticity status %d (reason %q)", entity.Status.OverallStatus, entity.Status.Reason)
	}
	return ports.DocumentAuthenticityResult{
		Status:      status,
		ProviderRef: "DOJAH-" + strings.TrimSpace(request.EvidenceKey),
		Message:     authenticityMessage(status, entity.Status.Reason),
	}, nil
}

// post performs the HTTP POST with Dojah's auth headers and returns the
// decoded entity.
func (d *Dojah) post(ctx context.Context, payload []byte) (*dojahDocumentEntity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/api/v1/document/analysis", bytes.NewReader(payload))
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("document authenticity request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap (images)
	if err != nil {
		return nil, fmt.Errorf("read document authenticity response: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("document authenticity API key rejected (401)")
	case http.StatusPaymentRequired:
		return nil, fmt.Errorf("document authenticity unavailable: Dojah wallet balance is too low (402)")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("document authenticity rate limit exceeded (429)")
	case http.StatusFailedDependency:
		return nil, fmt.Errorf("document authenticity unavailable: Dojah could not process the document (424)")
	default:
		var errResp struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil {
			if msg := firstNonEmpty(errResp.Message, errResp.Error); msg != "" {
				return nil, fmt.Errorf("document authenticity error (HTTP %d): %s", resp.StatusCode, msg)
			}
		}
		return nil, fmt.Errorf("document authenticity error HTTP %d", resp.StatusCode)
	}
	var parsed dojahDocumentResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode document authenticity response: %w", err)
	}
	if parsed.Entity == nil {
		return nil, fmt.Errorf("Dojah returned no document-analysis entity")
	}
	return parsed.Entity, nil
}

// documentImage decides whether the stored value is a URL, a Base64 image, or
// something that cannot be analysed (extracted text). It returns the
// documented input_type and the sanitised value.
func documentImage(value string) (inputType, image string, ok bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", "", false
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return "url", trimmed, true
	}
	if idx := strings.Index(trimmed, "base64,"); idx >= 0 {
		trimmed = trimmed[idx+len("base64,"):]
	}
	trimmed = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, trimmed)
	if trimmed == "" {
		return "", "", false
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if _, err := encoding.DecodeString(trimmed); err == nil {
			return "base64", trimmed, true
		}
	}
	return "", "", false
}

// dojahAuthenticityStatus maps the documented overall_status onto the shared
// vocabulary: 1 is VALID, 0 is INVALID.
func dojahAuthenticityStatus(overallStatus int) (string, bool) {
	switch overallStatus {
	case 1:
		return ports.AuthenticityGenuine, true
	case 0:
		return ports.AuthenticityTampered, true
	}
	return "", false
}

// authenticityMessage keeps the vendor's reason when present so the reviewer
// sees why a document was rejected.
func authenticityMessage(status, reason string) string {
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		return defaultAuthenticityMessage(status) + " (" + trimmed + ")"
	}
	return defaultAuthenticityMessage(status)
}
