package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Payment preferences (SAVED_CARD_METHODS_PLAN.md Phase 1): the rail a payer
// picked last time, remembered so the method picker can offer it first.
//
// The table holds no card data — posture A is "remember the choice, not the
// instrument" — and a saved card is a separate, gated decision. This file owns
// the write path (what a successful payment remembers), the read path (what the
// pickers offer) and the Phase 0.5 measurement that decides whether any of it
// earns its keep.
const (
	// PreferenceScopeDefault is the payer's standing choice on a surface.
	PreferenceScopeDefault = "default"
	// PreferenceScopeMerchant narrows a choice to one payee.
	PreferenceScopeMerchant = "merchant"

	// PreferenceChannelWhatsApp is the in-chat surface.
	PreferenceChannelWhatsApp = "whatsapp"
	// PreferenceChannelWeb is the browser surface.
	PreferenceChannelWeb = "web"
)

// PaymentPreference is one remembered rail choice.
type PaymentPreference struct {
	UserID    uuid.UUID
	Channel   string
	Scope     string
	ScopeRef  string
	Rail      string
	UpdatedAt time.Time
}

// ValidPreferenceChannel reports whether a channel names a real surface. It
// exists because the channel value is part of the uniqueness key: a typo would
// silently create a second, never-read preference row rather than failing.
func ValidPreferenceChannel(channel string) bool {
	switch channel {
	case PreferenceChannelWhatsApp, PreferenceChannelWeb:
		return true
	}
	return false
}

// RememberPaymentRail records the rail a payer just used successfully. It is the
// whole write path, and it is deliberately idempotent: a repeated call (a hook
// retry, a duplicate webhook) updates the same two rows.
//
// Two rows are written on purpose. The merchant-scoped row captures the habit at
// this payee, so a payer who always pays one merchant by bank transfer keeps
// that even after paying someone else by card. The default row captures the most
// recent choice overall, which is what a first-time payee to that merchant
// falls back to. Resolution prefers the merchant row.
//
// A merchantID of uuid.Nil (a checkout link resolved before a payee, an
// individual payout) writes only the default row.
func (s *Store) RememberPaymentRail(ctx context.Context, userID uuid.UUID, channel string, merchantID uuid.UUID, rail string) error {
	rail = strings.TrimSpace(rail)
	if userID == uuid.Nil || rail == "" || !ValidPreferenceChannel(channel) {
		return nil
	}
	if err := s.UpsertPaymentPreference(ctx, userID, channel, PreferenceScopeDefault, "", rail); err != nil {
		return err
	}
	if merchantID == uuid.Nil {
		return nil
	}
	return s.UpsertPaymentPreference(ctx, userID, channel, PreferenceScopeMerchant, merchantID.String(), rail)
}

// UpsertPaymentPreference stores one rail choice for a payer, surface and scope.
// The unique key makes this an update on repeat rather than an append.
func (s *Store) UpsertPaymentPreference(ctx context.Context, userID uuid.UUID, channel, scope, scopeRef, rail string) error {
	if userID == uuid.Nil || !ValidPreferenceChannel(channel) {
		return nil
	}
	rail = strings.TrimSpace(rail)
	if rail == "" {
		return nil
	}
	scopeRef = strings.TrimSpace(scopeRef)
	if scope != PreferenceScopeMerchant {
		scope = PreferenceScopeDefault
		scopeRef = ""
	}
	if scopeRef == "" {
		scope = PreferenceScopeDefault
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO payment_preferences(user_id, channel, scope, scope_ref, rail)
		VALUES($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, channel, scope, scope_ref)
		DO UPDATE SET rail = EXCLUDED.rail, updated_at = now()`,
		userID, channel, scope, scopeRef, rail)
	return err
}

// PaymentPreferenceFor resolves the rail to offer a payer on a surface: their
// habit at this merchant if we know it, otherwise their standing choice, else
// "" (meaning "ask normally, presume nothing").
func (s *Store) PaymentPreferenceFor(ctx context.Context, userID uuid.UUID, channel string, merchantID uuid.UUID) (string, error) {
	if userID == uuid.Nil || !ValidPreferenceChannel(channel) {
		return "", nil
	}
	merchantRef := ""
	if merchantID != uuid.Nil {
		merchantRef = merchantID.String()
	}
	var rail string
	err := s.pool.QueryRow(ctx, `
		SELECT rail FROM payment_preferences
		WHERE user_id = $1 AND channel = $2
		  AND ((scope = 'merchant' AND scope_ref = $3) OR scope = 'default')
		ORDER BY (scope = 'merchant') DESC
		LIMIT 1`, userID, channel, merchantRef).Scan(&rail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return rail, nil
}

// ClearPaymentPreference forgets one remembered rail choice. A payer asking to
// stop being remembered should not have to close their account to do it.
func (s *Store) ClearPaymentPreference(ctx context.Context, userID uuid.UUID, channel, scope, scopeRef string) error {
	if userID == uuid.Nil || !ValidPreferenceChannel(channel) {
		return nil
	}
	scopeRef = strings.TrimSpace(scopeRef)
	if scope != PreferenceScopeMerchant {
		scope = PreferenceScopeDefault
		scopeRef = ""
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM payment_preferences
		WHERE user_id = $1 AND channel = $2 AND scope = $3 AND scope_ref = $4`,
		userID, channel, scope, scopeRef)
	return err
}

