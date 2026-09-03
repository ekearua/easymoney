package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/store"
)

// startPayIndividual initiates the "pay an individual" flow. The sender pays
// an amount (plus collection fee) which is disbursed to the recipient's bank
// account (minus NIP flat fee).
func (s *ConversationService) startPayIndividual(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if user.AccountLevel != "individual" || !s.userIsApprovedIndividual(ctx, user) {
		return s.sendText(ctx, channel, recipient,
			"You need to complete individual verification (KYC Level 2) before sending money to other individuals. Send *menu* to go back.")
	}
	session.State = "pay_individual_phone"
	session.Data = map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		"Send money to an individual\n\nEnter the recipient's phone number (e.g. 08012345678):")
}

func (s *ConversationService) handlePayIndividualPhone(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	phone := strings.TrimSpace(input)
	if phone == "" {
		return s.sendText(ctx, channel, recipient, "Please enter a valid phone number.")
	}
	normalized := domain.CanonicalE164Phone(phone)
	if len(strings.TrimPrefix(normalized, "+")) < 10 {
		return s.sendText(ctx, channel, recipient, "That doesn't look like a valid phone number. Try again (e.g. 08012345678).")
	}
	if normalized == user.WhatsAppNumber {
		return s.sendText(ctx, channel, recipient, "You cannot send money to yourself. Enter a different phone number.")
	}
	session.Data["recipient_phone"] = normalized
	session.State = "pay_individual_amount"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Recipient: %s\n\nEnter the amount in Naira (e.g. 5000):", normalized))
}

func (s *ConversationService) handlePayIndividualAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amountKobo, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Invalid amount: %s. Enter a valid amount in Naira.", err))
	}
	session.Data["amount_kobo"] = fmt.Sprintf("%d", amountKobo)
	session.State = "pay_individual_bank_code"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Amount: %s\n\nEnter the recipient's bank code (e.g. 044 for Access, 058 for GTBank, 011 for First Bank):", domain.FormatNGN(amountKobo)))
}

func (s *ConversationService) handlePayIndividualBankCode(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	code := strings.TrimSpace(input)
	if len(code) < 3 || len(code) > 10 {
		return s.sendText(ctx, channel, recipient, "Bank code should be 3-10 characters. Try again.")
	}
	session.Data["bank_code"] = code
	session.State = "pay_individual_account"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		"Enter the recipient's 10-digit bank account number:")
}

func (s *ConversationService) handlePayIndividualAccount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	account := strings.TrimSpace(input)
	account = strings.NewReplacer(" ", "", "-", "").Replace(account)
	if len(account) != 10 || !isDigitsOnly(account) {
		return s.sendText(ctx, channel, recipient, "Account number must be exactly 10 digits. Try again.")
	}
	session.Data["account_number"] = account
	session.State = "pay_individual_confirm"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	collectionFee := XegoCollectionFee(s.cfg, "transfer", amountKobo)
	nipFee := XegoPayoutFee(s.cfg, amountKobo)
	totalPay := amountKobo + collectionFee.FeeKobo
	recipientGets := amountKobo - nipFee
	bankCode := session.Data["bank_code"]

	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("*Review your transfer*\n\nRecipient phone: %s\nBank: %s\nAccount: %s\n\nAmount: %s\nCollection fee: %s\nTotal you pay: %s\n\nRecipient receives: %s (after NIP fee)\n\nSend *1* to confirm or *cancel* to abort.",
			session.Data["recipient_phone"], bankCode, account,
			domain.FormatNGN(amountKobo), domain.FormatNGN(collectionFee.FeeKobo),
			domain.FormatNGN(totalPay), domain.FormatNGN(recipientGets)))
}

func (s *ConversationService) handlePayIndividualConfirm(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if strings.TrimSpace(input) == "1" {
		return s.initPayIndividualBankTransfer(ctx, channel, recipient, user, session)
	}
	if strings.EqualFold(strings.TrimSpace(input), "cancel") {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Transfer cancelled. Send *menu* to start over.")
	}
	return s.sendText(ctx, channel, recipient, "Send *1* to confirm or *cancel* to abort.")
}

func (s *ConversationService) handlePayIndividualMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "bank_transfer", "method_bank_transfer":
		return s.initPayIndividualBankTransfer(ctx, channel, recipient, user, session)
	default:
		return s.sendText(ctx, channel, recipient, "Send *bank transfer* to proceed with a bank transfer.")
	}
}

func (s *ConversationService) handleAwaitIndividualBankTransfer(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if strings.EqualFold(strings.TrimSpace(input), "cancel") {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Transfer cancelled. Send *menu* to start over.")
	}
	if !strings.EqualFold(strings.TrimSpace(input), "confirm") {
		return s.sendText(ctx, channel, recipient, "Send *confirm* when you've completed the transfer, or *cancel* to abort.")
	}

	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	recipientGets := amountKobo - XegoPayoutFee(s.cfg, amountKobo)

	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Payment confirmed!\n\nRecipient %s will receive %s within 24 hours.\nSend *menu* for more options.",
			session.Data["recipient_phone"], domain.FormatNGN(recipientGets)))
}

