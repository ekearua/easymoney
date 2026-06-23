package domain

import (
	"strings"
	"testing"
)

func TestParseNGNAmount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{name: "plain naira", input: "100", want: 10_000},
		{name: "formatted", input: "₦25,000.50", want: 2_500_050},
		{name: "currency prefix", input: "NGN 1,200", want: 120_000},
		{name: "too low", input: "99", wantErr: true},
		{name: "too high", input: "100001", wantErr: true},
		{name: "too precise", input: "500.001", wantErr: true},
		{name: "negative", input: "-500", wantErr: true},
		{name: "invalid", input: "five hundred", wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseNGNAmount(test.input, 10_000, 10_000_000)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseNGNAmount(%q) expected error", test.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseNGNAmount(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("ParseNGNAmount(%q)=%d, want %d", test.input, got, test.want)
			}
		})
	}
}

func TestPaymentTransitionsAreMonotonic(t *testing.T) {
	t.Parallel()
	if !CanTransition(StatusDraft, StatusAwaitingConfirmation) {
		t.Fatal("draft should advance to awaiting confirmation")
	}
	if !CanTransition(StatusPending, StatusSucceeded) {
		t.Fatal("pending should advance to succeeded")
	}
	if CanTransition(StatusSucceeded, StatusPending) {
		t.Fatal("terminal success must never regress")
	}
	if CanTransition(StatusFailed, StatusSucceeded) {
		t.Fatal("terminal failure must never change to success")
	}
	if !CanTransition(StatusSucceeded, StatusSucceeded) {
		t.Fatal("idempotent same-state transition should be accepted")
	}
}

func TestReceiptTokensAreOpaqueAndUnique(t *testing.T) {
	t.Parallel()
	first, err := NewReceiptToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewReceiptToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("receipt tokens must be unique")
	}
	if len(first) < 40 || strings.ContainsAny(first, "+/=") {
		t.Fatalf("receipt token is not long URL-safe text: %q", first)
	}
}

func TestFormatNGN(t *testing.T) {
	t.Parallel()
	if got := FormatNGN(12_345_067); got != "₦123,450.67" {
		t.Fatalf("FormatNGN=%q", got)
	}
}
