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
	// RailCashPickup screens a remittance recipient (a counterparty with no Xego
	// account) at POS-agent redemption. The movement ref is the remittance id.
	RailCashPickup = "cash_pickup"
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
	// ListVersion is the vendor list the counterparty was screened against.
	// The rescreen worker re-evaluates open holds whose recorded version
	// predates the newest one on the rail (vendor record §8(5)).
	ListVersion string
	Message     string
	CreatedAt   time.Time
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
		INSERT INTO payment_screenings(rail,movement_ref,subject_id,amount_kobo,counterparty_name,account_number,bank_code,provider,provider_ref,decision,matched_names,message,list_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13)
		ON CONFLICT (rail, movement_ref) DO NOTHING
		RETURNING `+paymentScreeningCols,
		scr.Rail, scr.MovementRef, scr.SubjectID, scr.AmountKobo, scr.CounterpartyName,
		scr.AccountNumber, scr.BankCode, scr.Provider, scr.ProviderRef, scr.Decision,
		string(names), scr.Message, scr.ListVersion)
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

const paymentScreeningCols = `id,rail,movement_ref,subject_id,amount_kobo,counterparty_name,account_number,bank_code,provider,provider_ref,decision,matched_names::text,message,list_version,created_at`

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

// PaymentScreeningForRef is PaymentScreeningByRef with an explicit found flag, so
// callers outside this package need not import pgx to tell "no recorded
// decision" from a real error.
func (s *Store) PaymentScreeningForRef(ctx context.Context, rail, movementRef string) (PaymentScreening, bool, error) {
	scr, err := s.PaymentScreeningByRef(ctx, rail, movementRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return PaymentScreening{}, false, nil
	}
	if err != nil {
		return PaymentScreening{}, false, err
	}
	return scr, true, nil
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
		&scr.ProviderRef, &scr.Decision, &namesText, &scr.Message, &scr.ListVersion, &scr.CreatedAt)
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
// (kyc.Screen*), matched names, provider name/ref, the vendor's list version
// and a human message; a non-nil error means the screen could not complete
// (provider outage).
type screenFn func(context.Context) (decision, provider, providerRef, listVersion, message string, matched []string, err error)

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
	decision, provider, providerRef, listVersion, message, matched, err := screen(ctx)
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
		ListVersion:      listVersion,
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
	decision, provider, providerRef, listVersion, message, matched, err := screen(ctx)
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
		ListVersion:      listVersion,
		Message:          message,
	}); err != nil {
		return "", err
	}
	return decision, nil
}

// ErrReevaluationInterdicted is returned by ReevaluateHeldPayout when the
// fresh screen on a list-version change blocks the counterparty outright.
var ErrReevaluationInterdicted = errors.New("held payout interdicted by list re-evaluation")

// HeldScreeningForRescreen couples an open-hold (possible/strong) payment
// screening with the held payout row and its batch, so the rescreen worker
// can re-screen the movement when the vendor list version moves.
type HeldScreeningForRescreen struct {
	Screening PaymentScreening
	PayoutID  uuid.UUID
	BatchID   uuid.UUID
}

// HeldScreeningsOnStaleList returns open-hold payout screenings whose
// recorded list_version predates the newest list_version in the store — the
// vendor list moved after the movement was held, so the recorded decision may
// be stale and the rescreen worker should re-evaluate it (vendor record
// §8(5)). It also returns the newest list_version used as the reference. An
// empty store yields no rows and an empty reference.
func (s *Store) HeldScreeningsOnStaleList(ctx context.Context, limit int) ([]HeldScreeningForRescreen, string, error) {
	if limit <= 0 || limit > 500 {
		limit = 25
	}
	var reference string
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(list_version,'') FROM payment_screenings ORDER BY created_at DESC LIMIT 1`).Scan(&reference); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("current list version: %w", err)
	}
	psCols := `ps.id,ps.rail,ps.movement_ref,ps.subject_id,ps.amount_kobo,ps.counterparty_name,` +
		`ps.account_number,ps.bank_code,ps.provider,ps.provider_ref,ps.decision,ps.matched_names::text,` +
		`ps.message,ps.list_version,ps.created_at`
	rows, err := s.pool.Query(ctx, `
		SELECT `+psCols+`, p.id, p.batch_id
		FROM payment_screenings ps
		JOIN payouts p ON p.status='held'
		JOIN settlement_batches b ON b.id = p.batch_id
		WHERE ps.rail=$1
		  AND ps.decision IN ('possible','strong')
		  AND ps.movement_ref = 'payout:' || b.batch_no || ':' || p.destination_id::text
		  AND ps.list_version <> $2
		ORDER BY ps.created_at DESC
		LIMIT $3`, RailMerchantPayout, reference, limit)
	if err != nil {
		return nil, "", fmt.Errorf("list stale held screenings: %w", err)
	}
	defer rows.Close()
	var out []HeldScreeningForRescreen
	for rows.Next() {
		var hs HeldScreeningForRescreen
		var namesText string
		if err := rows.Scan(&hs.Screening.ID, &hs.Screening.Rail, &hs.Screening.MovementRef,
			&hs.Screening.SubjectID, &hs.Screening.AmountKobo, &hs.Screening.CounterpartyName,
			&hs.Screening.AccountNumber, &hs.Screening.BankCode, &hs.Screening.Provider,
			&hs.Screening.ProviderRef, &hs.Screening.Decision, &namesText, &hs.Screening.Message,
			&hs.Screening.ListVersion, &hs.Screening.CreatedAt, &hs.PayoutID, &hs.BatchID); err != nil {
			return nil, "", fmt.Errorf("scan stale held screening: %w", err)
		}
		hs.Screening.MatchedNames = []string{}
		if namesText != "" {
			_ = json.Unmarshal([]byte(namesText), &hs.Screening.MatchedNames)
		}
		out = append(out, hs)
	}
	return out, reference, rows.Err()
}

