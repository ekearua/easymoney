package service

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Tolerant parsers for the aggregated chat data-collection steps. Instead of
// one message per field, a customer sends one message per form: DOB, gender
// and address together at the CBN Tier 1 gate; legal name through occupation
// together at the individual-profile step; and so on. Each parser accepts
// labeled ("DOB: 1992-05-24") and unlabeled (one fact per line) input, keeps
// whatever parts it could read, and reports exactly which parts are missing
// so the re-ask never makes the customer retype a captured field.

var (
	dobISORe   = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	dobDMYRe   = regexp.MustCompile(`\b(\d{2})[-/.](\d{2})[-/.](\d{4})\b`)
	genderRe   = regexp.MustCompile(`\b(male|female)\b`)
	idNumberRe = regexp.MustCompile(`\b([A-Za-z0-9-]{6,25})\b`)
	elevenRe   = regexp.MustCompile(`\b(\d{11})\b`)
)

// dobFromPart extracts a date of birth from one line of input. Accepts the
// canonical YYYY-MM-DD, and the common Nigerian spoken order DD-MM-YYYY (only
// when DD/leap-consistent, so 05-06-2000 reads as May 6). Returns "" when no
// consistent date is present.
func dobFromPart(line string) string {
	if m := dobISORe.FindStringSubmatch(line); m != nil {
		if _, err := time.Parse("2006-01-02", m[0]); err == nil {
			return m[0]
		}
	}
	// A labeled DOB field may repeat the value, e.g. "DOB: 1992-05-24 (age 33)".
	if m := dobDMYRe.FindStringSubmatch(line); m != nil {
		day, month := m[1], m[2]
		if day > "12" && day <= "31" {
			d, err := time.Parse("2006-01-02", m[3]+"-"+month+"-"+day)
			if err == nil {
				return d.Format("2006-01-02")
			}
		}
	}
	return ""
}

// genderFromPart extracts MALE/FEMALE from one line. Empty when absent.
func genderFromPart(line string) string {
	if m := genderRe.FindStringSubmatch(strings.ToLower(line)); m != nil {
		return m[1]
	}
	return ""
}

// missingParts renders the friendly re-ask for the parts a customer still owes
// ("Still need your date of birth and gender."). Empty when nothing is missing.
func missingParts(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	if len(missing) == 1 {
		return "Still need your " + missing[0] + "."
	}
	return "Still need your " + strings.Join(missing[:len(missing)-1], ", ") +
		" and " + missing[len(missing)-1] + "."
}

// capturedNote echoes what was already captured so a partial re-ask never
// asks the customer to retype a captured field.
func capturedNote(captured []string) string {
	if len(captured) == 0 {
		return ""
	}
	return "Got " + strings.Join(captured, " and ") + "."
}

// Tier1Details is the CBN Tier 1 self-declared data set: date of birth,
// gender and residential address.
type Tier1Details struct {
	DOB     string // YYYY-MM-DD
	Gender  string // male | female
	Address string
}

// ParseTier1Details reads DOB, gender and address from one free-form message.
// Lines that look like a date or carry a DOB/gender label are consumed as
// those fields; the longest remaining line (>=10 chars, the existing address
// rule) becomes the address. Any subset may be present.
func ParseTier1Details(input string) Tier1Details {
	var out Tier1Details
	var addressCandidates []string
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "dob"), strings.HasPrefix(lower, "date of birth"):
			out.DOB = dobFromPart(stripLabel(line))
			continue
		case strings.HasPrefix(lower, "gender"):
			out.Gender = genderFromPart(stripLabel(line))
			continue
		case strings.HasPrefix(lower, "address"), strings.HasPrefix(lower, "residential"):
			addressCandidates = append(addressCandidates, stripLabel(line))
			continue
		}
		if dob := dobFromPart(line); dob != "" {
			if out.DOB == "" {
				out.DOB = dob
			}
			// The rest of the line (minus the date and its label) may still be
			// address text.
			line = residueAfterDOB(line)
			if len([]rune(line)) >= 10 {
				addressCandidates = append(addressCandidates, line)
			}
			continue
		}
		if gender := genderFromPart(line); gender != "" && len([]rune(line)) <= 20 {
			if out.Gender == "" {
				out.Gender = gender
			}
			continue
		}
		if len([]rune(line)) >= 10 {
			addressCandidates = append(addressCandidates, line)
		}
	}
	for _, candidate := range addressCandidates {
		if len([]rune(candidate)) <= 240 {
			out.Address = candidate
			break
		}
	}
	return out
}

