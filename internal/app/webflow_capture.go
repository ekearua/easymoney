package app

import (
	"fmt"
	"net/http"
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
	// wfBillAskField stores the free text typed into the AI ask-bar. It flows
	// through the same parsers as OCR/STT output.
	wfBillAskField = "ai_ask"

	// wfBillMinKobo / wfBillMaxKobo bound an amount read from OCR/STT text so
	// a stray phone number or date never becomes the charge. The web flow's
	// own min/max still gate the final amount.
	wfBillMinKobo = 100           // ₦1
	wfBillMaxKobo = 1_000_000_000 // ₦10,000,000
)

// wfBillCaptureFields returns the optional capture fields shown on the pay
// flow's first step. They are ordinary upload/voice web-flow fields, so the
// existing media endpoint stores their extracted text under the field name.
// The BarIcon markers fold the file/camera/voice affordances into the AI
// ask-bar (webflow.html) instead of rendering stacked media sections.
func wfBillCaptureFields() []webFlowField {
	return []webFlowField{
		{Name: wfBillPhotoField, Label: "Snap the bill instead (optional)", Type: "upload", BarIcon: "file",
			MediaPrompt: "This photo shows a bill or receipt. Reply with the merchant name and the total amount in naira, e.g. \"MERCHANT Ade's Kitchen | AMOUNT 2500\".",
			Hint:        "Upload a photo of the bill — Xego reads the merchant and amount to prefill the review."},
		{Name: wfBillVoiceField, Label: "Or say the amount (optional)", Type: "voice", BarIcon: "mic",
			Hint: "Record a short note, e.g. \"pay Ade's Kitchen two thousand five hundred\"."},
	}
}

// wfBillCapturedText joins the text the customer supplied on the pay capture
// step. The typed ask-bar text is the most deliberate input and wins when
// present; otherwise the OCR/STT extracts are joined.
func wfBillCapturedText(payload map[string]string) string {
	if ask := strings.TrimSpace(payload[wfBillAskField]); ask != "" {
		return ask
	}
	return strings.TrimSpace(strings.TrimSpace(payload[wfBillPhotoField]) + " " + strings.TrimSpace(payload[wfBillVoiceField]))
}

// wfIndPhotoField / wfIndVoiceField / wfIndAskField store the extracted text
// of the individual-pay capture step under the same PRG keys the bill flow
// uses (upload/voice handled by the media endpoint, the typed ask-bar flowing
// straight into the same parsers).
const (
	wfIndPhotoField = "ind_photo"
	wfIndVoiceField = "ind_voice"
	wfIndAskField   = "ind_ask"
)

// wfIndividualAskField returns the AI ask-bar field shown first on the
// individual-pay capture step; its typed text flows into the same parsers as
// the OCR/STT extracts below it.
func wfIndividualAskField(payload map[string]string) webFlowField {
	return webFlowField{
		Name: wfIndAskField, Label: "Who to pay", Type: "aisearch",
		Value: payload[wfIndAskField],
		Hint:  "e.g. “send 5000 to 08012345678 GTBank 0123456789” — Xego reads it and prefills the fields below, or use the icons to snap or say it.",
	}
}

// wfIndividualCaptureFields returns the optional upload/voice controls shown
// after the individual-pay form fields: snapping a transfer slip or saying the
// instruction supplies the same free text as the ask-bar, handled by the same
// media endpoint as the bill flow.
func wfIndividualCaptureFields() []webFlowField {
	return []webFlowField{
		{Name: wfIndPhotoField, Label: "Snap the instruction instead (optional)", Type: "upload", BarIcon: "file",
			MediaPrompt: "This photo shows a money-transfer instruction. Reply with the recipient phone number, the amount in naira, the bank name, and the 10-digit account number, e.g. \"PHONE 08012345678 | AMOUNT 2500 | BANK GTBank | ACCOUNT 0123456789\".",
			Hint:        "Upload a photo — Xego reads recipient, amount, bank, and account to prefill the form."},
		{Name: wfIndVoiceField, Label: "Or say the instruction (optional)", Type: "voice", BarIcon: "mic",
			Hint: "Record it, e.g. \"send five thousand to 08012345678 GTBank account 0123456789\"."},
	}
}

// wfIndividualCapturedText joins the customer-supplied instruction on the
// individual-pay capture step, preferring the typed ask-bar like the bill flow.
func wfIndividualCapturedText(payload map[string]string) string {
	if ask := strings.TrimSpace(payload[wfIndAskField]); ask != "" {
		return ask
	}
	return strings.TrimSpace(strings.TrimSpace(payload[wfIndPhotoField]) + " " + strings.TrimSpace(payload[wfIndVoiceField]))
}

