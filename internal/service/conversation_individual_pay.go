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
	session.State = firstMissingIndividualPayStep(session.Data)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.promptPayIndividualStep(ctx, channel, recipient, session.Data)
}

func (s *ConversationService) handlePayIndividualAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amountKobo, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Invalid amount: %s. Enter a valid amount in Naira.", err))
	}
	session.Data["amount_kobo"] = fmt.Sprintf("%d", amountKobo)
	session.State = firstMissingIndividualPayStep(session.Data)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.promptPayIndividualStep(ctx, channel, recipient, session.Data)
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
	session.State = firstMissingIndividualPayStep(session.Data)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.promptPayIndividualStep(ctx, channel, recipient, session.Data)
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
	session.State = firstMissingIndividualPayStep(session.Data)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.promptPayIndividualStep(ctx, channel, recipient, session.Data)
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
	session.State = firstMissingIndividualPayStep(session.Data)
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
		fmt.Sprintf("*Review your transfer*\n\nRecipient phone: %s\nBank: %s\nAccount: %s\n\nAmount: %s\nCollection fee: %s\nTotal you pay: %s\n\nRecipient receives: %s (after NIP fee)\n\nSend *1* to pay by bank transfer, *2* to pay from your wallet, or *cancel* to abort.",
			session.Data["recipient_phone"], bankLabel, account,
			domain.FormatNGN(amountKobo), domain.FormatNGN(collectionFee.FeeKobo),
			domain.FormatNGN(totalPay), domain.FormatNGN(recipientGets)))
}

func (s *ConversationService) handlePayIndividualConfirm(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "1":
		return s.initPayIndividualBankTransfer(ctx, channel, recipient, user, session)
	case "2", "wallet", "pay from wallet":
		return s.initPayIndividualWallet(ctx, channel, recipient, user, session)
	case "cancel":
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Transfer cancelled. Send *menu* to start over.")
	}
	return s.sendText(ctx, channel, recipient, "Send *1* to pay by bank transfer, *2* to pay from your wallet, or *cancel* to abort.")
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

