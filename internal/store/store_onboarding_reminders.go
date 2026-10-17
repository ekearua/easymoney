package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Onboarding reminders: the chat-side nudge for users whose onboarding or
// merchant registration stalled. The store owns the candidate sweep (one
// statement covering the detection buckets plus the data-side gates), the
// idempotent claim, and outcome updates. The service owns the remaining
// eligibility rules (quiet hours, the WhatsApp service-window lane) and the
// stage copy.
//
// Detection buckets:
//
//   - stalled: a live FSM session untouched for longer than the stall window.
//     Narrow under the default 30-minute CONVERSATION_TTL, but correct for
//     any TTL an operator configures.
//   - abandoned: the most recent session has expired and was last touched
//     before the stall window. The stage classifier reads that session's
//     final state, so a user who quit mid Tier-1 form still gets the
//     details-stage copy, not the generic one.
//   - never started: first contact older than the start window, no session
//     row at all.
//
// Data-side gates (in the same statement): fewer counted attempts for the
// stage than the cap (failed sends don't count), no unanswered reminder still
// open (sent after the user's last inbound), and no reminder for the stage
// within the minimum gap. Completed users and users who own a merchant
// (merchant_owners) are never candidates; a merchant registration in flight
// is fair game because that chain stalls before any merchant row exists.

// OnboardingReminderCandidate is one user the sweep reports as stalled, with
// the stage classification and the delivery facts the service needs (channel
// routing and the 24-hour service-window lane).
type OnboardingReminderCandidate struct {
	UserID        uuid.UUID
	Stage         string
	StageState    string
	Channel       string
	Recipient     string
	DisplayName   string
	LastInboundAt time.Time
	NextAttemptNo int
}

// OnboardingReminder is one recorded reminder attempt, written before the
// message_outbox row so the (user_id, stage, attempt_no) unique index
// arbitrates concurrent sweeps. The recipient is sealed through the house
// envelope helper so the row is auditable without a live contact in the clear.
type OnboardingReminder struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Stage      string
	AttemptNo  int
	StageState string
	Channel    string
	Recipient  string
	Template   string
	Locale     string
	Body       string
	Outcome    string
	SentAt     time.Time
}

// ErrReminderClaimed is returned by ClaimOnboardingReminder when another
// sweep already recorded this (user, stage, attempt) — the caller skips.
var ErrReminderClaimed = errors.New("onboarding reminder already claimed")

// stageCaseSQL classifies a conversation state into a reminder stage. Kept as
// one shared fragment so the projection and the gate subqueries cannot drift.
// States that no longer exist (the FSM moved on) classify as 'start', which
// still reminds with generic copy instead of silently skipping.
const stageCaseSQL = `(CASE
	WHEN p.stage_state LIKE 'merchant_register%' THEN 'kyb'
	WHEN p.stage_state = 'onboard_name' THEN 'name'
	WHEN p.stage_state IN ('onboard_email','onboard_email_code') THEN 'email'
	WHEN p.stage_state IN ('onboard_details','onboard_dob','onboard_gender','onboard_address') THEN 'details'
	WHEN p.stage_state = 'onboard_confirm_account' THEN 'confirm'
	ELSE 'start'
END)`

// OnboardingReminderCandidates runs the detection sweep. stallAfter gates the
// two session buckets, startAfter the never-started bucket, maxAttempts the
// per-stage attempt cap, minGap the spacing between reminders for a stage.
func (s *Store) OnboardingReminderCandidates(ctx context.Context, stallAfter, startAfter, minGap time.Duration, maxAttempts int) ([]OnboardingReminderCandidate, error) {
	return s.onboardingReminderCandidates(ctx, stallAfter, startAfter, minGap, maxAttempts)
}

// OnboardingReminderStats is the admin-facing summary behind the reminder
// conversion panel: per-stage counts of stalled users, reminders sent/pending/
// failed in the last day, and the overall completed-after-reminder vs still-
// stalled split. Stage here is the recallable stage label, not the raw FSM state.
type OnboardingReminderStats struct {
	// Stalled counts per stage for users who are still incomplete and appear
	// in the current sweep (live session, expired session, or never started).
	StalledByStage map[string]int
	// Sent, pending, and failed reminder attempts in the last day.
	Sent24h    int
	Pending24h int
	Failed24h  int
	// Conversion proxy: incomplete users whose newest reminder outcome is one of
	// the terminal success markers (sent/delivered) and who now have a completed
	// KYC or a merchant registration. The sweep never records that completion —
	// that is a read-side metric only — but this is the number that decides
	// whether the feature earns its message budget.
	CompletedAfterReminder int
}

