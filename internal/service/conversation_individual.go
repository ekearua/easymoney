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
	return s.sendText(ctx, channel, recipient, "Email verified.\n\n"+individualDetailsPrompt())
}

// handleIndividualDetails collects the whole individual-profile form (legal
// name, DOB, gender, address, occupation) in one message, then runs the same
// screening and L1 advance the old per-field chain did. The legacy
// single-field states route here too, so sessions saved before the
// aggregation complete; anything earlier prompts collected (session.Data) is
// merged in, so a partial re-ask never retypes a captured field.
func (s *ConversationService) handleIndividualDetails(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	details := ParseIndividualProfileDetails(input)
	if details.LegalName == "" {
		details.LegalName = session.Data["legal_name"]
	}
	if details.DOB == "" {
		details.DOB = session.Data["dob"]
	}
	if details.Gender == "" {
		details.Gender = session.Data["gender"]
	}
	if details.Address == "" {
		details.Address = session.Data["address"]
	}
	if details.Occupation == "" {
		details.Occupation = session.Data["occupation"]
	}
	var problem string
	if field := ValidateIndividualProfileDetails(details); field != "" {
		switch field {
		case "legal name":
			problem = "The legal name must be between 3 and 100 characters."
			details.LegalName = ""
		case "date of birth":
			problem = "The date of birth must be a real date in YYYY-MM-DD format, and for this demo the individual must be at least 18."
			details.DOB = ""
		case "gender":
			problem = "Gender must be MALE or FEMALE."
			details.Gender = ""
		case "residential address":
			problem = "The residential address must be between 10 and 240 characters."
			details.Address = ""
		case "occupation":
			problem = "The occupation must be between 2 and 80 characters."
			details.Occupation = ""
		}
	}
	session.Data["legal_name"] = details.LegalName
	session.Data["dob"] = details.DOB
	session.Data["gender"] = details.Gender
	session.Data["address"] = details.Address
	session.Data["occupation"] = details.Occupation
	if !details.Complete() {
		session.State = "individual_details"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		msg := ""
		if problem != "" {
			msg = problem + "\n\n"
		}
		if note := capturedNote(details.Captured()); note != "" {
			msg += note + "\n"
		}
		msg += missingParts(details.Missing())
		msg += "\n\n" + individualDetailsPrompt()
		return s.sendText(ctx, channel, recipient, msg)
	}
	dob, err := time.Parse("2006-01-02", details.DOB)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That individual onboarding session expired. Please start again.")
	}
	if _, err := s.store.UpsertIndividualProfile(ctx, user.ID, details.LegalName, dob, details.Address, details.Occupation, details.Gender); err != nil {
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
	session.State = "individual_id_details"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Your Xego individual profile is approved at CBN Tier 1 (basic details on file).\n\nTo reach CBN Tier 2, we need a government-issued photo ID, your BVN, and your NIN.\n\n"+individualIDDetailsPrompt())
}

// individualDetailsPrompt is the aggregated individual-profile collection
// prompt shown once instead of the old one-message-per-field chain.
func individualDetailsPrompt() string {
	return "Send these five for your individual profile, in one message:\n\n" +
		promptListing([]string{"Legal name", "Date of birth as YYYY-MM-DD", "Gender: MALE or FEMALE", "Residential address", "Occupation"}) +
		"Example:\nName: Ada Obi\nDOB: 1992-05-24\nGender: Female\n14 Admiralty Way, Lekki, Lagos\nOccupation: Software engineer"
}