// initPayIndividualWallet is initPayIndividualBankTransfer on the wallet rail:
// the recipient settlement is identical, but the sender's payment is funded
// instantly from their wallet instead of the Interswitch checkout (billed
// under the transfer fee parameters, matching the review shown up front).
func (s *ConversationService) initPayIndividualWallet(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	collectionFee := XegoCollectionFee(s.cfg, "transfer", amountKobo)
	totalPay := amountKobo + collectionFee.FeeKobo
	recipientGets := amountKobo - XegoPayoutFee(s.cfg, amountKobo)
	nipFee := XegoPayoutFee(s.cfg, amountKobo)

	recipientPhone := session.Data["recipient_phone"]
	bankCode := session.Data["bank_code"]
	bankName := session.Data["bank_name"]
	accountNumber := session.Data["account_number"]

	recipientUser, err := s.store.GetOrCreateUser(ctx, recipientPhone)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not resolve recipient: %s. Send *menu* to try again.", err))
	}
	if _, err := s.store.GetOrCreateUserPayoutDestination(ctx, recipientUser.ID, bankCode, bankName, accountNumber, ""); err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Could not save recipient details: %s. Send *menu* to try again.", err))
	}

	payment, err := s.payments.CreateIndividualPayDraftWithProvider(ctx, user, channel, recipient, totalPay, ProviderWallet, map[string]any{
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
	if err := s.beginWalletConfirm(ctx, channel, recipient, user, session, payment); err != nil {
		return err
	}
	return s.sendIndividualPayWalletReview(ctx, channel, recipient, user, session)
}

// sendIndividualPayWalletReview confirms an instant wallet-funded individual
// transfer showing the same numbers as the review step that preceded it.
func (s *ConversationService) sendIndividualPayWalletReview(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	collectionFee := XegoCollectionFee(s.cfg, "transfer", amountKobo).FeeKobo
	totalPay := amountKobo + collectionFee
	recipientGets := amountKobo - XegoPayoutFee(s.cfg, amountKobo)
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("*Pay from wallet*\n\nRecipient: %s\nBank: %s\nAccount: %s\n\nAmount: %s\nCollection fee: %s\nTotal you pay: %s\n\nRecipient receives: %s (after NIP fee)%s\n\nPay instantly from your Xego wallet?",
			session.Data["recipient_phone"], sessionBankLabel(session.Data), session.Data["account_number"],
			domain.FormatNGN(amountKobo), domain.FormatNGN(collectionFee), domain.FormatNGN(totalPay),
			domain.FormatNGN(recipientGets), s.walletBalanceLine(ctx, user, totalPay)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_payment", Title: "Pay from wallet"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
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

// routePaymentHint dispatches a parsed payment instruction: a matched merchant
// goes to merchant pay, everything else (phone/account/name) goes to the
// individual flow.
func (s *ConversationService) routePaymentHint(ctx context.Context, channel, recipient string, user store.User, session store.Session, hint IndividualPayHint) error {
	if hint.MerchantTarget() || hint.AmbiguousMerchant() {
		return s.startMerchantFromHint(ctx, channel, recipient, user, session, hint)
	}
	return s.startPayIndividualFromHint(ctx, channel, recipient, user, session, hint)
}

// startMerchantFromHint routes a merchant instruction seeded from a parsed
// hint. Web-flow channels open the browser flow with the merchant and amount
// prefilled; chat channels carry the merchant picker like the AI "pay" route.
func (s *ConversationService) startMerchantFromHint(ctx context.Context, channel, recipient string, user store.User, session store.Session, hint IndividualPayHint) error {
	query := ""
	if hint.MerchantSlug != "" {
		if merchant, err := s.store.MerchantBySlug(ctx, hint.MerchantSlug); err == nil {
			query = merchant.Name
		}
	} else if hint.Name != "" {
		query = hint.Name
	}
	payload := map[string]string{}
	if hint.MerchantSlug != "" {
		payload["merchant_slug"] = hint.MerchantSlug
	}
	if hint.AmountKobo > 0 {
		payload["amount_kobo"] = fmt.Sprintf("%d", hint.AmountKobo)
	}
	if s.WebFlowEnabled(channel, WebFlowPay) {
		return s.StartWebFlow(ctx, channel, recipient, user, session, WebFlowPay, "", "", payload)
	}
	session.State = "select_merchant"
	session.Data = map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendMerchantPicker(ctx, channel, recipient, user, query, 0)
}

// startPayIndividualFromHint routes a parsed individual instruction, seeding
// the prefill and asking only for what is missing. The KYC-L2 gate is
// identical to startPayIndividual: unapproved senders get the upgrade web flow
// (where supported) or the chat gate message.
func (s *ConversationService) startPayIndividualFromHint(ctx context.Context, channel, recipient string, user store.User, session store.Session, hint IndividualPayHint) error {
	if user.AccountLevel != "individual" || !s.userIsApprovedIndividual(ctx, user) {
		if s.WebFlowEnabled(channel, WebFlowIndividualUpgrade) {
			return s.StartWebFlow(ctx, channel, recipient, user, session, WebFlowIndividualUpgrade,
				"", "Verify profile", nil)
		}
		return s.sendText(ctx, channel, recipient,
			"You need to complete individual verification (KYC Level 2) before sending money to other individuals. Send *menu* to go back.")
	}
	data := hintSessionData(hint)
	if s.WebFlowEnabled(channel, WebFlowIndividualPay) {
		return s.StartWebFlow(ctx, channel, recipient, user, session, WebFlowIndividualPay, "", "", data)
	}
	session.State = firstMissingIndividualPayStep(data)
	session.Data = data
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.promptPayIndividualStep(ctx, channel, recipient, data)
}

// hintSessionData maps the parsed fields into the FSM/web-flow prefill keys.
func hintSessionData(h IndividualPayHint) map[string]string {
	data := map[string]string{}
	if h.RecipientPhone != "" {
		data["recipient_phone"] = h.RecipientPhone
	}
	if h.AmountKobo > 0 {
		data["amount_kobo"] = fmt.Sprintf("%d", h.AmountKobo)
	}
	if h.BankCode != "" {
		data["bank_code"] = h.BankCode
		data["bank_name"] = h.BankName
	}
	if h.AccountNumber != "" {
		data["account_number"] = h.AccountNumber
	}
	if h.Name != "" {
		data["recipient_name"] = h.Name
	}
	return data
}

// firstMissingIndividualPayStep returns the earliest step that still needs
// data. Steps are ordered phone → amount → bank → account → confirm, and each
// depends on the fields before it, so continuing from the first gap is always
// correct regardless of how the prefill arrived.
func firstMissingIndividualPayStep(data map[string]string) string {
	if strings.TrimSpace(data["recipient_phone"]) == "" {
		return "pay_individual_phone"
	}
	if strings.TrimSpace(data["amount_kobo"]) == "" {
		return "pay_individual_amount"
	}
	if strings.TrimSpace(data["bank_code"]) == "" {
		return "pay_individual_bank_code"
	}
	if strings.TrimSpace(data["account_number"]) == "" {
		return "pay_individual_account"
	}
	return "pay_individual_confirm"
}

// promptPayIndividualStep sends the prompt for the next missing field,
// reproducing the exact copy the previous hard-coded transitions used so the
// prefilled flow feels identical to the typed flow.
func (s *ConversationService) promptPayIndividualStep(ctx context.Context, channel, recipient string, data map[string]string) error {
	switch firstMissingIndividualPayStep(data) {
	case "pay_individual_phone":
		return s.sendText(ctx, channel, recipient,
			"Send money to an individual\n\nEnter the recipient's phone number (e.g. 08012345678):")
	case "pay_individual_amount":
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Recipient: %s\n\nEnter the amount in Naira (e.g. 5000):", data["recipient_phone"]))
	case "pay_individual_bank_code":
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Amount: %s\n\nEnter the recipient's bank name (e.g. Access, GTBank, Zenith) or bank code (e.g. 044):",
				domain.FormatNGN(parseAmountKobo(data["amount_kobo"]))))
	case "pay_individual_account":
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Bank: %s\n\nEnter the recipient's 10-digit bank account number:", sessionBankLabel(data)))
	case "pay_individual_confirm":
		return s.sendText(ctx, channel, recipient, s.individualPayReviewMessage(data))
	}
	return nil
}

// individualPayReviewMessage renders the confirm-step review for the current
// prefill, matching the message the typed flow has always shown.
func (s *ConversationService) individualPayReviewMessage(data map[string]string) string {
	amountKobo := parseAmountKobo(data["amount_kobo"])
	collectionFee := XegoCollectionFee(s.cfg, "transfer", amountKobo)
	nipFee := XegoPayoutFee(s.cfg, amountKobo)
	totalPay := amountKobo + collectionFee.FeeKobo
	recipientGets := amountKobo - nipFee
	return fmt.Sprintf("*Review your transfer*\n\nRecipient phone: %s\nBank: %s\nAccount: %s\n\nAmount: %s\nCollection fee: %s\nTotal you pay: %s\n\nRecipient receives: %s (after NIP fee)\n\nSend *1* to pay by bank transfer, *2* to pay from your wallet, or *cancel* to abort.",
		data["recipient_phone"], sessionBankLabel(data), data["account_number"],
		domain.FormatNGN(amountKobo), domain.FormatNGN(collectionFee.FeeKobo),
		domain.FormatNGN(totalPay), domain.FormatNGN(recipientGets))
}

// intentToHint converts AI-extracted entities into the individual-flow
// prefill. Bad values are left empty so the FSM asks for them; the amount is
// validated against the configured payment bounds.
func (s *ConversationService) intentToHint(ctx context.Context, entities map[string]string) IndividualPayHint {
	hint := IndividualPayHint{SendIntent: true}
	if p := strings.TrimSpace(entities["recipient_phone"]); p != "" {
		hint.RecipientPhone = domain.CanonicalE164Phone(p)
	}
	if acc := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(entities["account_number"])); len(acc) == 10 && isDigitsOnly(acc) {
		hint.AccountNumber = acc
	}
	if raw := strings.TrimSpace(entities["amount"]); raw != "" {
		if k, err := domain.ParseNGNAmount(raw, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo); err == nil {
			hint.AmountKobo = k
		}
	}
	bankText := strings.TrimSpace(entities["bank"])
	if bankText == "" {
		bankText = strings.TrimSpace(entities["bank_name"])
	}
	if bankText != "" {
		if res, err := ResolveBank(ctx, s.store, bankText); err == nil {
			hint.BankCode, hint.BankName = res.Code, res.Name
		}
	}
	hint.Name = strings.TrimSpace(entities["recipient_name"])
	return hint
}
