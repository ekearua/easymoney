package store

// Unit tests for the feedback loop's suggestion builder (the SQL behind the
// stats is covered by gated tests; the policy here is pure logic).

import (
	"strings"
	"testing"
)

func TestBuildTuningSuggestionsThresholdsAndOrdering(t *testing.T) {
	t.Parallel()
	stats := []RuleFPStat{
		{Rule: "noisy", Total: 12, Confirmed: 2, FalsePositive: 10, FPPercent: 83.3},
		{Rule: "mild", Total: 12, Confirmed: 5, FalsePositive: 7, FPPercent: 58.3},
		{Rule: "thin", Total: 3, Confirmed: 0, FalsePositive: 3, FPPercent: 100},
		{Rule: "clean", Total: 9, Confirmed: 9, FalsePositive: 0, FPPercent: 0},
	}
	th := TuningThresholds{FPPercent: 60, MinLabelled: 10}
	got := BuildTuningSuggestions(stats, th)
	if len(got) != 1 {
		t.Fatalf("got %d suggestions, want 1 (noisy); %+v", len(got), got)
	}
	s := got[0]
	if s.Rule != "noisy" || s.Severity != "warn" {
		t.Errorf("suggestion = %+v, want noisy/warn", s)
	}
	if s.Labelled != 12 || s.FPPercent < 83 || s.FPPercent > 84 {
		t.Errorf("labelled/percent = %d/%v, want 12/≈83.3", s.Labelled, s.FPPercent)
	}
	if !strings.Contains(s.Message, "83%") || !strings.Contains(s.Message, "12") {
		t.Errorf("message %q must name the rounded rate and the label count", s.Message)
	}
	// A lower threshold pulls "mild" in as an info-level suggestion and
	// orders worst-first; the thin-labels rule stays excluded.
	got = BuildTuningSuggestions(stats, TuningThresholds{FPPercent: 55, MinLabelled: 10})
	if len(got) != 2 || got[0].Rule != "noisy" || got[1].Rule != "mild" {
		t.Fatalf("relaxed threshold → %+v, want noisy then mild", got)
	}
	if got[1].Severity != "info" {
		t.Errorf("mild severity = %q, want info", got[1].Severity)
	}
	// Zero threshold falls back to the shipped defaults.
	if got := BuildTuningSuggestions(stats, TuningThresholds{}); len(got) != 1 || got[0].Rule != "noisy" {
		t.Errorf("default thresholds → %+v, want noisy only", got)
	}
}
