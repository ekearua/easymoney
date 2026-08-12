package chatguard

import (
	"strings"
	"testing"
)

func TestInspectBlocksCardNumbers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain visa", "4111111111111111", true},
		{"spaced visa", "4111 1111 1111 1111", true},
		{"dashed visa", "4111-1111-1111-1111", true},
		{"amex 15", "378282246310005", true},
		{"maestro 16", "6759649826438453", true},
		{"with words", "my card is 4111 1111 1111 1111 thanks", true},
		{"amount", "5000", false},
		{"phone number", "08031234567", false},
		{"amount with naira", "pay 150000 now", false},
		{"quantity", "3", false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			res := Inspect(test.in)
			if res.Blocked != test.want {
				t.Fatalf("Inspect(%q).Blocked=%v want %v", test.in, res.Blocked, test.want)
			}
		})
	}
}

func TestInspectBlocksPINCVVOTPWithKeyword(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Category
	}{
		{"otp", "the otp is 123456", OtpCategory},
		{"otp long", "otp: 482913", OtpCategory},
		{"pin", "my pin is 4321", Pincategory},
		{"atm pin", "atm pin 567890", Pincategory},
		{"cvv", "cvv is 123", CvvCategory},
		{"security code", "security code 999", CvvCategory},
		{"bare digits no keyword", "123456", ""},
		{"otp without keyword", "482913", ""},
		{"amount near word code", "book code 15", ""},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			res := Inspect(test.in)
			if test.want == "" {
				if res.Blocked {
					t.Fatalf("Inspect(%q).Blocked=true, want false", test.in)
				}
				return
			}
			if !res.Blocked || res.Category != test.want {
				t.Fatalf("Inspect(%q)=blocked:%v cat:%q want blocked cat:%q", test.in, res.Blocked, res.Category, test.want)
			}
		})
	}
}

func TestInspectPrefersCardCategory(t *testing.T) {
	// A message containing both a card number and CVV must be classified as card.
	res := Inspect("4111 1111 1111 1111 cvv 123")
	if !res.Blocked || res.Category != CardCategory {
		t.Fatalf("expected card category, got blocked=%v cat=%q", res.Blocked, res.Category)
	}
}

func TestInspectRedactsTheCredential(t *testing.T) {
	res := Inspect("my card 4111 1111 1111 1111 thanks")
	if !res.Blocked {
		t.Fatal("expected blocked")
	}
	if strings.Contains(res.Redacted, "4111") {
		t.Fatalf("redacted copy leaks the card number: %q", res.Redacted)
	}
	if !strings.Contains(res.Redacted, "[REDACTED:card]") {
		t.Fatalf("redacted copy should keep the mask label: %q", res.Redacted)
	}
	if !strings.Contains(res.Redacted, "thanks") {
		t.Fatalf("redacted copy should keep surrounding text: %q", res.Redacted)
	}

	res = Inspect("the otp is 123456")
	if !res.Blocked || strings.Contains(res.Redacted, "123456") {
		t.Fatalf("expected the OTP digits to be replaced in the redacted copy, got %q", res.Redacted)
	}
	if !strings.Contains(res.Redacted, "[REDACTED:otp]") {
		t.Fatalf("redacted copy should keep the otp mask label: %q", res.Redacted)
	}
}

func TestInspectIgnoresNormalConversation(t *testing.T) {
	for _, text := range []string{
		"1kg of rice",
		"I want to pay a merchant",
		"MTN MTN1GB 08031234567",
		"email me at a@b.com",
		"menu",
	} {
		if res := Inspect(text); res.Blocked {
			t.Fatalf("Inspect(%q) should not block, got %q", text, res.Category)
		}
	}
}