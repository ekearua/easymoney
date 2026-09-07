package service

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func (s *ConversationService) handleSessionSwitchConfirm(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "switch_yes", "yes", "switch", "confirm":
		pendingType := session.Data["pending_type"]
		pendingCommand := session.Data["pending_command"]
		prevState := session.Data["prev_state"]
		prevDataRaw := session.Data["prev_data"]
		var prevData map[string]string
		if prevDataRaw != "" {
			_ = json.Unmarshal([]byte(prevDataRaw), &prevData)
		}
		if prevState != "" {
			oldSession := store.Session{UserID: user.ID, State: prevState, Data: prevData}
			s.abandonSessionPayment(ctx, user, oldSession)
		}
		// "Back to main menu" means cancel the active session outright.
		if pendingType == "menu" {
			session.State, session.Data = "menu", map[string]string{}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.sendMenu(ctx, channel, recipient, user)
		}
		// Route the chosen service through handleMenu, the single source of
		// truth for both free-text commands and interactive row selections.
		if strings.TrimSpace(pendingCommand) != "" {
			session.State, session.Data = "menu", map[string]string{}
			if err := s.saveSession(ctx, session); err != nil {
				return err
			}
			return s.handleMenu(ctx, channel, recipient, user, session, pendingCommand)
		}
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That session expired. Please start again.")
	case "switch_no", "no", "continue", "stay":
		prevState := session.Data["prev_state"]
		prevDataRaw := session.Data["prev_data"]
		if prevState == "" {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That session expired. Please start again.")
		}
		var prevData map[string]string
		if prevDataRaw != "" {
			_ = json.Unmarshal([]byte(prevDataRaw), &prevData)
		}
		session.State = prevState
		session.Data = prevData
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.redispatchToState(ctx, channel, recipient, user, session)
	default:
		return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
			To:   recipient,
			Body: "Choose Switch to change what you're doing, or Continue current to stay on your current payment.",
			Buttons: []ports.InteractiveButton{
				{ID: "switch_yes", Title: "Switch"},
				{ID: "switch_no", Title: "Continue current"},
			},
		})
	}
}

func (s *ConversationService) handleConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_payment" && !strings.EqualFold(input, "confirm") && !strings.EqualFold(input, "continue") {
		return s.sendText(ctx, channel, recipient, "Choose Continue or Cancel to proceed.")
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That payment session expired. Please start again.")
	}
	// W1: wallet-funded payments are confirmed inline — the wallet is debited
	// and the payment succeeds immediately, with no hosted checkout. On a
	// failure the session is kept so the customer can retry or choose another
	// method from the menu.
	if payment.Provider == ProviderWallet {
		_, changed, err := s.payments.ConfirmWalletPayment(ctx, payment)
		if err != nil {
			if errors.Is(err, store.ErrInsufficientWalletBalance) {
				return s.sendText(ctx, channel, recipient,
					fmt.Sprintf("Your wallet balance is too low for this payment (%s).\n\nTop up your wallet, then tap Continue to try again - or type MENU to choose another payment method.", domain.FormatNGN(payment.AmountKobo)))
			}
			if errors.Is(err, store.ErrWalletNotActive) {
				return s.sendText(ctx, channel, recipient,
					"Wallet payments need an active wallet. Confirm your account to reach level L1 and activate your wallet - or type MENU to choose another payment method.")
			}
			slog.Error("wallet payment confirmation failed", "payment_id", payment.ID, "error", err)
			return s.sendText(ctx, channel, recipient, "The wallet payment couldn't be completed. Tap Continue to retry, or type MENU to return to the menu.")
		}
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		_ = changed
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("✅ Paid from your Xego wallet.\n\nMerchant: %s\nAmount: %s\n\nReceipt: %s/receipts/%s",
				payment.MerchantName, domain.FormatNGN(payment.AmountKobo), s.cfg.BaseURL, payment.ReceiptToken))
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendCheckout(ctx, channel, recipient,
		fmt.Sprintf("Your secure checkout is ready.\n\nMerchant: %s\nAmount: %s\n\nXego will verify the result before issuing your receipt.", payment.MerchantName, domain.FormatNGN(payment.AmountKobo)),
		s.payments.HostedCheckoutURL(payment))
}

