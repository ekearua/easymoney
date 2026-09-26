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
		return s.sendText(ctx, channel, recipient, "This account is currently approved as a merchant. For this demo, sending money to individuals and creating thrift groups are only available to individual accounts.")
	}
	if kycProfile, err := s.store.KYCProfileByUser(ctx, user.ID); err == nil && kyc.Order(kycProfile.Tier) >= kyc.Order(kyc.TierL2) {
		return s.sendText(ctx, channel, recipient, "Your Xego individual profile is already approved for the demo. You can send money to individuals and create thrift groups.")
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
		return s.sendText(ctx, channel, recipient, "Let's set up your Xego individual profile.\n\nIt lets you send money to individuals and run thrift groups.\n\nFirst, send the email address we should verify.")
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
	case "change email", "email":
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
	session.State = "individual_gender"
	session.Data["dob"] = dob.Format("2006-01-02")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Send your gender: MALE or FEMALE.")
}

func (s *ConversationService) handleIndividualGender(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	gender := strings.ToLower(strings.TrimSpace(input))
	if gender != "male" && gender != "female" {
		return s.sendText(ctx, channel, recipient, "Send your gender: MALE or FEMALE.")
	}
	session.State = "individual_address"
	session.Data["gender"] = gender
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
	if _, err := s.store.UpsertIndividualProfile(ctx, user.ID, session.Data["legal_name"], dob, session.Data["address"], occupation, session.Data["gender"]); err != nil {
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
	if _, err := s.store.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL1,
		[]string{kyc.EvChannelConfirmed, kyc.EvTier1BasicDetails}, nil); err != nil {
		return err
	}
	if _, err := s.store.RecomputeRiskScore(ctx, user.ID); err != nil {
		return err
	}
	session.State = "individual_id_type"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Your Xego individual profile is approved at CBN Tier 1 (basic details on file).\n\nTo reach CBN Tier 2, we need a government-issued photo ID, your BVN, and your NIN.\n\nWhich photo ID do you have? Send PASSPORT, DRIVERS LICENCE, NATIONAL ID, or VOTERS CARD.")
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
		Provider:     s.screeningProviderName,
		Decision:     result.Decision,
		MatchedNames: result.MatchedNames,
	}, s.cfg.KYCRescreenPeriod); err != nil {
		return "", err
	}
	return result.Decision, nil
}

func (s *ConversationService) handleIndividualIDType(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	kind := canonicalPhotoIDType(input)
	if kind == "" {
		return s.sendText(ctx, channel, recipient, "Send one of: PASSPORT, DRIVERS LICENCE, NATIONAL ID, or VOTERS CARD.")
	}
	session.State = "individual_id_number"
	session.Data["photo_id_type"] = kind
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Send the ID number printed on your "+photoIDTypeLabel(kind)+".")
}

func (s *ConversationService) handleIndividualIDNumber(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	number := strings.TrimSpace(input)
	if len([]rune(number)) < 6 || len([]rune(number)) > 25 {
		return s.sendText(ctx, channel, recipient, "Send the full ID number printed on your card, between 6 and 25 characters.")
	}
	if _, err := s.store.RecordCustomerVerification(ctx, store.CustomerVerification{
		UserID:           user.ID,
		VerificationType: kyc.EvGovPhotoID,
		Status:           "completed",
		Provider:         "simulated",
		Result:           map[string]any{"id_type": session.Data["photo_id_type"], "id_number": number},
	}); err != nil {
		return err
	}
	session.State = "individual_bvn"
	session.Data["photo_id_number"] = number
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Now send your 11-digit Bank Verification Number (BVN).")
}

func (s *ConversationService) handleIndividualBVN(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	number := strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	if len(number) != 11 || strings.Trim(number, "0123456789") != "" {
		return s.sendText(ctx, channel, recipient, "That BVN is not 11 digits. Send your 11-digit BVN, like: 22222222222")
	}
	message, err := s.verifyIndividualID(ctx, user, session, "bvn", number)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	if message != "" {
		return s.sendText(ctx, channel, recipient, message)
	}
	session.State = "individual_nin"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Your BVN is verified.\n\nNow send your 11-digit National Identification Number (NIN).")
}

func (s *ConversationService) handleIndividualNIN(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	number := strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	if len(number) != 11 || strings.Trim(number, "0123456789") != "" {
		return s.sendText(ctx, channel, recipient, "That NIN is not 11 digits. Send your 11-digit NIN, like: 12345678901")
	}
	message, err := s.verifyIndividualID(ctx, user, session, "nin", number)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	if message != "" {
		return s.sendText(ctx, channel, recipient, message)
	}
	if _, err := s.store.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL2,
		[]string{kyc.EvGovPhotoID, kyc.EvBVNVerified, kyc.EvNINVerified}, nil); err != nil {
		return err
	}
	if _, err := s.store.RecomputeRiskScore(ctx, user.ID); err != nil {
		return err
	}
	session.State = "individual_address_proof"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Your NIN is verified. Your Xego individual profile is now approved at CBN Tier 2.\n\nTo reach CBN Tier 3, we need a structural proof of your residential address and an enhanced due-diligence check.\n\nSend a recent utility bill reference (like a prepaid meter or account number), or type SELF-DECLARED.")
}

