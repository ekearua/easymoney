package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/store"
)

// OnboardingReminderServiceOutbox is the read-side outbox seam the reminder
// guard uses, kept on the service so the guard can be faked in tests without
// reaching the real database.
type OnboardingReminderServiceOutbox interface {
	OutboxDeliveryOutcomeRows(ctx context.Context, window time.Duration) ([]string, error)
}

// stubStoreOutbox satisfies OnboardingReminderServiceOutbox for the
// hermetic delivery-guard test.
type stubStoreOutbox struct {
	outcomes []string
	err      error
}

func (s *stubStoreOutbox) OutboxDeliveryOutcomeRows(ctx context.Context, window time.Duration) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]string, len(s.outcomes))
	copy(out, s.outcomes)
	return out, nil
}

func (s *stubStoreOutbox) SetOutcomeRows(outcomes []string) {
	s.outcomes = make([]string, len(outcomes))
	copy(s.outcomes, outcomes)
}

func (s *stubStoreOutbox) SetErr(err error) {
	s.err = err
}

// pausedForDeliveryGuard is a per-sweep flag so the guard, when it fires, only
// pauses the current tick rather than becoming a permanent off switch. The guard
// is read-side: it inspects outbox outcomes, never mutates them.

// OnboardingReminderService sweeps for users whose onboarding or merchant
// registration stalled and nudges them once per stage attempt. Eligibility
// lives in two places: the data-side gates (attempt cap, gap, unanswered
// suppression) are enforced by the store sweep query; the time-of-day and
// channel rules (quiet hours, the WhatsApp 24-hour service window) live here
// because they need the clock, not the database.
//
// Delivery legality: inside the service window (last inbound < 23h ago, one
// hour of slack for outbox backlog, matching the payment-status convention in
// resultOutbox) the reminder is a plain free-form text via the message
// outbox. Outside it, WhatsApp requires an approved business-initiated
// template: with ONBOARDING_REMINDER_TEMPLATE unset the attempt is claimed
// and recorded as 'pending_template' — counted, never sent as a raw text —
// so the number's quality rating is never burned. Non-WhatsApp channels have
// no template constraint and always use plain text.
type OnboardingReminderService struct {
	store  *store.Store
	outbox OnboardingReminderServiceOutbox
	cfg    config.Config
	logger *slog.Logger
}

// NewOnboardingReminderService wires the reminder sweep.
func NewOnboardingReminderService(cfg config.Config, repository *store.Store, logger *slog.Logger) *OnboardingReminderService {
	return &OnboardingReminderService{store: repository, outbox: repository, cfg: cfg, logger: logger}
}

// reminderCopy is the stage-specific nudge. One message, one call to action,
// always ending in "reply to continue" — the reply re-opens the FSM session
// (CONVERSATION_TTL is fresh per inbound) which is the actual conversion
// mechanism.
func reminderCopy(stage, displayName string) string {
	hi := "Hi"
	if displayName != "" {
		hi = "Hi " + displayName
	}
	switch stage {
	case "name":
		return hi + "! Your Faya setup is one step in: we still need the name for your account. Reply to this message with your full name to continue."
	case "email":
		return hi + "! Your Faya setup paused at email confirmation — receipts and login codes reach you through it. Reply with your email address to continue."
	case "email_code":
		return hi + "! Your Faya email code may have expired. Reply to this message and we will send a fresh one to finish setup."
	case "details":
		return hi + "! Your Faya setup is one message away from Tier 1: reply with your date of birth, gender, and area of residence (for example: 12 March 1998, female, Yaba Lagos) to continue."
	case "confirm":
		return hi + "! Your Faya setup paused at account confirmation. Reply to this message to review and confirm the details you entered."
	case "kyb":
		return hi + "! Your Faya business registration is half-done. Have your CAC registration number, TIN, and owner details ready, then reply to this message to continue."
	default:
		return hi + "! You started setting up Faya but haven't finished. Reply to this message and we will pick up right where you left off — it takes about two minutes."
	}
}

// inQuietHours reports whether now falls in [quietStart, quietEnd) platform
// time, handling the overnight wrap (start 20, end 8 => 20:00-07:59 quiet).
func (s *OnboardingReminderService) inQuietHours(now time.Time) bool {
	qs, qe := s.cfg.OnboardingReminderQuietStart, s.cfg.OnboardingReminderQuietEnd
	if qs == qe {
		return false // zero-width window means disabled
	}
	h := now.Hour()
	if qs < qe {
		return h >= qs && h < qe
	}
	return h >= qs || h < qe
}

