// Package identity provides provider adapters for NIN/BVN identity
// verification. The simulator stands in for Smile ID / Youverify / Dojah /
// Prembly while preserving the same boundary a live vendor will use later.
package identity

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"whatsapp-payment-demo/internal/ports"
)

var digits11 = regexp.MustCompile(`^[0-9]{11}$`)

// Simulator verifies identity locally for the demo. Deterministic rules:
//   - NIN starting with 9 → not found; starting with 8 → record mismatch;
//     otherwise verified.
//   - BVN starting with 9 → not found; starting with 8 → record mismatch;
//     otherwise verified.
type Simulator struct{}

// NewSimulator creates the local identity verification provider.
func NewSimulator() *Simulator {
	return &Simulator{}
}

// VerifyIdentity returns a deterministic simulated outcome.
func (s *Simulator) VerifyIdentity(_ context.Context, request ports.IdentityVerificationRequest) (ports.IdentityVerificationResult, error) {
	number := strings.TrimSpace(request.IDNumber)
	idType := strings.ToLower(request.IDType)
	if idType != "nin" && idType != "bvn" {
		return ports.IdentityVerificationResult{}, fmt.Errorf("unsupported id type %q", request.IDType)
	}
	if !digits11.MatchString(number) {
		return ports.IdentityVerificationResult{}, fmt.Errorf("%s must be exactly 11 digits", strings.ToUpper(idType))
	}
	status := "verified"
	message := fmt.Sprintf("%s verified", strings.ToUpper(idType))
	switch number[0] {
	case '9':
		status = "not_found"
		message = fmt.Sprintf("no %s record matched", strings.ToUpper(idType))
	case '8':
		status = "mismatch"
		message = "record matched but name or date of birth differ"
	}
	return ports.IdentityVerificationResult{
		Status:      status,
		ProviderRef: "SIM-" + strings.ToUpper(idType) + "-" + number,
		MatchName:   status == "verified" && strings.TrimSpace(request.LegalName) != "",
		MatchDOB:    status == "verified" && strings.TrimSpace(request.DateOfBirth) != "",
		Message:     message,
	}, nil
}
