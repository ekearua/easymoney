package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/store"
)

// MyDetailsLine is one label/value row on the "My details" page and chat block.
type MyDetailsLine struct {
	Term string
	Desc string
}

// MyDetailsMerchant is one merchant's contribution to the details summary.
type MyDetailsMerchant struct {
	Heading string
	Lines   []MyDetailsLine
}

// MyDetailsSummary is the structured account summary backing both the chat
// "My details" block (Telegram and other non-web channels) and the browser
// page (WhatsApp/Instagram), so both use the same data and copy.
type MyDetailsSummary struct {
	Name          string
	Phone         string
	Email         string
	Tier          string
	AccountLevel  string
	WalletStatus  string
	WalletBalance string
	WalletNote    string
	PayingLines   []MyDetailsLine
	Merchants     []MyDetailsMerchant
	Payments      []MyDetailsLine
	Note          string
}

// MyDetailsSummaryOf loads the account details shown by "My details". Each
// section degrades independently so a single store hiccup never blanks the
// whole page; callers without a KYC profile or merchant records still get a
// useful summary.
func (s *ConversationService) MyDetailsSummaryOf(ctx context.Context, user store.User) MyDetailsSummary {
	summary := MyDetailsSummary{
		Name:         user.DisplayName,
		Phone:        user.WhatsAppNumber,
		Email:        user.Email,
		AccountLevel: user.AccountLevel,
		Tier:         kyc.TierL0,
	}
	if strings.TrimSpace(summary.AccountLevel) == "" {
		summary.AccountLevel = "consumer"
	}
	if profile, err := s.store.EnsureKYCProfile(ctx, user.ID); err == nil {
		summary.Tier = profile.Tier
	}
	wallet, err := s.store.WalletByOwner(ctx, store.WalletOwnerUser, user.ID)
	switch {
	case err == nil:
		summary.WalletStatus = wallet.Status
		if wallet.Status == store.WalletStatusActive {
			if balance, err := s.store.WalletBalance(ctx, wallet.ID); err == nil {
				summary.WalletBalance = domain.FormatNGN(balance)
			}
		}
		if wallet.Status == store.WalletStatusPending {
			summary.WalletNote = "pending — it activates automatically when your account reaches Level 1"
		}
	case errors.Is(err, pgx.ErrNoRows):
		summary.WalletStatus = "none"
	}
	summary.PayingLines = s.individualLimitData(ctx, user.ID, summary.Tier)
	if merchants, err := s.store.ApprovedMerchantsForUser(ctx, user.ID); err == nil {
		for _, merchant := range merchants {
			if group, ok := s.merchantLimitData(ctx, merchant.ID, merchant.Name); ok {
				summary.Merchants = append(summary.Merchants, group)
			}
		}
	}
	if payments, err := s.store.RecentPaymentsForUser(ctx, user.ID, 5); err == nil {
		for _, payment := range payments {
			summary.Payments = append(summary.Payments, MyDetailsLine{
				Term: "Recent payment",
				Desc: fmt.Sprintf("%s — %s — %s", payment.MerchantName, domain.FormatNGN(payment.AmountKobo), strings.ToUpper(string(payment.Status))),
			})
		}
	}
	summary.Note = "No standing account number — each payment gets its own virtual account number."
	return summary
}

// individualLimitData returns the payer-facing money-in limits and current
// usage as label/value rows shared by chat and the details web page.
func (s *ConversationService) individualLimitData(ctx context.Context, userID uuid.UUID, tier string) []MyDetailsLine {
	now := time.Now()
	limits, err := s.store.TierLimits(ctx, store.AccountIndividual, tier, kyc.DirIn)
	if err != nil {
		return []MyDetailsLine{{Term: "Paying limits", Desc: "could not be loaded right now."}}
	}
	usedDay, err := s.store.AllowanceUsage(ctx, store.AccountIndividual, userID, kyc.DirIn, kyc.DayWindowStart(now))
	if err != nil {
		return []MyDetailsLine{{Term: "Paying limits", Desc: "could not be loaded right now."}}
	}
	usedMonth, err := s.store.AllowanceUsage(ctx, store.AccountIndividual, userID, kyc.DirIn, kyc.MonthWindowStart(now))
	if err != nil {
		return []MyDetailsLine{{Term: "Paying limits", Desc: "could not be loaded right now."}}
	}
	return []MyDetailsLine{
		{Term: "Per payment", Desc: domain.FormatNGN(limits.SingleLimitKobo)},
		{Term: "Per day", Desc: domain.FormatNGN(limits.DailyLimitKobo)},
		{Term: "Per month", Desc: domain.FormatNGN(limits.MonthlyLimitKobo)},
		{Term: "Used today", Desc: fmt.Sprintf("%s of %s (left %s)", domain.FormatNGN(usedDay), domain.FormatNGN(limits.DailyLimitKobo), domain.FormatNGN(kyc.RemainingDaily(limits, usedDay)))},
		{Term: "Used this month", Desc: fmt.Sprintf("%s of %s (left %s)", domain.FormatNGN(usedMonth), domain.FormatNGN(limits.MonthlyLimitKobo), domain.FormatNGN(kyc.RemainingMonthly(limits, usedMonth)))},
	}
}

