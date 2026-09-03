package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/store"
)

// handleMyLimits shows the caller's KYC tier and how much of their allowance
// remains for the current day and month. It is reachable from the main menu.
func (s *ConversationService) handleMyLimits(ctx context.Context, channel, recipient string, user store.User) error {
	profile, err := s.store.EnsureKYCProfile(ctx, user.ID)
	if err != nil {
		return err
	}
	lines := []string{fmt.Sprintf("Your Xego limits\n\nVerification tier: %s", profile.Tier)}
	lines = append(lines, s.individualLimitLines(ctx, user.ID, profile.Tier)...)
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, merchant := range merchants {
		lines = append(lines, s.merchantLimitLines(ctx, merchant.ID, merchant.Name)...)
	}
	lines = append(lines, "\nComplete more verification to raise your limits (menu → Become individual, or contact support for a merchant KYB upgrade).")
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

// handleMerchantKYBStatus shows a merchant owner their KYB tier, review state,
// payout limits, and what the next tier unlocks.
func (s *ConversationService) handleMerchantKYBStatus(ctx context.Context, channel, recipient string, user store.User) error {
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(merchants) == 0 {
		return s.sendText(ctx, channel, recipient, "KYB status is available after your merchant registration is approved.")
	}
	var lines []string
	for _, merchant := range merchants {
		lines = append(lines, s.merchantLimitLines(ctx, merchant.ID, merchant.Name)...)
	}
	lines = append(lines,
		"\nRaise your payout limits with higher KYB tiers:",
		"• B1: RC/CAC documents and directors verified (B0 → B1)",
		"• B2: settlement bank verified (B1 → B2)",
		"• B3: enhanced due diligence (B2 → B3)",
		"\nChoose 'Request KYB upgrade' from the merchant menu to submit an upgrade request for review.")
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func (s *ConversationService) individualLimitLines(ctx context.Context, userID uuid.UUID, tier string) []string {
	now := time.Now()
	limits, err := s.store.TierLimits(ctx, store.AccountIndividual, tier, kyc.DirIn)
	if err != nil {
		return []string{"Could not load your limit settings."}
	}
	usedDay, err := s.store.AllowanceUsage(ctx, store.AccountIndividual, userID, kyc.DirIn, kyc.DayWindowStart(now))
	if err != nil {
		return []string{"Could not load today's usage."}
	}
	usedMonth, err := s.store.AllowanceUsage(ctx, store.AccountIndividual, userID, kyc.DirIn, kyc.MonthWindowStart(now))
	if err != nil {
		return []string{"Could not load this month's usage."}
	}
	return []string{
		"Paying limits (money in):",
		fmt.Sprintf("• Per payment: %s", domain.FormatNGN(limits.SingleLimitKobo)),
		fmt.Sprintf("• Per day: %s", domain.FormatNGN(limits.DailyLimitKobo)),
		fmt.Sprintf("• Per month: %s", domain.FormatNGN(limits.MonthlyLimitKobo)),
		fmt.Sprintf("\nToday: %s used, %s left", domain.FormatNGN(usedDay), domain.FormatNGN(kyc.RemainingDaily(limits, usedDay))),
		fmt.Sprintf("This month: %s used, %s left", domain.FormatNGN(usedMonth), domain.FormatNGN(kyc.RemainingMonthly(limits, usedMonth))),
	}
}

// merchantLimitLines renders one merchant's KYB tier and payout ceilings.
func (s *ConversationService) merchantLimitLines(ctx context.Context, merchantID uuid.UUID, merchantName string) []string {
	profile, err := s.store.EnsureKYBProfile(ctx, merchantID)
	if err != nil {
		return []string{fmt.Sprintf("\nCould not load KYB status for %s.", merchantName)}
	}
	limits, err := s.store.TierLimits(ctx, store.AccountBusiness, profile.Tier, kyc.DirOut)
	if err != nil {
		return []string{fmt.Sprintf("\nCould not load payout limits for %s.", merchantName)}
	}
	usedMonth, err := s.store.AllowanceUsage(ctx, store.AccountBusiness, merchantID, kyc.DirOut, kyc.MonthWindowStart(time.Now()))
	if err != nil {
		return []string{fmt.Sprintf("\nCould not load monthly usage for %s.", merchantName)}
	}
	review := profile.ReviewStatus
	if review == "" {
		review = "none"
	}
	return []string{
		fmt.Sprintf("\n%s — KYB tier %s (review: %s)", merchantName, profile.Tier, review),
		"Payout limits (money out):",
		fmt.Sprintf("• Per payout: %s", domain.FormatNGN(limits.SingleLimitKobo)),
		fmt.Sprintf("• Per day: %s", domain.FormatNGN(limits.DailyLimitKobo)),
		fmt.Sprintf("• Per month: %s", domain.FormatNGN(limits.MonthlyLimitKobo)),
		fmt.Sprintf("Used this month: %s (%.1f%%)", domain.FormatNGN(usedMonth), pctOf(usedMonth, limits.MonthlyLimitKobo)),
	}
}

// createPaymentDraft wraps payments.CreateDraftForProvider so allowance
// rejections become customer-friendly copy instead of raw kobo arithmetic.
// Non-allowance errors are returned unchanged. It charges exactly the given
// amount and is used by flows with their own payment semantics (invoice
// contributions, thrift) that do not add the collection surcharge.
func (s *ConversationService) createPaymentDraft(ctx context.Context, user store.User, merchant store.Merchant, amountKobo int64, provider, channel, recipient string) (store.PaymentView, error) {
	payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, amountKobo, provider, channel, recipient)
	if err != nil {
		if msg, ok := allowanceRejectionMessage(err); ok && msg != "" {
			return store.PaymentView{}, errors.New(msg)
		}
	}
	return payment, err
}

// createCollectionPaymentDraft wraps payments.CreateCollectionDraft so a plain
// merchant collection adds the collection fee on top of the merchant's base
// amount (customer pays base+fee). Allowance rejections become
// customer-friendly copy.
func (s *ConversationService) createCollectionPaymentDraft(ctx context.Context, user store.User, merchant store.Merchant, baseKobo int64, provider, channel, recipient string) (store.PaymentView, error) {
	payment, err := s.payments.CreateCollectionDraft(ctx, user, merchant, baseKobo, provider, channel, recipient)
	if err != nil {
		if msg, ok := allowanceRejectionMessage(err); ok && msg != "" {
			return store.PaymentView{}, errors.New(msg)
		}
	}
	return payment, err
}

// friendlyAllowanceErr rewrites allowance rejections into copy a WhatsApp user
// can act on; every other error is returned unchanged.
func friendlyAllowanceErr(err error) error {
	if msg, ok := allowanceRejectionMessage(err); ok && msg != "" {
		return errors.New(msg)
	}
	return err
}

// allowanceRejectionMessage converts a kyc.LimitError into customer-facing copy
// and reports whether the error was an allowance rejection at all.
func allowanceRejectionMessage(err error) (string, bool) {
	var le *kyc.LimitError
	if !errors.As(err, &le) {
		return "", false
	}
	switch le.Limit {
	case kyc.LimitSingle:
		return fmt.Sprintf("That amount is over the single-transaction limit for your account tier (%s). Complete more verification to raise your limits; type MENU and choose *my limits* to see what you can pay.",
			domain.FormatNGN(le.Ceiling)), true
	case kyc.LimitDaily:
		return fmt.Sprintf("That would push you over today's limit for your tier. You've used %s today out of %s. Try again after midnight, or complete more verification to raise your limits.",
			domain.FormatNGN(le.Used), domain.FormatNGN(le.Ceiling)), true
	case kyc.LimitMonthly:
		return fmt.Sprintf("That would push you over this month's limit for your tier. You've used %s this month out of %s. Complete more verification to raise your limits, or wait for the new month.",
			domain.FormatNGN(le.Used), domain.FormatNGN(le.Ceiling)), true
	}
	return "", true
}

func pctOf(used, ceiling int64) float64 {
	if ceiling <= 0 {
		return 0
	}
	return float64(used) / float64(ceiling) * 100
}

// startKYBUpgradeRequest begins the self-service merchant upgrade request flow.
// It selects a merchant if the user owns multiple, then prompts the user to
// confirm the request for the next tier.
func (s *ConversationService) startKYBUpgradeRequest(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(merchants) == 0 {
		return s.sendText(ctx, channel, recipient, "You don't have an approved merchant yet. Register a business first, then come back to request an upgrade.")
	}
	if len(merchants) == 1 {
		return s.startKYBUpgradeRequestForMerchant(ctx, channel, recipient, session, merchants[0].ID, merchants[0].Name)
	}
	var lines []string
	lines = append(lines, "Which merchant do you want to request an upgrade for?")
	for i, m := range merchants {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, m.Name))
	}
	lines = append(lines, "\nReply with the number.")
	session.State = "kyb_request_select_merchant"
	ids := make([]string, len(merchants))
	for i, m := range merchants {
		ids[i] = m.ID.String()
	}
	session.Data = map[string]string{
		"merchant_ids":   strings.Join(ids, ","),
		"merchant_names": merchantNamesJoined(merchants),
	}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func merchantNamesJoined(merchants []store.Merchant) string {
	names := make([]string, len(merchants))
	for i, m := range merchants {
		names[i] = m.Name
	}
	return strings.Join(names, ",")
}

