package domain

import "testing"

func TestNormalizeNigerianPhone(t *testing.T) {
	tests := map[string]string{
		"08031234567":    "+2348031234567",
		"2348031234567":  "+2348031234567",
		"+2348031234567": "+2348031234567",
	}
	for input, want := range tests {
		got, err := NormalizeNigerianPhone(input)
		if err != nil {
			t.Fatalf("NormalizeNigerianPhone(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeNigerianPhone(%q)=%q want %q", input, got, want)
		}
	}
	if _, err := NormalizeNigerianPhone("12345"); err == nil {
		t.Fatal("expected invalid phone to fail")
	}
}

func TestDataOrderTransitions(t *testing.T) {
	if !CanTransitionDataOrder(DataOrderAwaitingPayment, DataOrderPaid) {
		t.Fatal("awaiting_payment should transition to paid")
	}
	if CanTransitionDataOrder(DataOrderFulfilled, DataOrderFailed) {
		t.Fatal("fulfilled order must not transition to failed")
	}
}

func TestNewDataRequestCode(t *testing.T) {
	code, err := NewDataRequestCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != len("XG-DATA-12345678") {
		t.Fatalf("unexpected request code length: %q", code)
	}
	if code[:8] != "XG-DATA-" {
		t.Fatalf("unexpected request code prefix: %q", code)
	}
}