// merchantLimitData renders one merchant's KYB tier and payout ceilings as
// label/value rows shared by chat and the details web page.
func (s *ConversationService) merchantLimitData(ctx context.Context, merchantID uuid.UUID, merchantName string) (MyDetailsMerchant, bool) {
	profile, err := s.store.EnsureKYBProfile(ctx, merchantID)
	if err != nil {
		return MyDetailsMerchant{}, false
	}
	limits, err := s.store.TierLimits(ctx, store.AccountBusiness, profile.Tier, kyc.DirOut)
	if err != nil {
		return MyDetailsMerchant{}, false
	}
	usedMonth, err := s.store.AllowanceUsage(ctx, store.AccountBusiness, merchantID, kyc.DirOut, kyc.MonthWindowStart(time.Now()))
	if err != nil {
		return MyDetailsMerchant{}, false
	}
	review := profile.ReviewStatus
	if review == "" {
		review = "none"
	}
	return MyDetailsMerchant{
		Heading: fmt.Sprintf("%s — KYB tier %s (review: %s)", merchantName, profile.Tier, review),
		Lines: []MyDetailsLine{
			{Term: "Per payout", Desc: domain.FormatNGN(limits.SingleLimitKobo)},
			{Term: "Per day", Desc: domain.FormatNGN(limits.DailyLimitKobo)},
			{Term: "Per month", Desc: domain.FormatNGN(limits.MonthlyLimitKobo)},
			{Term: "Used this month", Desc: fmt.Sprintf("%s (%.1f%%)", domain.FormatNGN(usedMonth), pctOf(usedMonth, limits.MonthlyLimitKobo))},
		},
	}, true
}

// handleMyDetails shows the caller's account, wallet, limits, and recent
// payments. WhatsApp/Instagram get a browser page ("My details"); Telegram and
// TikTok keep the full chat block because their messengers have no web flow.
func (s *ConversationService) handleMyDetails(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if s.WebFlowEnabled(channel, WebFlowMyDetails) {
		return s.startMyDetailsWebFlow(ctx, channel, recipient, user)
	}
	return s.sendMyDetailsChat(ctx, channel, recipient, user)
}

// startMyDetailsWebFlow is message 1 of the details web flow. Unlike StartWebFlow
// it does NOT park the chat session: the page is read-only, so the customer stays
// on the menu and can pick the next option straight away. Repeated taps reuse the
// open flow link instead of spamming new messages.
func (s *ConversationService) startMyDetailsWebFlow(ctx context.Context, channel, recipient string, user store.User) error {
	token := ""
	if flow, found, err := s.store.OpenWebFlowForUser(ctx, user.ID, WebFlowMyDetails); err != nil {
		return err
	} else if found {
		token = flow.Token
	} else {
		flow, err := s.store.MintWebFlow(ctx, user.ID, channel, WebFlowMyDetails, map[string]string{}, "", s.cfg.SessionTTL)
		if err != nil {
			return err
		}
		token = flow.Token
	}
	ctx = withMessageFlow(ctx, WebFlowMyDetails)
	meta, _ := webFlowCatalog[WebFlowMyDetails]
	return s.sendLink(ctx, channel, recipient, meta.intro, s.cfg.BaseURL+"/w/"+token, meta.label)
}

// sendMyDetailsChat renders the details block in chat (Telegram/TikTok).
func (s *ConversationService) sendMyDetailsChat(ctx context.Context, channel, recipient string, user store.User) error {
	summary := s.MyDetailsSummaryOf(ctx, user)
	var lines []string
	lines = append(lines, "Your Xego details\n\nAccount")
	lines = append(lines,
		"• Name: "+orDash(summary.Name),
		"• WhatsApp: "+orDash(summary.Phone),
		"• Email: "+orDash(summary.Email),
		"• Account level: "+orDash(summary.AccountLevel),
		"• Verification tier: "+orDash(summary.Tier))
	lines = append(lines, "\nWallet")
	if summary.WalletStatus == "" || summary.WalletStatus == "none" {
		lines = append(lines, "• Status: not opened yet")
	} else {
		lines = append(lines, "• Status: "+summary.WalletStatus)
		if summary.WalletBalance != "" {
			lines = append(lines, "• Balance: "+summary.WalletBalance)
		}
		if summary.WalletNote != "" {
			lines = append(lines, "• "+summary.WalletNote)
		}
	}
	lines = append(lines, "\nPaying limits (money in)")
	for _, row := range summary.PayingLines {
		lines = append(lines, "• "+row.Term+": "+row.Desc)
	}
	for _, merchant := range summary.Merchants {
		lines = append(lines, "\n"+merchant.Heading)
		for _, row := range merchant.Lines {
			lines = append(lines, "• "+row.Term+": "+row.Desc)
		}
	}
	lines = append(lines, "\nRecent payments")
	if len(summary.Payments) == 0 {
		lines = append(lines, "• You don't have any Xego payments yet.")
	} else {
		for _, payment := range summary.Payments {
			lines = append(lines, "• "+payment.Desc)
		}
	}
	lines = append(lines, "\n"+summary.Note)
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}
