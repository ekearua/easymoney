// Package chatguard inspects free-text chat input for card, PIN, CVV, and OTP
// material. Xego never asks for this data in chat, so detecting it lets the
// conversation layer refuse to act on it, tell the customer why, and record the
// attempt (C18, PCI + CBN consumer protection). Card numbers are confirmed with
// the Luhn algorithm to avoid flagging ordinary amounts and phone numbers;
// PIN/CVV/OTP codes are only flagged when a sensitive keyword is nearby.
package chatguard

import (
	"regexp"
	"strings"
)

// Category identifies the kind of payment credential found.
type Category string

const (
	CardCategory Category = "card"
	Pincategory  Category = "pin"
	CvvCategory  Category = "cvv"
	OtpCategory  Category = "otp"
)

// Result is what the guard found, if anything.
type Result struct {
	Blocked  bool
	Category Category
	Redacted string // text with the matched credential masked
}

var (
	cardCandidateRE = regexp.MustCompile(`(?:\d[ \-]?){13,19}`)
	digitTokenRE    = regexp.MustCompile(`\b\d{3,8}\b`)
	cvvKeywordRE    = regexp.MustCompile(`(?i)\b(cvv|cvc|csv|security[ ]code|card[ ]verification)\b`)
	pinKeywordRE    = regexp.MustCompile(`(?i)\b(atm[ ]pin|transaction[ ]pin|card[ ]pin|pin)\b`)
	otpKeywordRE    = regexp.MustCompile(`(?i)\b(otp|one[ \-]?time[ ](?:password|code)|verification[ ]code)\b`)
	cleanRE         = regexp.MustCompile(`[ -]`)
)

// luhnValid reports whether digits pass the Luhn checksum. Card-issuer PANs are
// 13-19 digits and always Luhn-valid; amounts and phone numbers almost never are.
func luhnValid(digits string) bool {
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum > 0 && sum%10 == 0
}

// maskCandidate returns the redacted copy of text for a single matched range
// (byte offsets from a regexp index), plus a mask label for the category.
func maskCandidate(text string, start, end int, category Category) string {
	label := "[REDACTED:" + string(category) + "]"
	return text[:start] + label + text[end:]
}

// Inspect examines one inbound message (incoming free text or interactive arg)
// and returns whether it contains payment-credential material. The returned text
// is safe to log: the matched credential is replaced, never the original.
func Inspect(text string) Result {
	if text == "" {
		return Result{}
	}

	// Card PANs take precedence: a Luhn-valid 13-19 digit number is a card
	// regardless of surrounding words, so any keyword proximity checks below
	// cannot hijack the category.
	for _, loc := range cardCandidateRE.FindAllStringIndex(text, -1) {
		candidate := cleanRE.ReplaceAllString(text[loc[0]:loc[1]], "")
		if luhnValid(candidate) {
			return Result{
				Blocked:  true,
				Category: CardCategory,
				Redacted: maskCandidate(text, loc[0], loc[1], CardCategory),
			}
		}
	}

	// PIN/CVV/OTP only count when a sensitive keyword is close by; a bare 3-8
	// digit string is an amount, plan code or similar, never a credential.
	checks := []struct {
		keyword *regexp.Regexp
		cat     Category
		minLen  int
		maxLen  int
	}{
		{cvvKeywordRE, CvvCategory, 3, 4},
		{pinKeywordRE, Pincategory, 4, 6},
		{otpKeywordRE, OtpCategory, 4, 8},
	}
	for _, check := range checks {
		for _, kw := range check.keyword.FindAllStringIndex(text, -1) {
			for _, tok := range digitTokenRE.FindAllStringIndex(text, -1) {
				// Gap between the token and the keyword region (0 if adjacent).
				var gap int
				if tok[1] <= kw[0] {
					gap = kw[0] - tok[1]
				} else if kw[1] <= tok[0] {
					gap = tok[0] - kw[1]
				}
				if gap > 12 {
					continue
				}
				digits := strings.ReplaceAll(text[tok[0]:tok[1]], " ", "")
				if len(digits) < check.minLen || len(digits) > check.maxLen {
					continue
				}
				return Result{
					Blocked:  true,
					Category: check.cat,
					Redacted: maskCandidate(text, tok[0], tok[1], check.cat),
				}
			}
		}
	}
	return Result{}
}