// CountOnboardingReminderOutcomeStats is the read-side companion to the
// reminder sweep. It returns the three terminal-outcome buckets for reminder
// attempts in the last 24 hours (sent/delivered, pending_template, failed) so
// the admin panel can show both volume and deliverability.
func (s *Store) CountOnboardingReminderOutcomeStats(ctx context.Context) (sent, pending, failed int, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT outcome, COUNT(*)
		FROM onboarding_reminders
		WHERE sent_at > now() - interval '1 day'
		GROUP BY outcome`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var outcome string
		var n int
		if err := rows.Scan(&outcome, &n); err != nil {
			return 0, 0, 0, err
		}
		switch outcome {
		case "queued", "sent", "delivered":
			sent += n
		case "pending_template":
			pending += n
		case "failed":
			failed += n
		}
	}
	return sent, pending, failed, rows.Err()
}

// StalledOnboardingUsersByStage returns how many incomplete users currently
// fall into each reminder stage bucket (the same classification as the sweep,
// but counted rather than enumerated). Used by the admin panel so a reviewer
// can see where onboarding friction actually sits.
func (s *Store) StalledOnboardingUsersByStage(ctx context.Context) (map[string]int, error) {
	const query = `
WITH stalled AS (
    SELECT u.id, cs.state AS stage_state
    FROM conversation_sessions cs
    JOIN users u ON u.id = cs.user_id
    WHERE u.onboarding_complete = false
      AND NOT EXISTS (SELECT 1 FROM merchant_owners mo WHERE mo.user_id = u.id)
      AND cs.expires_at > now()
      AND cs.updated_at < now() - make_interval(secs => $1::int)
), abandoned AS (
    SELECT DISTINCT ON (cs.user_id) cs.user_id AS id, cs.state AS stage_state
    FROM conversation_sessions cs
    JOIN users u ON u.id = cs.user_id
    WHERE u.onboarding_complete = false
      AND NOT EXISTS (SELECT 1 FROM merchant_owners mo WHERE mo.user_id = u.id)
      AND cs.expires_at <= now()
      AND cs.updated_at < now() - make_interval(secs => $1::int)
      AND NOT EXISTS (
          SELECT 1 FROM conversation_sessions x
          WHERE x.user_id = cs.user_id AND x.expires_at > now())
    ORDER BY cs.user_id, cs.updated_at DESC
), never AS (
    SELECT u.id, ''::text AS stage_state
    FROM users u
    WHERE u.onboarding_complete = false
      AND NOT EXISTS (SELECT 1 FROM merchant_owners mo WHERE mo.user_id = u.id)
      AND u.created_at < now() - make_interval(secs => $2::int)
      AND u.last_inbound_at < now() - make_interval(secs => $2::int)
      AND NOT EXISTS (SELECT 1 FROM conversation_sessions cs WHERE cs.user_id = u.id)
)
SELECT ` + stageCaseSQL + ` AS stage, COUNT(*)
FROM (
    SELECT id, stage_state, 'live' AS src FROM stalled
    UNION ALL
    SELECT id, stage_state, 'expired' AS src FROM abandoned
    UNION ALL
    SELECT id, stage_state, 'never' AS src FROM never
) p
JOIN users u ON u.id = p.id
GROUP BY stage`
	rows, err := s.pool.Query(ctx, query,
		int64(12*time.Hour/time.Second), int64(24*time.Hour/time.Second))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{
		"name": 0, "email": 0, "details": 0, "confirm": 0, "kyb": 0, "start": 0,
	}
	for rows.Next() {
		var stage string
		var n int
		if err := rows.Scan(&stage, &n); err != nil {
			return nil, err
		}
		if _, ok := out[stage]; ok {
			out[stage] = n
		}
	}
	return out, rows.Err()
}

// CompletedAfterReminder is the conversion proxy behind the admin panel: how
// many incomplete users we have nudged (a sent or delivered reminder in the
// last 7 days) and who now present as on board. The sweep never writes this —
// it is a read-side metric only — so a bug in the reminder path can never
// corrupt onboarding state.
func (s *Store) CompletedAfterReminder(ctx context.Context) (int, error) {
	const query = `
SELECT COUNT(DISTINCT r.user_id)
FROM onboarding_reminders r
WHERE r.outcome IN ('sent','delivered')
  AND r.sent_at > now() - interval '7 days'
  AND EXISTS (
      SELECT 1 FROM users u
      WHERE u.id = r.user_id
        AND u.onboarding_complete = true)`
	var n int
	if err := s.pool.QueryRow(ctx, query).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// OnboardingReminderStats returns the admin-facing summary behind the reminder
// conversion panel. It is deliberately small and read-only: per-stage stalled
// counts, the last-day send outcome buckets, and the completed-after-reminder
// proxy.
func (s *Store) OnboardingReminderStats(ctx context.Context) (OnboardingReminderStats, error) {
	stalled, err := s.StalledOnboardingUsersByStage(ctx)
	if err != nil {
		return OnboardingReminderStats{}, err
	}
	sent, pending, failed, err := s.CountOnboardingReminderOutcomeStats(ctx)
	if err != nil {
		return OnboardingReminderStats{}, err
	}
	completed, err := s.CompletedAfterReminder(ctx)
	if err != nil {
		return OnboardingReminderStats{}, err
	}
	return OnboardingReminderStats{
		StalledByStage:         stalled,
		Sent24h:                sent,
		Pending24h:             pending,
		Failed24h:              failed,
		CompletedAfterReminder: completed,
	}, nil
}

// onboardingReminderCandidates is the internal name so the exported helper can
// live above the query without renaming the package-visible sweep.
func (s *Store) onboardingReminderCandidates(ctx context.Context, stallAfter, startAfter, minGap time.Duration, maxAttempts int) ([]OnboardingReminderCandidate, error) {
	const query = `
WITH stalled AS (
    SELECT u.id AS user_id, cs.state AS stage_state, u.last_inbound_at,
           COALESCE(u.whatsapp_number,'') AS wa_number, u.display_name
    FROM conversation_sessions cs
    JOIN users u ON u.id = cs.user_id
    WHERE u.onboarding_complete = false
      AND NOT EXISTS (SELECT 1 FROM merchant_owners mo WHERE mo.user_id = u.id)
      AND cs.expires_at > now()
      AND cs.updated_at < now() - make_interval(secs => $1::int)
), abandoned AS (
    SELECT DISTINCT ON (cs.user_id)
           cs.user_id, cs.state AS stage_state, u.last_inbound_at,
           COALESCE(u.whatsapp_number,'') AS wa_number, u.display_name
    FROM conversation_sessions cs
    JOIN users u ON u.id = cs.user_id
    WHERE u.onboarding_complete = false
      AND NOT EXISTS (SELECT 1 FROM merchant_owners mo WHERE mo.user_id = u.id)
      AND cs.expires_at <= now()
      AND cs.updated_at < now() - make_interval(secs => $1::int)
      AND NOT EXISTS (
          SELECT 1 FROM conversation_sessions x
          WHERE x.user_id = cs.user_id AND x.expires_at > now())
    ORDER BY cs.user_id, cs.updated_at DESC
), never AS (
    SELECT u.id AS user_id, ''::text AS stage_state, u.last_inbound_at,
           COALESCE(u.whatsapp_number,'') AS wa_number, u.display_name
    FROM users u
    WHERE u.onboarding_complete = false
      AND NOT EXISTS (SELECT 1 FROM merchant_owners mo WHERE mo.user_id = u.id)
      AND u.created_at < now() - make_interval(secs => $2::int)
      AND u.last_inbound_at < now() - make_interval(secs => $2::int)
      AND NOT EXISTS (SELECT 1 FROM conversation_sessions cs WHERE cs.user_id = u.id)
), pool AS (
    SELECT * FROM stalled
    UNION ALL
    SELECT * FROM abandoned
    UNION ALL
    SELECT * FROM never
)
SELECT s.user_id, s.stage, s.stage_state, s.channel, s.recipient,
       s.display_name, s.last_inbound_at, s.next_attempt_no
FROM (
    SELECT p.user_id, p.stage_state, p.wa_number, p.display_name, p.last_inbound_at,
` + stageCaseSQL + ` AS stage,
        CASE
            WHEN COALESCE(u.telegram_chat_id,'') <> '' THEN 'telegram'
            WHEN COALESCE(u.instagram_igsid,'') <> '' THEN 'instagram'
            WHEN COALESCE(u.facebook_psid,'') <> '' THEN 'facebookmessenger'
            WHEN COALESCE(u.tiktok_open_id,'') <> '' THEN 'tiktok'
            ELSE 'whatsapp'
        END AS channel,
        CASE
            WHEN COALESCE(u.telegram_chat_id,'') <> '' THEN u.telegram_chat_id
            WHEN COALESCE(u.instagram_igsid,'') <> '' THEN u.instagram_igsid
            WHEN COALESCE(u.facebook_psid,'') <> '' THEN u.facebook_psid
            WHEN COALESCE(u.tiktok_open_id,'') <> '' THEN u.tiktok_open_id
            ELSE p.wa_number
        END AS recipient,
        COALESCE((SELECT COUNT(*) FROM onboarding_reminders r
                  WHERE r.user_id = p.user_id AND r.stage = ` + stageCaseSQL + `), 0) + 1 AS next_attempt_no,
        COALESCE((SELECT COUNT(*) FROM onboarding_reminders r
                  WHERE r.user_id = p.user_id AND r.stage = ` + stageCaseSQL + `
                    AND r.outcome <> 'failed'), 0) AS used_attempts
    FROM pool p
    JOIN users u ON u.id = p.user_id
) s
WHERE s.used_attempts < $3
  AND NOT EXISTS (
      SELECT 1 FROM onboarding_reminders r
      WHERE r.user_id = s.user_id AND r.stage = s.stage
        AND r.outcome IN ('queued','sent')
        AND r.sent_at > s.last_inbound_at)
  AND NOT EXISTS (
      SELECT 1 FROM onboarding_reminders r
      WHERE r.user_id = s.user_id AND r.stage = s.stage
        AND r.outcome <> 'failed'
        AND r.sent_at > now() - make_interval(secs => $4::int))
ORDER BY s.user_id`

	rows, err := s.pool.Query(ctx, query,
		int64(stallAfter/time.Second), int64(startAfter/time.Second),
		maxAttempts, int64(minGap/time.Second))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OnboardingReminderCandidate
	for rows.Next() {
		var c OnboardingReminderCandidate
		if err := rows.Scan(&c.UserID, &c.Stage, &c.StageState, &c.Channel,
			&c.Recipient, &c.DisplayName, &c.LastInboundAt, &c.NextAttemptNo); err != nil {
			return nil, err
		}
		if c.Recipient == "" {
			continue // no reachable contact on any channel
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ClaimOnboardingReminder records a reminder attempt before the outbox row is
// written. The (user_id, stage, attempt_no) unique index makes the claim
// idempotent: two sweeps racing on the same user produce exactly one winner,
// and the loser gets ErrReminderClaimed. On success the row's ID and sent_at
// are filled in and outcome starts at 'queued'.
func (s *Store) ClaimOnboardingReminder(ctx context.Context, r *OnboardingReminder) error {
	sealed, err := s.sealValue([]byte(r.Recipient))
	if err != nil {
		return err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO onboarding_reminders
			(user_id, stage, attempt_no, stage_state, channel, recipient_sealed, template, locale, body, outcome)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'queued')
		RETURNING id, sent_at`,
		r.UserID, r.Stage, r.AttemptNo, r.StageState, r.Channel,
		sealed, r.Template, r.Locale, r.Body)
	if err := row.Scan(&r.ID, &r.SentAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrReminderClaimed
		}
		return err
	}
	return nil
}

// SetOnboardingReminderOutcome stamps the delivery result after the outbox
// enqueue (or the template-suppression decision) resolves.
func (s *Store) SetOnboardingReminderOutcome(ctx context.Context, id uuid.UUID, outcome string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE onboarding_reminders SET outcome = $2 WHERE id = $1`, id, outcome)
	return err
}
