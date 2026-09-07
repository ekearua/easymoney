package app

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"whatsapp-payment-demo/internal/store"
)

// webFlow capture: the pay flow lets a customer start a payment by snapping a
// bill or saying the amount, instead of typing every field. The upload/voice
// engine stores the extracted text in the flow payload under these field
// names; these helpers turn that free text into a merchant pick and a naira
// amount that prefill the review step. Both are best-effort: when the text is
// ambiguous, the flow falls back to the normal typed fields and the customer
// stays in control.
const (
	wfBillPhotoField = "bill_photo"
	wfBillVoiceField = "bill_voice"

	// wfBillMinKobo / wfBillMaxKobo bound an amount read from OCR/STT text so
	// a stray phone number or date never becomes the charge. The web flow's
	// own min/max still gate the final amount.
	wfBillMinKobo = 100           // ₦1
	wfBillMaxKobo = 1_000_000_000 // ₦10,000,000
)

// wfBillCaptureFields returns the optional capture fields shown on the pay
// flow's first step. They are ordinary upload/voice web-flow fields, so the
// existing media endpoint stores their extracted text under the field name.
func wfBillCaptureFields() []webFlowField {
	return []webFlowField{
		{Name: wfBillPhotoField, Label: "Snap the bill instead (optional)", Type: "upload",
			MediaPrompt: "This photo shows a bill or receipt. Reply with the merchant name and the total amount in naira, e.g. \"MERCHANT Ade's Kitchen | AMOUNT 2500\".",
			Hint:        "Upload a photo of the bill — Xego reads the merchant and amount to prefill the review."},
		{Name: wfBillVoiceField, Label: "Or say the amount (optional)", Type: "voice",
			Hint: "Record a short note, e.g. \"pay Ade's Kitchen two thousand five hundred\"."},
	}
}

// wfBillCapturedText joins the OCR/STT text from the pay capture fields.
func wfBillCapturedText(payload map[string]string) string {
	return strings.TrimSpace(strings.TrimSpace(payload[wfBillPhotoField]) + " " + strings.TrimSpace(payload[wfBillVoiceField]))
}

// wfBillMerchantSlug picks the merchant whose name appears in the captured
// text. When several names match, the longest (most specific) wins; with no
// match it returns "" so the customer chooses from the list.
func wfBillMerchantSlug(text string, merchants []store.Merchant) string {
	lower := strings.ToLower(text)
	if lower == "" {
		return ""
	}
	best := ""
	bestLen := 0
	for _, m := range merchants {
		name := strings.ToLower(strings.TrimSpace(m.Name))
		if len(name) < 3 || !strings.Contains(lower, name) {
			continue
		}
		if len(name) > bestLen {
			best = m.Slug
			bestLen = len(name)
		}
	}
	return best
}

var (
	// wfMoneyMarked matches an amount beside a naira marker, in either order:
	//   ₦5,000   NGN 5,000   5,000 naira   2500 naira
	wfMoneyMarked = regexp.MustCompile(`(?i)(?:₦|NGN|naira)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)|([0-9][0-9,]*(?:\.[0-9]{1,2})?)\s*naira`)
	// wfMoneyPlain matches a standalone whole-naira figure (3-9 digits,
	// optionally thousands-separated) in a spoken payment instruction, e.g.
	// "pay Ade's Kitchen 2500", "send 5000 to jumia", "pay 3,750 today".
	// Best-effort: the review step still confirms.
	wfMoneyPlain = regexp.MustCompile(`(?i)(?:\bpay\b|\bsend\b)\s+(?:[a-z0-9'&.]+\s+)*([0-9][0-9,]*(?:\.[0-9]{1,2})?)(?:\s|$|[.,])`)
)

// wfBillAmountKobo finds the amount in the captured text and returns it in
// kobo, or 0 when nothing credible is present. Marked amounts (₦ / NGN /
// naira) are trusted first; a spoken instruction is accepted only when it
// names an amount after "pay"/"send". Amounts outside ₦1..₦10m are rejected
// so a stray phone number or date never becomes the charge.
func wfBillAmountKobo(text string) int64 {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	if m := wfMoneyMarked.FindStringSubmatch(text); len(m) == 3 {
		if k, ok := moneyDigitsToKobo(m[1]); ok {
			return k
		}
		if k, ok := moneyDigitsToKobo(m[2]); ok {
			return k
		}
	}
	if m := wfMoneyPlain.FindStringSubmatch(text); len(m) == 2 {
		if k, ok := moneyDigitsToKobo(m[1]); ok {
			return k
		}
	}
	return 0
}

// moneyDigitsToKobo converts a digit string (possibly grouped with commas,
// with up to two decimal places) into kobo within the accepted bounds.
func moneyDigitsToKobo(raw string) (int64, bool) {
	clean := strings.ReplaceAll(raw, ",", "")
	if clean == "" {
		return 0, false
	}
	naira, frac := int64(0), int64(0)
	if dot := strings.IndexByte(clean, '.'); dot >= 0 {
		whole, err := strconv.ParseInt(clean[:dot], 10, 64)
		if err != nil {
			return 0, false
		}
		part := clean[dot+1:]
		for len(part) < 2 {
			part += "0"
		}
		if len(part) > 2 {
			part = part[:2]
		}
		f, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return 0, false
		}
		naira, frac = whole, f
	} else {
		n, err := strconv.ParseInt(clean, 10, 64)
		if err != nil {
			return 0, false
		}
		naira = n
	}
	kobo := naira*100 + frac
	if kobo < wfBillMinKobo || kobo > wfBillMaxKobo {
		return 0, false
	}
	return kobo, true
}

// wfKoboToNairaInput renders kobo as the plain naira string the amount input
// expects (e.g. 250000 -> "2500", 250050 -> "2500.50").
func wfKoboToNairaInput(kobo int64) string {
	if kobo < 0 {
		return ""
	}
	naira := kobo / 100
	frac := kobo % 100
	if frac == 0 {
		return strconv.FormatInt(naira, 10)
	}
	return strconv.FormatInt(naira, 10) + fmt.Sprintf(".%02d", frac)
}
