// Package geo provides provider adapters for operating-address verification:
// the address a merchant declared is geocoded, then compared against the
// position their browser reported with the visitor's permission.
//
// This is the only check that uses something the merchant cannot type: a
// declared address is easy to assert and hard to falsify when the browser has
// to agree with it. Like every other verifier here it proposes a disposition;
// a reviewer records the evidence.
package geo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

const (
	// DefaultMaxDistanceM is how far a reported position may sit from the
	// geocoded operating address and still count as the same place.
	DefaultMaxDistanceM = 5000
	// DefaultMaxAccuracyM rejects a browser fix too coarse to compare: a
	// network-derived position accurate to kilometres says nothing about a
	// street address, so it is sent to a human instead of being accepted.
	DefaultMaxAccuracyM = 2000
	// earthRadiusM is the mean Earth radius used by DistanceMeters.
	earthRadiusM = 6_371_000
)

// DistanceMeters returns the great-circle distance between two coordinates
// using the haversine formula. Exported so the browser-reported position and a
// vendor's resolved coordinates are compared the same way everywhere.
func DistanceMeters(lat1, lng1, lat2, lng2 float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp := (lat2 - lat1) * math.Pi / 180
	dl := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * earthRadiusM * math.Asin(math.Min(1, math.Sqrt(a)))
}

// Simulated is the stand-in used when no geocoding credentials are configured.
// Real address verification needs real geocoding, which a simulator does not
// do, so it reports only what it can honestly observe about the browser's fix:
//
//   - no coordinates reported -> no_location;
//   - accuracy absent (0) or worse than the configured maximum -> review,
//     because the fix is too coarse to compare;
//   - otherwise -> matched, with DistanceMeters = -1: the simulator does not
//     geocode, so it never claims a distance.
type Simulated struct {
	maxAccuracyM float64
}

// NewSimulated creates the deterministic location simulator.
func NewSimulated() *Simulated { return &Simulated{maxAccuracyM: DefaultMaxAccuracyM} }

// ProviderName returns the adapter identifier for audit records.
func (s *Simulated) ProviderName() string { return "simulated" }

// VerifyLocation applies the documented simulator contract.
func (s *Simulated) VerifyLocation(_ context.Context, request ports.LocationRequest) (ports.LocationResult, error) {
	if !hasCoordinates(request) {
		return ports.LocationResult{
			Status:         ports.VerifyNoLocation,
			DistanceMeters: -1,
			Message:        "no position was reported, so the address could not be compared",
		}, nil
	}
	if request.AccuracyMeters <= 0 || request.AccuracyMeters > s.maxAccuracyM {
		return ports.LocationResult{
			Status:         ports.VerifyReview,
			DistanceMeters: -1,
			Message:        "the reported position is too imprecise to compare with the address; a reviewer must judge it",
		}, nil
	}
	return ports.LocationResult{
		Status:         ports.VerifyMatched,
		DistanceMeters: -1,
		Message:        "a position was reported from this device at submission time",
	}, nil
}

// HTTP calls a generic REST geocoding/address-verification endpoint.
//
// Request (POST {baseURL}/location-verification, Content-Type: application/json):
//
//	{"address":"12 Marina, Lagos","utility_bill_ref":"MTR-123",
//	 "latitude":6.4531,"longitude":3.3958,"accuracy_m":25}
//
// Response:
//
//	{"status":"matched|review|no_location","distance_m":812.4,
//	 "resolved_address":"12 Marina Rd, Lagos Island","reference":"vendor-ref-123",
//	 "message":"address resolved within tolerance"}
//
// When the vendor returns a distance but no status, the configured maximum
// distance decides. Unknown statuses are rejected rather than treated as
// matched.
type HTTP struct {
	baseURL      string
	apiKey       string
	maxDistanceM float64
	httpClient   *http.Client
}