// handleKYBRequestSelectMerchant handles merchant selection when the user owns
// multiple approved merchants and wants to request a KYB upgrade.
func (s *ConversationService) handleKYBRequestSelectMerchant(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || n < 1 || n > len(merchants) {
		return s.sendText(ctx, channel, recipient, "Please reply with a number from the list.")
	}
	m := merchants[n-1]
	return s.startKYBUpgradeRequestForMerchant(ctx, channel, recipient, session, m.ID, m.Name)
}

// startKYBUpgradeRequestForMerchant shows the required evidence for the next
// tier and prompts the user to confirm the request.
func (s *ConversationService) startKYBUpgradeRequestForMerchant(ctx context.Context, channel, recipient string, session store.Session, merchantID uuid.UUID, merchantName string) error {
	profile, err := s.store.EnsureKYBProfile(ctx, merchantID)
	if err != nil {
		return err
	}
	next := store.NextBusinessTier(profile.Tier)
	if next == "" {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("%s is already at the highest business tier (B3). No upgrade is available.", merchantName))
	}
	req := kycEvidenceRequirement(next)
	lines := []string{
		fmt.Sprintf("Upgrade request: %s (%s → %s)", merchantName, profile.Tier, next),
		"",
		fmt.Sprintf("Requirement: %s", req),
		"",
		"Reply YES to submit this upgrade request, or anything else to cancel.",
	}
	session.State = "kyb_request_confirm"
	session.Data = map[string]string{
		"merchant_id":   merchantID.String(),
		"merchant_name": merchantName,
		"from_tier":     profile.Tier,
		"next_tier":     next,
	}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

