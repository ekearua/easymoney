package service

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/store"
)

// Deterministic parsing of free-form payment instructions sent as chat text,
// OCR text from a photo, or a transcribed voice note. A instruction names a
// send keyword followed by an amount and a destination:
//
//	send 5000 to 08039999900 GTBank 0123456789
//	faya 2000 to Ada
//	transfer 2500 naira to Jumia
//
// Parsing is deliberately lexical and best-effort: numbers are classified as
// a phone, an account number, or an amount by shape and order, banks are
// resolved against the directory, and merchants are matched by name. Anything
// ambiguous falls back to the AI classifier or the normal menu flow, and the
// review step always confirms before money moves.
const (
	ipMinKobo = 100           // â‚¦1
	ipMaxKobo = 1_000_000_000 // â‚¦10,000,000, mirroring the web-flow capture bounds
)

// ipSendKeywordBare matches the trigger words that mark a money-transfer
// instruction. "faya" is a send keyword, so it both triggers parsing and can
// name the recipient ("... to faya").
var ipSendKeywordRe = regexp.MustCompile(`(?i)\b(send|pay|faya|transfer)\b`)

// ipWordRe and ipDigitRunRe split a normalized instruction into letter words
// and digit runs for the shape-based classification below.
var (
	ipPlus234Re = regexp.MustCompile(`\+?234`)
	ipWordRe    = regexp.MustCompile(`[a-zA-Z]+`)
	ipDigitRe   = regexp.MustCompile(`[0-9]+`)
)

// IndividualPayHint is the outcome of parsing an instruction. It is data only;
// the caller decides the route (merchant web flow, individual web flow, or the
// chat FSM) from the fields present.
type IndividualPayHint struct {
	SendIntent        bool
	RecipientPhone    string // canonical E.164 when the text named a phone
	BankCode          string
	BankName          string
	AccountNumber     string // 10 digits when present
	Name              string // trailing phrase (merchant or individual) after "to"
	MerchantSlug      string
	MerchantAmbiguous bool
	AmountKobo        int64
}

// Routeable reports whether the instruction is confident enough to act on
// without confirming with the customer: a keyword plus at least one field.
func (h IndividualPayHint) Routeable() bool {
	return h.SendIntent && (h.RecipientPhone != "" || h.AccountNumber != "" ||
		h.MerchantSlug != "" || h.Name != "" || h.AmountKobo > 0)
}

// MerchantTarget reports a merchant-only instruction: a matched merchant with
// no individual destination (phone or account).
func (h IndividualPayHint) MerchantTarget() bool {
	return h.MerchantSlug != "" && h.RecipientPhone == "" && h.AccountNumber == ""
}

// IndividualTarget reports an instruction that should open the individual-pay
// flow: anything routeable that is not a merchant pick and not ambiguous.
func (h IndividualPayHint) IndividualTarget() bool {
	return h.Routeable() && !h.MerchantTarget() && !h.MerchantAmbiguous
}

// AmbiguousMerchant reports the "that name matches several merchants" state,
// which routes to the merchant picker rather than guessing.
func (h IndividualPayHint) AmbiguousMerchant() bool {
	return h.MerchantAmbiguous
}

// ParseIndividualPayText parses a payment instruction from any supported
// input (chat text, OCR, or transcription). The keyword gates routing; the
// destination is disambiguated by shape: a phone or account number means an
// individual, a matched merchant name means merchant pay. When SendIntent is
// false the hint is empty and the caller continues with the normal flow.
func (s *ConversationService) ParseIndividualPayText(ctx context.Context, text string) (IndividualPayHint, error) {
	lex := lexIndividualPay(text)
	if !lex.send {
		return IndividualPayHint{}, nil
	}
	hint := IndividualPayHint{
		SendIntent:     true,
		RecipientPhone: lex.phoneE164,
		AccountNumber:  lex.account,
		AmountKobo:     lex.amountKobo,
	}

	// Merchant-first disambiguation, but only when no phone/account pins the
	// recipient as an individual.
	merchants, err := s.store.ListMerchants(ctx)
	if err != nil {
		return hint, err
	}
	if lex.phoneE164 == "" && lex.account == "" {
		hint.MerchantSlug, hint.MerchantAmbiguous = merchantSlugFromText(lex.nameSrc, merchants)
	}

	if !hint.MerchantTarget() && !hint.MerchantAmbiguous {
		nameTokens := ipNameTokens(lex.nameSrc)
		res, consumed, rerr := resolveBankTokens(ctx, s.store, nameTokens)
		if rerr == nil && res.Code != "" {
			hint.BankCode = res.Code
			hint.BankName = res.Name
		}
		if lex.phoneE164 == "" && lex.account == "" {
			hint.Name = ipNameFromTokens(ipStripTokens(nameTokens, consumed))
		}
	}
	return hint, nil
}

