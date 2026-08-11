// Package screening provides provider adapters for sanctions/PEP name
// screening. The simulator stands in for Smile ID / Youverify / Dojah /
// Prembly / OFAC while preserving the same boundary a live vendor uses later.
package screening

import (
	"context"
	"fmt"
	"strings"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

// Simulator returns deterministic sanctions/PEP outcomes for the demo.
// A legal name containing one of the blockedNames yields a "strong" match;
// names containing a PEP token yield "possible"; everything else is "clear".
type Simulator struct{}

// NewSimulator creates the local name screening provider.
func NewSimulator() *Simulator {
	return &Simulator{}
}

// blockedNames stand in for OFAC/UN/EU high-confidence sanctions matches.
var blockedNames = []string{
	"sanctioned", "terror", "money launder", "drug lord", "pep high risk",
}

// pepTokens indicate a lower-confidence political exposure.
var pepTokens = []string{"senator", "minister", "governor", "pep"}

// Screen returns a deterministic screening decision.
func (s *Simulator) Screen(_ context.Context, request ports.ScreeningRequest) (ports.ScreeningDecision, error) {
	name := strings.ToLower(strings.TrimSpace(request.LegalName))
	if name == "" {
		return ports.ScreeningDecision{}, fmt.Errorf("legal name is required for screening")
	}
	var matched []string
	for _, token := range blockedNames {
		if strings.Contains(name, token) {
			matched = append(matched, token)
		}
	}
	if len(matched) > 0 {
		return ports.ScreeningDecision{
			Decision:     kyc.ScreenStrong,
			MatchedNames: matched,
			ProviderRef:  "SIM-SCR-" + shortHash(name),
			Message:      "high-confidence sanctions match detected",
		}, nil
	}
	for _, token := range pepTokens {
		if strings.Contains(name, token) {
			matched = append(matched, token)
		}
	}
	if len(matched) > 0 {
		return ports.ScreeningDecision{
			Decision:     kyc.ScreenPossible,
			MatchedNames: matched,
			ProviderRef:  "SIM-SCR-" + shortHash(name),
			Message:      "potential PEP match detected",
		}, nil
	}
	return ports.ScreeningDecision{
		Decision:    kyc.ScreenClear,
		ProviderRef: "SIM-SCR-" + shortHash(name),
		Message:     "no sanctions or PEP matches",
	}, nil
}

func shortHash(value string) string {
	sum := 0
	for _, r := range value {
		sum = (sum*31 + int(r)) % 1000000
	}
	return fmt.Sprintf("%06d", sum)
}