// Missing reports which Tier-1 parts the message did not supply, using the
// customer-facing field names the re-ask shows.
func (d Tier1Details) Missing() []string {
	var missing []string
	if d.DOB == "" {
		missing = append(missing, "date of birth (YYYY-MM-DD)")
	}
	if d.Gender == "" {
		missing = append(missing, "gender (MALE or FEMALE)")
	}
	if d.Address == "" {
		missing = append(missing, "residential address")
	}
	return missing
}

// Captured lists the parts the message did supply, for the re-ask echo.
func (d Tier1Details) Captured() []string {
	var captured []string
	if d.DOB != "" {
		captured = append(captured, "your date of birth ("+d.DOB+")")
	}
	if d.Gender != "" {
		captured = append(captured, "your gender ("+strings.ToUpper(d.Gender)+")")
	}
	if d.Address != "" {
		captured = append(captured, "your address")
	}
	return captured
}

// Complete reports whether the whole Tier-1 set was supplied.
func (d Tier1Details) Complete() bool { return len(d.Missing()) == 0 }

// ValidateTier1Details applies the existing per-field rules (18+, gender
// vocabulary, address length) to a parsed set. Returns the offending field
// name, or "" when everything is valid.
func ValidateTier1Details(d Tier1Details) string {
	if d.DOB != "" {
		dob, err := time.Parse("2006-01-02", d.DOB)
		if err != nil || dob.After(time.Now().AddDate(-18, 0, 0)) {
			return "date of birth"
		}
	}
	if d.Gender != "" && d.Gender != "male" && d.Gender != "female" {
		return "gender"
	}
	if d.Address != "" {
		if n := len([]rune(d.Address)); n < 10 || n > 240 {
			return "residential address"
		}
	}
	return ""
}

// IndividualProfileDetails is the individual-profile form: the Tier-1 set plus
// legal name and occupation.
type IndividualProfileDetails struct {
	LegalName  string
	DOB        string // YYYY-MM-DD
	Gender     string // male | female
	Address    string
	Occupation string
}

// ParseIndividualProfileDetails reads legal name, DOB, gender, address and
// occupation from one labeled or line-per-fact message. Labeled fields win;
// the first short unlabeled line is the legal name, the last short one the
// occupation, and the longest remaining line the address.
func ParseIndividualProfileDetails(input string) IndividualProfileDetails {
	var out IndividualProfileDetails
	type shortLine struct {
		text  string
		index int
	}
	var shorts []shortLine
	var addressCandidates []string
	for i, raw := range strings.Split(input, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		value := stripLabel(line)
		switch {
		case strings.HasPrefix(lower, "name"), strings.HasPrefix(lower, "legal name"), strings.HasPrefix(lower, "full name"):
			if out.LegalName == "" {
				out.LegalName = strings.TrimSpace(value)
			}
			continue
		case strings.HasPrefix(lower, "dob"), strings.HasPrefix(lower, "date of birth"):
			out.DOB = dobFromPart(value)
			continue
		case strings.HasPrefix(lower, "gender"):
			out.Gender = genderFromPart(value)
			continue
		case strings.HasPrefix(lower, "occupation"), strings.HasPrefix(lower, "job"), strings.HasPrefix(lower, "work"):
			if out.Occupation == "" {
				out.Occupation = strings.TrimSpace(value)
			}
			continue
		case strings.HasPrefix(lower, "address"):
			addressCandidates = append(addressCandidates, strings.TrimSpace(value))
			continue
		}
		if dob := dobFromPart(line); dob != "" {
			if out.DOB == "" {
				out.DOB = dob
			}
			line = strings.TrimSpace(dobISORe.ReplaceAllString(line, ""))
			if len([]rune(line)) >= 10 {
				addressCandidates = append(addressCandidates, line)
			}
			continue
		}
		if gender := genderFromPart(line); gender != "" && len([]rune(line)) <= 20 {
			if out.Gender == "" {
				out.Gender = gender
			}
			continue
		}
		switch n := len([]rune(line)); {
		// Long lines read as addresses; short ones as name/occupation text.
		// (Labeled fields bypass this heuristic entirely.)
		case n >= 25 && n <= 240:
			addressCandidates = append(addressCandidates, line)
		case n >= 2:
			shorts = append(shorts, shortLine{text: line, index: i})
		}
	}
	// Unlabeled shorts: first = legal name, last = occupation (same line never
	// serves both; a single short line is the name and occupation stays unset).
	if out.LegalName == "" && len(shorts) > 0 {
		out.LegalName = shorts[0].text
		shorts = shorts[1:]
	}
	if out.Occupation == "" && len(shorts) > 0 {
		out.Occupation = shorts[len(shorts)-1].text
	}
	for _, candidate := range addressCandidates {
		if len([]rune(candidate)) <= 240 {
			out.Address = candidate
			break
		}
	}
	return out
}

