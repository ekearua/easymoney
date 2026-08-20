package service

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func (s *ConversationService) startIndividualUpgrade(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if user.AccountLevel == "merchant" {
		return s.sendText(ctx, channel, recipient, "This account is currently approved as a merchant. For this demo, thrift creation is only available to individual accounts.")
	}
	if kycProfile, err := s.store.KYCProfileByUser(ctx, user.ID); err == nil && kyc.Order(kycProfile.Tier) >= kyc.Order(kyc.TierL2) {
		return s.sendText(ctx, channel, recipient, "Your Xego individual profile is already approved for the demo. Choose Create thrift to start a contribution group.")
	}
	if !s.cfg.EmailConfirmationEnabled {
		return s.sendText(ctx, channel, recipient, "Individual onboarding is not accepting email-verified upgrades right now. Please try again later.")
	}
	session.Data = map[string]string{}
	email := strings.TrimSpace(user.Email)
	if email == "" {
		session.State = "individual_email"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Let's set up your Xego individual profile for thrift contributions.\n\nFirst, send the email address we should verify.")
	}
	session.State = "individual_email_code"
	session.Data["email"] = email
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
}

func (s *ConversationService) handleIndividualEmail(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	address, err := mail.ParseAddress(input)
	if err != nil || !strings.Contains(address.Address, "@") || len(address.Address) > 254 {
		return s.sendText(ctx, channel, recipient, "That email doesn't look valid. Please send a valid email address, like name@example.com.")
	}
	email := strings.ToLower(address.Address)
	if err := s.store.UpdateUserEmail(ctx, user.ID, email); err != nil {
		return err
	}
	session.State = "individual_email_code"
	session.Data = map[string]string{"email": email}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
}

func (s *ConversationService) handleIndividualEmailCode(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	email := session.Data["email"]
	if email == "" {
		email = user.Email
	}
	switch strings.ToLower(input) {
	case "resend", "resend code":
		return s.startEmailConfirmation(ctx, channel, recipient, user.ID, email)
	case "change_email", "change email", "email":
		session.State, session.Data = "individual_email", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Send the email address to verify for your individual profile.")
	}
	code := normalizeEmailCode(input)
	if len(code) != 6 {
		return s.sendText(ctx, channel, recipient, "Please enter the 6-digit code we sent to "+email+". You can also type RESEND or CHANGE EMAIL.")
	}
	ok, err := s.store.VerifyEmailCode(ctx, user.ID, email, emailCodeDigest(email, code))
	if err != nil {
		return err
	}
	if !ok {
		return s.sendText(ctx, channel, recipient, "That code is incorrect or has expired. Please try again, type RESEND, or type CHANGE EMAIL.")
	}
	session.State = "individual_legal_name"
	session.Data = map[string]string{"email": email}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Email verified.\n\nSend your legal name for this demo individual profile.")
}

func (s *ConversationService) handleIndividualLegalName(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	name := strings.TrimSpace(input)
	if len([]rune(name)) < 3 || len([]rune(name)) > 100 {
		return s.sendText(ctx, channel, recipient, "Send your legal name, between 3 and 100 characters.")
	}
	session.State = "individual_dob"
	session.Data["legal_name"] = name
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Send your date of birth as YYYY-MM-DD. Example: 1992-05-24")
}

func (s *ConversationService) handleIndividualDOB(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	dob, err := time.Parse("2006-01-02", strings.TrimSpace(input))
	if err != nil || dob.After(time.Now().AddDate(-18, 0, 0)) {
		return s.sendText(ctx, channel, recipient, "Send a valid date of birth as YYYY-MM-DD. For this demo, the individual must be at least 18.")
	}
	session.State = "individual_address"
	session.Data["dob"] = dob.Format("2006-01-02")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Send your residential address for the demo profile.")
}

func (s *ConversationService) handleIndividualAddress(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	address := strings.TrimSpace(input)
	if len([]rune(address)) < 10 || len([]rune(address)) > 240 {
		return s.sendText(ctx, channel, recipient, "Send an address between 10 and 240 characters.")
	}
	session.State = "individual_occupation"
	session.Data["address"] = address
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "What is your occupation?")
}

