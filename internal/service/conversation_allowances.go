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

// createPurseFundedCollectionDraft drafts a merchant collection funded from a
// restricted programme purse. The purse pays the merchant's amount exactly:
// programme money buys the goods the programme intended, so no Faya collection
// fee is added on top of it.
//
// The errPurseNotAvailable sentinel is returned when no purse can fund the
// charge, so a caller that reached here on the strength of an eligibility check
// can fall back to the ordinary rail rather than failing the payment.
func (s *ConversationService) createPurseFundedCollectionDraft(ctx context.Context, user store.User, merchant store.Merchant, baseKobo int64, channel, recipient string) (store.PaymentView, error) {
	payment, ok, err := s.payments.CreatePurseFundedDraft(ctx, user, merchant, baseKobo, channel, recipient)
	if err != nil {
		return store.PaymentView{}, friendlyAllowanceErr(err)
	}
	if !ok {
		return store.PaymentView{}, errPurseNotAvailable
	}
	return payment, nil
}

// errPurseNotAvailable means no restricted purse covers this merchant and
// amount, which is an ordinary outcome (the customer simply pays another way),
// not an error to show anyone.
var errPurseNotAvailable = errors.New("no programme purse funds this purchase")

// walletOrPurseDraft funds a merchant collection from the payer's restricted
// programme purse when one covers this merchant, and from the ordinary wallet
// otherwise. Both are instant in-platform transfers, so the customer sees no
// difference in the rail - only in which balance is drawn down, which the
// review message states before anything is confirmed.
func (s *ConversationService) walletOrPurseDraft(ctx context.Context, user store.User, merchant store.Merchant, baseKobo int64, channel, recipient string) (store.PaymentView, error) {
	if _, ok := s.payments.EligiblePurse(ctx, user.ID, merchant.ID, baseKobo); !ok {
		return s.createCollectionPaymentDraft(ctx, user, merchant, baseKobo, ProviderWallet, channel, recipient)
	}
	payment, err := s.createPurseFundedCollectionDraft(ctx, user, merchant, baseKobo, channel, recipient)
	if errors.Is(err, errPurseNotAvailable) {
		// The purse was spent or closed between the check and the draft.
		return s.createCollectionPaymentDraft(ctx, user, merchant, baseKobo, ProviderWallet, channel, recipient)
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

// AllowanceMessage converts a kyc.LimitError into customer-facing copy and
// reports whether the error was an allowance rejection at all. Exported for
// web flow handlers so browser forms show the same copy as chat.
func AllowanceMessage(err error) (string, bool) {
	return allowanceRejectionMessage(err)
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
	session.Data = map[string]string{
		"merchant_id":   merchantID.String(),
		"merchant_name": merchantName,
		"from_tier":     profile.Tier,
		"next_tier":     next,
	}
	// B0 -> B1 is the corporate hard gate, so it needs the documents rather
	// than a bare confirmation: collect them in one message. The higher rungs
	// are a settlement-bank and an enhanced-due-diligence attestation, which
	// the compliance team records itself, so they stay a one-word confirm.
	if next == kyc.TierB1 {
		session.State = "kyb_request_details"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, kybDetailsPrompt(merchantName, profile.Tier, next))
	}
	session.State = "kyb_request_confirm"
	lines := []string{
		fmt.Sprintf("Upgrade request: %s (%s → %s)", merchantName, profile.Tier, next),
		"",
		fmt.Sprintf("Requirement: %s", kycEvidenceRequirement(next)),
		"",
		"Reply YES to submit this upgrade request, or anything else to cancel.",
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
	// The request asserts nothing on the merchant's behalf. Recording the
	// tier's requirement list here would hand the compliance team a request
	// that already "has" every document, so an approval could grant a tier
	// with nothing behind it. The reviewer records what they verified.
	profile, err := s.store.RequestKYBAdvancement(ctx, merchantID, []string{}, "requested in chat")
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

// kybDetailsPrompt asks for the five corporate documents in one message. Like
// every other aggregated collection prompt it shows a labeled example, so a
// customer can reply with exactly those labels and nothing has to be guessed.
func kybDetailsPrompt(merchantName, from, to string) string {
	return strings.Join([]string{
		fmt.Sprintf("Upgrade request: %s (%s → %s)", merchantName, from, to),
		"",
		"This rung needs your corporate documents. Send all five in one message, one per line:",
		promptListing([]string{
			"CAC registration number (RC or CAC number)",
			"Business TIN",
			"Beneficial owners (each name and shareholding)",
			"Business operating address",
			"SCUML registration number",
		}),
		"For example:",
		"CAC: RC1234567",
		"TIN: 1234567890",
		"Owners: Ada Obi 60%, Chidi Obi 40%",
		"Address: 12 Marina Road, Lagos Island, Lagos",
		"SCUML: SC123456",
		"",
		"Nothing is granted from this message alone. The compliance team checks every item against the register and will ask for the certificates if they need them.",
		"Reply CANCEL to stop.",
	}, "\n")
}

// handleKYBRequestDetails records the merchant's corporate claims and files the
// upgrade request with exactly the evidence those claims support. It never
// asserts a tier's requirement list on the merchant's behalf: a document the
// merchant did not supply is not evidence, and the store's hard gate refuses a
// B1 advance until a reviewer has checked each one.
func (s *ConversationService) handleKYBRequestDetails(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if strings.EqualFold(strings.TrimSpace(input), "cancel") {
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Upgrade request cancelled. You can always come back from the merchant menu.")
	}
	merchantID, err := uuid.Parse(session.Data["merchant_id"])
	if err != nil {
		session.State, session.Data = "menu", map[string]string{}
		if err2 := s.saveSession(ctx, session); err2 != nil {
			return err2
		}
		return s.sendText(ctx, channel, recipient, "Something went wrong. Please try again from the menu.")
	}
	details := ParseKYBDocumentDetails(input)
	if missing := details.Missing(); len(missing) > 0 {
		return s.sendText(ctx, channel, recipient, missingParts(missing)+" Send them all in one message, one per line.")
	}
	docs := store.KYBDocuments{
		CACRegNumber:     details.CACRegNumber,
		TIN:              details.TIN,
		BeneficialOwners: details.BeneficialOwners,
		BusinessAddress:  details.BusinessAddress,
		SCUMLNumber:      details.SCUMLNumber,
	}
	if err := s.store.UpsertKYBDocuments(ctx, merchantID, docs); err != nil {
		return err
	}
	evidence := store.DeriveKYBEvidence(docs)
	next := session.Data["next_tier"]
	profile, err := s.store.RequestKYBAdvancement(ctx, merchantID, evidence, "submitted in chat")
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
		"Upgrade request submitted for %s (%s → %s) with %d of 5 documents on file.\n\nOur compliance team verifies each one before your tier changes. If you have the CAC and TIN certificates, opening the browser upgrade flow and uploading them speeds this up.",
		merchantName, profile.Tier, next, len(evidence)))
}

// kycEvidenceRequirement returns a human-readable description of what the
// next business tier requires, for use in the upgrade request prompt.
func kycEvidenceRequirement(tier string) string {
	switch tier {
	case kyc.TierB1:
		return "CAC registration certificate, business TIN, beneficial owners, business address and SCUML certificate"
	case kyc.TierB2:
		return "Settlement bank verified against the business identity"
	case kyc.TierB3:
		return "Enhanced due diligence on beneficial owners and source of funds"
	}
	return ""
}
