package app

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// wfMyDetailsStep renders the read-only "My details" page: the customer's
// account, wallet, limits, and recent payments as a plain (non-shell) review
// page. There are no fields and no actions — tapping the link views the
// details and returns to the chat. A POST to this page (only a fabricated
// client sends one) re-renders the same page.
func (a *App) wfMyDetailsStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	page.Title = "Your Faya details"
	page.Intro = "Your Faya account, wallet, and limits — all in one place."
	summary := a.conversation.MyDetailsSummaryOf(r.Context(), user)
	review := make([]webFlowLine, 0, 12+len(summary.PayingLines)+len(summary.Merchants)+len(summary.Payments))
	review = append(review,
		webFlowLine{Term: "Name", Desc: summary.Name},
		webFlowLine{Term: "WhatsApp", Desc: summary.Phone},
		webFlowLine{Term: "Email", Desc: summary.Email},
		webFlowLine{Term: "Account level", Desc: summary.AccountLevel},
		webFlowLine{Term: "Verification tier", Desc: summary.Tier},
		webFlowLine{Term: "Wallet status", Desc: walletStatusDesc(summary.WalletStatus)},
	)
	if summary.CountryOfResidence != "" {
		review = append(review, webFlowLine{Term: "Country of residence", Desc: summary.CountryOfResidence})
	}
	if summary.ExpectedVolumeBand != "" {
		review = append(review, webFlowLine{Term: "Expected volume band", Desc: summary.ExpectedVolumeBand})
	}
	if summary.WalletBalance != "" {
		review = append(review, webFlowLine{Term: "Wallet balance", Desc: summary.WalletBalance})
	}
	if summary.WalletNote != "" {
		review = append(review, webFlowLine{Term: "Wallet", Desc: summary.WalletNote})
	}
	var limitParts []string
	for _, row := range summary.PayingLines {
		limitParts = append(limitParts, row.Term+": "+row.Desc)
	}
	review = append(review, webFlowLine{Term: "Paying limits (money in)", Desc: strings.Join(limitParts, "\n")})
	// Restricted programme balances: one read-only row per purse between the
	// limits and the merchant sections, rendered from the same pre-rendered
	// lines the chat block uses so the two surfaces cannot disagree.
	for _, row := range summary.Purses {
		review = append(review, webFlowLine{Term: row.Term, Desc: row.Desc})
	}
	for _, merchant := range summary.Merchants {
		var parts []string
		for _, row := range merchant.Lines {
			parts = append(parts, row.Term+": "+row.Desc)
		}
		review = append(review, webFlowLine{Term: merchant.Heading, Desc: strings.Join(parts, "\n")})
	}
	if len(summary.Payments) == 0 {
		review = append(review, webFlowLine{Term: "Recent payments", Desc: "You don't have any Faya payments yet."})
	} else {
		var paymentParts []string
		for _, payment := range summary.Payments {
			paymentParts = append(paymentParts, payment.Desc)
		}
		review = append(review, webFlowLine{Term: "Recent payments", Desc: strings.Join(paymentParts, "\n")})
	}
	review = append(review, webFlowLine{Term: "Account number", Desc: summary.Note})
	// Saved payment methods (SAVED_CARD_METHODS_PLAN.md Phase 1): show what we
	// remember and offer one action to forget all of it. Nothing here is card
	// data — only which rail the payer chose last.
	saved := a.savedMethodLines(r, user.ID)
	if len(saved) == 0 {
		review = append(review, webFlowLine{Term: "Saved payment methods", Desc: "You haven't saved any payment methods."})
	} else {
		review = append(review, webFlowLine{Term: "Saved payment methods", Desc: strings.Join(saved, "\n")})
	}
	page.Review = review
	if len(saved) > 0 {
		page.Actions = []webFlowAction{{Name: "forget_saved_methods", Label: "Forget saved payment methods"}}
	}
	return page, nil
}

// savedMethodLines renders the payer's remembered rails for the details page,
// using the same formatter as the chat block so the two cannot disagree.
func (a *App) savedMethodLines(r *http.Request, userID uuid.UUID) []string {
	prefs, err := a.store.PaymentPreferencesForUser(r.Context(), userID)
	if err != nil {
		return nil
	}
	return service.SavedMethodLines(prefs)
}

func (a *App) wfMyDetailsSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	forgot := action == "forget_saved_methods"
	if forgot {
		if err := a.store.ClearAllPaymentPreferences(r.Context(), user.ID); err != nil {
			a.logger.WarnContext(r.Context(), "clear payment preferences", "user_id", user.ID, "error", err)
			page, renderErr := a.wfMyDetailsStep(r, flow, user)
			if renderErr != nil {
				return nil, renderErr
			}
			return a.wfPageWithError(flow, page, "Could not update your saved payment methods right now. Please try again."), nil
		}
	}
	page, err := a.wfMyDetailsStep(r, flow, user)
	if err != nil {
		return nil, err
	}
	if forgot {
		page.Intro = "Done — Xego will no longer remember how you paid. You'll choose a payment method each time, and nothing was charged."
	}
	return &page, nil
}

func walletStatusDesc(status string) string {
	switch status {
	case store.WalletStatusActive:
		return "Active"
	case store.WalletStatusPending:
		return "Pending"
	case store.WalletStatusFrozen:
		return "Frozen"
	}
	if status == "" || status == "none" {
		return "Not opened yet"
	}
	return status
}