func (s *ConversationService) sendAccountConfirmation(ctx context.Context, channel, recipient string) error {
	label := "WhatsApp number"
	if channel == ChannelTelegram {
		label = "Telegram account"
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: fmt.Sprintf("Xego will use this %s as your account identity.\n\nConfirm this account?", label),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_account", Title: "Confirm"},
			{ID: "change_email", Title: "Change email"},
			{ID: "cancel_account", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) sendLatestStatus(ctx context.Context, channel, recipient string, user store.User) error {
	payments, err := s.store.RecentPaymentsForUser(ctx, user.ID, 1)
	if err != nil {
		return err
	}
	if len(payments) == 0 {
		return s.sendText(ctx, channel, recipient, "You don't have any Xego payments yet. Choose Make payment to try one.")
	}
	payment := payments[0]
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Latest Xego payment\n\nMerchant: %s\nAmount: %s\nStatus: %s\nReceipt/status: %s/receipts/%s",
			payment.MerchantName, domain.FormatNGN(payment.AmountKobo), strings.ToUpper(string(payment.Status)),
			s.cfg.BaseURL, payment.ReceiptToken))
}

func (s *ConversationService) sendHistory(ctx context.Context, channel, recipient string, user store.User) error {
	payments, err := s.store.RecentPaymentsForUser(ctx, user.ID, 5)
	if err != nil {
		return err
	}
	if len(payments) == 0 {
		return s.sendText(ctx, channel, recipient, "You don't have any Xego payments yet.")
	}
	lines := []string{"Your recent Xego payments:"}
	for _, payment := range payments {
		lines = append(lines, fmt.Sprintf("• %s — %s — %s", payment.MerchantName, domain.FormatNGN(payment.AmountKobo), strings.ToUpper(string(payment.Status))))
	}
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func (s *ConversationService) rejectedPhoneMessage() string {
	numbers := s.AcceptedInvoiceNumbers()
	if len(numbers) == 0 {
		return "That phone number is not accepted for invoices."
	}
	var b strings.Builder
	b.WriteString("That phone number is not on the accepted list. Allowed numbers:\n")
	for _, n := range numbers {
		b.WriteString(n)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *ConversationService) resetWithMessage(ctx context.Context, channel, recipient string, user store.User, session store.Session, body string) error {
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, body)
}

func (s *ConversationService) sessionMerchantAndAmount(ctx context.Context, session store.Session) (store.Merchant, int64, error) {
	merchant, err := s.store.MerchantBySlug(ctx, session.Data["merchant_slug"])
	if err != nil {
		return store.Merchant{}, 0, err
	}
	amount, err := strconv.ParseInt(session.Data["amount_kobo"], 10, 64)
	if err != nil || amount <= 0 {
		return store.Merchant{}, 0, fmt.Errorf("invalid session amount")
	}
	return merchant, amount, nil
}

func (s *ConversationService) paymentFromSession(ctx context.Context, user store.User, session store.Session) (store.PaymentView, error) {
	paymentID, err := uuid.Parse(session.Data["payment_id"])
	if err != nil {
		return store.PaymentView{}, err
	}
	payment, err := s.store.PaymentByID(ctx, paymentID)
	if err != nil {
		return store.PaymentView{}, err
	}
	if payment.UserID != user.ID {
		return store.PaymentView{}, fmt.Errorf("payment does not belong to user")
	}
	return payment, nil
}

func (s *ConversationService) thriftContributionFromSession(ctx context.Context, session store.Session) (store.ThriftContributionView, error) {
	id, err := uuid.Parse(session.Data["thrift_contribution_id"])
	if err != nil {
		return store.ThriftContributionView{}, err
	}
	return s.store.ThriftContributionByID(ctx, id)
}

func (s *ConversationService) abandonSessionPayment(ctx context.Context, user store.User, session store.Session) {
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		if raw := session.Data["prev_data"]; raw != "" {
			var prevData map[string]string
			if json.Unmarshal([]byte(raw), &prevData) == nil {
				if pid, ok := prevData["payment_id"]; ok {
					if id, pErr := uuid.Parse(pid); pErr == nil {
						if p, fErr := s.store.PaymentByID(ctx, id); fErr == nil && p.UserID == user.ID {
							switch p.Status {
							case domain.StatusDraft, domain.StatusAwaitingConfirmation, domain.StatusInitialized, domain.StatusPending:
								_, _ = s.store.TransitionPayment(ctx, p.ID, domain.StatusAbandoned, "conversation.cancel", map[string]any{"reason": "customer_cancelled"})
							}
						}
					}
				}
			}
		}
		return
	}
	switch payment.Status {
	case domain.StatusDraft, domain.StatusAwaitingConfirmation, domain.StatusInitialized, domain.StatusPending:
		_, _ = s.store.TransitionPayment(ctx, payment.ID, domain.StatusAbandoned, "conversation.cancel", map[string]any{"reason": "customer_cancelled"})
	}
}

