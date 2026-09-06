// Bank directory lookups. The bank list (code + name + short name) is seeded
// by migration 061 so chat and web flows can accept a bank name and hang onto
// the canonical NUBAN bank code for the NIP payout.
package store

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// ErrBankNotFound is returned by bank lookups when no directory entry matches.
var ErrBankNotFound = errors.New("bank not found")

// Bank is one entry from the embedded bank directory.
type Bank struct {
	Code      string
	Name      string
	ShortName string
}

// SearchBanks returns active banks whose name, short name, or known aliases
// match query. Numeric codes are matched exactly; everything else uses a
// substring/trigram match over the seeded search terms. Results are ordered
// by relevance.
func (s *Store) SearchBanks(ctx context.Context, query string, limit int) ([]Bank, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args := []any{limit}
	sql := `
		SELECT code, name, short_name
		FROM bank_directory
		WHERE active = true`
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, q)
		sql += ` AND (
			search_terms ILIKE '%' || $2 || '%'
			OR word_similarity($2, search_terms) > 0.3
		)`
		sql += ` ORDER BY GREATEST(
			CASE WHEN search_terms ILIKE $2 || '%' THEN 2.0 ELSE 0.0 END,
			word_similarity($2, search_terms)
		) DESC, sort_order, name`
	} else {
		sql += ` ORDER BY sort_order, name`
	}
	sql += ` LIMIT $1`

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var banks []Bank
	for rows.Next() {
		var b Bank
		if err := rows.Scan(&b.Code, &b.Name, &b.ShortName); err != nil {
			return nil, err
		}
		banks = append(banks, b)
	}
	return banks, rows.Err()
}

// BankByCode resolves a NUBAN-style bank code to its directory entry.
func (s *Store) BankByCode(ctx context.Context, code string) (Bank, error) {
	var b Bank
	err := s.pool.QueryRow(ctx,
		`SELECT code, name, short_name FROM bank_directory WHERE active = true AND code = $1`,
		strings.TrimSpace(code)).Scan(&b.Code, &b.Name, &b.ShortName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Bank{}, ErrBankNotFound
	}
	if err != nil {
		return Bank{}, err
	}
	return b, nil
}

// BankByNormalizedName resolves a caller-typed name to the directory entry
// whose compacted "name short_name" form equals the compacted input
// (e.g. "guaranty trust bank" == "guarantytrustbank"). "GTBank" matches the
// compacted short name, so the exact-match path needs no aliases.
func (s *Store) BankByNormalizedName(ctx context.Context, name string) (Bank, error) {
	var b Bank
	err := s.pool.QueryRow(ctx,
		`SELECT code, name, short_name FROM bank_directory WHERE active = true AND normalized = $1`,
		compactBankName(name)).Scan(&b.Code, &b.Name, &b.ShortName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Bank{}, ErrBankNotFound
	}
	if err != nil {
		return Bank{}, err
	}
	return b, nil
}

// compactBankName lowers a name and strips punctuation so that a caller-typed
// "Guaranty Trust Bank" and the seeded "guarantytrustbank" share a key.
func compactBankName(value string) string {
	return strings.ToLower(strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), ""))
}