// ReevaluateHeldPayout re-screens a held payout against the live list,
// skipping the recorded-decision shortcut that ScreenPayout uses on replays:
// it is the §8(5) path for a hold whose list version has moved. The fresh
// verdict is applied to the held screening row and the payout:
//
//   - clear:           the row records the new version as clear and the payout
//     re-queues for dispatch (the list moved and the counterparty is clean);
//   - possible/strong: the row's match details refresh against the new
//     version and the payout stays held (ErrMovementHeld);
//   - blocked:         the row flips to blocked, the payout fails with the
//     interdiction message, and ErrReevaluationInterdicted is returned.
//
// A screener outage leaves the hold untouched and returns the error.
func (s *Store) ReevaluateHeldPayout(ctx context.Context, payout Payout, batchNo string, dest MerchantSettlementAccount, screen screenFn) (Payout, error) {
	movementRef := payoutMovementRef(batchNo, dest.ID)
	decision, provider, providerRef, listVersion, message, matched, err := screen(ctx)
	if err != nil {
		return Payout{}, err
	}
	names, _ := json.Marshal(matched)
	if names == nil {
		names = []byte("[]")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE payment_screenings
		SET list_version=$1, provider=$2, provider_ref=$3, matched_names=$4::jsonb, message=$5
		WHERE rail=$6 AND movement_ref=$7 AND decision IN ('possible','strong')`,
		listVersion, provider, providerRef, string(names), message, RailMerchantPayout, movementRef)
	if err != nil {
		return Payout{}, fmt.Errorf("refresh held payout screening: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Payout{}, errors.New("held payout screening left the open-hold set before re-evaluation")
	}
	switch decision {
	case kyc.ScreenClear:
		if _, err := s.pool.Exec(ctx, `
			UPDATE payment_screenings SET decision='clear'
			WHERE rail=$1 AND movement_ref=$2 AND decision IN ('possible','strong')`,
			RailMerchantPayout, movementRef); err != nil {
			return Payout{}, fmt.Errorf("clear held payout screening: %w", err)
		}
		rtag, err := s.pool.Exec(ctx,
			`UPDATE payouts SET status='queued',last_error='' WHERE id=$1 AND status='held'`, payout.ID)
		if err != nil {
			return Payout{}, fmt.Errorf("requeue payout after list re-evaluation: %w", err)
		}
		if rtag.RowsAffected() == 0 {
			return Payout{}, errors.New("payout left the held state before list re-evaluation")
		}
		return s.PayoutByID(ctx, payout.ID)
	case kyc.ScreenPossible, kyc.ScreenStrong:
		return payout, ErrMovementHeld
	case kyc.ScreenBlocked:
		if _, err := s.pool.Exec(ctx,
			`UPDATE payment_screenings SET decision='blocked'
			 WHERE rail=$1 AND movement_ref=$2 AND decision IN ('possible','strong')`,
			RailMerchantPayout, movementRef); err != nil {
			return Payout{}, fmt.Errorf("mark held payout screening blocked: %w", err)
		}
		msg := "counterparty screening interdiction after list re-evaluation: " + message
		if _, err := s.pool.Exec(ctx,
			`UPDATE payouts SET status='failed',last_error=$2 WHERE id=$1 AND status='held'`,
			payout.ID, msg); err != nil {
			return Payout{}, fmt.Errorf("interdict payout on re-evaluation: %w", err)
		}
		return Payout{}, fmt.Errorf("%w: %s", ErrReevaluationInterdicted, message)
	default:
		return Payout{}, fmt.Errorf("unknown payment screening decision %q", decision)
	}
}
