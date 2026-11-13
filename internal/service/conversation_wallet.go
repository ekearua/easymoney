package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// startWalletTopup begins the "fund wallet" flow. When Interswitch wallet mode
// is enabled, funding happens by paying into the customer's linked virtual
// account so the provider wallet (the balance authority) is credited directly;
// Xego mirrors that confirmed credit into the local ledger. Otherwise the
// customer pays through the gateway and the wallet is credited on verification.
func (s *ConversationService) startWalletTopup(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	balance := int64(0)
	if wallet, err := s.store.WalletByOwner(ctx, store.WalletOwnerUser, user.ID); err == nil {
		balance, _ = s.store.WalletBalance(ctx, wallet.ID)
	}
	if s.cfg.InterswitchWalletsEnabled {
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		account, err := s.store.StandingAccountByUser(ctx, user.ID)
		if err != nil || strings.TrimSpace(account.AccountNumber) == "" || account.ProvisioningStatus != "provisioned" {
			return s.sendText(ctx, channel, recipient,
				fmt.Sprintf("Fund your Xego wallet\n\nCurrent balance: %s\n\nYour Interswitch wallet and linked account are still being provisioned. You’ll receive an account number to fund as soon as setup is complete.", domain.FormatNGN(balance)))
		}
		bank := strings.TrimSpace(account.BankName)
		if bank == "" {
			bank = "Interswitch"
		}
		name := strings.TrimSpace(account.AccountName)
		if name == "" {
			name = strings.TrimSpace(user.DisplayName)
		}
		message := fmt.Sprintf("Fund your Xego wallet\n\nCurrent balance: %s\n\nSend any amount to this linked account and your wallet will be credited after Interswitch confirms the transfer:\n\nAccount name: %s\nAccount number: %s\nBank: %s\n\nThis account is linked to your wallet, so credits go straight to your balance.", domain.FormatNGN(balance), name, account.AccountNumber, bank)
		return s.sendText(ctx, channel, recipient, message)
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
	return s.sendWalletTopupMethods(ctx, channel, recipient, user, amount)
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
		return s.sendWalletTopupMethods(ctx, channel, recipient, user, amount)
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

func (s *ConversationService) sendWalletTopupMethods(ctx context.Context, channel, recipient string, user store.User, amount int64) error {
	// A top-up is not a merchant collection, so there is no merchant to scope the
	// habit to; the payer's standing choice orders the buttons.
	remembered := s.savedRailFor(ctx, user, uuid.Nil)
	body := fmt.Sprintf("Add %s to your Xego wallet. Choose how to pay:", domain.FormatNGN(amount))
	body += savedRailNoticeSuffix(remembered)
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: body,
		Buttons: orderMethodButtonsByPreference(remembered, []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
			{ID: "cancel_payment", Title: "Cancel"},
		}),
	})
}
