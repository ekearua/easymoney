package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// stubPayoutProvider is a hermetic payout rail for the settlement tests,
// mirroring the retired simulated provider contract: bank code "011" always
// declines, anything else succeeds.
type stubPayoutProvider struct {
	now func() time.Time
}

func (p stubPayoutProvider) Payout(_ context.Context, req ports.PayoutRequest) (ports.PayoutResult, error) {
	if req.BankCode == "011" {
		return ports.PayoutResult{
			Status:  "failed",
			Message: "rail declined: recipient bank unavailable",
		}, nil
	}
	return ports.PayoutResult{
		Status:      "succeeded",
		ExternalRef: fmt.Sprintf("SIM-PAY-%s-%d", req.Reference, p.now().Unix()),
		Message:     "payout completed",
	}, nil
}

// stubRefundProvider is a hermetic refund rail for tests that construct a
// RefundService: it mirrors the real constructor's non-nil provider guard, so
// a nil provider can no longer be smuggled into a test fixture.
type stubRefundProvider struct{}

func (stubRefundProvider) Refund(_ context.Context, req ports.RefundRequest) (ports.RefundResult, error) {
	return ports.RefundResult{Status: "succeeded", RefundID: "SIM-REFUND-" + req.PaymentID, Message: "stub refund completed"}, nil
}

// stubChatAI reproduces the retired simulated AI classifier: a strict
// substring matcher that would turn every menu_* row id into a concrete
// intent, which is exactly the double-menu root cause the regression test
// documents.
type stubChatAI struct{}

func (stubChatAI) ClassifyIntent(_ context.Context, userMessage string, _ []string) (ports.IntentResult, error) {
	lower := strings.ToLower(strings.TrimSpace(userMessage))
	switch {
	case strings.Contains(lower, "pay") || strings.Contains(lower, "send money"):
		return ports.IntentResult{Intent: "pay", Confidence: 0.9, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "data") || strings.Contains(lower, "buy data"):
		return ports.IntentResult{Intent: "buy_data", Confidence: 0.85, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "invoice"):
		return ports.IntentResult{Intent: "invoice", Confidence: 0.85, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "thrift"):
		return ports.IntentResult{Intent: "thrift", Confidence: 0.85, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "individual") || strings.Contains(lower, "become"):
		return ports.IntentResult{Intent: "become_individual", Confidence: 0.8, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "nin") || strings.Contains(lower, "bvn") || strings.Contains(lower, "verify"):
		return ports.IntentResult{Intent: "verify_id", Confidence: 0.8, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "status") || strings.Contains(lower, "check"):
		return ports.IntentResult{Intent: "status", Confidence: 0.7, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "help"):
		return ports.IntentResult{Intent: "help", Confidence: 0.9, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "menu"):
		return ports.IntentResult{Intent: "menu", Confidence: 0.95, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "cancel"):
		return ports.IntentResult{Intent: "cancel", Confidence: 0.9, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "skip"):
		return ports.IntentResult{Intent: "skip", Confidence: 0.9, Entities: map[string]string{}}, nil
	default:
		return ports.IntentResult{Intent: "none", Confidence: 0}, nil
	}
}

func (stubChatAI) Answer(_ context.Context, question string, _ []string) (string, error) {
	lower := strings.ToLower(question)
	switch {
	case strings.Contains(lower, "how") || strings.Contains(lower, "what"):
		return "Xego lets you send money, buy data, create invoices, and join thrift groups — all from WhatsApp. Type MENU to see your options.", nil
	default:
		return "I'm here to help! You can type MENU to see your options, or ask me anything about Xego.", nil
	}
}