// individualIDDetailsPrompt is the aggregated photo-ID prompt.
func individualIDDetailsPrompt() string {
	return "Send your photo ID type and its number in one message.\n\n" +
		promptListing([]string{"ID type: PASSPORT, DRIVERS LICENCE, NATIONAL ID, or VOTERS CARD", "The ID number printed on the card"}) +
		"Example:\nID type: Passport\nID number: A12345678"
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
	// The recorded decision is authoritative: RecordScreeningResult rewrites
	// it to blocked when the identity is on the platform's internal
	// watchlist (G3).
	recorded, err := s.store.RecordScreeningResult(ctx, store.ScreeningResult{
		UserID:       user.ID,
		Provider:     s.screeningProviderName,
		Decision:     result.Decision,
		MatchedNames: result.MatchedNames,
		LegalName:    name,
		PhoneNumber:  user.WhatsAppNumber,
	}, s.cfg.KYCRescreenPeriod)
	if err != nil {
		return "", err
	}
	return recorded.Decision, nil
}

// handleIndividualIDDetails collects the photo ID type and number in one
// message and records the government_photo_id evidence. The legacy
// id_type/id_number states route here too, so sessions saved before the
// aggregation complete.
func (s *ConversationService) handleIndividualIDDetails(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	details := ParsePhotoIDDetails(input)
	if details.IDType == "" {
		details.IDType = session.Data["photo_id_type"]
	}
	if details.IDNumber == "" {
		details.IDNumber = session.Data["photo_id_number"]
	}
	session.Data["photo_id_type"] = details.IDType
	session.Data["photo_id_number"] = details.IDNumber
	if details.IDNumber != "" && (len([]rune(details.IDNumber)) < 6 || len([]rune(details.IDNumber)) > 25) {
		problem := "The ID number must be between 6 and 25 characters."
		details.IDNumber = ""
		session.Data["photo_id_number"] = ""
		session.State = "individual_id_details"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, problem+"\n\n"+individualIDDetailsPrompt())
	}
	if !details.Complete() {
		session.State = "individual_id_details"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		msg := ""
		if note := capturedNote(details.Captured()); note != "" {
			msg += note + "\n"
		}
		msg += missingParts(details.Missing()) + "\n\n" + individualIDDetailsPrompt()
		return s.sendText(ctx, channel, recipient, msg)
	}
	if _, err := s.store.RecordCustomerVerification(ctx, store.CustomerVerification{
		UserID:           user.ID,
		VerificationType: kyc.EvGovPhotoID,
		Status:           "completed",
		Provider:         "simulated",
		Result:           map[string]any{"id_type": details.IDType, "id_number": details.IDNumber},
	}); err != nil {
		return err
	}
	session.State = "individual_bvn_nin"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Photo ID recorded.\n\n"+bvnNinPrompt())
}

// bvnNinPrompt is the aggregated electronic-identity prompt: both 11-digit
// numbers in one message, either order.
func bvnNinPrompt() string {
	return "Send your 11-digit Bank Verification Number (BVN) and your 11-digit National Identification Number (NIN) in one message.\n\nExample:\nBVN: 22222222222\nNIN: 12345678901"
}