// Missing reports which profile parts are still owed.
func (d IndividualProfileDetails) Missing() []string {
	var missing []string
	if d.LegalName == "" {
		missing = append(missing, "legal name")
	}
	if d.DOB == "" {
		missing = append(missing, "date of birth (YYYY-MM-DD)")
	}
	if d.Gender == "" {
		missing = append(missing, "gender (MALE or FEMALE)")
	}
	if d.Address == "" {
		missing = append(missing, "residential address")
	}
	if d.Occupation == "" {
		missing = append(missing, "occupation")
	}
	return missing
}

// Captured lists the parts supplied, for the re-ask echo.
func (d IndividualProfileDetails) Captured() []string {
	var captured []string
	if d.LegalName != "" {
		captured = append(captured, "your legal name")
	}
	if d.DOB != "" {
		captured = append(captured, "your date of birth ("+d.DOB+")")
	}
	if d.Gender != "" {
		captured = append(captured, "your gender ("+strings.ToUpper(d.Gender)+")")
	}
	if d.Address != "" {
		captured = append(captured, "your address")
	}
	if d.Occupation != "" {
		captured = append(captured, "your occupation")
	}
	return captured
}

// Complete reports whether the whole profile set was supplied.
func (d IndividualProfileDetails) Complete() bool { return len(d.Missing()) == 0 }

// ValidateIndividualProfileDetails applies the existing per-field rules
// (name 3-100, DOB 18+, gender vocabulary, address 10-240, occupation 2-80).
// Returns the offending field name, or "" when everything is valid.
func ValidateIndividualProfileDetails(d IndividualProfileDetails) string {
	if d.LegalName != "" {
		if n := len([]rune(d.LegalName)); n < 3 || n > 100 {
			return "legal name"
		}
	}
	if d.DOB != "" {
		dob, err := time.Parse("2006-01-02", d.DOB)
		if err != nil || dob.After(time.Now().AddDate(-18, 0, 0)) {
			return "date of birth"
		}
	}
	if d.Gender != "" && d.Gender != "male" && d.Gender != "female" {
		return "gender"
	}
	if d.Address != "" {
		if n := len([]rune(d.Address)); n < 10 || n > 240 {
			return "residential address"
		}
	}
	if d.Occupation != "" {
		if n := len([]rune(d.Occupation)); n < 2 || n > 80 {
			return "occupation"
		}
	}
	return ""
}

// PhotoIDDetails is the government photo ID form: which card and its number.
type PhotoIDDetails struct {
	IDType   string // canonicalPhotoIDType vocabulary
	IDNumber string
}

// ParsePhotoIDDetails reads the ID type word and the ID number from one
// message in either order ("passport A12345678" or "A12345678 passport"),
// with or without a label ("ID type: passport\nID number: A12345678").
func ParsePhotoIDDetails(input string) PhotoIDDetails {
	var out PhotoIDDetails
	lower := strings.ToLower(input)
	// Detection order matters: specific vocabulary first, so "voters card"
	// is not stolen by national_id's generic "card" word.
	for _, kind := range []string{"passport", "drivers_licence", "voters_card", "national_id"} {
		for _, word := range strings.Fields(idTypeWordVocabulary(kind)) {
			if strings.Contains(lower, word) {
				out.IDType = kind
				break
			}
		}
		if out.IDType != "" {
			break
		}
	}
	// The number: the first 6-25 char alphanumeric token left after stripping
	// the label prefixes and type vocabulary (case preserved from the input).
	cleaned := idNumberLabelsRe.ReplaceAllString(input, " ")
	cleaned = idTypeWordRe.ReplaceAllString(cleaned, " ")
	if m := idNumberRe.FindStringSubmatch(cleaned); m != nil {
		out.IDNumber = m[1]
	}
	return out
}

// idNumberLabelsRe strips field-label words ("ID number:", "Number") without
// touching the case of the value that follows.
var idNumberLabelsRe = regexp.MustCompile(`(?i)\b(id\s+type|photo\s+id|id\s+number|number|id)\b`)