func appendPickerNavigation(rows []ports.InteractiveRow, prefix string, page int, hasMore bool) []ports.InteractiveRow {
	if page > 0 {
		rows = append(rows, ports.InteractiveRow{
			ID:          prefix + strconv.Itoa(page-1),
			Title:       "Previous page",
			Description: "Show earlier options",
		})
	}
	if hasMore {
		rows = append(rows, ports.InteractiveRow{
			ID:          prefix + strconv.Itoa(page+1),
			Title:       "Next page",
			Description: "Show more options",
		})
	}
	return rows
}

func parsePickerPage(value string) int {
	page, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return normalizePickerPage(page)
}

func normalizePickerPage(page int) int {
	if page < 0 {
		return 0
	}
	return page
}

func newEmailCode() (string, error) {
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

// emailCodeDigest returns the keyed digest used to verify a submitted
// confirmation code against its stored bcrypt hash.
func emailCodeDigest(email, code string) []byte {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)) + ":" + normalizeEmailCode(code)))
	return digest[:]
}

// emailCodeHash returns a salted, slow hash of the email-bound confirmation
// code. bcrypt embeds its own per-code salt, so equal codes for different
// users yield different hashes and offline brute force of a leaked database
// is not practical.
func emailCodeHash(email, code string) ([]byte, error) {
	return bcrypt.GenerateFromPassword(emailCodeDigest(email, code), bcrypt.DefaultCost)
}