// PaymentPreferencesForUser lists every remembered choice for a payer, for the
// data-subject export and the admin profile surface.
func (s *Store) PaymentPreferencesForUser(ctx context.Context, userID uuid.UUID) ([]PaymentPreference, error) {
	if userID == uuid.Nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, channel, scope, scope_ref, rail, updated_at
		FROM payment_preferences WHERE user_id = $1
		ORDER BY channel, scope, scope_ref`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentPreference
	for rows.Next() {
		var p PaymentPreference
		if err := rows.Scan(&p.UserID, &p.Channel, &p.Scope, &p.ScopeRef, &p.Rail, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// =========================================================================
// Phase 0.5 — the measurement that decides whether Phase 1 was worth building
// =========================================================================

// CardReentryMetrics is the evidence behind the saved-card decision
// (SAVED_CARD_METHODS_PLAN.md Phase 0.5). It is read-only and derived from
// payments the platform already stores; it collects no new data.
//
// The question it answers: how much does re-entering card details actually
// cost? Posture B carries a PCI DSS scope change, and posture A is cheap but
// delivers little, so neither should be built on the assumption that re-entry
// is painful. If abandonment on the card rail is immaterial, the honest answer
// is Option 0 — build neither, and speed up the hosted entry instead.
type CardReentryMetrics struct {
	// Since is the inclusive start of the window the figures cover.
	Since time.Time
	// CardAttempts is every card (Interswitch) attempt in the window.
	CardAttempts int64
	// CardAbandoned is the card attempts that ended failed, abandoned or
	// expired — the payer reaching the card page and not completing. A
	// declined card is included: from the payer's seat they either finished
	// or they did not.
	CardAbandoned int64
	// RepeatPayments is payments from a payer who had already paid someone
	// before the window opened, i.e. the population a remembered rail could
	// spare an extra step.
	RepeatPayments int64
	// RepeatPaymentPayerCount is how many distinct payers those payments
	// came from.
	RepeatPaymentPayerCount int64
	// Payments is every payment in the window, so the repeat share has a
	// denominator the caller does not have to guess.
	Payments int64
}

// CardAbandonmentRate returns the share of card attempts that did not complete,
// or 0 when there were no attempts.
func (m CardReentryMetrics) CardAbandonmentRate() float64 {
	if m.CardAttempts == 0 {
		return 0
	}
	return float64(m.CardAbandoned) / float64(m.CardAttempts)
}

// RepeatPaymentShare returns the share of payments made by returning payers,
// or 0 when there were no payments.
func (m CardReentryMetrics) RepeatPaymentShare() float64 {
	if m.Payments == 0 {
		return 0
	}
	return float64(m.RepeatPayments) / float64(m.Payments)
}

// CardReentryMetricsSince measures card-rail re-entry friction over the window
// starting at since. Read-only: one aggregate pass over payments, no new tables
// and nothing written.
func (s *Store) CardReentryMetricsSince(ctx context.Context, since time.Time) (CardReentryMetrics, error) {
	out := CardReentryMetrics{Since: since}
	err := s.pool.QueryRow(ctx, `
		WITH window_payments AS (
		    SELECT p.id, p.user_id, p.provider, p.status
		    FROM payments p
		    WHERE p.created_at >= $1
		),
		prior_payers AS (
		    SELECT DISTINCT p.user_id
		    FROM payments p
		    WHERE p.created_at < $1
		)
		SELECT
		    count(*) FILTER (WHERE provider = 'interswitch'),
		    count(*) FILTER (WHERE provider = 'interswitch'
		                     AND status IN ('failed', 'abandoned', 'expired')),
		    count(*) FILTER (WHERE user_id IN (SELECT user_id FROM prior_payers)),
		    count(DISTINCT user_id) FILTER (WHERE user_id IN (SELECT user_id FROM prior_payers)),
		    count(*)
		FROM window_payments`, since,
	).Scan(&out.CardAttempts, &out.CardAbandoned, &out.RepeatPayments, &out.RepeatPaymentPayerCount, &out.Payments)
	if err != nil {
		return CardReentryMetrics{}, err
	}
	return out, nil
}