// NewHTTP creates the generic location adapter. A non-positive maxDistanceM
// falls back to DefaultMaxDistanceM; an empty timeout to 30 seconds.
func NewHTTP(baseURL, apiKey string, maxDistanceM float64, timeout time.Duration) *HTTP {
	if maxDistanceM <= 0 {
		maxDistanceM = DefaultMaxDistanceM
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTP{
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:       strings.TrimSpace(apiKey),
		maxDistanceM: maxDistanceM,
		httpClient:   &http.Client{Timeout: timeout},
	}
}

// ProviderName returns the adapter identifier for audit records.
func (h *HTTP) ProviderName() string { return "http" }

type httpLocationRequest struct {
	Address        string  `json:"address"`
	UtilityBillRef string  `json:"utility_bill_ref,omitempty"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	AccuracyMeters float64 `json:"accuracy_m"`
}

type httpLocationResponse struct {
	Status          string   `json:"status"`
	Decision        string   `json:"decision"`
	DistanceM       *float64 `json:"distance_m"`
	ResolvedAddress string   `json:"resolved_address"`
	Reference       string   `json:"reference"`
	ProviderRef     string   `json:"provider_ref"`
	Message         string   `json:"message"`
}

// VerifyLocation performs one address check against the configured vendor.
func (h *HTTP) VerifyLocation(ctx context.Context, request ports.LocationRequest) (ports.LocationResult, error) {
	if h.baseURL == "" {
		return ports.LocationResult{}, fmt.Errorf("location API base URL is not configured")
	}
	payload, err := json.Marshal(httpLocationRequest{
		Address:        strings.TrimSpace(request.Address),
		UtilityBillRef: strings.TrimSpace(request.UtilityBillRef),
		Latitude:       request.Latitude,
		Longitude:      request.Longitude,
		AccuracyMeters: request.AccuracyMeters,
	})
	if err != nil {
		return ports.LocationResult{}, err
	}
	body, err := h.post(ctx, "/location-verification", payload)
	if err != nil {
		return ports.LocationResult{}, err
	}
	var resp httpLocationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ports.LocationResult{}, fmt.Errorf("decode location response: %w", err)
	}
	status, ok := h.normalizeStatus(resp)
	if !ok {
		return ports.LocationResult{}, fmt.Errorf("unrecognized location status (status=%q decision=%q)", resp.Status, resp.Decision)
	}
	distance := -1.0
	if resp.DistanceM != nil {
		distance = *resp.DistanceM
	}
	ref := firstNonEmpty(resp.Reference, resp.ProviderRef)
	if ref == "" {
		ref = "GEO-" + strings.TrimSpace(request.UtilityBillRef)
	}
	message := strings.TrimSpace(resp.Message)
	if message == "" {
		message = defaultLocationMessage(status, distance, h.maxDistanceM)
	}
	return ports.LocationResult{
		Status:          status,
		DistanceMeters:  distance,
		ResolvedAddress: strings.TrimSpace(resp.ResolvedAddress),
		ProviderRef:     ref,
		Message:         message,
	}, nil
}

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
		return nil, fmt.Errorf("location request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read location response: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return body, nil
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("location API key rejected (401)")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("location rate limit exceeded (429)")
	}
	var errResp httpLocationResponse
	if json.Unmarshal(body, &errResp) == nil && strings.TrimSpace(errResp.Message) != "" {
		return nil, fmt.Errorf("location error (HTTP %d): %s", resp.StatusCode, errResp.Message)
	}
	return nil, fmt.Errorf("location error HTTP %d", resp.StatusCode)
}

// normalizeStatus maps a vendor status token onto the shared vocabulary, and
// derives one from the distance when the vendor returns a distance only.
func (h *HTTP) normalizeStatus(resp httpLocationResponse) (string, bool) {
	for _, token := range []string{resp.Status, resp.Decision} {
		normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(token)))
		switch normalized {
		case "matched", "match", "verified", "within_tolerance", "same", "ok":
			return ports.VerifyMatched, true
		case "review", "manual_review", "needs_review", "out_of_tolerance", "too_far":
			return ports.VerifyReview, true
		case "no_location", "no_position", "missing", "unknown":
			return ports.VerifyNoLocation, true
		}
	}
	if resp.DistanceM != nil && resp.Status == "" && resp.Decision == "" {
		if *resp.DistanceM >= 0 && *resp.DistanceM <= h.maxDistanceM {
			return ports.VerifyMatched, true
		}
		return ports.VerifyReview, true
	}
	return "", false
}

func defaultLocationMessage(status string, distance, maxDistance float64) string {
	switch status {
	case ports.VerifyMatched:
		if distance >= 0 {
			return fmt.Sprintf("the reported position is %.0fm from the address (within %.0fm)", distance, maxDistance)
		}
		return "the reported position is within tolerance of the address"
	case ports.VerifyReview:
		if distance >= 0 {
			return fmt.Sprintf("the reported position is %.0fm from the address (over %.0fm)", distance, maxDistance)
		}
		return "the position could not be compared with confidence"
	}
	return "no position was reported, so the address could not be compared"
}

func hasCoordinates(request ports.LocationRequest) bool {
	return request.Latitude != 0 || request.Longitude != 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
