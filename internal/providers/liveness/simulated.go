// Package liveness provides the biometric liveness (face-verification)
// adapters behind ports.LivenessVerifier. The deterministic simulator is the
// default so the Tier-3 EDD gate is exercisable without a vendor contract
// (spec G10); the live adapter drops in behind the same port when the
// identity purchase is made.
package liveness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"whatsapp-payment-demo/internal/ports"
)

// Simulated is the deterministic stand-in for a liveness vendor. A missing
// capture never passes (the gate exists precisely so an absent selfie cannot
// grant EDD); a reference marked "fail" is rejected and one marked "review"
// goes to a human; anything else passes. It is deliberately conservative:
// every outcome is derived from the capture reference, never assumed.
type Simulated struct{}

// NewSimulated returns the deterministic liveness verifier.
func NewSimulated() *Simulated { return &Simulated{} }

// ProviderName labels the outcome in the verification's provider column.
func (Simulated) ProviderName() string { return "simulated" }

// VerifyLiveness maps the capture reference onto a pass/fail/review verdict.
func (Simulated) VerifyLiveness(_ context.Context, request ports.LivenessRequest) (ports.LivenessResult, error) {
	ref := strings.TrimSpace(request.CaptureRef)
	if ref == "" {
		return ports.LivenessResult{
			Status:  ports.LivenessFailed,
			Message: "no liveness capture was provided",
		}, nil
	}
	lower := strings.ToLower(ref)
	switch {
	case strings.Contains(lower, "fail"):
		return ports.LivenessResult{
			Status:  ports.LivenessFailed,
			Message: "the liveness check did not pass",
		}, nil
	case strings.Contains(lower, "review"):
		return ports.LivenessResult{
			Status:      ports.LivenessReview,
			ProviderRef: simulatedRef(ref),
			Message:     "the liveness check needs a manual review",
		}, nil
	default:
		return ports.LivenessResult{
			Status:      ports.LivenessPassed,
			ProviderRef: simulatedRef(ref),
			Confidence:  0.97,
		}, nil
	}
}

// simulatedRef is a stable, non-reversible provider reference so the recorded
// evidence carries a reference like a live vendor would, without echoing the
// capture.
func simulatedRef(ref string) string {
	sum := sha256.Sum256([]byte("liveness:" + ref))
	return "sim-live-" + hex.EncodeToString(sum[:8])
}

// Compile-time assertion that the simulator satisfies the port.
var _ ports.LivenessVerifier = (*Simulated)(nil)