// individualPayLexemes is the pure lexical layer of parsing. It classifies
// digit runs by shape into a phone (Nigerian mobile: 0XXXXXXXXXX, 234XXXXXXXX,
// or a bare national number), a 10-digit account number, or the first in-range
// amount. The letter-only residue becomes nameSrc for bank/merchant/name
// resolution.
type individualPayLexemes struct {
	send       bool
	phoneE164  string
	account    string
	amountKobo int64
	nameSrc    string
}

func lexIndividualPay(text string) individualPayLexemes {
	text = strings.TrimSpace(text)
	lex := individualPayLexemes{}
	if text == "" || !ipSendKeywordRe.MatchString(text) {
		return lex
	}
	lex.send = true

	// Normalize the "+234" form so the leading + never splits a digit run.
	normalized := ipPlus234Re.ReplaceAllString(text, "234")

	var phone string
	for _, run := range ipDigitRe.FindAllString(normalized, -1) {
		if phoneClass(run) {
			phone = run
			break
		}
	}
	var account string
	for _, run := range ipDigitRe.FindAllString(normalized, -1) {
		if run == phone {
			continue
		}
		if len(run) == 10 {
			account = run
			break
		}
	}
	for _, run := range ipDigitRe.FindAllString(normalized, -1) {
		if run == phone || run == account {
			continue
		}
		if kobo, ok := ipRunToKobo(run); ok {
			lex.amountKobo = kobo
			break
		}
	}

	if phone != "" {
		switch {
		case strings.HasPrefix(phone, "234") && len(phone) == 13:
			lex.phoneE164 = domain.CanonicalE164Phone("0" + phone[3:])
		case strings.HasPrefix(phone, "0") && len(phone) == 11:
			lex.phoneE164 = domain.CanonicalE164Phone(phone)
		case len(phone) == 10:
			// A bare national number (8039999900) is the 0-prefixed form.
			lex.phoneE164 = domain.CanonicalE164Phone("0" + phone)
		default:
			lex.phoneE164 = domain.CanonicalE164Phone(phone)
		}
	}
	lex.account = account
	lex.nameSrc = strings.Join(ipWordRe.FindAllString(normalized, -1), " ")
	return lex
}

// phoneClass reports whether a digit run is a Nigerian mobile number by
// displaying form: 0XXXXXXXXXX, 234XXXXXXXXX, or a bare national 10-digit
// 7/8/9 number.
func phoneClass(run string) bool {
	switch {
	case len(run) == 11 && run[0] == '0' && run[1] >= '7' && run[1] <= '9' && (run[2] == '0' || run[2] == '1'):
		return true
	case len(run) == 13 && strings.HasPrefix(run, "234") && run[3] >= '7' && run[3] <= '9' && (run[4] == '0' || run[4] == '1'):
		return true
	case len(run) == 10 && run[0] >= '7' && run[0] <= '9' && (run[1] == '0' || run[1] == '1'):
		return true
	}
	return false
}

// ipRunToKobo converts a pure digit run to kobo within the accepted bounds,
// returning ok=false when the run is not a credible amount (a phone number, a
// date, or a figure outside â‚¦1..â‚¦10m).
func ipRunToKobo(run string) (int64, bool) {
	naira, err := strconv.ParseInt(strings.TrimLeft(run, "0"), 10, 64)
	if err != nil || naira == 0 {
		return 0, false
	}
	kobo := naira * 100
	if kobo < ipMinKobo || kobo > ipMaxKobo {
		return 0, false
	}
	return kobo, true
}

// resolveBankTokens tries to resolve the bank named in the instruction. The
// whole letter residue is tried first ("GTBank"), then each non-trivial word
// ("send 2000 to access ada" resolves "access"). It returns the words consumed
// so they can be dropped from the recipient name.
func resolveBankTokens(ctx context.Context, st *store.Store, tokens []string) (BankResolution, []string, error) {
	src := strings.Join(tokens, " ")
	if src == "" {
		return BankResolution{}, nil, ErrBankNotFound
	}
	// The whole phrase is only trusted when it compactly equals a bank name or
	// short name ("guaranty trust bank", "gtbank"), so a loose fuzzy hit on a
	// multi-word instruction never hijacks the recipient's bank.
	if res, err := ResolveBank(ctx, st, src); err == nil && bankNameMatches(res, src) {
		return res, tokens, nil
	}
	// Word by word: "gtbank" from "faya to gtbank", "access" from "send to
	// access ada". Each hit must also be an exact compact match.
	for _, tok := range tokens {
		if len(tok) < 3 {
			continue
		}
		if res, err := ResolveBank(ctx, st, tok); err == nil && bankNameMatches(res, tok) {
			return res, []string{tok}, nil
		}
	}
	return BankResolution{}, nil, ErrBankNotFound
}

