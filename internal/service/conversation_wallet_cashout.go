package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// SetWalletPayoutService wires the customer cash-out rail. Without it the
// cash-out entry points answer honestly that the feature is unavailable.
func (s *ConversationService) SetWalletPayoutService(service *WalletPayoutService) {
	s.walletPayouts = service
}

// sendWalletMenu is the second-level wallet menu behind the "My wallet" row:
// adding money and cashing out are two distinct rail directions, so the main
// menu's ten-row budget stays intact while both remain discoverable.
func (s *ConversationService) sendWalletMenu(ctx context.Context, channel, recipient string, user store.User) error {
	balance := int64(0)
	status := "Not opened yet"
	if wallet, err := s.store.WalletByOwner(ctx, store.WalletOwnerUser, user.ID); err == nil {
		balance, _ = s.store.WalletBalance(ctx, wallet.ID)
		switch wallet.Status {
		case store.WalletStatusActive:
			status = "Active"
		case store.WalletStatusFrozen:
			status = "Frozen"
		default:
			status = "Pending activation"
		}
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: fmt.Sprintf("My wallet\n\nBalance: %s\nStatus: %s\n\nChoose an action.", domain.FormatNGN(balance), status),
		Buttons: []ports.InteractiveButton{
			{ID: "wallet_add_money", Title: "Add money"},
			{ID: "wallet_cash_out", Title: "Cash out to bank"},
			{ID: "menu_main", Title: "Back to menu"},
		},
	})
}

// startWalletCashOut begins a wallet→bank transfer. The wallet must be active
// (L1+) and hold a positive balance; the money is debited only after the
// customer confirms the resolved account name and fee.
func (s *ConversationService) startWalletCashOut(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if s.walletPayouts == nil {
		return s.sendText(ctx, channel, recipient,
			"Cash-out is not available right now. Please try again later, or type MENU for other options.")
	}
	wallet, err := s.store.WalletByOwner(ctx, store.WalletOwnerUser, user.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.sendText(ctx, channel, recipient,
			"Your Faya wallet isn't open yet. Complete your profile verification first, then try again.")
	}
	if err != nil {
		return err
	}
	if wallet.Status != store.WalletStatusActive {
		return s.sendText(ctx, channel, recipient,
			"Your Faya wallet isn't active yet. Complete identity verification (Level 1) and try again.")
	}
	balance, err := s.store.WalletBalance(ctx, wallet.ID)
	if err != nil {
		return err
	}
	if balance <= 0 {
		return s.sendText(ctx, channel, recipient,
			"Your wallet balance is ₦0, so there's nothing to cash out yet. Add money first, then come back to cash out.")
	}
	session.State = "wallet_withdraw_amount"
	session.Data = map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Cash out to a bank account\n\nWallet balance: %s\n\nEnter the amount you want to cash out in Naira (e.g. 5000). The NIP fee is deducted from this amount.", domain.FormatNGN(balance)))
}

func (s *ConversationService) handleWalletCashOutAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amountKobo, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Invalid amount: %s. Enter a valid amount in Naira.", err))
	}
	if wallet, err := s.store.WalletByOwner(ctx, store.WalletOwnerUser, user.ID); err == nil {
		if balance, err := s.store.WalletBalance(ctx, wallet.ID); err == nil && amountKobo > balance {
			return s.sendText(ctx, channel, recipient,
				fmt.Sprintf("That's more than your wallet balance (%s). Enter a smaller amount or type MENU to cancel.", domain.FormatNGN(balance)))
		}
	}
	session.Data["amount_kobo"] = fmt.Sprintf("%d", amountKobo)
	session.State = "wallet_withdraw_bank"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Amount: %s\n\nEnter the destination bank name (e.g. Access, GTBank, Zenith) or bank code (e.g. 044):", domain.FormatNGN(amountKobo)))
}

func (s *ConversationService) handleWalletCashOutBank(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "paybank_page:") {
		page := parsePickerPage(strings.TrimPrefix(input, "paybank_page:"))
		return s.sendCashOutBankPicker(ctx, channel, recipient, session.Data["bank_query"], page)
	}
	if strings.HasPrefix(input, "bk:") {
		code := strings.TrimPrefix(input, "bk:")
		bank, err := s.store.BankByCode(ctx, code)
		if errors.Is(err, store.ErrBankNotFound) {
			return s.sendText(ctx, channel, recipient, "That bank is no longer available. Choose again from the list.")
		}
		if err != nil {
			return err
		}
		session.Data["bank_code"] = bank.Code
		session.Data["bank_name"] = bank.Name
		session.State = "wallet_withdraw_account"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient,
			fmt.Sprintf("Bank: %s\n\nEnter the 10-digit account number you're cashing out to:", sessionBankLabel(session.Data)))
	}
	if input == "bank_choose_other" {
		session.Data["bank_query"] = ""
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCashOutBankPicker(ctx, channel, recipient, "", 0)
	}
	if input == "" {
		return s.sendText(ctx, channel, recipient, "Enter the destination bank name (e.g. Access, GTBank, Zenith) or bank code (e.g. 044):")
	}
	resolution, err := ResolveBank(ctx, s.store, input)
	if errors.Is(err, ErrBankAmbiguous) {
		session.Data["bank_query"] = input
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCashOutBankPicker(ctx, channel, recipient, input, 0)
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
	session.State = "wallet_withdraw_account"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Bank: %s\n\nEnter the 10-digit account number you're cashing out to:", sessionBankLabel(session.Data)))
}