func normalizeEmailCode(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func truncateInteractiveTitle(value string) string {
	return truncateRunes(strings.TrimSpace(value), 24)
}

func truncateInteractiveDescription(value string) string {
	return truncateRunes(strings.TrimSpace(value), 72)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "\u2026"
}

// sessionSwitchable reports whether the current session is an active flow
// that a new service request should ask about switching from. The menu and a
// blank state are idle, and confirm_session_switch is already prompting.
func sessionSwitchable(state string) bool {
	switch state {
	case "", "menu", "confirm_session_switch":
		return false
	default:
		return true
	}
}

// serviceSwitchIntent detects an input that starts a distinct Xego service
// while another session is active. It mirrors handleMenu's vocabulary for
// both free-text commands and interactive row selections; the returned type
// distinguishes "back to main menu" (a cancel) from a new service. The actual
// routing stays in handleMenu so detection and routing can never drift.
func serviceSwitchIntent(input string) (string, string) {
	lower := strings.ToLower(strings.TrimSpace(input))
	switch lower {
	case "pay", "make payment", "menu_pay":
		return "pay", ""
	case "pay individual", "send money", "menu_pay_individual":
		return "individual_pay", ""
	case "fund wallet", "top up", "wallet top up", "add money", "menu_fund_wallet":
		return "topup", ""
	case "buy data", "data", "menu_buy_data":
		return "data", ""
	case "register merchant", "merchant registration", "menu_register_merchant":
		return "merchant_register", ""
	case "generate invoice", "create invoice", "menu_generate_invoice":
		return "invoice_create", ""
	case "become individual", "individual", "menu_become_individual", "verify id":
		return "individual_upgrade", ""
	case "create thrift", "menu_create_thrift":
		return "thrift_create", ""
	case "join thrift", "menu_join_thrift":
		return "thrift_join", ""
	case "thrift dashboard", "menu_thrift_dashboard":
		return "thrift_dashboard", ""
	case "thrift contributions", "menu_thrift_services":
		return "thrift_contributions", ""
	case "edit thrift", "menu_edit_thrift":
		return "thrift_edit", ""
	case "merchant services", "menu_merchant_services":
		return "merchant_services", ""
	case "merchant dashboard", "menu_merchant_dashboard":
		return "merchant_dashboard", ""
	case "kyb status", "menu_kyb_status":
		return "kyb_status", ""
	case "request kyb upgrade", "request upgrade", "menu_kyb_request":
		return "kyb_request", ""
	case "history", "recent transactions", "menu_history":
		return "history", ""
	case "my limits", "limits", "menu_my_limits":
		return "limits", ""
	case "ask xego", "ai", "assistant", "menu_ai":
		return "ai", ""
	case "complete profile", "profile", "menu_profile":
		return "profile", ""
	case "status", "check payment status", "menu_status":
		return "status", ""
	case "help", "menu_help":
		return "help", ""
	case "menu_main", "main menu", "back":
		return "menu", ""
	}
	if ref, ok := invoiceReferenceFromPAY(input); ok {
		return "invoice_payment", ref
	}
	if strings.HasPrefix(lower, "pay_invoice:") {
		ref := strings.TrimSpace(input[len("pay_invoice:"):])
		if ref != "" {
			return "invoice_payment", ref
		}
	}
	if code, ok := thriftContributeNameFromInput(input); ok {
		return "thrift_contribution", code
	}
	if code, ok := thriftJoinNameFromInput(input); ok {
		return "thrift_join", code
	}
	if code, ok := thriftActivateNameFromInput(input); ok {
		return "thrift_activate", code
	}
	if strings.HasPrefix(lower, "thrift_select:") {
		if name := strings.TrimSpace(input[len("thrift_select:"):]); name != "" {
			return "thrift_dashboard", name
		}
	}
	if strings.HasPrefix(lower, "thrift_edit:") {
		if name := strings.TrimSpace(input[len("thrift_edit:"):]); name != "" {
			return "thrift_edit", name
		}
	}
	return "", ""
}

func describeCurrentFlow(session store.Session) string {
	switch session.State {
	case "select_payment_method":
		merchant := session.Data["merchant_slug"]
		amount := session.Data["amount_kobo"]
		if amount != "" {
			if kobo, err := strconv.ParseInt(amount, 10, 64); err == nil {
				return fmt.Sprintf("selecting payment method for %s to %s", domain.FormatNGN(kobo), merchant)
			}
		}
		return fmt.Sprintf("selecting a payment method for %s", merchant)
	case "confirm_payment":
		merchant := session.Data["merchant_slug"]
		amount := session.Data["amount_kobo"]
		if amount != "" {
			if kobo, err := strconv.ParseInt(amount, 10, 64); err == nil {
				return fmt.Sprintf("confirming card payment of %s to %s", domain.FormatNGN(kobo), merchant)
			}
		}
		return fmt.Sprintf("confirming a card payment to %s", merchant)
	case "select_transfer_bank":
		merchant := session.Data["merchant_slug"]
		amount := session.Data["amount_kobo"]
		if amount != "" {
			if kobo, err := strconv.ParseInt(amount, 10, 64); err == nil {
				return fmt.Sprintf("selecting a bank for %s transfer to %s", domain.FormatNGN(kobo), merchant)
			}
		}
		return fmt.Sprintf("selecting a bank for transfer to %s", merchant)
	case "await_bank_transfer":
		merchant := session.Data["merchant_slug"]
		amount := session.Data["amount_kobo"]
		if amount != "" {
			if kobo, err := strconv.ParseInt(amount, 10, 64); err == nil {
				return fmt.Sprintf("waiting for your bank transfer of %s to %s", domain.FormatNGN(kobo), merchant)
			}
		}
		return fmt.Sprintf("waiting for your bank transfer to %s", merchant)
	case "invoice_pay_amount", "invoice_pay_method", "invoice_pay_bank", "await_invoice_bank_transfer":
		ref := session.Data["invoice_reference"]
		if ref != "" {
			return fmt.Sprintf("paying invoice %s", ref)
		}
		return "paying an invoice"
	case "thrift_pay_method", "thrift_pay_bank", "await_thrift_bank_transfer":
		name := session.Data["thrift_name"]
		if name != "" {
			return fmt.Sprintf("paying a thrift contribution to %s", name)
		}
		return "paying a thrift contribution"
	case "select_data_payment_method", "select_data_transfer_bank", "await_data_bank_transfer":
		return "purchasing mobile data"
	case "pay_individual_method", "await_individual_bank_transfer", "await_individual_payment":
		phone := session.Data["recipient_phone"]
		if phone != "" {
			return fmt.Sprintf("sending money to %s", phone)
		}
		return "sending money to an individual"
	case "wallet_topup_amount", "wallet_topup_method":
		return "funding the Xego wallet"
	default:
		return flowStateLabel(session.State)
	}
}

// flowStateLabel gives a readable, terse description of an active FSM state
// for the session-switch prompt. It covers every dispatched state; unknown
// states fall back to a generic phrase.
func flowStateLabel(state string) string {
	switch {
	case strings.HasPrefix(state, "select_merchant"), strings.HasPrefix(state, "select_service"),
		strings.HasPrefix(state, "select_event"), strings.HasPrefix(state, "enter_amount"),
		strings.HasPrefix(state, "enter_service"), strings.HasPrefix(state, "collect_custom"),
		strings.HasPrefix(state, "confirm_service"):
		return "selecting a merchant and amount to pay"
	case strings.HasPrefix(state, "pay_individual_"):
		return "sending money to an individual"
	case strings.HasPrefix(state, "onboard"):
		return "setting up your profile"
	case strings.HasPrefix(state, "individual_"):
		return "verifying your individual profile"
	case strings.HasPrefix(state, "merchant_register"):
		return "registering your business"
	case strings.HasPrefix(state, "thrift_"):
		return "setting up or managing a thrift group"
	case strings.HasPrefix(state, "invoice_"):
		return "creating an invoice"
	case strings.HasPrefix(state, "invoice_pay"):
		return "paying an invoice"
	case strings.HasPrefix(state, "select_data"), strings.HasPrefix(state, "enter_data"),
		strings.HasPrefix(state, "confirm_data"), strings.HasPrefix(state, "await_data"):
		return "buying mobile data"
	case strings.HasPrefix(state, "kyb_request"):
		return "requesting a business tier upgrade"
	case state == "web_flow_active":
		return "completing a request in your browser"
	case state == "ai_assistant":
		return "chatting with the Xego assistant"
	default:
		return "in a payment flow"
	}
}

func (s *ConversationService) redispatchToState(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	switch session.State {
	case "select_payment_method":
		merchant, amount, err := s.sessionMerchantAndAmount(ctx, session)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That payment session expired. Please start again.")
		}
		return s.sendPaymentMethods(ctx, channel, recipient, merchant, amount)
	case "confirm_payment":
		merchant, amount, err := s.sessionMerchantAndAmount(ctx, session)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That payment session expired. Please start again.")
		}
		return s.sendCardReview(ctx, channel, recipient, merchant, amount)
	case "select_transfer_bank":
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	case "await_bank_transfer":
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That transfer session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That transfer session expired. Please start again.")
		}
		return s.sendBankTransferInstructions(ctx, channel, recipient, payment, instruction)
	case "invoice_pay_amount":
		invoice, err := s.store.InvoiceByReference(ctx, session.Data["invoice_reference"])
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That invoice session expired. Please start again.")
		}
		remaining := invoice.TotalKobo - invoice.AmountPaidKobo
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Invoice %s\n\nMerchant: %s\nTotal: %s\nPaid so far: %s\nRemaining: %s\n\nHow much would you like to pay now?\nSend FULL to pay the remaining balance, or enter a naira amount for a split/partial payment.",
				invoice.Reference, invoice.MerchantName, domain.FormatNGN(invoice.TotalKobo), domain.FormatNGN(invoice.AmountPaidKobo), domain.FormatNGN(remaining)))
	case "invoice_pay_method":
		invoice, err := s.store.InvoiceByReference(ctx, session.Data["invoice_reference"])
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That invoice session expired. Please start again.")
		}
		amount, _ := strconv.ParseInt(session.Data["invoice_pay_amount_kobo"], 10, 64)
		return s.sendInvoicePayMethods(ctx, channel, recipient, invoice, amount)
	case "invoice_pay_bank":
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	case "await_invoice_bank_transfer":
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That transfer session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That transfer session expired. Please start again.")
		}
		return s.sendBankTransferInstructions(ctx, channel, recipient, payment, instruction)
	case "thrift_pay_method":
		contribution, err := s.thriftContributionFromSession(ctx, session)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That thrift session expired. Please start again.")
		}
		return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
			To: recipient,
			Body: fmt.Sprintf("Pay thrift contribution\n\nGroup: %s\nCycle: %d\nAmount: %s\n\nChoose a payment method.",
				contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(contribution.AmountKobo)),
			Buttons: []ports.InteractiveButton{
				{ID: "method_card", Title: "Card checkout"},
				{ID: "method_bank_transfer", Title: "Bank transfer"},
				{ID: "method_wallet", Title: "Pay from wallet"},
				{ID: "cancel_payment", Title: "Cancel"},
			},
		})
	case "thrift_pay_bank":
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	case "await_thrift_bank_transfer":
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That transfer session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.sendText(ctx, channel, recipient, "That transfer session expired. Please start again.")
		}
		return s.sendBankTransferInstructions(ctx, channel, recipient, payment, instruction)
	case "pay_individual_method":
		return s.sendText(ctx, channel, recipient, "Individual payments use bank transfer or your wallet. Send *bank transfer* or *wallet* to proceed.")
	case "await_individual_bank_transfer":
		return s.sendText(ctx, channel, recipient, "Waiting for your bank transfer confirmation. Send *confirm* when done.")
	case "await_individual_payment":
		return s.sendText(ctx, channel, recipient, "Your payment is being processed. We'll notify you when it's complete.")
	case "wallet_topup_amount":
		return s.startWalletTopup(ctx, channel, recipient, user, session)
	case "wallet_topup_method":
		amount, err := strconv.ParseInt(session.Data["amount_kobo"], 10, 64)
		if err != nil || amount <= 0 {
			return s.sendText(ctx, channel, recipient, "That top-up session expired. Please start again.")
		}
		return s.sendWalletTopupMethods(ctx, channel, recipient, amount)
	default:
		// State not backed by a re-renderable screen: the prompt already told
		// the customer what they were doing, so just confirm the session is
		// still active rather than falsely reporting it expired.
		return s.sendText(ctx, channel, recipient, "OK, staying on what you were doing. Type MENU to cancel it anytime.")
	}
}
