package app

import (
	"net/http"
	"strings"

	"whatsapp-payment-demo/internal/store"
)

// wfMyDetailsStep renders the read-only "My details" page: the customer's
// account, wallet, limits, and recent payments as a plain (non-shell) review
// page. There are no fields and no actions — tapping the link views the
// details and returns to the chat. A POST to this page (only a fabricated
// client sends one) re-renders the same page.
func (a *App) wfMyDetailsStep(r *http.Request, flow store.WebFlow, user store.User) (webFlowPage, error) {
	page := a.wfPage(flow)
	page.Title = "Your Xego details"
	page.Intro = "Your Xego account, wallet, and limits — all in one place."
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
	for _, merchant := range summary.Merchants {
		var parts []string
		for _, row := range merchant.Lines {
			parts = append(parts, row.Term+": "+row.Desc)
		}
		review = append(review, webFlowLine{Term: merchant.Heading, Desc: strings.Join(parts, "\n")})
	}
	if len(summary.Payments) == 0 {
		review = append(review, webFlowLine{Term: "Recent payments", Desc: "You don't have any Xego payments yet."})
	} else {
		var paymentParts []string
		for _, payment := range summary.Payments {
			paymentParts = append(paymentParts, payment.Desc)
		}
		review = append(review, webFlowLine{Term: "Recent payments", Desc: strings.Join(paymentParts, "\n")})
	}
	review = append(review, webFlowLine{Term: "Account number", Desc: summary.Note})
	page.Review = review
	return page, nil
}

func (a *App) wfMyDetailsSubmit(w http.ResponseWriter, r *http.Request, flow store.WebFlow, user store.User, action string) (*webFlowPage, error) {
	page, err := a.wfMyDetailsStep(r, flow, user)
	if err != nil {
		return nil, err
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