// verifyIndividualID verifies one electronic identity (BVN or NIN) against the
// provider and records the matching evidence. A non-empty message means the
// verification failed and the caller should surface it (staying on the same
// step); an empty message means success.
func (s *ConversationService) verifyIndividualID(ctx context.Context, user store.User, session store.Session, idType, number string) (string, error) {
	result, err := s.identity.VerifyIdentity(ctx, ports.IdentityVerificationRequest{
		IDType:      idType,
		IDNumber:    number,
		LegalName:   session.Data["legal_name"],
		DateOfBirth: session.Data["dob"],
	})
	if err != nil {
		return "We could not reach the verification provider. Please try again in a moment.", nil
	}
	switch result.Status {
	case "verified":
		evidence := kyc.EvBVNVerified
		if idType == "nin" {
			evidence = kyc.EvNINVerified
		}
		if _, err := s.store.RecordCustomerVerification(ctx, store.CustomerVerification{
			UserID:           user.ID,
			VerificationType: evidence,
			Status:           "completed",
			Provider:         s.identityProviderName,
			ProviderRef:      result.ProviderRef,
			Result:           map[string]any{"id_type": idType, "id_number": number, "match_name": result.MatchName, "match_dob": result.MatchDOB},
		}); err != nil {
			return "", err
		}
		return "", nil
	case "mismatch", "not_found":
		return result.Message + ".\n\nDouble-check the number and send it again as " + strings.ToUpper(idType) + " <number>.", nil
	default:
		return "Verification is still pending. Send the number again in a moment.", nil
	}
}

func (s *ConversationService) handleIndividualAddressProof(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	ref := strings.TrimSpace(input)
	if len([]rune(ref)) < 4 {
		return s.sendText(ctx, channel, recipient, "Send a utility bill reference or type SELF-DECLARED.")
	}
	if _, err := s.store.RecordCustomerVerification(ctx, store.CustomerVerification{
		UserID:           user.ID,
		VerificationType: kyc.EvAddressProof,
		Status:           "completed",
		Provider:         "simulated",
		Result:           map[string]any{"reference": ref, "address": session.Data["address"]},
	}); err != nil {
		return err
	}
	session.State = "individual_source_of_funds"
	session.Data["address_proof"] = ref
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Address proof recorded.\n\nSelect your primary source of funds: SALARY, BUSINESS, INVESTMENTS, GIFTS, or OTHER.")
}

func (s *ConversationService) handleIndividualSourceOfFunds(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	source := strings.ToUpper(strings.TrimSpace(input))
	switch source {
	case "SALARY", "BUSINESS", "INVESTMENTS", "GIFTS", "OTHER":
	default:
		return s.sendText(ctx, channel, recipient, "Send one of: SALARY, BUSINESS, INVESTMENTS, GIFTS, or OTHER.")
	}
	session.State = "individual_liveness"
	session.Data["source_of_funds"] = source
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "To finish the enhanced due-diligence check, confirm you are completing this verification yourself in real time.\n\nType YES to confirm.")
}

func (s *ConversationService) handleIndividualLiveness(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "yes", "1", "confirm", "confirm yes":
	default:
		return s.sendText(ctx, channel, recipient, "Type YES to confirm you are completing this verification yourself in real time.")
	}
	if _, err := s.store.RecordCustomerVerification(ctx, store.CustomerVerification{
		UserID:           user.ID,
		VerificationType: kyc.EvEDDCompleted,
		Status:           "completed",
		Provider:         "simulated",
		Result:           map[string]any{"liveness": "passed", "source_of_funds": session.Data["source_of_funds"]},
	}); err != nil {
		return err
	}
	if _, err := s.store.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL3,
		[]string{kyc.EvAddressProof, kyc.EvEDDCompleted}, nil); err != nil {
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
	if err := s.sendText(ctx, channel, recipient, fmt.Sprintf("Enhanced due diligence complete. Your Xego individual profile is now approved at CBN Tier 3 (uncapped).\n\nRisk profile: %s.\n\nYou can now send money to individuals and create thrift groups with the highest limits.", strings.ToUpper(riskProfile.RiskBand))); err != nil {
		return err
	}
	return s.sendMenu(ctx, channel, recipient, user)
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

func canonicalPhotoIDType(input string) string {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "passport", "international passport", "intl passport":
		return "passport"
	case "drivers licence", "driver's licence", "driver's license", "drivers license", "drivers", "licence", "license":
		return "drivers_licence"
	case "national id", "national id card", "national identity card", "national":
		return "national_id"
	case "voters card", "voters", "voter's card", "pvc":
		return "voters_card"
	}
	return ""
}

func photoIDTypeLabel(kind string) string {
	switch kind {
	case "passport":
		return "international passport"
	case "drivers_licence":
		return "driver's licence"
	case "national_id":
		return "National ID card"
	case "voters_card":
		return "PVC (voter's card)"
	}
	return kind
}
