package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
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
		fmt.Sprintf("Amount: %s\n\nEnter the recipient's bank name (e.g. Access, GTBank, Zenith) or bank code (e.g. 044):", domain.FormatNGN(amountKobo)))
}

func (s *ConversationService) handlePayIndividualBankCode(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "paybank_page:") {
		page := parsePickerPage(strings.TrimPrefix(input, "paybank_page:"))
		return s.sendPayBankNamePicker(ctx, channel, recipient, session.Data["bank_query"], page)
	}
	if strings.HasPrefix(input, "bk:") {
		return s.selectPayIndividualBank(ctx, channel, recipient, user, session, strings.TrimPrefix(input, "bk:"))
	}
	if input == "" {
		return s.sendText(ctx, channel, recipient, "Enter the recipient's bank name (e.g. Access, GTBank, Zenith) or bank code (e.g. 044):")
	}
	resolution, err := ResolveBank(ctx, s.store, input)
	if errors.Is(err, ErrBankAmbiguous) {
		session.Data["bank_query"] = input
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendPayBankNamePicker(ctx, channel, recipient, input, 0)
	}
	if errors.Is(err, ErrBankNotFound) {
		return s.sendText(ctx, channel, recipient,
			"I couldn't find that bank. Type the full bank name (e.g. Guaranty Trust Bank) or a bank code (e.g. 044), or send *menu* to cancel.")
	}
	if err != nil {
		return err
	}
	session.Data["bank_code"] = resolution.Code
	session.Data["bank_name"] = resolution.Name
	session.State = "pay_individual_account"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Bank: %s (%s)\n\nEnter the recipient's 10-digit bank account number:", resolution.Name, resolution.Code))
}

// handlePayIndividualBankPick handles the reply to the bank-name picker shown
// when ResolveBank could not narrow the input to a single bank.
func (s *ConversationService) handlePayIndividualBankPick(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "bk:") {
		return s.selectPayIndividualBank(ctx, channel, recipient, user, session, strings.TrimPrefix(input, "bk:"))
	}
	if strings.HasPrefix(input, "paybank_page:") {
		page := parsePickerPage(strings.TrimPrefix(input, "paybank_page:"))
		return s.sendPayBankNamePicker(ctx, channel, recipient, session.Data["bank_query"], page)
	}
	if input == "bank_choose_other" {
		session.Data["bank_query"] = ""
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendPayBankNamePicker(ctx, channel, recipient, "", 0)
	}
	if input == "" {
		return s.sendText(ctx, channel, recipient, "Choose a bank from the list, or type the bank name to search again (or send *menu* to cancel).")
	}
	return s.handlePayIndividualBankCode(ctx, channel, recipient, user, session, input)
}

// selectPayIndividualBank persists a picked directory entry and moves to the
// account-number step.
func (s *ConversationService) selectPayIndividualBank(ctx context.Context, channel, recipient string, user store.User, session store.Session, code string) error {
	bank, err := s.store.BankByCode(ctx, code)
	if errors.Is(err, store.ErrBankNotFound) {
		return s.sendText(ctx, channel, recipient, "That bank is no longer available. Choose again from the list.")
	}
	if err != nil {
		return err
	}
	session.Data["bank_code"] = bank.Code
	session.Data["bank_name"] = bank.Name
	session.State = "pay_individual_account"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Bank: %s (%s)\n\nEnter the recipient's 10-digit bank account number:", bank.Name, bank.Code))
}