// handleKYBRequestConfirm processes the user's YES/NO reply to a pending
// upgrade request and records the request if confirmed.
func (s *ConversationService) handleKYBRequestConfirm(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	merchantID, err := uuid.Parse(session.Data["merchant_id"])
	if err != nil {
		session.State, session.Data = "menu", map[string]string{}
		if err2 := s.saveSession(ctx, session); err2 != nil {
			return err2
		}
		return s.sendText(ctx, channel, recipient, "Something went wrong. Please try again from the menu.")
	}
	if !strings.EqualFold(strings.TrimSpace(input), "yes") {
		session.State, session.Data = "menu", map[string]string{}
		if err2 := s.saveSession(ctx, session); err2 != nil {
			return err2
		}
		return s.sendText(ctx, channel, recipient, "Upgrade request cancelled. You can always come back from the merchant menu.")
	}
	next := session.Data["next_tier"]
	evidenceKey := store.BusinessEvidenceFor(next)
	var evidence []string
	if evidenceKey != "" {
		evidence = []string{evidenceKey}
	}
	profile, err := s.store.RequestKYBAdvancement(ctx, merchantID, evidence, "")
	if err != nil {
		session.State, session.Data = "menu", map[string]string{}
		if err2 := s.saveSession(ctx, session); err2 != nil {
			return err2
		}
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Could not submit the upgrade request: %s", err))
	}
	merchantName := session.Data["merchant_name"]
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf(
		"Upgrade request submitted for %s (%s → %s).\n\nOur compliance team will review your request. You can check the status from the merchant menu.",
		merchantName, profile.Tier, next))
}

// kycEvidenceRequirement returns a human-readable description of what the
// next business tier requires, for use in the upgrade request prompt.
func kycEvidenceRequirement(tier string) string {
	switch tier {
	case kyc.TierB1:
		return "RC/CAC registration documents and directors verified"
	case kyc.TierB2:
		return "Settlement bank verified against the business identity"
	case kyc.TierB3:
		return "Enhanced due diligence on beneficial owners and source of funds"
	}
	return ""
}