// bankNameMatches reports whether a resolution's canonical name or short name
// compactly equals the caller's wording (apostrophes, spaces, and punctuation
// ignored), rejecting fuzzy similarity-only hits.
func bankNameMatches(res BankResolution, wording string) bool {
	n := compactBankNameInput(wording)
	return n != "" && (compactBankNameInput(res.Name) == n || compactBankNameInput(res.ShortName) == n)
}

var ipStopWords = map[string]bool{
	"to": true, "for": true, "from": true, "an": true, "a": true, "the": true,
	"and": true, "or": true, "of": true, "my": true, "i": true, "please": true,
	"pls": true, "now": true, "today": true, "tomorrow": true, "by": true,
	"using": true, "through": true, "bank": true,
}

// ipNameTokens keeps the letter words of the instruction for name/bank work.
func ipNameTokens(src string) []string {
	return strings.Fields(strings.Join(ipWordRe.FindAllString(src, -1), " "))
}

// ipNameFromTokens folds the surviving words back into a recipient name for
// display or prefill ("ada" from "to ada", "ada muo" from "to ada muo").
func ipNameFromTokens(tokens []string) string {
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if ipStopWords[strings.ToLower(tok)] {
			continue
		}
		out = append(out, tok)
	}
	return strings.TrimSpace(strings.Join(out, " "))
}

// ipStripTokens removes the words consumed by bank resolution (and the same
// words anywhere in the phrase) so they never leak into the recipient name.
func ipStripTokens(tokens, consumed []string) []string {
	if len(consumed) == 0 {
		return tokens
	}
	drop := make(map[string]bool, len(consumed))
	for _, c := range consumed {
		drop[strings.ToLower(c)] = true
	}
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if len(tok) < 3 || drop[strings.ToLower(tok)] {
			continue
		}
		if ipStopWords[strings.ToLower(tok)] {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// merchantSlugFromText matches a merchant by name inside the instruction,
// mirroring the web flow's two-pass matcher: an exact normalized substring
// first (longest name wins), then token matching that survives apostrophes,
// punctuation, and word order. When several distinct names tie, ambiguous=true
// asks the merchant picker instead of guessing.
func merchantSlugFromText(text string, merchants []store.Merchant) (slug string, ok bool) {
	if strings.TrimSpace(text) == "" {
		return "", true
	}
	lower := ipNormalizeName(text)
	best, bestLen, bestNorm := "", 0, ""
	for _, m := range merchants {
		name := ipNormalizeName(m.Name)
		if len(name) < 3 || !strings.Contains(lower, name) {
			continue
		}
		if len(name) > bestLen {
			best, bestLen, bestNorm = m.Slug, len(name), name
		}
	}
	if best != "" {
		for _, m := range merchants {
			name := ipNormalizeName(m.Name)
			if m.Slug != best && len(name) == bestLen && name == bestNorm {
				return "", false
			}
		}
		return best, true
	}
	tokens := ipNormalizeName(text)
	best, bestNorm = "", ""
	bestWords, ambiguous := 0, false
	for _, m := range merchants {
		words := strings.Fields(ipNormalizeName(m.Name))
		if len(words) > 0 && len(words) >= 2 && ipTokensMatch(strings.Fields(tokens), words) {
			if len(words) > bestWords {
				best, bestNorm, bestWords, ambiguous = m.Slug, m.Name, len(words), false
			} else if len(words) == bestWords && m.Name != bestNorm {
				ambiguous = true
			}
		}
	}
	if ambiguous {
		return "", false
	}
	return best, true
}

// ipNormalizeName folds a name to lowercase words with apostrophes and
// non-alphanumeric punctuation dropped, the same shape the token pass compares.
func ipNormalizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\'':
			return -1
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return ' '
		}
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// ipTokensMatch reports whether every wanted word matches some text token by
// equality or a â‰¥3-character prefix in either direction, mirroring the web
// flow's tolerance for truncated or apostrophe-less typing.
func ipTokensMatch(textTokens, wanted []string) bool {
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