// sendPayBankNamePicker renders the bank directory as an interactive list,
// paged like the other chat pickers. Row IDs use the bank code ("bk:044").
func (s *ConversationService) sendPayBankNamePicker(ctx context.Context, channel, recipient, query string, page int) error {
	page = normalizePickerPage(page)
	query = strings.TrimSpace(query)
	banks, err := s.store.SearchBanks(ctx, query, 100)
	if err != nil {
		return err
	}
	if len(banks) == 0 {
		if query == "" {
			return s.sendText(ctx, channel, recipient, "Please type the recipient's bank name (e.g. Access, GTBank, Zenith) or code (e.g. 044).")
		}
		return s.sendText(ctx, channel, recipient, "I couldn't find that bank. Try the full bank name (e.g. Guaranty Trust Bank), or send *menu* to cancel.")
	}
	start := page * pickerPageSize
	if start >= len(banks) {
		start = 0
	}
	end := start + pickerPageSize
	if end > len(banks) {
		end = len(banks)
	}
	rows := make([]ports.InteractiveRow, 0, end-start+2)
	for _, bank := range banks[start:end] {
		rows = append(rows, ports.InteractiveRow{
			ID:          "bk:" + bank.Code,
			Title:       bank.Name,
			Description: "Bank code " + bank.Code,
		})
	}
	rows = appendPickerNavigation(rows, "paybank_page:", page, end < len(banks))
	body := "Choose the recipient's bank, or type another bank name to search again."
	if query != "" {
		body = fmt.Sprintf("Bank search results for %q.\n\nChoose one or type another bank name to search again.", query)
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        body,
		ButtonLabel: "Choose bank",
		Sections:    []ports.InteractiveSection{{Title: "Banks", Rows: rows}},
	})
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
	bankLabel := sessionBankLabel(session.Data)

	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("*Review your transfer*\n\nRecipient phone: %s\nBank: %s\nAccount: %s\n\nAmount: %s\nCollection fee: %s\nTotal you pay: %s\n\nRecipient receives: %s (after NIP fee)\n\nSend *1* to confirm or *cancel* to abort.",
			session.Data["recipient_phone"], bankLabel, account,
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
	recipientPhone := session.Data["recipient_phone"]

	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Payment confirmed!\n\nRecipient %s will receive %s within 24 hours.\nSend *menu* for more options.",
			recipientPhone, domain.FormatNGN(recipientGets)))
}

func (s *ConversationService) initPayIndividualBankTransfer(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	collectionFee := XegoCollectionFee(s.cfg, FeeChannelForProvider("transfer"), amountKobo)
	totalPay := amountKobo + collectionFee.FeeKobo
	recipientGets := amountKobo - XegoPayoutFee(s.cfg, amountKobo)
	nipFee := XegoPayoutFee(s.cfg, amountKobo)

	recipientPhone := session.Data["recipient_phone"]
	bankCode := session.Data["bank_code"]
	bankName := session.Data["bank_name"]
	accountNumber := session.Data["account_number"]

	// Resolve or create the recipient user and save their bank details up
	// front so the post-success hook can settle the payout without session
	// data.
	recipientUser, err := s.store.GetOrCreateUser(ctx, recipientPhone)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not resolve recipient: %s. Send *menu* to try again.", err))
	}
	if _, err := s.store.GetOrCreateUserPayoutDestination(ctx, recipientUser.ID, bankCode, bankName, accountNumber, ""); err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not save recipient details: %s. Send *menu* to try again.", err))
	}

	// The sender's payment is a plain collection against the Xego system
	// merchant, routed through the Interswitch gateway like any other
	// checkout. Its money-in allowance is reserved at draft creation; once
	// the gateway verifies the payment, the post-success hook books the
	// splits (collection fee + NIP fee + payout liability) and disburses the
	// recipient's payout.
	payment, err := s.payments.CreateIndividualPayDraft(ctx, user, channel, recipient, totalPay, map[string]any{
		"individual_pay": map[string]any{
			"recipient_phone":     recipientPhone,
			"bank_code":           bankCode,
			"bank_name":           bankName,
			"account_number":      accountNumber,
			"amount_kobo":         amountKobo,
			"collection_fee_kobo": collectionFee.FeeKobo,
			"nip_fee_kobo":        nipFee,
			"recipient_gets":      recipientGets,
		},
	})
	if err != nil {
		return friendlyAllowanceErr(err)
	}
	bankLabel := sessionBankLabel(session.Data)
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendCheckout(ctx, channel, recipient,
		fmt.Sprintf("Your secure checkout is ready.\n\nRecipient: %s\nBank: %s\nAccount: %s\n\nAmount: %s\nCollection fee: %s\nTotal you pay: %s\n\nRecipient receives: %s (after NIP fee)\n\nXego verifies the payment with Interswitch before disbursing to the recipient's account.",
			recipientPhone, bankLabel, accountNumber,
			domain.FormatNGN(amountKobo), domain.FormatNGN(collectionFee.FeeKobo),
			domain.FormatNGN(totalPay), domain.FormatNGN(recipientGets)),
		s.payments.HostedCheckoutURL(payment))
}

// sessionBankLabel renders the bank part of a review line, preferring the
// resolved bank name with its code in parentheses.
func sessionBankLabel(data map[string]string) string {
	code := data["bank_code"]
	if name := data["bank_name"]; name != "" && code != "" {
		return name + " (" + code + ")"
	}
	if code != "" {
		return code
	}
	return "unknown"
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