func (s *ConversationService) handleIndividualOccupation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	occupation := strings.TrimSpace(input)
	if len([]rune(occupation)) < 2 || len([]rune(occupation)) > 80 {
		return s.sendText(ctx, channel, recipient, "Send an occupation between 2 and 80 characters.")
	}
	dob, err := time.Parse("2006-01-02", session.Data["dob"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That individual onboarding session expired. Please start again.")
	}
	if _, err := s.store.UpsertIndividualProfile(ctx, user.ID, session.Data["legal_name"], dob, session.Data["address"], occupation); err != nil {
		return err
	}
	decision, err := s.screenIndividual(ctx, user, session)
	if err != nil {
		return err
	}
	if kyc.BlockedByScreening(decision) {
		if _, err := s.store.CreateManualReviewCase(ctx, store.ManualReviewCase{
			UserID: user.ID, CaseType: "screening", Reason: "screening decision " + decision,
		}); err != nil {
			return err
		}
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Your Xego individual profile is under review.\n\nOur compliance team is reviewing your screening result and will follow up. You can still browse the menu.")
	}
	if _, err := s.store.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL2,
		[]string{kyc.EvChannelConfirmed, kyc.EvIdentityOnFile}, nil); err != nil {
		return err
	}
	if _, err := s.store.RecomputeRiskScore(ctx, user.ID); err != nil {
		return err
	}
	session.State = "individual_id_number"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Your Xego individual profile is approved at Level 2 (identity on file).\n\nTo finish Level 3 verification, send your 11-digit NIN or BVN. (Demo: a NIN starting 1 or a BVN starting 2 verifies; 8 = record mismatch; 9 = not found.)\n\nSend NIN or BVN, then your 11-digit number.")
}

func (s *ConversationService) screenIndividual(ctx context.Context, user store.User, session store.Session) (string, error) {
	name := strings.TrimSpace(session.Data["legal_name"])
	result, err := s.screener.Screen(ctx, ports.ScreeningRequest{
		LegalName:   name,
		DateOfBirth: session.Data["dob"],
		PhoneNumber: user.WhatsAppNumber,
	})
	if err != nil {
		return "", err
	}
	if _, err := s.store.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID:       user.ID,
		Provider:     "simulated",
		Decision:     result.Decision,
		MatchedNames: result.MatchedNames,
	}, s.cfg.KYCRescreenPeriod); err != nil {
		return "", err
	}
	return result.Decision, nil
}

func (s *ConversationService) handleIndividualIDNumber(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	raw := strings.TrimSpace(input)
	if strings.EqualFold(raw, "skip") {
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		if err := s.sendText(ctx, channel, recipient, "No problem. Your Xego individual profile is Level 2 (identity on file) and approved for this demo.\n\nYou can create thrift contribution groups."); err != nil {
			return err
		}
		return s.sendMenu(ctx, channel, recipient)
	}
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return s.sendText(ctx, channel, recipient, "Send the ID type and number, like: NIN 12345678901")
	}
	idType := strings.ToLower(fields[0])
	number := strings.Join(fields[1:], "")
	switch idType {
	case "nin", "bvn":
	default:
		return s.sendText(ctx, channel, recipient, "ID type must be NIN or BVN. Send the ID type and number, like: BVN 22222222222")
	}
	if len(number) != 11 {
		return s.sendText(ctx, channel, recipient, "That number is not 11 digits. Send the ID type and 11-digit number, like: NIN 12345678901")
	}
	result, err := s.identity.VerifyIdentity(ctx, ports.IdentityVerificationRequest{
		IDType:      idType,
		IDNumber:    number,
		LegalName:   session.Data["legal_name"],
		DateOfBirth: session.Data["dob"],
	})
	if err != nil {
		return s.sendText(ctx, channel, recipient, "We could not reach the verification provider. Please try again in a moment, or type SKIP to stay at Level 2.")
	}
	switch result.Status {
	case "verified":
		if _, err := s.store.RecordCustomerVerification(ctx, store.CustomerVerification{
			UserID:           user.ID,
			VerificationType: kyc.EvNINBVNVerified,
			Status:           "completed",
			Provider:         "simulated",
			ProviderRef:      result.ProviderRef,
			Result:           map[string]any{"id_type": idType, "match_name": result.MatchName, "match_dob": result.MatchDOB},
		}); err != nil {
			return err
		}
		if _, err := s.store.AdvanceKYCTier(ctx, user.ID, kyc.TierL3, []string{kyc.EvNINBVNVerified}, nil); err != nil {
			return err
		}
		riskProfile, err := s.store.RecomputeRiskScore(ctx, user.ID)
		if err != nil {
			return err
		}
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		if err := s.sendText(ctx, channel, recipient, fmt.Sprintf("Your NIN/BVN verified. Your Xego individual profile is now Level 3 (NIN/BVN verified).\n\nRisk profile: %s.\n\nYou can now create thrift contribution groups.", strings.ToUpper(riskProfile.RiskBand))); err != nil {
			return err
		}
		return s.sendMenu(ctx, channel, recipient)
	case "mismatch", "not_found":
		return s.sendText(ctx, channel, recipient, result.Message+".\n\nDouble-check the number and send it again as NIN <number> or BVN <number>, or type SKIP to stay at Level 2.")
	default:
		return s.sendText(ctx, channel, recipient, "Verification is still pending. Send the ID type and number again, or type SKIP to stay at Level 2.")
	}
}

func (s *ConversationService) userIsApprovedIndividual(ctx context.Context, user store.User) bool {
	if user.AccountLevel != "individual" {
		return false
	}
	profile, err := s.store.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		return false
	}
	return kyc.Order(profile.Tier) >= kyc.Order(kyc.TierL2)
}
