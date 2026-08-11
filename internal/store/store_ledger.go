// C16: double-entry append-only journal. Every money movement is posted as a
// balanced pair (one debit, one credit) under a shared journal reference, and
// chained with SHA-256 hashes like the audit log and archive ledger.
// Corrections are never in-place: they are new offsetting entries linked back
// through reversal_of, satisfying CBN record-keeping expectations.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Chart of accounts. Keeping codes as Go constants (no mutable accounts table)
// guarantees every posting references a known, balanced book.
const (
	LedgerAccountOperatingBank   = "1100_operating_bank"
	LedgerAccountCustomerFloat   = "2100_customer_float"
	LedgerAccountMerchantPayable = "3100_merchant_payable"
	LedgerAccountSalesRevenue    = "4100_sales_revenue"
	LedgerAccountProviderCost    = "5100_provider_cost"
	LedgerAccountThriftPool      = "6100_thrift_pool"
)

// LedgerEntry is one side of a double-entry posting.
type LedgerEntry struct {
	ID          int64
	JournalRef  string
	SourceType  string
	SourceID    string
	EntryType   string // "debit" or "credit"
	Account     string
	AmountKobo  int64
	Currency    string
	Description string
	PostedBy    string
	ReversalOf  int64
	PrevHash    string
	Hash        string
	CreatedAt   time.Time
}

// ledgerHash chains a ledger row to its predecessor, mirroring the audit log
// and archive ledger tamper-evident scheme.
func ledgerHash(prevHash, journalRef, sourceType, sourceID, entryType, account, currency, description, postedBy string, amountKobo, reversalOf int64) string {
	fields := []string{
		prevHash, journalRef, sourceType, sourceID, entryType, account,
		currency, description, postedBy, fmt.Sprintf("%d", amountKobo), fmt.Sprintf("%d", reversalOf),
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "|")))
	return hex.EncodeToString(sum[:])
}

// appendLedgerEntryTx inserts one immutable ledger row. The insert is
// serialized on the tail row so the hash chain cannot fork under concurrency.
func (s *Store) appendLedgerEntryTx(ctx context.Context, tx pgx.Tx, entry LedgerEntry) (int64, error) {
	var prevHash string
	if err := tx.QueryRow(ctx, `SELECT hash FROM ledger_entries ORDER BY id DESC LIMIT 1 FOR UPDATE`).Scan(&prevHash); err != nil && err != pgx.ErrNoRows {
		return 0, err
	}
	entry.PrevHash = prevHash
	entry.Hash = ledgerHash(prevHash, entry.JournalRef, entry.SourceType, entry.SourceID, entry.EntryType,
		entry.Account, entry.Currency, entry.Description, entry.PostedBy, entry.AmountKobo, entry.ReversalOf)
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_entries(journal_ref,source_type,source_id,entry_type,account,amount_kobo,currency,
			description,posted_by,reversal_of,prev_hash,hash)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id`,
		entry.JournalRef, entry.SourceType, entry.SourceID, entry.EntryType, entry.Account,
		entry.AmountKobo, entry.Currency, entry.Description, entry.PostedBy, orZeroReversal(entry.ReversalOf),
		entry.PrevHash, entry.Hash,
	).Scan(&id)
	return id, err
}

func orZeroReversal(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// postLedgerPair writes a balanced debit+credit pair for one money movement.
// Callers must hold an open transaction (the same tx that mutates the domain
// record) so the journal stays atomic with the business change.
func (s *Store) postLedgerPair(ctx context.Context, tx pgx.Tx, journalRef, sourceType, sourceID, debitAccount, creditAccount, currency, description, postedBy string, amountKobo int64) error {
	if amountKobo <= 0 {
		return fmt.Errorf("ledger posting requires a positive amount, got %d", amountKobo)
	}
	if debitAccount == creditAccount {
		return fmt.Errorf("ledger posting debits and credits the same account %q", debitAccount)
	}
	if _, err := s.appendLedgerEntryTx(ctx, tx, LedgerEntry{
		JournalRef: journalRef, SourceType: sourceType, SourceID: sourceID, EntryType: "debit",
		Account: debitAccount, AmountKobo: amountKobo, Currency: currency,
		Description: description, PostedBy: postedBy,
	}); err != nil {
		return err
	}
	if _, err := s.appendLedgerEntryTx(ctx, tx, LedgerEntry{
		JournalRef: journalRef, SourceType: sourceType, SourceID: sourceID, EntryType: "credit",
		Account: creditAccount, AmountKobo: amountKobo, Currency: currency,
		Description: description, PostedBy: postedBy,
	}); err != nil {
		return err
	}
	return nil
}

// PostLedgerReversal writes offsetting entries for every row sharing a journal
// reference, linking each through reversal_of. The book returns to zero for
// that reference without ever mutating an original row. It is a no-op (with an
// error) when the reference is already reversed or unknown.
func (s *Store) PostLedgerReversal(ctx context.Context, journalRef, reason, postedBy string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var rows pgx.Rows
	rows, err = tx.Query(ctx, `
		SELECT id, journal_ref, source_type, source_id, entry_type, account,
		       amount_kobo, currency, description, posted_by,
		       COALESCE(reversal_of,0), prev_hash, hash
		FROM ledger_entries WHERE journal_ref=$1 ORDER BY id ASC`, journalRef)
	if err != nil {
		return 0, err
	}
	var originals []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.JournalRef, &e.SourceType, &e.SourceID, &e.EntryType,
			&e.Account, &e.AmountKobo, &e.Currency, &e.Description, &e.PostedBy,
			&e.ReversalOf, &e.PrevHash, &e.Hash); err != nil {
			rows.Close()
			return 0, err
		}
		originals = append(originals, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(originals) == 0 {
		return 0, fmt.Errorf("no ledger entries for journal reference %q", journalRef)
	}
	for _, original := range originals {
		if original.ReversalOf != 0 {
			return 0, fmt.Errorf("journal reference %q is already reversed", journalRef)
		}
	}
	opposite := map[string]string{"debit": "credit", "credit": "debit"}
	reversed := 0
	for _, original := range originals {
		if _, err := s.appendLedgerEntryTx(ctx, tx, LedgerEntry{
			JournalRef: journalRef, SourceType: "reversal", SourceID: journalRef,
			EntryType: opposite[original.EntryType], Account: original.Account,
			AmountKobo: original.AmountKobo, Currency: original.Currency,
			Description: "Reversal: " + reason, PostedBy: postedBy, ReversalOf: original.ID,
		}); err != nil {
			return 0, err
		}
		reversed++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return reversed, nil
}

// ListLedgerEntries returns recent journal rows, newest first.
func (s *Store) ListLedgerEntries(ctx context.Context, limit int) ([]LedgerEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, journal_ref, source_type, source_id, entry_type, account,
		       amount_kobo, currency, description, posted_by, COALESCE(reversal_of,0),
		       prev_hash, hash, created_at
		FROM ledger_entries ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.JournalRef, &e.SourceType, &e.SourceID, &e.EntryType,
			&e.Account, &e.AmountKobo, &e.Currency, &e.Description, &e.PostedBy,
			&e.ReversalOf, &e.PrevHash, &e.Hash, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// LedgerAccountBalance is one chart-of-accounts net position.
type LedgerAccountBalance struct {
	Account    string
	NetKobo    int64
	DebitKobo  int64
	CreditKobo int64
}

// LedgerBalanceSummary nets debit vs credit per account. A sound double-entry
// book always sums to zero.
func (s *Store) LedgerBalanceSummary(ctx context.Context) ([]LedgerAccountBalance, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT account,
		       COALESCE(SUM(CASE WHEN entry_type='debit' THEN amount_kobo ELSE -amount_kobo END),0) AS net_kobo,
		       COALESCE(SUM(CASE WHEN entry_type='debit' THEN amount_kobo ELSE 0 END),0) AS debit_kobo,
		       COALESCE(SUM(CASE WHEN entry_type='credit' THEN amount_kobo ELSE 0 END),0) AS credit_kobo
		FROM ledger_entries GROUP BY account ORDER BY account`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var balances []LedgerAccountBalance
	for rows.Next() {
		var b LedgerAccountBalance
		if err := rows.Scan(&b.Account, &b.NetKobo, &b.DebitKobo, &b.CreditKobo); err != nil {
			return nil, err
		}
		balances = append(balances, b)
	}
	return balances, rows.Err()
}