// wfIndividualPrefill merges a parsed instruction into the current flow
// payload so the recipient/amount/bank/account fields prefill. Parsing runs on
// the captured text (typed, OCR'd, or transcribed); explicit form entries that
// the customer typed directly always win over the read text.
func (a *App) wfIndividualPrefill(r *http.Request, flow store.WebFlow, payload map[string]string) {
	captured := wfIndividualCapturedText(flow.Payload)
	if captured == "" {
		return
	}
	hint, err := a.conversation.ParseIndividualPayText(r.Context(), captured)
	if err != nil || !hint.SendIntent {
		return
	}
	if strings.TrimSpace(r.FormValue("recipient_phone")) == "" && hint.RecipientPhone != "" {
		payload["recipient_phone"] = hint.RecipientPhone
	}
	if strings.TrimSpace(r.FormValue("amount_kobo")) == "" && hint.AmountKobo > 0 {
		payload["amount_kobo"] = fmt.Sprintf("%d", hint.AmountKobo)
	}
	if strings.TrimSpace(r.FormValue("bank_code")) == "" && hint.BankCode != "" {
		payload["bank_code"] = hint.BankCode
		payload["bank_name"] = hint.BankName
	}
	if hint.AccountNumber != "" {
		payload["account_number"] = hint.AccountNumber
	}
}

// wfBillMerchantSlug picks the merchant whose name appears in the captured
// text. Matching runs in two passes: an exact substring pass first ("Ade's
// Kitchen" inside a typed sentence or OCR'd bill), then a normalized token
// pass that survives apostrophes, punctuation, and word-order ("ade kitchen"
// → "Ade's Kitchen", "kitchen from ade" → "Ade's Kitchen"). When several
// names match, the longest (most specific) wins; when the best candidates
// are indistinguishable (same normalized name), ok=false reports the
// ambiguity so the caller can make the customer confirm instead of guessing.
func wfBillMerchantSlug(text string, merchants []store.Merchant) (slug string, ok bool) {
	if strings.TrimSpace(text) == "" {
		return "", true
	}
	// Pass 1: exact substring, longest name wins.
	lower := strings.ToLower(text)
	best, bestLen := "", 0
	bestNorm := ""
	for _, m := range merchants {
		name := strings.ToLower(strings.TrimSpace(m.Name))
		if len(name) < 3 || !strings.Contains(lower, name) {
			continue
		}
		if len(name) > bestLen {
			best, bestLen, bestNorm = m.Slug, len(name), wfNormalizeMerchantName(name)
		}
	}
	if best != "" {
		// Tied longest names that normalize identically are the same business;
		// genuinely different names are ambiguous.
		for _, m := range merchants {
			name := strings.ToLower(strings.TrimSpace(m.Name))
			if m.Slug != best && len(name) == bestLen && wfNormalizeMerchantName(name) == bestNorm {
				return "", false
			}
		}
		return best, true
	}
	// Pass 2: normalized token matching — every word of the merchant name
	// must match a text token (equality or a ≥3-char prefix, so "ade kitchen"
	// matches "Ade's Kitchen" and "kora book" matches "Kora Books"). A name
	// that is itself a subset of another candidate's words loses to it.
	tokens := wfMerchantTokens(wfNormalizeMerchantName(text))
	best, bestNorm = "", ""
	bestWords := 0
	ambiguous := false
	for _, m := range merchants {
		name := strings.TrimSpace(m.Name)
		words := wfMerchantTokens(wfNormalizeMerchantName(name))
		if len(words) == 0 || len(words) < 2 || !matchAllTokens(tokens, words) {
			continue
		}
		if len(words) > bestWords {
			best, bestNorm, bestWords, ambiguous = m.Slug, wfNormalizeMerchantName(name), len(words), false
		} else if len(words) == bestWords && wfNormalizeMerchantName(name) != bestNorm {
			ambiguous = true
		}
	}
	if ambiguous {
		return "", false
	}
	return best, true
}

// wfNormalizeMerchantName folds a name or sentence to comparable lowercase
// tokens: apostrophes dropped (ade's → ades), punctuation and diacritic-ish
// separators folded to spaces. Numbers are kept so "shop 5" stays distinct.
func wfNormalizeMerchantName(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\'':
			return -1 // drop apostrophes: ade's -> ades
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == '&':
			return ' '
		default:
			if r >= 128 {
				return -1 // fold non-ascii punctuation like ₦
			}
			return ' '
		}
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// wfMerchantTokens splits a normalized name into word tokens.
func wfMerchantTokens(s string) []string {
	return strings.Fields(s)
}

// matchAllTokens reports whether every merchant-name word matches some text
// token: either equality or a prefix in either direction of at least three
// characters. Prefixes let truncated or apostrophe-less typing ("ade",
// "kora book") resolve to the full name ("Ade's", "Kora Books") without
// letting one- and two-letter noise match everything.
func matchAllTokens(textTokens, wanted []string) bool {
	for _, w := range wanted {
		matched := false
		for _, t := range textTokens {
			if t == w || (len(t) >= 3 && strings.HasPrefix(w, t)) || (len(w) >= 3 && strings.HasPrefix(t, w)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
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
