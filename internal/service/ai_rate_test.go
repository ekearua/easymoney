package service

import (
	"context"
	"testing"

	"whatsapp-payment-demo/internal/ratelimit"
)

func TestAILimiterUnlimitedWhenNotConfigured(t *testing.T) {
	s := &ConversationService{}
	if !s.aiAllowed(context.Background(), "intent:u1") {
		t.Fatalf("AI must be allowed when no limiter is configured")
	}
}

func TestAILimiterZeroMaxRPMIsUnlimited(t *testing.T) {
	s := &ConversationService{}
	s.SetAIRateLimiter(ratelimit.NewMemory(), 0)
	if !s.aiAllowed(context.Background(), "assistant:u1") {
		t.Fatalf("maxRPM 0 must mean unlimited")
	}
}

func TestAILimiterPerKeyBudget(t *testing.T) {
	s := &ConversationService{}
	s.SetAIRateLimiter(ratelimit.NewMemory(), 2)
	ctx := context.Background()

	if !s.aiAllowed(ctx, "intent:+2348000000001") {
		t.Fatalf("first call within budget")
	}
	if !s.aiAllowed(ctx, "intent:+2348000000001") {
		t.Fatalf("second call within budget")
	}
	if s.aiAllowed(ctx, "intent:+2348000000001") {
		t.Fatalf("third call must be throttled")
	}
	// A different customer has its own budget.
	if !s.aiAllowed(ctx, "intent:+2348000000002") {
		t.Fatalf("budget must be tracked per key")
	}
	// Media and assistant slots are distinct budgets per key.
	if !s.aiAllowed(ctx, "assistant:+2348000000001") {
		t.Fatalf("different AI slot should have its own budget")
	}
}
