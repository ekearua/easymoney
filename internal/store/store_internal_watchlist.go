package store

// Spec G3 (docs/compliance/CBN_AML_COMPLIANCE_SPEC.md §5): the owned internal watchlist —
// the platform's own list of named identities that must never be auto-
// cleared by any screening provider. Entries are reviewer-owned (reason +
// listed_by); enforcement rewrites any screening decision on a watchlisted
// identity to "blocked" and opens a review case (see RecordScreeningResult).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// InternalWatchlistEntry is one platform-owned listed identity.
type InternalWatchlistEntry struct {
	ID             uuid.UUID
	FullName       string
	DateOfBirth    *time.Time
	PhoneNumber    string
	BankAccountRef string
	Reason         string
	ListedBy       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func normalizeWatchlistName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// nullUUID maps the zero UUID to SQL NULL for optional uuid columns.
func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// AddInternalWatchlistEntry lists an identity. The unique identity key makes
// re-adding the same name/DOB idempotent; re-adding refreshes the reason and
// the lister.
func (s *Store) AddInternalWatchlistEntry(ctx context.Context, e InternalWatchlistEntry) (InternalWatchlistEntry, error) {
	if normalizeWatchlistName(e.FullName) == "" {
		return InternalWatchlistEntry{}, fmt.Errorf("internal watchlist: full_name is required")
	}
	if strings.TrimSpace(e.Reason) == "" || strings.TrimSpace(e.ListedBy) == "" {
		return InternalWatchlistEntry{}, fmt.Errorf("internal watchlist: reason and listed_by are required")
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO internal_watchlist(full_name, date_of_birth, phone_number, bank_account_ref, reason, listed_by)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (lower(btrim(full_name)), COALESCE(date_of_birth, DATE '0001-01-01')) DO UPDATE
		SET reason=EXCLUDED.reason, listed_by=EXCLUDED.listed_by, phone_number=EXCLUDED.phone_number,
		    bank_account_ref=EXCLUDED.bank_account_ref, updated_at=now()
		RETURNING id, created_at, updated_at`,
		strings.Join(strings.Fields(e.FullName), " "), e.DateOfBirth,
		strings.TrimSpace(e.PhoneNumber), strings.TrimSpace(e.BankAccountRef),
		strings.TrimSpace(e.Reason), strings.TrimSpace(e.ListedBy)).
		Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return InternalWatchlistEntry{}, fmt.Errorf("add internal watchlist entry: %w", err)
	}
	return e, nil
}

// RemoveInternalWatchlistEntry delists an identity by id.
func (s *Store) RemoveInternalWatchlistEntry(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM internal_watchlist WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("remove internal watchlist entry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("internal watchlist entry %s not found", id)
	}
	return nil
}

// ListInternalWatchlist returns the list, optionally filtered by a name
// substring, newest first.
func (s *Store) ListInternalWatchlist(ctx context.Context, nameFilter string, limit int) ([]InternalWatchlistEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT id, full_name, date_of_birth, phone_number, bank_account_ref, reason, listed_by, created_at, updated_at
	      FROM internal_watchlist`
	args := []any{}
	if f := strings.TrimSpace(nameFilter); f != "" {
		q += ` WHERE lower(btrim(full_name)) LIKE lower(btrim($1)) || '%'`
		args = append(args, "%"+f+"%")
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list internal watchlist: %w", err)
	}
	defer rows.Close()
	var entries []InternalWatchlistEntry
	for rows.Next() {
		var e InternalWatchlistEntry
		if err := rows.Scan(&e.ID, &e.FullName, &e.DateOfBirth, &e.PhoneNumber,
			&e.BankAccountRef, &e.Reason, &e.ListedBy, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// IsInternallyWatchlisted reports whether the given identity (name plus
// optional DOB / phone / bank account reference) is on the platform's own
// list. The name must match exactly after normalisation when a DOB is
// supplied; without a DOB any DOB matches (the conservative reading for a
// list whose only action is "stop and review").
func (s *Store) IsInternallyWatchlisted(ctx context.Context, fullName string, dateOfBirth *time.Time, phoneNumber, bankAccountRef string) (bool, InternalWatchlistEntry, error) {
	name := normalizeWatchlistName(fullName)
	if name == "" {
		return false, InternalWatchlistEntry{}, nil
	}
	q := `SELECT id, full_name, date_of_birth, phone_number, bank_account_ref, reason, listed_by, created_at, updated_at
	      FROM internal_watchlist
	      WHERE lower(btrim(full_name)) = $1`
	args := []any{name}
	if dateOfBirth != nil {
		q += ` AND date_of_birth = $2`
		args = append(args, dateOfBirth)
	}
	if p := strings.TrimSpace(phoneNumber); p != "" {
		q += fmt.Sprintf(` AND phone_number = $%d`, len(args)+1)
		args = append(args, p)
	}
	if b := strings.TrimSpace(bankAccountRef); b != "" {
		q += fmt.Sprintf(` AND bank_account_ref = $%d`, len(args)+1)
		args = append(args, b)
	}
	var e InternalWatchlistEntry
	err := s.pool.QueryRow(ctx, q, args...).Scan(&e.ID, &e.FullName, &e.DateOfBirth, &e.PhoneNumber,
		&e.BankAccountRef, &e.Reason, &e.ListedBy, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, InternalWatchlistEntry{}, nil
	}
	if err != nil {
		return false, InternalWatchlistEntry{}, fmt.Errorf("internal watchlist check: %w", err)
	}
	return true, e, nil
}