// VerifyLedgerChain recomputes every hash and checks linkage. It returns the
// number of rows and the index of the first broken entry (-1 if sound).
func (s *Store) VerifyLedgerChain(ctx context.Context) (int, int, error) {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries`).Scan(&count); err != nil {
		return 0, -1, err
	}
	var cursor int64
	var prevHash string
	expected := prevHash
	broken := -1
	seen := 0
	for {
		rows, err := s.pool.Query(ctx, `
			SELECT id, journal_ref, source_type, source_id, entry_type, account,
			       amount_kobo, currency, description, posted_by, COALESCE(reversal_of,0),
			       prev_hash, hash
			FROM ledger_entries WHERE id > $1 ORDER BY id ASC LIMIT 1000`, cursor)
		if err != nil {
			return 0, -1, err
		}
		var batch []LedgerEntry
		for rows.Next() {
			var e LedgerEntry
			if err := rows.Scan(&e.ID, &e.JournalRef, &e.SourceType, &e.SourceID, &e.EntryType,
				&e.Account, &e.AmountKobo, &e.Currency, &e.Description, &e.PostedBy,
				&e.ReversalOf, &e.PrevHash, &e.Hash); err != nil {
				rows.Close()
				return 0, -1, err
			}
			batch = append(batch, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, -1, err
		}
		if len(batch) == 0 {
			break
		}
		for _, entry := range batch {
			recomputed := ledgerHash(entry.PrevHash, entry.JournalRef, entry.SourceType, entry.SourceID,
				entry.EntryType, entry.Account, entry.Currency, entry.Description, entry.PostedBy,
				entry.AmountKobo, entry.ReversalOf)
			if entry.PrevHash != expected || recomputed != entry.Hash {
				broken = seen
				return count, broken, nil
			}
			prevHash = entry.Hash
			expected = entry.Hash
			cursor = entry.ID
			seen++
		}
	}
	return count, broken, nil
}
