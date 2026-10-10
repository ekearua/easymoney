// Package adversemedia provides the adverse-media screening vendor behind the
// AdverseMediaScreener port for spec gap G9. It mirrors the
// cac/firs/geo/screening simulated-adapter convention: a deterministic,
// dependency-free stand-in for the real vendor (the Dojah screening purchase
// named in AML_VENDOR_DECISION_RECORD.md §11), so the domestic-list and
// adverse-media decision vocabulary is exercisable end to end and a live
// adapter can be dropped in behind the same interface without touching the
// service seam. Domestic lists (NSC/NIGSAC) are already owned through the
// internal watchlist (G3); this lane carries the adverse-media provider port.
package adversemedia

import (
	"context"
	"strings"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

// Simulated screens legal names against two deterministic adverse-media
// signals:
//
//   - a name containing "adverse" is a decisive media hit — blocked;
//   - a name containing "mediawatch" is an ambiguous media hit — strong
//     (hold for review, never auto-clear);
//   - everyone else is clear.
//
// The thresholds (name containment) are deliberately simple; the point of the
// simulator is the decision vocabulary and the seam's enforcement behavior,
// not matching quality.
type Simulated struct{}

// NewSimulated creates the deterministic simulated adverse-media screener.
func NewSimulated() *Simulated { return &Simulated{} }

// ProviderName names the vendor recorded on screening rows.
func (s *Simulated) ProviderName() string { return "simulated" }

// ScreenAdverseMedia applies the deterministic signals to the legal name.
func (s *Simulated) ScreenAdverseMedia(_ context.Context, req ports.AdverseMediaRequest) (ports.AdverseMediaDecision, error) {
	name := strings.TrimSpace(req.LegalName)
	if name == "" {
		return ports.AdverseMediaDecision{
			Decision:    kyc.ScreenPossible,
			MatchedHits: nil,
			Message:     "legal name missing; adverse-media screen cannot auto-clear",
		}, nil
	}
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "adverse"):
		return ports.AdverseMediaDecision{
			Decision: kyc.ScreenBlocked,
			MatchedHits: []ports.AdverseMediaHit{{
				Source:      "OpenSanctions ng_nigsac_sanctions (simulated)",
				Title:       "record flagged for adverse media",
				ProviderRef: "sim-adverse-1",
				ListVersion: "sim-1",
			}},
			ProviderRef: "sim-adverse-" + name,
			ListVersion: "sim-1",
			Message:     "simulated deterministic adverse-media list matched; blocked by the adverse-media simulator",
		}, nil
	case strings.Contains(lower, "mediawatch"):
		return ports.AdverseMediaDecision{
			Decision: kyc.ScreenStrong,
			MatchedHits: []ports.AdverseMediaHit{{
				Source:      "OpenSanctions ng_nigsac_sanctions (simulated)",
				Title:       "name resembles a flagged adverse-media record",
				ProviderRef: "sim-mediawatch-1",
				ListVersion: "sim-1",
			}},
			ProviderRef: "sim-mediawatch-" + name,
			ListVersion: "sim-1",
			Message:     "simulated deterministic adverse-media resemblance; review required (adverse-media simulator)",
		}, nil
	default:
		return ports.AdverseMediaDecision{
			Decision:    kyc.ScreenClear,
			ProviderRef: "sim-adverse-clear-" + name,
			ListVersion: "sim-1",
			Message:     "no simulated adverse-media list matches",
		}, nil
	}
}
