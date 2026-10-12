package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// Saved payment methods — the posture-neutral half of
// SAVED_CARD_METHODS_PLAN.md Phase 1.
//
// What this is NOT: a stored card. The platform holds no card data at all — the
// card rail renders Interswitch's Hosted Fields iframes, so the PAN never
// reaches us — and this layer only remembers *which rail* the payer chose, per
// surface, so the picker can stop asking the same question on every purchase.
// A saved card is a separate, gated decision (CARD_ON_FILE_DECISION.md): it
// changes PCI scope, and nothing here presumes its outcome.
//
// The remembered rail is a hint, never an authorisation. A payer arriving on a
// surface we remember still taps a rail and still pays; we only reorder the
// choices and name the habit in the copy. So a wrong or stale preference can
// cost one extra tap, and can never move money.

// DeclaredPaymentRails is every rail a collection payment may be drafted on.
// It is the single source of truth for rail validation, and the fee guard test
// asserts each entry prices through FeeChannelForProvider to a known fee
// channel — so adding a rail constant without a fee mapping fails the build
// instead of silently billing nothing.
var DeclaredPaymentRails = []string{ProviderInterswitch, ProviderBankTransfer, ProviderWallet}

// railExemptProviders is keyed by provider *value* (what is stored on a
// payment), and holds the provider strings that are not collection rails and
// therefore have no fee channel and no rail a payer could prefer:
//
//   - ProviderAuto selects a gateway through the router rather than naming how
//     the payer paid.
//   - ProviderSimulatedData names a data/VTU fulfilment provider, a different
//     rail family that is never a merchant collection.
//
// The fee guard test reads this to tell "declared and priced" from "not a rail
// at all", so every entry here is a deliberate decision rather than an
// oversight it should have caught.
var railExemptProviders = map[string]bool{
	ProviderAuto:          true,
	ProviderSimulatedData: true,
}

// IsExemptProviderValue reports whether a provider value is deliberately not a
// payable rail. The fee guard test reads this to tell "declared and priced"
// from "not a rail at all", so anything reachable as `provider` on a payment
// must appear either in DeclaredPaymentRails or here — never in neither.
func IsExemptProviderValue(value string) bool { return railExemptProviders[value] }

// IsDeclaredPaymentRail reports whether a rail is one we can draft a collection
// payment on.
func IsDeclaredPaymentRail(rail string) bool {
	for _, r := range DeclaredPaymentRails {
		if r == rail {
			return true
		}
	}
	return false
}

// RailDisplayName is the payer-facing name of a rail, for copy that names the
// remembered habit.
func RailDisplayName(rail string) string {
	switch rail {
	case ProviderInterswitch:
		return "Card checkout"
	case ProviderBankTransfer:
		return "Bank transfer"
	case ProviderWallet:
		return "Wallet"
	}
	return ""
}

// SavedRail returns the rail to offer a payer first on a surface, or "" when we
// know nothing about them. It is best-effort by design: a preference is a
// convenience, so a read failure degrades to "ask normally" rather than
// blocking a payment.
func (s *PaymentService) SavedRail(ctx context.Context, userID uuid.UUID, channel string, merchantID uuid.UUID) string {
	if s == nil || s.store == nil || userID == uuid.Nil || !store.ValidPreferenceChannel(channel) {
		return ""
	}
	rail, err := s.store.PaymentPreferenceFor(ctx, userID, channel, merchantID)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("payment preference read failed", "user_id", userID, "channel", channel, "error", err)
		}
		return ""
	}
	return rail
}

// RememberRail records the rail a payer chose. Only declared rails are stored,
// so an unknown value can never become a preference that is later offered —
// and, because an unmapped rail would price at zero fee, this is also where a
// bad rail is stopped before it can influence a future draft.
func (s *PaymentService) RememberRail(ctx context.Context, userID uuid.UUID, channel string, merchantID uuid.UUID, rail string) error {
	if s == nil || s.store == nil || !IsDeclaredPaymentRail(rail) {
		return nil
	}
	return s.store.RememberPaymentRail(ctx, userID, channel, merchantID, rail)
}

// rememberPaymentRail is the post-success hook: a payment that completed is the
// only evidence of what the payer actually wanted, so a failed or abandoned
// attempt changes nothing.
//
// It runs best-effort (logged, never returned) because a preference is not a
// money movement: failing it must not fail a payment that already succeeded, and
// the next successful payment simply writes it again.
func (s *PaymentService) rememberPaymentRail(ctx context.Context, payment store.PaymentView) {
	if s == nil || s.store == nil {
		return
	}
	if !IsDeclaredPaymentRail(payment.Provider) {
		return
	}
	channel := PaymentPreferenceChannel(payment.Channel)
	if channel == "" {
		return
	}
	if err := s.RememberRail(ctx, payment.UserID, channel, payment.MerchantID, payment.Provider); err != nil {
		if s.logger != nil {
			s.logger.Warn("remember payment rail failed", "payment_id", payment.ID, "error", err)
		}
	}
}

// PaymentPreferenceChannel maps the channel a payment was made on to the
// surface a preference is remembered per. A payment's channel is the delivery
// context it was drafted in; anything that is not a chat payment is the browser
// surface.
func PaymentPreferenceChannel(paymentChannel string) string {
	switch strings.ToLower(strings.TrimSpace(paymentChannel)) {
	case ChannelWhatsApp:
		return store.PreferenceChannelWhatsApp
	case ChannelCheckout, "web", "browser":
		return store.PreferenceChannelWeb
	}
	return ""
}

// OrderRailsByPreference moves the remembered rail to the front, preserving the
// relative order of the rest. A remembered rail that is not in the list (a rail
// since retired, or one this surface does not offer) leaves the order alone
// rather than dropping a choice.
func OrderRailsByPreference(remembered string, rails []string) []string {
	if remembered == "" || len(rails) < 2 {
		return rails
	}
	out := make([]string, 0, len(rails))
	found := false
	for _, r := range rails {
		if r == remembered {
			found = true
			break
		}
	}
	if !found {
		return rails
	}
	out = append(out, remembered)
	for _, r := range rails {
		if r != remembered {
			out = append(out, r)
		}
	}
	return out
}

// SavedRailNotice is the one-line acknowledgement appended to a method prompt
// when we remember how the payer paid before.
//
// It is deliberately modest. It says what we did (put it first) and what it
// does not do (nothing is charged, nothing about the card is stored), so a
// payer is never left thinking a card is on file. Over-promising here would be
// a trust problem the moment they discovered otherwise.
func SavedRailNotice(rail string) string {
	name := RailDisplayName(rail)
	if name == "" {
		return ""
	}
	return name + " is first below — you paid that way last time. Nothing is charged until you choose."
}
