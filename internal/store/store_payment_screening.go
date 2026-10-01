package store

// Spec G2 (CBN_AML_COMPLIANCE_SPEC.md §3.5): payment-time counterparty
// screening. The recorded screening row is the authoritative decision for a
// movement: the payout gate consults it (a re-attempt of the same movement
// reuses its recorded decision instead of re-screening), payouts dispatch
// only past a clear decision, and held movements (ambiguous hits) are
// dispositioned from the admin queue. Auditing happens at the app layer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/kyc"
)

// Rails recorded on payment_screenings. The wallet-withdrawal movement ref
// is exactly the allowance reservation ref; the payout ref is
// "payout:<batch_no>:<destination_id>" — the allowance reservation stays
// batch-keyed ("payout:<batch_no>", one money intent per batch), while the
// screening is destination-keyed because a re-created payout to a different
// account is a new counterparty that must re-screen.
const (
	RailMerchantPayout   = "merchant_payout"
	RailWalletWithdrawal = "wallet_withdrawal"
)

// payoutMovementRef composes the payout screening ref.
func payoutMovementRef(batchNo string, destinationID uuid.UUID) string {
	return "payout:" + batchNo + ":" + destinationID.String()
}

// PaymentScreening is one recorded counterparty screen for a money-out
// movement, keyed by (rail, movement_ref).
type PaymentScreening struct {
	ID               int64
	Rail             string
	MovementRef      string
	SubjectID        uuid.UUID
	AmountKobo       int64
	CounterpartyName string
	AccountNumber    string
	BankCode         string
	Provider         string
	ProviderRef      string
	Decision         string
	MatchedNames     []string
	Message          string
	CreatedAt        time.Time
}

// ErrMovementHeld is returned by ScreenPayout when the counterparty screen
// was ambiguous: the payout row is held for review and must not dispatch.
// Callers treat it as a control outcome, not a system failure.
var ErrMovementHeld = errors.New("movement held for counterparty-screen review")

// RecordPaymentScreening persists (or returns the already-recorded) screening
// row for a movement. The UNIQUE(rail, movement_ref) key makes replays
// idempotent: on conflict the stored row is returned untouched, so a
// re-attempt reads the original decision rather than re-screening.
func (s *Store) RecordPaymentScreening(ctx context.Context, scr PaymentScreening) (PaymentScreening, error) {
	names, _ := json.Marshal(scr.MatchedNames)
	if names == nil {
		names = []byte("[]")
	}
	rows, err := s.pool.Query(ctx, `
		INSERT INTO payment_screenings(rail,movement_ref,subject_id,amount_kobo,counterparty_name,account_number,bank_code,provider,provider_ref,decision,matched_names,message)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12)
		ON CONFLICT (rail, movement_ref) DO NOTHING
		RETURNING `+paymentScreeningCols,
		scr.Rail, scr.MovementRef, scr.SubjectID, scr.AmountKobo, scr.CounterpartyName,
		scr.AccountNumber, scr.BankCode, scr.Provider, scr.ProviderRef, scr.Decision,
		string(names), scr.Message)
	if err != nil {
		return PaymentScreening{}, fmt.Errorf("record payment screening: %w", err)
	}
	out, scanErr := collectPaymentScreening(rows)
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			// Already recorded: return the stored decision untouched.
			return s.PaymentScreeningByRef(ctx, scr.Rail, scr.MovementRef)
		}
		return PaymentScreening{}, scanErr
	}
	return out, nil
}

const paymentScreeningCols = `id,rail,movement_ref,subject_id,amount_kobo,counterparty_name,account_number,bank_code,provider,provider_ref,decision,matched_names::text,message,created_at`

// PaymentScreeningByRef returns the recorded screening for a movement, or
// pgx.ErrNoRows when none exists.
func (s *Store) PaymentScreeningByRef(ctx context.Context, rail, movementRef string) (PaymentScreening, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+paymentScreeningCols+` FROM payment_screenings WHERE rail=$1 AND movement_ref=$2`,
		rail, movementRef)
	if err != nil {
		return PaymentScreening{}, fmt.Errorf("payment screening by ref: %w", err)
	}
	return collectPaymentScreening(rows)
}

// PaymentScreeningByID returns one screening row by id, or pgx.ErrNoRows.
func (s *Store) PaymentScreeningByID(ctx context.Context, id int64) (PaymentScreening, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+paymentScreeningCols+` FROM payment_screenings WHERE id=$1`, id)
	if err != nil {
		return PaymentScreening{}, fmt.Errorf("payment screening by id: %w", err)
	}
	return collectPaymentScreening(rows)
}

