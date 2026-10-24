// This file wires Dojah's liveness endpoint (POST /api/v1/ml/liveness) behind
// the LivenessVerifier port, per docs/compliance/AML_VENDOR_DECISION_RECORD.md
// (G10). Dojah authenticates with "Authorization: <secret key>" (raw) and
// "AppId: <app id>". The endpoint accepts a Base64 selfie image and answers
// with entity.liveness.liveness_check / liveness_probability, which this
// adapter maps onto the shared passed/failed vocabulary.
//
// The port carries a capture reference today, not an image: the chat and
// browser EDD flows record "chat-confirm:<user>" / "browser-edd:<user>" rather
// than a selfie. A non-image capture is rejected as an error (fail closed)
// instead of being sent to the vendor as if it were a photo.
package liveness

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

// Dojah verifies selfies against Dojah's liveness API.
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

// ProviderName returns the adapter identifier for audit records.
func (d *Dojah) ProviderName() string { return "dojah" }

// dojahLivenessRequest is the documented wire body: a Base64 image with no
// data: URI prefix.
type dojahLivenessRequest struct {
	Image string `json:"image"`
}

// dojahLivenessEntity is the documented reply body. It is reached through a
// pointer so an empty body is distinguishable from a failed check.
type dojahLivenessEntity struct {
	Liveness struct {
		Check       bool    `json:"liveness_check"`
		Probability float64 `json:"liveness_probability"`
	} `json:"liveness"`
	Face struct {
		Detected bool   `json:"face_detected"`
		Message  string `json:"message"`
	} `json:"face"`
}

type dojahLivenessResponse struct {
	Entity *dojahLivenessEntity `json:"entity"`
}

// VerifyLiveness performs one face-liveness check against Dojah. A missing or
// non-image capture is an error rather than a pass, because the gate exists
// precisely so an absent selfie cannot grant EDD.
func (d *Dojah) VerifyLiveness(ctx context.Context, request ports.LivenessRequest) (ports.LivenessResult, error) {
	image, ok := decodeImage(request.CaptureRef)
	if !ok {
		return ports.LivenessResult{}, fmt.Errorf("Dojah liveness requires a Base64 selfie image; the capture reference is not an image")
	}
	entity, err := d.lookup(ctx, image)
	if err != nil {
		return ports.LivenessResult{}, err
	}
	status := ports.LivenessFailed
	message := defaultLivenessMessage(status)
	if entity.Liveness.Check {
		status = ports.LivenessPassed
		message = defaultLivenessMessage(status)
	} else if !entity.Face.Detected && strings.TrimSpace(entity.Face.Message) != "" {
		message = "no face was detected in the capture"
	}
	return ports.LivenessResult{
		Status:      status,
		ProviderRef: "DOJAH-LIVE-" + shortHash(image),
		Confidence:  clampProbability(entity.Liveness.Probability),
		Message:     message,
	}, nil
}

// lookup performs the HTTP POST with Dojah's auth headers and decodes the
// documented response.
func (d *Dojah) lookup(ctx context.Context, image string) (*dojahLivenessEntity, error) {
	body, err := json.Marshal(dojahLivenessRequest{Image: image})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/api/v1/ml/liveness", bytes.NewReader(body))
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
		return nil, fmt.Errorf("liveness request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap (images)
	if err != nil {
		return nil, fmt.Errorf("read liveness response: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// fall through to parse
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("liveness API key rejected (401)")
	case http.StatusPaymentRequired:
		return nil, fmt.Errorf("liveness unavailable: Dojah wallet balance is too low (402)")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("liveness rate limit exceeded (429)")
	case http.StatusFailedDependency:
		return nil, fmt.Errorf("liveness unavailable: Dojah upstream source failed (424)")
	default:
		var errResp struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil {
			if msg := firstNonEmpty(errResp.Message, errResp.Error); msg != "" {
				return nil, fmt.Errorf("liveness error (HTTP %d): %s", resp.StatusCode, msg)
			}
		}
		return nil, fmt.Errorf("liveness error HTTP %d", resp.StatusCode)
	}

	var parsed dojahLivenessResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode liveness response: %w", err)
	}
	if parsed.Entity == nil {
		return nil, fmt.Errorf("Dojah returned no liveness entity")
	}
	return parsed.Entity, nil
}

// decodeImage strips a data: URI prefix, removes whitespace, and validates the
// remainder as Base64. A short reference such as "chat-confirm:<id>" fails the
// Base64 check and is therefore rejected rather than sent as an image.
func decodeImage(capture string) (string, bool) {
	value := strings.TrimSpace(capture)
	if idx := strings.Index(value, "base64,"); idx >= 0 {
		value = value[idx+len("base64,"):]
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
	if value == "" {
		return "", false
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if _, err := encoding.DecodeString(value); err == nil {
			return value, true
		}
	}
	return "", false
}

// clampProbability converts Dojah's percentage (0-100) into the port's 0-1
// confidence scale.
func clampProbability(probability float64) float64 {
	value := probability / 100
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
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
