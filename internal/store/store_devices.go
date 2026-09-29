package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/kyc"
)

// TrustedDevice is one browser a subject has used on a portal surface,
// keyed by a coarse fingerprint (user-agent class + accept-language class),
// not a raw identifier: the table exists to correlate sign-ins and payments
// per device, not to track users across sites. Migration 026 created the
// table; this is the code path that finally fills it.
type TrustedDevice struct {
	ID                int64
	UserID            uuid.UUID
	DeviceFingerprint string
	Label             string
	FirstSeenAt       time.Time
	LastSeenAt        time.Time
}

// TouchTrustedDevice upserts one device observation for a user: the first
// sighting inserts, later sightings only advance last_seen_at, so the row
// stays stable across sessions (UNIQUE (user_id, device_fingerprint)).
//
// A sighting is also the moment a browser turns out to be shared, so it feeds
// the network risk signal for every identity on that fingerprint.
func (s *Store) TouchTrustedDevice(ctx context.Context, userID uuid.UUID, fingerprint, label string) error {
	if userID == uuid.Nil || fingerprint == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO trusted_devices(user_id, device_fingerprint, label, first_seen_at, last_seen_at)
		VALUES($1, $2, $3, now(), now())
		ON CONFLICT (user_id, device_fingerprint)
		DO UPDATE SET last_seen_at = now()`, userID, fingerprint, label)
	if err != nil {
		return err
	}
	return s.recordSharedDeviceRisk(ctx, fingerprint, label)
}

// recordSharedDeviceRisk records one network_shared_device observation for
// every identity on a fingerprint used by more than one, once each. It is
// symmetric on purpose: the identity that happened to sign in second must not
// be the only one carrying the link, and the one that was already there gets
// its observation backfilled the moment the share becomes knowable.
//
// Sharing a browser is a fact, not a suspicion, so nothing cancels it — a
// family laptop is still a family laptop after a reviewer clears the case. It
// ages on the risk-decay clock like every other observation, and the details
// carry no other identity's identifiers: a customer's own risk record must not
// name the strangers they share a machine with.
func (s *Store) recordSharedDeviceRisk(ctx context.Context, fingerprint, label string) error {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id FROM trusted_devices
		WHERE device_fingerprint=$1
		ORDER BY first_seen_at ASC`, fingerprint)
	if err != nil {
		return err
	}
	var users []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		users = append(users, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(users) < 2 {
		return nil
	}
	coUsers := len(users) - 1
	for _, id := range users {
		var exists bool
		err := s.pool.QueryRow(ctx, `
			SELECT true FROM risk_events
			WHERE user_id=$1 AND event_type=$2 AND details->>'fingerprint'=$3
			LIMIT 1`, id, kyc.RiskEventSharedDevice, fingerprint).Scan(&exists)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check shared device risk event: %w", err)
		}
		if exists {
			continue
		}
		if _, err := s.RecordRiskEvent(ctx, RiskEvent{
			UserID:    id,
			EventType: kyc.RiskEventSharedDevice,
			Score:     kyc.SharedDeviceRisk(coUsers),
			Details: map[string]any{
				"fingerprint":   fingerprint,
				"device_label":  label,
				"co_user_count": coUsers,
				"reason":        "browser fingerprint shared with another identity",
			},
		}); err != nil {
			return fmt.Errorf("record shared device risk: %w", err)
		}
	}
	return nil
}

// TrustedDevicesForUser lists a user's known devices, most recently seen
// first, for the admin KYC profile surface.
func (s *Store) TrustedDevicesForUser(ctx context.Context, userID uuid.UUID) ([]TrustedDevice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, device_fingerprint, label, first_seen_at, last_seen_at
		FROM trusted_devices WHERE user_id=$1 ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrustedDevice
	for rows.Next() {
		var d TrustedDevice
		if err := rows.Scan(&d.ID, &d.UserID, &d.DeviceFingerprint, &d.Label, &d.FirstSeenAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeviceSummaryForUsers returns one summary per user id: the device count and
// the most recent device label, for the KYC profiles table.
type DeviceSummary struct {
	DeviceCount int
	LastLabel   string
	LastSeenAt  time.Time
}

func (s *Store) DeviceSummaryForUsers(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]DeviceSummary, error) {
	out := make(map[uuid.UUID]DeviceSummary, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (user_id) user_id, label, last_seen_at,
		       count(*) OVER (PARTITION BY user_id) AS device_count
		FROM trusted_devices
		WHERE user_id = ANY($1)
		ORDER BY user_id, last_seen_at DESC`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var sum DeviceSummary
		if err := rows.Scan(&id, &sum.LastLabel, &sum.LastSeenAt, &sum.DeviceCount); err != nil {
			return nil, err
		}
		out[id] = sum
	}
	return out, rows.Err()
}

// DeviceFingerprint derives the coarse browser fingerprint: the first 160
// bits of SHA-256 over the user agent and the accept-language header's
// primary tag. It is stable for the same browser+language, opaque, and
// deliberately not a hardware identifier.
func DeviceFingerprint(userAgent, acceptLanguage string) string {
	if userAgent == "" {
		return ""
	}
	lang := acceptLanguage
	if i := strings.IndexByte(lang, ','); i >= 0 {
		lang = lang[:i]
	}
	if i := strings.IndexByte(lang, ';'); i >= 0 {
		lang = lang[:i]
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(userAgent) + "|" + strings.TrimSpace(lang)))
	return hex.EncodeToString(sum[:16])
}

// DeviceLabel renders a short human-readable label from the raw user agent:
// the platform token plus the first browser-like token, so the admin sees
// "Windows Chrome" rather than a hash.
func DeviceLabel(userAgent string) string {
	ua := strings.ToLower(userAgent)
	platform := "unknown"
	switch {
	case strings.Contains(ua, "windows"):
		platform = "Windows"
	case strings.Contains(ua, "android"):
		platform = "Android"
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"):
		platform = "iOS"
	case strings.Contains(ua, "mac os"), strings.Contains(ua, "macintosh"):
		platform = "macOS"
	case strings.Contains(ua, "linux"):
		platform = "Linux"
	}
	browser := ""
	switch {
	case strings.Contains(ua, "edg/"), strings.Contains(ua, "edge"):
		browser = "Edge"
	case strings.Contains(ua, "chrome"), strings.Contains(ua, "crios"):
		browser = "Chrome"
	case strings.Contains(ua, "firefox"), strings.Contains(ua, "fxios"):
		browser = "Firefox"
	case strings.Contains(ua, "safari"):
		browser = "Safari"
	}
	if browser == "" {
		return platform
	}
	return platform + " " + browser
}