// ListPaymentScreenings returns recent screening rows, optionally restricted
// to the ambiguous (open-hold) decisions for the review queue.
func (s *Store) ListPaymentScreenings(ctx context.Context, openHoldsOnly bool, limit int) ([]PaymentScreening, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + paymentScreeningCols + ` FROM payment_screenings`
	if openHoldsOnly {
		q += ` WHERE decision IN ('possible','strong')`
	}
	q += ` ORDER BY created_at DESC LIMIT $1`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list payment screenings: %w", err)
	}
	defer rows.Close()
	var out []PaymentScreening
	for rows.Next() {
		scr, err := scanPaymentScreening(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, scr)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func collectPaymentScreening(rows pgx.Rows) (PaymentScreening, error) {
	defer rows.Close()
	if !rows.Next() {
		// pgx Rows reports nil after exhausting an empty result set, so the
		// not-found case must be raised here explicitly (QueryRow does this
		// for us, but the INSERT ... DO NOTHING RETURNING path needs Rows).
		if err := rows.Err(); err != nil {
			return PaymentScreening{}, err
		}
		return PaymentScreening{}, pgx.ErrNoRows
	}
	return scanPaymentScreening(rows)
}

func scanPaymentScreening(row rowScanner) (PaymentScreening, error) {
	var scr PaymentScreening
	var namesText string
	err := row.Scan(&scr.ID, &scr.Rail, &scr.MovementRef, &scr.SubjectID, &scr.AmountKobo,
		&scr.CounterpartyName, &scr.AccountNumber, &scr.BankCode, &scr.Provider,
		&scr.ProviderRef, &scr.Decision, &namesText, &scr.Message, &scr.CreatedAt)
	if err != nil {
		return scr, err
	}
	scr.MatchedNames = []string{}
	if namesText != "" {
		_ = json.Unmarshal([]byte(namesText), &scr.MatchedNames)
	}
	return scr, nil
}

// screenFn performs the vendor call. It returns the decision vocabulary
// (kyc.Screen*), matched names, provider name/ref and a human message; a
// non-nil error means the screen could not complete (provider outage).
type screenFn func(context.Context) (decision, provider, providerRef, message string, matched []string, err error)

// ScreenPayout is the G2 gate for the settlement rail. It screens the payout
// destination's account holder (consulting any recorded decision for the
// movement first) and enforces the outcome:
//
//   - clear/manually_cleared: the payout is returned unchanged and may dispatch;
//   - possible/strong:        the payout is moved to held and ErrMovementHeld
//     is returned — dispatch never sees it until the queue releases it;
//   - blocked:                the payout is failed with the interdiction message
//     and an error is returned.
//
// The movement ref carries the destination id: a reversed payout re-created
// for the same batch to a DIFFERENT account is a new counterparty and must
// re-screen, so the recorded decision must not leak across destinations.
//
// Nothing here touches allowance_usage: on a hold or interdiction the
// money-out reservation stays in place, so the amount remains counted against
// the merchant's ceiling exactly as the spec requires.
//
// A screener outage (screen returns an error) also holds the payout: when in
// doubt the money stays put, and a transient vendor error must neither clear
// a movement nor look like an interdiction.
func (s *Store) ScreenPayout(ctx context.Context, payout Payout, batchNo string, dest MerchantSettlementAccount, screen screenFn) (Payout, error) {
	movementRef := payoutMovementRef(batchNo, dest.ID)
	recorded, err := s.PaymentScreeningByRef(ctx, RailMerchantPayout, movementRef)
	switch {
	case err == nil:
		return s.applyPayoutScreenDecision(ctx, payout, recorded)
	case !errors.Is(err, pgx.ErrNoRows):
		return Payout{}, err
	}
	decision, provider, providerRef, message, matched, err := screen(ctx)
	if err != nil {
		return s.holdPayoutForScreen(ctx, payout, "screening provider unavailable: "+err.Error())
	}
	recorded, err = s.RecordPaymentScreening(ctx, PaymentScreening{
		Rail:             RailMerchantPayout,
		MovementRef:      movementRef,
		SubjectID:        payout.MerchantID,
		AmountKobo:       payout.AmountKobo,
		CounterpartyName: dest.AccountName,
		AccountNumber:    dest.AccountNumber,
		BankCode:         dest.BankCode,
		Provider:         provider,
		ProviderRef:      providerRef,
		Decision:         decision,
		MatchedNames:     matched,
		Message:          message,
	})
	if err != nil {
		return Payout{}, err
	}
	return s.applyPayoutScreenDecision(ctx, payout, recorded)
}

// applyPayoutScreenDecision enforces a (possibly recorded) decision on the
// payout row.
func (s *Store) applyPayoutScreenDecision(ctx context.Context, payout Payout, recorded PaymentScreening) (Payout, error) {
	switch recorded.Decision {
	case kyc.ScreenClear, kyc.ScreenManuallyCleared:
		return payout, nil
	case kyc.ScreenPossible, kyc.ScreenStrong:
		return s.holdPayoutForScreen(ctx, payout, screenHoldMessage(recorded))
	case kyc.ScreenBlocked:
		msg := "counterparty screening interdiction: " + recorded.Message
		if _, err := s.pool.Exec(ctx,
			`UPDATE payouts SET status='failed',last_error=$2 WHERE id=$1 AND status='queued'`,
			payout.ID, msg); err != nil {
			return Payout{}, fmt.Errorf("interdict payout: %w", err)
		}
		return Payout{}, fmt.Errorf("payout interdicted by counterparty screening: %s", recorded.Message)
	default:
		return Payout{}, fmt.Errorf("unknown payment screening decision %q", recorded.Decision)
	}
}

// holdPayoutForScreen moves a queued payout to held with the given reason and
// returns ErrMovementHeld.
func (s *Store) holdPayoutForScreen(ctx context.Context, payout Payout, reason string) (Payout, error) {
	if _, err := s.pool.Exec(ctx,
		`UPDATE payouts SET status='held',last_error=$2 WHERE id=$1 AND status='queued'`,
		payout.ID, reason); err != nil {
		return Payout{}, fmt.Errorf("hold payout for screening: %w", err)
	}
	return Payout{}, ErrMovementHeld
}

// screenHoldMessage renders the reviewer-facing hold reason.
func screenHoldMessage(scr PaymentScreening) string {
	msg := "counterparty screening requires review: " + scr.Message
	if len(scr.MatchedNames) > 0 {
		msg += " (matched: " + strings.Join(scr.MatchedNames, ", ") + ")"
	}
	return msg
}

// ReleaseHeldPayout applies a reviewer disposition to a held payout.
// Approve re-queues it for dispatch and flips the recorded screening to
// manually_cleared, so the re-attempt passes the gate without re-screening.
// Reject reverses the payout (the house cancelled-intent status, so a fresh
// payout row can be created for a different destination), releases the
// batch's money-out reservation, and reopens the batch. Every disposition is
// audited by the caller.
func (s *Store) ReleaseHeldPayout(ctx context.Context, payoutID uuid.UUID, approve bool, reviewer, reason string) (Payout, error) {
	payout, err := s.PayoutByID(ctx, payoutID)
	if err != nil {
		return Payout{}, err
	}
	if payout.Status != PayoutHeld {
		return Payout{}, fmt.Errorf("payout %s is not held (status %s)", payoutID, payout.Status)
	}
	batch, err := s.SettlementBatchByID(ctx, payout.BatchID)
	if err != nil {
		return Payout{}, err
	}
	if approve {
		tag, err := s.pool.Exec(ctx, `
			UPDATE payment_screenings SET decision='manually_cleared', message=$1
			WHERE rail=$2 AND movement_ref=$3 AND decision IN ('possible','strong')`,
			"held decision cleared by "+reviewer+": "+reason, RailMerchantPayout, payoutMovementRef(batch.BatchNo, payout.DestinationID))
		if err != nil {
			return Payout{}, fmt.Errorf("clear payout screening: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return Payout{}, errors.New("no open screening row for the held payout")
		}
		rtag, err := s.pool.Exec(ctx,
			`UPDATE payouts SET status='queued',last_error='' WHERE id=$1 AND status='held'`, payoutID)
		if err != nil {
			return Payout{}, fmt.Errorf("release payout: %w", err)
		}
		if rtag.RowsAffected() == 0 {
			return Payout{}, errors.New("payout left the held state before release")
		}
		return s.PayoutByID(ctx, payoutID)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE payouts SET status='reversed',last_error=$2 WHERE id=$1`,
		payoutID, "counterparty screening rejected by "+reviewer+": "+reason); err != nil {
		return Payout{}, err
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM allowance_usage WHERE transaction_ref=$1`, "payout:"+batch.BatchNo); err != nil {
		return Payout{}, err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE settlement_batches SET status='open' WHERE id=$1 AND status='scheduled'`, payout.BatchID); err != nil {
		return Payout{}, err
	}
	return s.PayoutByID(ctx, payoutID)
}

// ScreenUserWithdrawal records and enforces a counterparty screen for a
// wallet withdrawal. The withdrawal reserves its allowance under a fresh ref
// before this runs. Decisions are returned to the caller for enforcement:
//
//   - clear:               the withdrawal may proceed;
//   - possible/strong/blocked: the withdrawal must be refused and the
//     reservation left in place — the movement is stopped but stays counted
//     against the subject's ceiling (the hold-with-reservation semantics),
//     and the recorded row surfaces in the review queue;
//   - screen error (outage): the caller should refuse and release the
//     reservation — a system failure must not silently eat the user's
//     ceiling, and unlike the payout rail there is no reviewer release path
//     for a withdrawal reservation.
func (s *Store) ScreenUserWithdrawal(ctx context.Context, userID uuid.UUID, reservationRef string, amountKobo int64, counterpartyName string, screen screenFn) (string, error) {
	recorded, err := s.PaymentScreeningByRef(ctx, RailWalletWithdrawal, reservationRef)
	switch {
	case err == nil:
		return recorded.Decision, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	decision, provider, providerRef, message, matched, err := screen(ctx)
	if err != nil {
		return "", err
	}
	if _, err := s.RecordPaymentScreening(ctx, PaymentScreening{
		Rail:             RailWalletWithdrawal,
		MovementRef:      reservationRef,
		SubjectID:        userID,
		AmountKobo:       amountKobo,
		CounterpartyName: counterpartyName,
		Provider:         provider,
		ProviderRef:      providerRef,
		Decision:         decision,
		MatchedNames:     matched,
		Message:          message,
	}); err != nil {
		return "", err
	}
	return decision, nil
}