// idTypeWordRe strips every spoken ID-type word (case-insensitively) so the
// remaining text is just the number the customer typed.
var idTypeWordRe = regexp.MustCompile(`(?i)\b(international\s+)?passport\b|(?i)\bdrivers?'?s?\s+licen[cs]e\b|(?i)\blicen[cs]e\b|(?i)\bnational\s+(id\s+card\s+|id\s+|card\s+)?|(?i)\bidentity\b|(?i)\bvoter'?s?\s+card\b|(?i)\bvoters\b|(?i)\bpvc\b`)

// idTypeWordVocabulary lists the spoken forms of a canonical ID type used for
// detecting which card the customer named.
func idTypeWordVocabulary(kind string) string {
	switch kind {
	case "passport":
		return "passport international passport"
	case "drivers_licence":
		return "drivers licence driver's license licence"
	case "national_id":
		return "national id card identity"
	case "voters_card":
		return "voters card voter's pvc"
	}
	return ""
}

// Missing reports which ID parts are still owed (missingParts adds the
// "Still need your ..." wrapper, so the field names stay bare).
func (d PhotoIDDetails) Missing() []string {
	var missing []string
	if d.IDType == "" {
		missing = append(missing, "ID type (PASSPORT, DRIVERS LICENCE, NATIONAL ID, or VOTERS CARD)")
	}
	if d.IDNumber == "" {
		missing = append(missing, "ID number")
	}
	return missing
}

// Captured lists the parts supplied, for the re-ask echo.
func (d PhotoIDDetails) Captured() []string {
	var captured []string
	if d.IDType != "" {
		captured = append(captured, "your ID type ("+photoIDTypeLabel(d.IDType)+")")
	}
	if d.IDNumber != "" {
		captured = append(captured, "the ID number")
	}
	return captured
}

// Complete reports whether the whole ID set was supplied.
func (d PhotoIDDetails) Complete() bool { return d.IDType != "" && d.IDNumber != "" }

// BVNNINDetails is the electronic identity form: both 11-digit numbers.
type BVNNINDetails struct {
	BVN string
	NIN string
}

// ParseBVNNIN reads both 11-digit numbers from one message in either order,
// labeled ("BVN: 22222222222") or positional (first is BVN, second NIN — the
// prompt's stated order). Returns the parsed set; callers decide what to do
// with partial captures.
func ParseBVNNIN(input string) BVNNINDetails {
	var out BVNNINDetails
	var unlabeled []string
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		matches := elevenRe.FindAllStringSubmatchIndex(line, -1)
		if len(matches) == 0 {
			continue
		}
		// Assign each 11-digit token: a label in the text before the token
		// ("BVN: 222... ") names its field; otherwise tokens fill positionally
		// in the prompt's stated order (first BVN, then NIN).
		lineNamesBVN := strings.Contains(lower, "bvn")
		lineNamesNIN := strings.Contains(lower, "nin")
		prevEnd := 0
		for _, m := range matches {
			number := line[m[2]:m[3]]
			context := strings.ToLower(line[prevEnd:m[0]])
			prevEnd = m[1]
			switch {
			case strings.Contains(context, "bvn") || (lineNamesBVN && !lineNamesNIN):
				if out.BVN == "" {
					out.BVN = number
				}
			case strings.Contains(context, "nin") || (lineNamesNIN && !lineNamesBVN):
				if out.NIN == "" {
					out.NIN = number
				}
			default:
				unlabeled = append(unlabeled, number)
			}
		}
	}
	for _, number := range unlabeled {
		if out.BVN == "" {
			out.BVN = number
		} else if out.NIN == "" && number != out.BVN {
			out.NIN = number
		}
	}
	return out
}

// Missing reports which numbers are still owed (missingParts adds the
// "Still need your ..." wrapper, so the field names stay bare).
func (d BVNNINDetails) Missing() []string {
	var missing []string
	if d.BVN == "" {
		missing = append(missing, "11-digit BVN")
	}
	if d.NIN == "" {
		missing = append(missing, "11-digit NIN")
	}
	return missing
}

// Captured lists the numbers supplied, for the re-ask echo.
func (d BVNNINDetails) Captured() []string {
	var captured []string
	if d.BVN != "" {
		captured = append(captured, "your BVN")
	}
	if d.NIN != "" {
		captured = append(captured, "your NIN")
	}
	return captured
}

