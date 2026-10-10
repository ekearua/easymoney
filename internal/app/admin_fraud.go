package app

import (
	"net/http"
	"strings"

	"whatsapp-payment-demo/internal/fraud"
	"whatsapp-payment-demo/internal/store"
)

// adminFraud renders the G13 fraud lane: detections raised by internal/fraud
// (failed-attempt bursts, payment/dispute/refund velocity, destination churn),
// kept on their own queue so AML and fraud signals stay independently
// triageable. Resolutions reuse the shared monitoring endpoint with
// next=/admin/fraud, so the M4 outcome/labelling loop applies unchanged.
func (a *App) adminFraud(w http.ResponseWriter, r *http.Request) {
	alertRail := strings.TrimSpace(r.URL.Query().Get("rail"))
	alerts, err := a.store.ListFraudAlerts(r.Context(), "", alertRail, 100)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list fraud alerts", "error", err)
		http.Error(w, "fraud queue unavailable", http.StatusInternalServerError)
		return
	}
	allCases, err := a.store.ListManualReviewCases(r.Context(), "", 200)
	if err != nil {
		a.logger.WarnContext(r.Context(), "list review cases", "error", err)
		http.Error(w, "fraud queue unavailable", http.StatusInternalServerError)
		return
	}
	var cases []store.ManualReviewCase
	for _, c := range allCases {
		if c.CaseType == "fraud" {
			cases = append(cases, c)
		}
	}
	a.renderAdmin(w, "admin_fraud.html", r, "Fraud queue", map[string]any{
		"Alerts":     alerts,
		"Cases":      cases,
		"AlertRails": []string{fraud.RailPayment, fraud.RailDispute, fraud.RailRefund, fraud.RailPayout},
		"AlertRail":  alertRail,
	})
}