// sendCashOutBankPicker renders the bank directory as an interactive list. Row
// ids reuse the "bk:" convention; the withdrawal FSM owns the reply, so the
// shared prefix cannot collide with the individual-pay picker.
func (s *ConversationService) sendCashOutBankPicker(ctx context.Context, channel, recipient, query string, page int) error {
	page = normalizePickerPage(page)
	query = strings.TrimSpace(query)
	banks, err := s.store.SearchBanks(ctx, query, 100)
	if err != nil {
		return err
	}
	if len(banks) == 0 {
		if query == "" {
			return s.sendText(ctx, channel, recipient, "Please type the destination bank name (e.g. Access, GTBank, Zenith) or code (e.g. 044).")
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
	body := "Choose the bank you're cashing out to, or type another bank name to search again."
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

func (s *ConversationService) handleWalletCashOutAccount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	account := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(input))
	if len(account) != 10 || !isDigitsOnly(account) {
		return s.sendText(ctx, channel, recipient, "Account number must be exactly 10 digits. Try again.")
	}
	if s.walletPayouts == nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Cash-out is not available right now.")
	}
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	quote, err := s.walletPayouts.Quote(ctx, amountKobo, session.Data["bank_code"], account)
	if err != nil {
		if msg, ok := AllowanceMessage(err); ok {
			return s.sendText(ctx, channel, recipient, msg)
		}
		return s.sendText(ctx, channel, recipient,
			"We couldn't verify that account with the bank. Check the account number and try again, or send *menu* to cancel.")
	}
	session.Data["account_number"] = account
	session.Data["account_name"] = quote.AccountName
	session.Data["fee_kobo"] = fmt.Sprintf("%d", quote.FeeKobo)
	session.Data["payout_kobo"] = fmt.Sprintf("%d", quote.PayoutKobo)
	session.State = "wallet_withdraw_confirm"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("*Confirm cash-out*\n\nTo: %s\nBank: %s\nAccount: %s\n\nAmount debited from wallet: %s\nNIP fee: %s\nAmount sent to bank: %s\n\nCashing out debits your Faya wallet immediately. Cash out now?",
			quote.AccountName, sessionBankLabel(session.Data), account,
			domain.FormatNGN(amountKobo), domain.FormatNGN(quote.FeeKobo), domain.FormatNGN(quote.PayoutKobo)),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_cashout", Title: "Cash out"},
			{ID: "cancel_cashout", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleWalletCashOutConfirm(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "cancel_cashout", "cancel", "menu":
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Cash-out cancelled. Nothing was debited. Send *menu* for more options.")
	case "confirm_cashout", "1", "confirm", "yes", "cash out":
		// proceed
	default:
		return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
			To:   recipient,
			Body: "Tap Cash out to send the money to that account, or Cancel to abort.",
			Buttons: []ports.InteractiveButton{
				{ID: "confirm_cashout", Title: "Cash out"},
				{ID: "cancel_cashout", Title: "Cancel"},
			},
		})
	}
	if s.walletPayouts == nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Cash-out is not available right now.")
	}
	amountKobo := parseAmountKobo(session.Data["amount_kobo"])
	payout, err := s.walletPayouts.Request(ctx, user.ID, WalletPayoutRequest{
		AmountKobo:    amountKobo,
		BankCode:      session.Data["bank_code"],
		BankName:      session.Data["bank_name"],
		AccountNumber: session.Data["account_number"],
		AccountName:   session.Data["account_name"],
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrWalletCashOutUnderReview):
			return s.resetWithMessage(ctx, channel, recipient, user, session,
				"This cash-out is under compliance review. No money has left your wallet; our team will contact you if anything else is needed.")
		case errors.Is(err, store.ErrInsufficientWalletBalance):
			return s.resetWithMessage(ctx, channel, recipient, user, session,
				"Your wallet balance is too low for this cash-out. Nothing was debited. Send *menu* to try a smaller amount.")
		case errors.Is(err, store.ErrWalletNotActive):
			return s.resetWithMessage(ctx, channel, recipient, user, session,
				"Your wallet isn't active for money-out yet. Complete verification and try again.")
		}
		if msg, ok := AllowanceMessage(err); ok {
			return s.resetWithMessage(ctx, channel, recipient, user, session, msg)
		}
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			"We couldn't start the cash-out: "+err.Error())
	}

	reference := payout.ExternalRef
	if reference == "" {
		reference = payout.ID.String()[:8]
	}
	target := fmt.Sprintf("%s (%s) acct %s", payout.AccountName, sessionBankLabel(map[string]string{
		"bank_code": payout.BankCode, "bank_name": payout.BankName,
	}), payout.AccountNumber)
	switch payout.Status {
	case store.WalletPayoutCompleted:
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Cash-out successful.\n\n%s sent to %s.\nReference: %s\n\nYour wallet has been debited %s (including the %s NIP fee).",
				domain.FormatNGN(payout.PayoutKobo), target, reference,
				domain.FormatNGN(payout.AmountKobo), domain.FormatNGN(payout.FeeKobo)))
	case store.WalletPayoutFailed:
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("We couldn't complete the bank transfer right now.\n\nYour money is safe: we'll retry automatically and return it to your wallet if the transfer still can't go through.\nReference: %s", reference))
	default:
		return s.resetWithMessage(ctx, channel, recipient, user, session,
			fmt.Sprintf("Cash-out is processing.\n\nWe'll send %s to %s as soon as the bank confirms.\nReference: %s",
				domain.FormatNGN(payout.PayoutKobo), target, reference))
	}
}
