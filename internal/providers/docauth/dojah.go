// This file wires the Dojah document-analysis endpoint
// (POST /api/v1/document/analysis) behind the DocumentAuthenticityVerifier
// port, per docs/compliance/AML_VENDOR_DECISION_RECORD.md §11.1 (G12). Dojah authenticates
// with "Authorization: <secret key>" (raw) and "AppId: <app id>". The
// response is normalized into the shared genuine/tampered/inconclusive
// vocabulary, so a reviewer-verified key counts as evidence only when the
// verdict is genuine.
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

// VerifyDocumentAuthenticity performs one document-analysis call against Dojah.
func (d *Dojah) VerifyDocumentAuthenticity(ctx context.Context, request ports.DocumentAuthenticityRequest) (ports.DocumentAuthenticityResult, error) {
	reading := strings.TrimSpace(request.Document)
	if reading == "" {
		return ports.DocumentAuthenticityResult{}, fmt.Errorf("document text is required")
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
	body, err := d.post(ctx, "/document/analysis", payload)
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
		ref = "DOJAH-" + strings.TrimSpace(request.EvidenceKey)
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

// post performs the HTTP POST with Dojah's auth headers and returns the body,
// mapping transport-level failures to operator-safe errors.
func (d *Dojah) post(ctx context.Context, path string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+path, bytes.NewReader(payload))
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
