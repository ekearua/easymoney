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
	Name               string
	Phone              string
	Email              string
	Tier               string
	AccountLevel       string
	WalletStatus       string
	WalletBalance      string
	WalletNote         string
	PayingLines        []MyDetailsLine
	Merchants          []MyDetailsMerchant
	Payments           []MyDetailsLine
	Note               string
	CountryOfResidence string
	ExpectedVolumeBand string
	// Purses holds the beneficiary's restricted programme balances (balance,
	// expiry and where the money may be spent) as pre-rendered lines. Empty
	// for customers who hold no programme money, in which case neither surface
	// shows a programme section at all.
	Purses []MyDetailsLine
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
	// G14: the declared geography and expected-activity inputs live on the
	// individual profile beside the ladder position; a user without one (or
	// without a declaration) simply gets no line.
	if profile, err := s.store.IndividualProfileByUser(ctx, user.ID); err == nil {
		summary.CountryOfResidence = profile.CountryISO
		summary.ExpectedVolumeBand = profile.ExpectedVolumeBand
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
			summary.WalletNote = "pending — it activates automatically when your account reaches CBN Tier 1"
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
	summary.Note = s.standingAccountNote(ctx, user.ID, summary.Tier)
	// Restricted programme balances: one line per live purse with balance,
	// expiry and the spendable merchant count, then one history line per
	// returned grant. Degraded independently like every other section, and
	// absent entirely when the customer holds no programme money.
	if s.programPurses != nil {
		if summaries, err := s.programPurses.PurseSummariesForUser(ctx, user.ID); err == nil {
			summary.Purses = programPurseDetailLines(summaries)
		}
	}
	return summary
}

// programPurseDetailLines renders the beneficiary's programme balances for the
// details surfaces. Active purses first (balance, expiry, spendable merchants),
// then returned grants summarised as history. A customer with no purses gets
// no lines at all so the section disappears rather than showing an empty list.
func programPurseDetailLines(summaries []PurseSummary) []MyDetailsLine {
	var active, closed []PurseSummary
	for _, summary := range summaries {
		if summary.Purse.Status == store.PurseStatusActive {
			active = append(active, summary)
		} else {
			closed = append(closed, summary)
		}
	}
	lines := make([]MyDetailsLine, 0, len(active)+len(closed))
	for _, summary := range active {
		purse := summary.Purse
		heading := purse.ProgramName
		if strings.TrimSpace(heading) == "" {
			heading = purse.ProgramCode
		}
		expiry := "no expiry"
		if purse.ExpiresAt != nil {
			expiry = purse.ExpiresAt.Format("2 Jan 2006")
		}
		merchantWord := "merchants"
		if len(summary.Merchants) == 1 {
			merchantWord = "merchant"
		}
		merchantCount := "no approved merchants"
		if len(summary.Merchants) > 0 {
			merchantCount = fmt.Sprintf("%d approved %s", len(summary.Merchants), merchantWord)
		}
		line := fmt.Sprintf("%s — balance %s, expires %s, spend at %s",
			heading, domain.FormatNGN(purse.BalanceKobo), expiry, merchantCount)
		if purse.BalanceKobo == 0 {
			line = fmt.Sprintf("%s — balance spent, expires %s, spend at %s",
				heading, expiry, merchantCount)
		}
		lines = append(lines, MyDetailsLine{Term: "Programme funds", Desc: line})
	}
	for _, summary := range closed {
		purse := summary.Purse
		heading := purse.ProgramName
		if strings.TrimSpace(heading) == "" {
			heading = purse.ProgramCode
		}
		closedOn := ""
		if purse.ClosedAt != nil {
			closedOn = " on " + purse.ClosedAt.Format("2 Jan 2006")
		}
		lines = append(lines, MyDetailsLine{
			Term: "Programme funds (returned)",
			Desc: fmt.Sprintf("%s — unspent %s returned to the sponsor%s",
				heading, domain.FormatNGN(purse.ReturnedKobo), closedOn),
		})
	}
	return lines
}

// standingAccountNote describes the payer-facing standing account shown at the
// foot of the details page/chat block. L0 users have no standing account — each
// payment gets its own virtual account number — while L1+ holders see the
// reserved number they can be paid into at any time.
func (s *ConversationService) standingAccountNote(ctx context.Context, userID uuid.UUID, tier string) string {
	if kyc.Order(tier) < kyc.Order(kyc.TierL1) {
		return "No standing account number yet — each payment gets its own virtual account number. Reach CBN Tier 1 (complete onboarding) to get your standing account number."
	}
	account, err := s.store.StandingAccountByUser(ctx, userID)
	if err != nil || strings.TrimSpace(account.AccountNumber) == "" {
		return "Your standing account number is being provisioned."
	}
	bank := strings.TrimSpace(account.BankName)
	if bank == "" {
		bank = "Interswitch"
	}
	note := "Standing account number: " + account.AccountNumber + " (" + bank + ")"
	if account.Provider != "" {
		note += " — pay into it any time to top up your wallet"
	}
	return note
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
	if summary.CountryOfResidence != "" {
		lines = append(lines, "• Country of residence: "+summary.CountryOfResidence)
	}
	if summary.ExpectedVolumeBand != "" {
		lines = append(lines, "• Expected volume band: "+summary.ExpectedVolumeBand)
	}
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
	// Restricted programme balances: shown between the limits and the merchant
	// sections so restricted money is visible with the same prominence as the
	// ordinary wallet. Lines are pre-rendered by the summary itself, so the
	// chat block and the browser page cannot disagree.
	for _, row := range summary.Purses {
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
		lines = append(lines, "• You don't have any Faya payments yet.")
	} else {
		for _, payment := range summary.Payments {
			lines = append(lines, "• "+payment.Desc)
		}
	}
	lines = append(lines, "\nSaved payment methods")
	if saved := s.savedMethodLines(ctx, user.ID); len(saved) == 0 {
		lines = append(lines, "• You haven't saved any payment methods.")
	} else {
		for _, line := range saved {
			lines = append(lines, "• "+line)
		}
		lines = append(lines, "• Reply \"forget saved methods\" to have Xego stop remembering how you pay.")
	}
	lines = append(lines, "\n"+summary.Note)
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

// savedMethodLines loads and renders the payer's remembered rails for the chat
// details block. A store hiccup degrades to no section rather than a broken
// details page.
func (s *ConversationService) savedMethodLines(ctx context.Context, userID uuid.UUID) []string {
	prefs, err := s.store.PaymentPreferencesForUser(ctx, userID)
	if err != nil {
		return nil
	}
	return SavedMethodLines(prefs)
}

// forgetSavedMethods clears every remembered rail for the payer and confirms in
// chat. The browser "My details" page performs the same clearing, so "forget"
// means the same thing on every surface: nothing is charged, and the payer is
// asked to choose a method afresh each time.
func (s *ConversationService) forgetSavedMethods(ctx context.Context, channel, recipient string, user store.User) error {
	if err := s.store.ClearAllPaymentPreferences(ctx, user.ID); err != nil {
		return s.sendText(ctx, channel, recipient, "Could not update your saved payment methods right now. Please try again.")
	}
	return s.sendText(ctx, channel, recipient, "Done — Xego will no longer remember how you paid. I'll ask you to choose a payment method each time, and nothing was charged.")
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}
