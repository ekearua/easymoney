package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
)

var errSample = errors.New("outbox read failed")

type fakeOutbox struct {
	outcomes []string
	err      error
}

func (f *fakeOutbox) OutboxDeliveryOutcomeRows(ctx context.Context, window time.Duration) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]string, len(f.outcomes))
	copy(out, f.outcomes)
	return out, nil
}

// TestDeliveryGuard asserts the recent-delivery-failure-ratio gate behaves as
// a read-side safety net: it stays quiet below the configured threshold, fires
// exactly at it, and is disabled entirely when the ratio knob is zero.
func TestDeliveryGuard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		outcomes         []string
		ratio            float64
		expectPaused     bool
		expectRatioError bool
	}{
		{
			name:      "stays quiet below threshold",
			outcomes:  []string{"delivered", "failed", "delivered"},
			ratio:     0.5,
		},
		{
			name:          "fires at threshold",
			outcomes:      []string{"delivered", "failed", "delivered", "failed"},
			ratio:         0.5,
			expectPaused:  true,
		},
		{
			name:         "fires above threshold",
			outcomes:     []string{"failed", "failed"},
			ratio:        0.25,
			expectPaused: true,
		},
		{
			name:     "disabled when ratio is zero",
			outcomes: []string{"failed", "failed"},
			ratio:    0,
		},
		{
			name:     "quiet when no terminal rows",
			outcomes: []string{},
			ratio:    0.5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeOutbox{outcomes: tc.outcomes}
			svc := &OnboardingReminderService{
				outbox: f,
				cfg:    config.Config{OnboardingReminderDeliveryGuardFailureRatio: tc.ratio, OnboardingReminderDeliveryGuardWindow: time.Hour},
			}
			paused, err := svc.recentDeliveryFailureRatio(context.Background())
			if tc.expectRatioError {
				t.Fatalf("recentDeliveryFailureRatio: want error, got (paused=%v, err=%v)", paused, err)
			}
			if err != nil {
				t.Fatalf("recentDeliveryFailureRatio: unexpected error: %v", err)
			}
			if paused != tc.expectPaused {
				t.Fatalf("recentDeliveryFailureRatio: want paused=%v, got %v", tc.expectPaused, paused)
			}
		})
	}
}

// TestDeliveryGuardError propagates a read-side store failure as an error
// instead of freezing the sweep.
func TestDeliveryGuardError(t *testing.T) {
	t.Parallel()
	f := &fakeOutbox{err: errSample}
	svc := &OnboardingReminderService{
		outbox: f,
		cfg:    config.Config{OnboardingReminderDeliveryGuardFailureRatio: 0.5, OnboardingReminderDeliveryGuardWindow: time.Hour},
	}
	_, err := svc.recentDeliveryFailureRatio(context.Background())
	if err == nil {
		t.Fatal("expected a store error through recentDeliveryFailureRatio, got nil")
	}
	if !errors.Is(err, errSample) {
		t.Fatalf("expected errSample, got %v", err)
	}
}
