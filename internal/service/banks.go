// Bank-name resolution for pay-out flows. Users type either a NUBAN bank code
// or a bank name ("GTBank", "guaranty trust bank"); this maps it to the
// canonical code that the NIP payout expects, falling back to a picker when
// several banks match.
package service

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"whatsapp-payment-demo/internal/store"
)

// ErrBankNotFound is returned by ResolveBank when no bank matches the input.
var ErrBankNotFound = errors.New("bank not found")

// ErrBankAmbiguous is returned by ResolveBank when several banks match and
// the caller should render a picker from SearchBanks.
var ErrBankAmbiguous = errors.New("bank name matches multiple banks")

// BankResolution is the canonical outcome of resolving a bank input.
type BankResolution struct {
	Code string
	Name string
}

// ResolveBank turns a user-typed bank code or name into a canonical directory
// entry. All-digit input is treated as a code first (with the 3-10 digit
// acceptance window); names/short names/aliases are matched exactly first,
// then by single-result fuzzy search.
func ResolveBank(ctx context.Context, st *store.Store, input string) (BankResolution, error) {
	input = strings.TrimSpace(input)
	if looksNumeric(input) {
		b, err := st.BankByCode(ctx, input)
		if err == nil {
			return BankResolution{Code: b.Code, Name: b.Name}, nil
		}
		return BankResolution{}, ErrBankNotFound
	}
	norm := compactBankNameInput(input)
	if norm == "" {
		return BankResolution{}, ErrBankNotFound
	}
	candidates, err := st.SearchBanks(ctx, input, 5)
	if err != nil {
		return BankResolution{}, err
	}
	for _, candidate := range candidates {
		if compactBankNameInput(candidate.Name) == norm || compactBankNameInput(candidate.ShortName) == norm {
			return BankResolution{Code: candidate.Code, Name: candidate.Name}, nil
		}
	}
	switch len(candidates) {
	case 0:
		return BankResolution{}, ErrBankNotFound
	case 1:
		return BankResolution{Code: candidates[0].Code, Name: candidates[0].Name}, nil
	default:
		return BankResolution{}, ErrBankAmbiguous
	}
}

// looksNumeric reports whether value is entirely digits within the bank-code
// length window (NUBAN codes are 2-3 padded digits, mobile banks 3).
func looksNumeric(value string) bool {
	if value == "" || len(value) < 2 || len(value) > 10 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compactBankNameInput lowers a name and strips punctuation so a caller-typed
// "Guaranty Trust Bank" compares equal to the seeded compacted name.
func compactBankNameInput(value string) string {
	return strings.ToLower(strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), ""))
}