func (s *ConversationService) initPayIndividualBankTransfer(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	collectionFee := XegoCollectionFee(s.cfg, "transfer", amountKobo)
	totalPay := amountKobo + collectionFee.FeeKobo
	recipientGets := amountKobo - XegoPayoutFee(s.cfg, amountKobo)

	// Record the money-in split for the total the sender pays. The legs must
	// reconcile with totalPay quoted at review:
	//   totalPay = amountKobo + collection fee
	//           = collection fee + NIP fee + recipientGets
	// The NIP fee is a separate leg (deducted from the recipient's payout) so
	// the sum of credited legs exactly equals what the sender pays.
	fee := XegoCollectionFee(s.cfg, FeeChannelForProvider("transfer"), amountKobo)
	nipFee := XegoPayoutFee(s.cfg, amountKobo)
	var splits []store.SplitSpec
	if fee.FeeKobo > 0 {
		splits = append(splits, store.SplitSpec{
			SplitType:   "xego_collection_fee",
			Account:     store.LedgerAccountXegoPayable,
			AmountKobo:  fee.FeeKobo,
			Currency:    "NGN",
			Description: "Xego collection fee (individual pay)",
		})
	}
	if nipFee > 0 {
		splits = append(splits, store.SplitSpec{
			SplitType:   "nip_fee",
			Account:     store.LedgerAccountXegoPayable,
			AmountKobo:  nipFee,
			Currency:    "NGN",
			Description: "NIP payout fee (individual pay)",
		})
	}
	if recipientGets > 0 {
		splits = append(splits, store.SplitSpec{
			SplitType:   "individual_payout",
			Account:     store.LedgerAccountUserPayable,
			AmountKobo:  recipientGets,
			Currency:    "NGN",
			Description: "Individual payout liability",
		})
	}
	splitRef := fmt.Sprintf("individual-pay-%s", uuid.New().String())

	// Resolve or create the recipient user and save their bank details.
	recipientPhone := session.Data["recipient_phone"]
	recipientUser, err := s.store.GetOrCreateUser(ctx, recipientPhone)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not resolve recipient: %s. Send *menu* to try again.", err))
	}
	_, err = s.store.GetOrCreateUserPayoutDestination(ctx, recipientUser.ID,
		session.Data["bank_code"], "", session.Data["account_number"], "")
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not save recipient details: %s. Send *menu* to try again.", err))
	}

	// C9-tiers: individual pay is a money-out movement for the recipient. Their
	// tier ceiling gates how much can be disbursed to their account, and the
	// reservation is keyed by a unique reference so replays never double-count.
	// It is taken before the payout ledger entry so a rejected reservation
	// stops the payout from being recorded at all.
	recipientKYC, err := s.store.KYCProfileByUser(ctx, recipientUser.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		recipientKYC, err = s.store.EnsureKYCProfile(ctx, recipientUser.ID)
	}
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not load recipient KYC: %s. Send *menu* to try again.", err))
	}
	if err := s.store.ReserveAllowance(ctx, store.AllowanceReservation{
		AccountType: store.AccountIndividual,
		SubjectID:   recipientUser.ID,
		Direction:   kyc.DirOut,
		Tier:        recipientKYC.Tier,
		AmountKobo:  recipientGets,
		Ref:         "individual-payout:" + uuid.New().String(),
	}); err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not reserve payout: %s. Send *menu* to try again.", err))
	}

	// Record the payout ledger entry.
	if _, err := s.store.RecordPayout(ctx, recipientUser.ID, recipientGets,
		store.UserPayoutDestination{BankCode: session.Data["bank_code"], AccountNumber: session.Data["account_number"], BankName: ""},
		fmt.Sprintf("Individual payout from %s", user.WhatsAppNumber)); err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not record payout: %s. Send *menu* to try again.", err))
	}
	if err := s.store.RecordIndividualPaySplits(ctx, splitRef, splits); err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not record payment splits: %s. Send *menu* to try again.", err))
	}

	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Transfer initiated!\n\nTotal to pay: %s (amount + collection fee)\n\nTransfer this amount to Xego's bank account. Once received, %s will be sent to %s (account %s, bank %s) within 24 hours.\n\nSend *menu* for more options.",
			domain.FormatNGN(totalPay), domain.FormatNGN(recipientGets),
			recipientPhone, session.Data["account_number"], session.Data["bank_code"]))
}

// parseAmountKobo safely converts a kobo string to int64.
func parseAmountKobo(s string) int64 {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}

// isDigitsOnly reports whether every rune in s is an ASCII digit.
func isDigitsOnly(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
