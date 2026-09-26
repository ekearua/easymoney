package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
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
func (s *Store) TouchTrustedDevice(ctx context.Context, userID uuid.UUID, fingerprint, label string) error {
	if userID == uuid.Nil || fingerprint == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO trusted_devices(user_id, device_fingerprint, label, first_seen_at, last_seen_at)
		VALUES($1, $2, $3, now(), now())
		ON CONFLICT (user_id, device_fingerprint)
		DO UPDATE SET last_seen_at = now()`, userID, fingerprint, label)
	return err
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