// Complete reports whether both numbers were supplied.
func (d BVNNINDetails) Complete() bool { return d.BVN != "" && d.NIN != "" }

// EDDDetails is the Tier 3 enhanced-due-diligence form: an address-proof
// reference (or SELF-DECLARED) and a source of funds.
type EDDDetails struct {
	AddressProof  string // the reference, or "SELF-DECLARED"
	SourceOfFunds string // SALARY | BUSINESS | INVESTMENTS | GIFTS | OTHER
}

var sourceOfFundsWords = []string{"SALARY", "BUSINESS", "INVESTMENTS", "GIFTS", "OTHER"}

// ParseEDDDetails reads the address-proof reference and the source-of-funds
// word from one message. SELF-DECLARED maps to the address proof.
func ParseEDDDetails(input string) EDDDetails {
	var out EDDDetails
	upper := strings.ToUpper(input)
	if strings.Contains(upper, "SELF-DECLARED") || strings.Contains(upper, "SELF DECLARED") {
		out.AddressProof = "SELF-DECLARED"
	}
	for _, word := range sourceOfFundsWords {
		if strings.Contains(upper, word) {
			out.SourceOfFunds = word
			break
		}
	}
	if out.AddressProof == "" {
		// The remaining text minus the source-of-funds word and labels is the
		// reference; keep the first line long enough to be one. A labeled line
		// ("Utility ref: ...", "Address proof: ...") contributes its value.
		for _, line := range strings.Split(input, "\n") {
			line = strings.TrimSpace(line)
			lower := strings.ToLower(line)
			if strings.HasPrefix(lower, "utility") || strings.HasPrefix(lower, "address proof") || strings.HasPrefix(lower, "ref") || strings.HasPrefix(lower, "reference") {
				line = stripLabel(line)
			}
			if word := sourceOfFundsIn(line); word != "" {
				line = strings.TrimSpace(strings.Replace(line, word, "", 1))
			}
			if len([]rune(line)) >= 4 {
				out.AddressProof = line
				break
			}
		}
	}
	return out
}

// sourceOfFundsIn returns the source-of-funds word appearing in s, if any.
func sourceOfFundsIn(s string) string {
	upper := strings.ToUpper(s)
	for _, word := range sourceOfFundsWords {
		if strings.Contains(upper, word) {
			return word
		}
	}
	return ""
}

// Missing reports which EDD parts are still owed (missingParts adds the
// "Still need your ..." wrapper, so the field names stay bare).
func (d EDDDetails) Missing() []string {
	var missing []string
	if d.AddressProof == "" {
		missing = append(missing, "utility bill reference (or SELF-DECLARED)")
	}
	if d.SourceOfFunds == "" {
		missing = append(missing, "source of funds (SALARY, BUSINESS, INVESTMENTS, GIFTS, or OTHER)")
	}
	return missing
}

// Captured lists the parts supplied, for the re-ask echo.
func (d EDDDetails) Captured() []string {
	var captured []string
	if d.AddressProof != "" {
		captured = append(captured, "your address proof")
	}
	if d.SourceOfFunds != "" {
		captured = append(captured, "your source of funds ("+d.SourceOfFunds+")")
	}
	return captured
}

// Complete reports whether the whole EDD set was supplied.
func (d EDDDetails) Complete() bool { return d.AddressProof != "" && d.SourceOfFunds != "" }

// stripLabel removes a trailing "field:" label, returning just the value.
func stripLabel(line string) string {
	if i := strings.Index(line, ":"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return strings.TrimSpace(line)
}

// collapseSpaces trims and collapses internal whitespace runs to single
// spaces, cleaning residue left after a date token is removed.
func collapseSpaces(line string) string {
	return strings.Join(strings.Fields(line), " ")
}

// residueAfterDOB removes the date token and a leading date label ("born",
// "dob", "date of birth") from a line, leaving the rest as address text.
func residueAfterDOB(line string) string {
	line = collapseSpaces(dobISORe.ReplaceAllString(line, ""))
	lower := strings.ToLower(line)
	for _, label := range []string{"date of birth", "dateofbirth", "dob", "born"} {
		if strings.HasPrefix(lower, label) {
			return collapseSpaces(line[len(label):])
		}
	}
	return line
}

// promptListing renders the numbered field list an aggregated prompt shows.
func promptListing(fields []string) string {
	out := ""
	for i, field := range fields {
		out += fmt.Sprintf("%d. %s\n", i+1, field)
	}
	return out
}
