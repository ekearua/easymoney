package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// startWalletTopup begins the "fund wallet" flow. The customer pays an amount
// through the gateway (card or bank transfer, both on the secure checkout);
// once the payment is verified the wallet is credited with the full amount.
func (s *ConversationService) startWalletTopup(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	balance := int64(0)
	if wallet, err := s.store.WalletByOwner(ctx, store.WalletOwnerUser, user.ID); err == nil {
		balance, _ = s.store.WalletBalance(ctx, wallet.ID)
	}
	session.State = "wallet_topup_amount"
	session.Data = map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient,
		fmt.Sprintf("Fund your Xego wallet\n\nCurrent balance: %s\n\nEnter the amount in Naira you'd like to add (e.g. 5000). Your wallet is credited in full as soon as the payment is verified.", domain.FormatNGN(balance)))
}

func (s *ConversationService) handleWalletTopupAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amount, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Invalid amount: %s. Enter a valid amount in Naira.", err))
	}
	session.Data["amount_kobo"] = strconv.FormatInt(amount, 10)
	session.State = "wallet_topup_method"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendWalletTopupMethods(ctx, channel, recipient, amount)
}

func (s *ConversationService) handleWalletTopupMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amount, err := strconv.ParseInt(session.Data["amount_kobo"], 10, 64)
	if err != nil || amount <= 0 {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That top-up session expired. Please start again.")
	}
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "cancel_payment", "cancel", "menu":
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Top-up cancelled. Send *menu* for more options.")
	case "method_card", "card", "card checkout":
		return s.initWalletTopup(ctx, channel, recipient, user, session, amount, ProviderInterswitch, "Card checkout")
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		return s.initWalletTopup(ctx, channel, recipient, user, session, amount, ProviderBankTransfer, "Bank transfer")
	default:
		return s.sendWalletTopupMethods(ctx, channel, recipient, amount)
	}
}

// initWalletTopup mints the top-up draft and sends the customer to the secure
// checkout. The payment is a plain gateway collection against the Xego system
// merchant; the wallet credit is applied by the post-success hook once the
// gateway verifies it.
func (s *ConversationService) initWalletTopup(ctx context.Context, channel, recipient string, user store.User, session store.Session, amountKobo int64, provider, railName string) error {
	payment, err := s.payments.CreateWalletTopupDraft(ctx, user, channel, recipient, amountKobo, provider)
	if err != nil {
		return friendlyAllowanceErr(err)
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendCheckout(ctx, channel, recipient,
		fmt.Sprintf("Your secure checkout is ready.\n\nTop-up amount: %s\nMethod: %s\n\nYour Xego wallet will be credited %s as soon as Xego verifies the payment.", domain.FormatNGN(amountKobo), railName, domain.FormatNGN(amountKobo)),
		s.payments.HostedCheckoutURL(payment))
}

func (s *ConversationService) sendWalletTopupMethods(ctx context.Context, channel, recipient string, amount int64) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: fmt.Sprintf("Add %s to your Xego wallet. Choose how to pay:", domain.FormatNGN(amount)),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}