// handleIndividualBVNNIN collects both 11-digit numbers in one message and
// verifies each through the electronic identity provider, recording the same
// bvn_verified/nin_verified evidence the old two-step chain did. Numbers
// already verified earlier in the session are not re-verified.
func (s *ConversationService) handleIndividualBVNNIN(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	details := ParseBVNNIN(input)
	if details.BVN == "" {
		details.BVN = session.Data["bvn"]
	}
	if details.NIN == "" {
		details.NIN = session.Data["nin"]
	}
	var problems []string
	for _, check := range []struct{ label, number string }{{"BVN", details.BVN}, {"NIN", details.NIN}} {
		if check.number != "" && (len(check.number) != 11 || strings.Trim(check.number, "0123456789") != "") {
			problems = append(problems, "The "+check.label+" must be exactly 11 digits.")
			if check.label == "BVN" {
				details.BVN = ""
			} else {
				details.NIN = ""
			}
		}
	}
	session.Data["bvn"] = details.BVN
	session.Data["nin"] = details.NIN
	if !details.Complete() {
		session.State = "individual_bvn_nin"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		msg := ""
		for _, problem := range problems {
			msg += problem + "\n"
		}
		if note := capturedNote(details.Captured()); note != "" {
			msg += note + "\n"
		}
		msg += missingParts(details.Missing()) + "\n\n" + bvnNinPrompt()
		return s.sendText(ctx, channel, recipient, msg)
	}
	if session.Data["bvn_verified"] != "1" {
		message, err := s.verifyIndividualID(ctx, user, session, "bvn", details.BVN)
		if err != nil {
			return s.sendText(ctx, channel, recipient, err.Error())
		}
		if message != "" {
			session.State = "individual_bvn_nin"
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendText(ctx, channel, recipient, message+"\n\n"+bvnNinPrompt())
		}
		session.Data["bvn_verified"] = "1"
	}
	if session.Data["nin_verified"] != "1" {
		message, err := s.verifyIndividualID(ctx, user, session, "nin", details.NIN)
		if err != nil {
			return s.sendText(ctx, channel, recipient, err.Error())
		}
		if message != "" {
			session.State = "individual_bvn_nin"
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendText(ctx, channel, recipient, message+"\n\n"+bvnNinPrompt())
		}
		session.Data["nin_verified"] = "1"
	}
	if _, err := s.store.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL2,
		[]string{kyc.EvGovPhotoID, kyc.EvBVNVerified, kyc.EvNINVerified}, nil); err != nil {
		return err
	}
	if _, err := s.store.RecomputeRiskScore(ctx, user.ID); err != nil {
		return err
	}
	session.State = "individual_edd_details"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Your BVN and NIN are verified. Your Xego individual profile is now approved at CBN Tier 2.\n\nTo reach CBN Tier 3, we need a structural proof of your residential address and an enhanced due-diligence check.\n\n"+eddDetailsPrompt())
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

// handleIndividualEDDDetails collects the Tier 3 enhanced-due-diligence set
// (address-proof reference or SELF-DECLARED, plus the source of funds) in one
// message and records the same evidence rows the old two-step chain did. The
// legacy address_proof/source_of_funds states route here too.
func (s *ConversationService) handleIndividualEDDDetails(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	details := ParseEDDDetails(input)
	if details.AddressProof == "" {
		details.AddressProof = session.Data["address_proof"]
	}
	if details.SourceOfFunds == "" {
		details.SourceOfFunds = session.Data["source_of_funds"]
	}
	session.Data["address_proof"] = details.AddressProof
	session.Data["source_of_funds"] = details.SourceOfFunds
	if !details.Complete() {
		session.State = "individual_edd_details"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		msg := ""
		if note := capturedNote(details.Captured()); note != "" {
			msg += note + "\n"
		}
		msg += missingParts(details.Missing()) + "\n\n" + eddDetailsPrompt()
		return s.sendText(ctx, channel, recipient, msg)
	}
	if session.Data["address_proof_recorded"] != "1" {
		if _, err := s.store.RecordCustomerVerification(ctx, store.CustomerVerification{
			UserID:           user.ID,
			VerificationType: kyc.EvAddressProof,
			Status:           "completed",
			Provider:         "simulated",
			Result:           map[string]any{"reference": details.AddressProof, "address": session.Data["address"]},
		}); err != nil {
			return err
		}
		session.Data["address_proof_recorded"] = "1"
	}
	session.State = "individual_liveness"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Address proof and source of funds recorded.\n\n"+eddLivenessPrompt())
}

// eddDetailsPrompt is the aggregated Tier 3 EDD prompt.
func eddDetailsPrompt() string {
	return "Send a recent utility bill reference (like a prepaid meter or account number), or SELF-DECLARED, together with your primary source of funds, in one message.\n\nExample:\nUtility ref: 12345678901\nSource of funds: SALARY"
}

// eddLivenessPrompt is the real-time self-attestation, kept deliberately as
// its own single-purpose turn.
func eddLivenessPrompt() string {
	return "To finish the enhanced due-diligence check, confirm you are completing this verification yourself in real time.\n\nType YES to confirm."
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