// RunSweep performs one reminder pass and returns how many reminders were
// claimed. Errors on individual users are logged and skipped: one bad row
// must not stall the whole sweep. Before anything else, quiet hours and the
// deliverability guard can silence the whole sweep for the current tick.
func (s *OnboardingReminderService) RunSweep(ctx context.Context) (int, error) {
	if s.inQuietHours(time.Now()) {
		return 0, nil
	}
	if pausedForDeliveryGuard, err := s.recentDeliveryFailureRatio(ctx); err != nil {
		s.logger.WarnContext(ctx, "delivery-guard ratio check failed", "error", err)
		// A read-side stat failure must not freeze onboarding nudges.
	} else if pausedForDeliveryGuard {
		s.logger.WarnContext(ctx, "delivery guard paused reminder sweep", "window", s.cfg.OnboardingReminderDeliveryGuardWindow, "ratio", s.cfg.OnboardingReminderDeliveryGuardFailureRatio)
		return 0, nil
	}
	candidates, err := s.store.OnboardingReminderCandidates(ctx,
		s.cfg.OnboardingReminderStallAfter,
		s.cfg.OnboardingReminderStartAfter,
		s.cfg.OnboardingReminderGap,
		s.cfg.OnboardingReminderMax)
	if err != nil {
		return 0, err
	}
	claimed := 0
	for _, c := range candidates {
		n, err := s.remind(ctx, c)
		if err != nil {
			s.logger.WarnContext(ctx, "onboarding reminder skipped",
				"user_id", c.UserID, "stage", c.Stage, "error", err)
			continue
		}
		claimed += n
	}
	if claimed > 0 {
		s.logger.InfoContext(ctx, "onboarding reminders claimed", "count", claimed)
	}
	return claimed, nil
}

// recentDeliveryFailureRatio reports whether the recent WhatsApp outbox
// failure/delivery ratio meets or exceeds the configured guard threshold.
// Only terminal failed or delivered rows in the guard window count, so a
// worker still retrying a message does not move the needle.
func (s *OnboardingReminderService) recentDeliveryFailureRatio(ctx context.Context) (bool, error) {
	if s.cfg.OnboardingReminderDeliveryGuardFailureRatio <= 0 || s.cfg.OnboardingReminderDeliveryGuardWindow <= 0 {
		return false, nil
	}
	var total, failed int
	rows, err := s.outbox.OutboxDeliveryOutcomeRows(ctx, s.cfg.OnboardingReminderDeliveryGuardWindow)
	// A read-side store failure must not freeze the reminder sweep.
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	for _, outcome := range rows {
		total++
		if outcome == "failed" {
			failed++
		}
	}
	if total == 0 {
		return false, nil
	}
	ratio := float64(failed) / float64(total)
	return ratio >= s.cfg.OnboardingReminderDeliveryGuardFailureRatio, nil
}

// remind claims and (maybe) enqueues one reminder for a candidate. The claim
// happens before the outbox row so a concurrent sweep cannot double-send.
func (s *OnboardingReminderService) remind(ctx context.Context, c store.OnboardingReminderCandidate) (int, error) {
	body := reminderCopy(c.Stage, c.DisplayName)
	template, locale := s.cfg.OnboardingReminderTemplate, s.cfg.OnboardingReminderTemplateLoc

	useTemplate := c.Channel == ChannelWhatsApp && time.Since(c.LastInboundAt) >= 23*time.Hour
	if useTemplate && template == "" {
		// Out of the WhatsApp service window with no approved template:
		// record the attempt as suppressed so the cap and gap still apply,
		// but send nothing.
		r := store.OnboardingReminder{
			UserID: c.UserID, Stage: c.Stage, AttemptNo: c.NextAttemptNo,
			StageState: c.StageState, Channel: c.Channel, Recipient: c.Recipient,
			Outcome: "pending_template",
		}
		if err := s.store.ClaimOnboardingReminder(ctx, &r); err != nil {
			if errors.Is(err, store.ErrReminderClaimed) {
				return 0, nil
			}
			return 0, err
		}
		if err := s.store.SetOnboardingReminderOutcome(ctx, r.ID, "pending_template"); err != nil {
			return 0, err
		}
		return 0, nil
	}

	r := store.OnboardingReminder{
		UserID: c.UserID, Stage: c.Stage, AttemptNo: c.NextAttemptNo,
		StageState: c.StageState, Channel: c.Channel, Recipient: c.Recipient,
		Body: body,
	}
	if useTemplate {
		r.Template, r.Locale = template, locale
	}
	if err := s.store.ClaimOnboardingReminder(ctx, &r); err != nil {
		if errors.Is(err, store.ErrReminderClaimed) {
			return 0, nil
		}
		return 0, err
	}

	if useTemplate {
		// The approved template carries the nudge as its parameter.
		if err := s.store.EnqueueTemplate(ctx, c.UserID, c.Recipient, template, locale,
			[]string{body}); err != nil {
			_ = s.store.SetOnboardingReminderOutcome(ctx, r.ID, "failed")
			return 0, fmt.Errorf("enqueue reminder template: %w", err)
		}
	} else if err := s.store.EnqueueTextForChannel(ctx, c.UserID, c.Channel, c.Recipient, body); err != nil {
		_ = s.store.SetOnboardingReminderOutcome(ctx, r.ID, "failed")
		return 0, fmt.Errorf("enqueue reminder text: %w", err)
	}
	if err := s.store.SetOnboardingReminderOutcome(ctx, r.ID, "sent"); err != nil {
		return 0, err
	}
	return 1, nil
}
