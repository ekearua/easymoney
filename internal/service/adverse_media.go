package service

import (
	"context"
	"strings"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// adverseMediaRank orders the shared screening vocabulary from least to most
// severe. Any unrecognized/empty token ranks -1 (no opinion).
func adverseMediaRank(decision string) int {
	switch decision {
	case kyc.ScreenClear:
		return 0
	case kyc.ScreenPossible:
		return 1
	case kyc.ScreenStrong:
		return 2
	case kyc.ScreenBlocked:
		return 3
	default:
		return -1
	}
}

// AdverseMediaStricter returns the stricter of two screening decisions, where
// clear < possible < strong < blocked. A rule with no opinion (unknown or
// empty) yields the other rule; when neither is recognized the result is
// empty.
func AdverseMediaStricter(a, b string) string {
	ra, rb := adverseMediaRank(a), adverseMediaRank(b)
	switch {
	case ra < 0 && rb < 0:
		return ""
	case ra < 0:
		return b
	case rb < 0:
		return a
	case ra >= rb:
		return a
	default:
		return b
	}
}

// FoldAdverseMedia folds the sanctions/PEP decision (base) with the
// adverse-media decision (adverse) into the single decision the onboarding and
// rescreen gates enforce: the stricter of the two wins.
func FoldAdverseMedia(base, adverse string) string {
	return AdverseMediaStricter(base, adverse)
}

// screenAdverseMedia runs the G9 adverse-media provider for the identity and,
// when the decision is non-clear, records the screening row (which opens an
// adverse_media review case). It returns the adverse-media decision; a nil
// screener returns an empty decision so the caller's fold is a no-op.
func (s *ConversationService) screenAdverseMedia(ctx context.Context, user store.User, legalName string) (string, error) {
	if s.adverseMediaScreener == nil {
		return "", nil
	}
	am, err := s.adverseMediaScreener.ScreenAdverseMedia(ctx, ports.AdverseMediaRequest{
		LegalName:   legalName,
		PhoneNumber: user.WhatsAppNumber,
	})
	if err != nil {
		return "", err
	}
	if am.Decision == kyc.ScreenClear {
		return am.Decision, nil
	}
	names := make([]string, 0, len(am.MatchedHits))
	for _, h := range am.MatchedHits {
		if strings.TrimSpace(h.Title) != "" {
			names = append(names, h.Title)
		}
	}
	if _, err := s.store.RecordAdverseMediaScreening(ctx, store.AdverseMediaScreening{
		UserID:       user.ID,
		Provider:     s.adverseMediaProviderName,
		Decision:     am.Decision,
		MatchedHits:  adverseMediaHits(am.MatchedHits),
		MatchedNames: names,
		LegalName:    legalName,
		ListVersion:  am.ListVersion,
		ProviderRef:  am.ProviderRef,
		Message:      am.Message,
	}); err != nil {
		return "", err
	}
	return am.Decision, nil
}

// adverseMediaHits maps the provider-neutral hit list onto the persisted store
// shape.
func adverseMediaHits(hits []ports.AdverseMediaHit) []store.AdverseMediaHit {
	out := make([]store.AdverseMediaHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, store.AdverseMediaHit{
			Source:      h.Source,
			Title:       h.Title,
			PublishedAt: h.PublishedAt,
			ProviderRef: h.ProviderRef,
			ListVersion: h.ListVersion,
		})
	}
	return out
}
