// Remittance redemption (migration 103, POS_AGENT_NETWORK_PLAN.md §7): the one
// function that can authorise a cash payout. RedeemPickupTx re-checks every rule
// inside the same transaction that moves the money, mirroring PurseSpendAllowedTx
// (PROGRAM_PURSES.md). Nothing else may credit an agent's float for a pickup.
//
// The recipient presents a pickup code, the sender-forwarded OTP and a
// government photo ID (D6/D8). The code is looked up among the agent's own
// awaiting remittances: a remittance is scoped to exactly one agent (D7), so a
// short code never needs a global index, and an agent can only ever redeem what
// was assigned to it. Both the code and the OTP are stored as salted HMAC
// digests and compared in constant time; neither plaintext is ever persisted.
//
// The recipient's ID is sealed at rest (receiver_id_number_enc) and reduced to a
// deterministic salted hash (receiver_id_hash) keyed by a server pepper. That
// hash is the only key by which Xego sums a recipient's cash-outs across agents
// for the CBN per-customer daily/weekly ceiling — the recipient has no Xego
// account, so the hash stands in for one.
package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/kyc"
)

// Redemption sentinel errors surfaced so callers can render friendly copy and
// tests can assert the exact refusal.
var (
	ErrRemittanceCodeInvalid   = errors.New("pickup code is invalid")
	ErrRemittanceOTPRequired   = errors.New("no active OTP for this remittance")
	ErrRemittanceOTPInvalid    = errors.New("OTP is invalid")
	ErrRemittanceOTPExpired    = errors.New("OTP has expired")
	ErrRemittanceOTPLocked     = errors.New("OTP attempt limit reached")
	ErrReceiverIDRequired      = errors.New("a government photo ID is required")
	ErrRecipientBlocked        = errors.New("recipient interdicted by screening")
	ErrCustomerCapExceeded     = errors.New("recipient CBN cash-out ceiling exceeded")
	ErrAgentRegionMismatch     = errors.New("agent is not in the remittance region")
	ErrRemittanceAlreadyRedeemed = errors.New("remittance is already redeemed")
)

// RedeemPickupParams is the agent's redemption request. RequireOTP and RequireID
// carry the operator policy (AGENT_* config) down to the choke point; the store
// itself reads no configuration. Caps of zero mean uncapped.
type RedeemPickupParams struct {
	AgentMerchantID  uuid.UUID
	OperatorUserID   *uuid.UUID
	Code             string
	OTP              string
	RequireOTP       bool
	RequireID        bool
	ReceiverIDType   string
	ReceiverIDNumber string
	IDHashPepper     string
	// ScreenDecision is the recorded counterparty-screen verdict for this
	// recipient (kyc.Screen*). An empty or blocked value is refused: a cash
	// payout is irreversible, so only a clear/manually-cleared decision passes.
	ScreenDecision        string
	CustomerDailyCapKobo  int64
	CustomerWeeklyCapKobo int64
	GeoLat                *float64
	GeoLng                *float64
}

// RemittancePayout is the evidence record that an agent paid a named,
// ID-checked person. The plaintext ID is never carried here.
type RemittancePayout struct {
	ID              uuid.UUID
	RemittanceID    uuid.UUID
	AgentMerchantID uuid.UUID
	OperatorUserID  *uuid.UUID
	ReceiverName    string
	ReceiverIDType  string
	GeoLat          *float64
	GeoLng          *float64
	JournalRef      string
	PaidAt          time.Time
}

// RedeemPickupResult couples the settled payout with the remittance it closed,
// so a receipt can show the agent name, region, amount and coordinates.
type RedeemPickupResult struct {
	Remittance Remittance
	Payout     RemittancePayout
}

const remittancePayoutColumns = `id, remittance_id, agent_merchant_id, operator_user_id,
	receiver_name, receiver_id_type, geo_lat, geo_lng, journal_ref, paid_at`

func scanRemittancePayout(row pgx.Row) (RemittancePayout, error) {
	var p RemittancePayout
	err := row.Scan(&p.ID, &p.RemittanceID, &p.AgentMerchantID, &p.OperatorUserID,
		&p.ReceiverName, &p.ReceiverIDType, &p.GeoLat, &p.GeoLng, &p.JournalRef, &p.PaidAt)
	return p, err
}

// normalizeGovernmentID reduces a government ID to the canonical form used for
// hashing: letters and digits only, upper-cased. Formatting (spaces, dashes,
// slashes) must not change a recipient's cap identity.
func normalizeGovernmentID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}

// RemittanceIDHash is the deterministic salted hash of a recipient's government
// ID: HMAC-SHA256(pepper, idType||':'||normalizedID) in hex. It is deliberately
// not per-row salted — the whole point is that the same person yields the same
// key at every agent, so their cash-outs sum. The plaintext ID is never part of
// the result.
func RemittanceIDHash(pepper, idType, idNumber string) string {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(strings.ToUpper(strings.TrimSpace(idType))))
	mac.Write([]byte(":"))
	mac.Write([]byte(normalizeGovernmentID(idNumber)))
	return hex.EncodeToString(mac.Sum(nil))
}

// redeemCandidate is one of an agent's pending pickups, locked for redemption.
type redeemCandidate struct {
	pickupID      uuid.UUID
	remittanceID  uuid.UUID
	codeHash      string
	codeSalt      string
	pickupExpiry  time.Time
	windowExpiry  *time.Time
	regionID      uuid.UUID
	amountKobo    int64
	commissionKob int64
	recipientName string
}

// RedeemPickupTx is the single authorisation choke point for a cash payout. It
// runs every rule atomically and posts the payout ledger in the same
// transaction:
//
//  1. the presented code matches one of the agent's pending pickups (constant
//     time), the pickup and the 72h window are unexpired and the remittance is
//     awaiting pickup;
//  2. the OTP matches, is unexpired, under its attempt limit, and is consumed
//     (single-use);
//  3. a government ID is captured when required, sealed, and hashed;
//  4. the recipient screening decision is clear;
//  5. the agent is active and in the remittance region, and the payout fits the
//     agent's per-payout/daily caps and its max outstanding float (float +
//     deposit);
//  6. the recipient's per-customer daily/weekly CBN ceilings still have room;
//  7. dr 2350_remittance_payable:<id> / cr 2400_agent_float:<agent> is posted,
//     the pickup and OTP are retired single-use, and the payout record is
//     written.
//
// A concurrent attempt on the same code blocks on the row lock and then finds no
// pending pickup: the code is redeemed exactly once.
func (s *Store) RedeemPickupTx(ctx context.Context, p RedeemPickupParams) (RedeemPickupResult, error) {
	if p.AgentMerchantID == uuid.Nil {
		return RedeemPickupResult{}, errors.New("redemption requires an agent")
	}
	if strings.TrimSpace(p.Code) == "" {
		return RedeemPickupResult{}, ErrRemittanceCodeInvalid
	}
	if p.RequireOTP && strings.TrimSpace(p.OTP) == "" {
		return RedeemPickupResult{}, ErrRemittanceOTPRequired
	}
	if p.RequireID && (strings.TrimSpace(p.ReceiverIDType) == "" || strings.TrimSpace(p.ReceiverIDNumber) == "") {
		return RedeemPickupResult{}, ErrReceiverIDRequired
	}
	if kyc.BlockedByScreening(p.ScreenDecision) {
		return RedeemPickupResult{}, fmt.Errorf("%w: %s", ErrRecipientBlocked, p.ScreenDecision)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RedeemPickupResult{}, err
	}
	defer tx.Rollback(ctx)

	cand, err := findRedeemCandidate(ctx, tx, p.AgentMerchantID, p.Code)
	if err != nil {
		return RedeemPickupResult{}, err
	}

	now := time.Now()
	if cand.windowExpiry == nil || !cand.windowExpiry.After(now) || !cand.pickupExpiry.After(now) {
		return RedeemPickupResult{}, ErrRemittanceNotClaimable
	}

	if p.RequireOTP {
		if err := verifyRedemptionOTP(ctx, tx, cand.remittanceID, p.OTP); err != nil {
			// A failed OTP must persist its attempt increment even though the
			// overall redemption is refused.
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return RedeemPickupResult{}, commitErr
			}
			return RedeemPickupResult{}, err
		}
	}

	idHash := ""
	idSealed := ""
	if strings.TrimSpace(p.ReceiverIDNumber) != "" {
		idHash = RemittanceIDHash(p.IDHashPepper, p.ReceiverIDType, p.ReceiverIDNumber)
		idSealed, err = s.sealValue([]byte(normalizeGovernmentID(p.ReceiverIDNumber)))
		if err != nil {
			return RedeemPickupResult{}, fmt.Errorf("seal receiver ID: %w", err)
		}
	}

	profile, err := agentProfileTx(ctx, tx, p.AgentMerchantID)
	if err != nil {
		return RedeemPickupResult{}, err
	}
	if profile.RegionID != cand.regionID {
		return RedeemPickupResult{}, ErrAgentRegionMismatch
	}

	agentFloat, err := walletBalanceQ(ctx, tx, AgentFloatAccountCode(p.AgentMerchantID))
	if err != nil {
		return RedeemPickupResult{}, err
	}
	agentDeposit, err := walletBalanceQ(ctx, tx, AgentDepositAccountCode(p.AgentMerchantID))
	if err != nil {
		return RedeemPickupResult{}, err
	}
	paidToday, err := agentPaidTodayTx(ctx, tx, p.AgentMerchantID)
	if err != nil {
		return RedeemPickupResult{}, err
	}
	if err := agentPayoutAllowed(profile, cand.amountKobo, agentFloat+agentDeposit, paidToday); err != nil {
		return RedeemPickupResult{}, err
	}

	if idHash != "" {
		custToday, err := receiverPaidSinceTx(ctx, tx, idHash, "day")
		if err != nil {
			return RedeemPickupResult{}, err
		}
		custWeek, err := receiverPaidSinceTx(ctx, tx, idHash, "week")
		if err != nil {
			return RedeemPickupResult{}, err
		}
		if p.CustomerDailyCapKobo > 0 && custToday+cand.amountKobo > p.CustomerDailyCapKobo {
			return RedeemPickupResult{}, fmt.Errorf("%w: %d would exceed daily cap %d",
				ErrCustomerCapExceeded, custToday+cand.amountKobo, p.CustomerDailyCapKobo)
		}
		if p.CustomerWeeklyCapKobo > 0 && custWeek+cand.amountKobo > p.CustomerWeeklyCapKobo {
			return RedeemPickupResult{}, fmt.Errorf("%w: %d would exceed weekly cap %d",
				ErrCustomerCapExceeded, custWeek+cand.amountKobo, p.CustomerWeeklyCapKobo)
		}
	}

	journalRef := "remittance-redeem:" + cand.remittanceID.String()
	if err := s.postLedgerPair(ctx, tx, journalRef, "remittance", cand.remittanceID.String(),
		RemittancePayableAccountCode(cand.remittanceID), AgentFloatAccountCode(p.AgentMerchantID),
		"NGN", "Agent cash payout to remittance recipient", "agent", cand.amountKobo, nil); err != nil {
		return RedeemPickupResult{}, err
	}

	payout := RemittancePayout{
		ID:              uuid.New(),
		RemittanceID:    cand.remittanceID,
		AgentMerchantID: p.AgentMerchantID,
		OperatorUserID:  p.OperatorUserID,
		ReceiverName:    cand.recipientName,
		ReceiverIDType:  strings.TrimSpace(p.ReceiverIDType),
		GeoLat:          p.GeoLat,
		GeoLng:          p.GeoLng,
		JournalRef:      journalRef,
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO remittance_payouts
			(id, remittance_id, agent_merchant_id, operator_user_id, receiver_name,
			 receiver_id_type, receiver_id_number_enc, receiver_id_hash, geo_lat, geo_lng, journal_ref)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING paid_at`,
		payout.ID, payout.RemittanceID, payout.AgentMerchantID, payout.OperatorUserID,
		payout.ReceiverName, payout.ReceiverIDType, idSealed, idHash, payout.GeoLat, payout.GeoLng, payout.JournalRef).
		Scan(&payout.PaidAt); err != nil {
		return RedeemPickupResult{}, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE remittance_pickups
		SET status='redeemed', redeemed_at=now(), redeemed_by_agent_id=$2
		WHERE id=$1 AND status='pending'`, cand.pickupID, p.AgentMerchantID); err != nil {
		return RedeemPickupResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE remittance_otps SET status='consumed', consumed_at=now()
		WHERE remittance_id=$1 AND status='active'`, cand.remittanceID); err != nil {
		return RedeemPickupResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE remittances SET status='paid', paid_at=now(), updated_at=now()
		WHERE id=$1 AND status='awaiting_pickup'`, cand.remittanceID); err != nil {
		return RedeemPickupResult{}, err
	}

	rem, err := scanRemittance(tx.QueryRow(ctx,
		`SELECT `+remittanceColumns+` FROM remittances WHERE id=$1`, cand.remittanceID))
	if err != nil {
		return RedeemPickupResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RedeemPickupResult{}, err
	}
	return RedeemPickupResult{Remittance: rem, Payout: payout}, nil
}

// findRedeemCandidate locks the agent's pending pickups and returns the one whose
// code matches, or ErrRemittanceCodeInvalid. The code is not globally indexed:
// a remittance belongs to exactly one agent, so the search is scoped to that
// agent's book.
func findRedeemCandidate(ctx context.Context, tx pgx.Tx, agentMerchantID uuid.UUID, code string) (redeemCandidate, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.remittance_id, p.code_hash, p.code_salt, p.expires_at,
		       r.expires_at, r.region_id, r.amount_kobo, r.commission_kobo, r.recipient_name
		FROM remittance_pickups p
		JOIN remittances r ON r.id = p.remittance_id
		WHERE p.status = 'pending' AND r.status = 'awaiting_pickup'
		  AND r.agent_merchant_id = $1
		ORDER BY p.created_at
		FOR UPDATE OF p, r`, agentMerchantID)
	if err != nil {
		return redeemCandidate{}, err
	}
	var cands []redeemCandidate
	for rows.Next() {
		var c redeemCandidate
		if err := rows.Scan(&c.pickupID, &c.remittanceID, &c.codeHash, &c.codeSalt, &c.pickupExpiry,
			&c.windowExpiry, &c.regionID, &c.amountKobo, &c.commissionKob, &c.recipientName); err != nil {
			rows.Close()
			return redeemCandidate{}, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return redeemCandidate{}, err
	}
	for _, c := range cands {
		got := RemittanceCodeHash(c.codeSalt, code)
		if hmac.Equal([]byte(got), []byte(c.codeHash)) {
			return c, nil
		}
	}
	return redeemCandidate{}, ErrRemittanceCodeInvalid
}

// verifyRedemptionOTP checks the agent-presented OTP against the remittance's
// active OTP, consuming it on success. A mismatch increments the attempt counter
// and retires the OTP once the limit is reached, so the increment must survive a
// refused redemption (the caller commits on the returned error).
func verifyRedemptionOTP(ctx context.Context, tx pgx.Tx, remittanceID uuid.UUID, otp string) error {
	var id uuid.UUID
	var hash, salt string
	var expiresAt time.Time
	var attempts, maxAttempts int
	err := tx.QueryRow(ctx, `
		SELECT id, otp_hash, otp_salt, expires_at, attempts, max_attempts
		FROM remittance_otps
		WHERE remittance_id=$1 AND status='active'
		FOR UPDATE`, remittanceID).Scan(&id, &hash, &salt, &expiresAt, &attempts, &maxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRemittanceOTPRequired
	}
	if err != nil {
		return err
	}
	if !expiresAt.After(time.Now()) {
		if _, err := tx.Exec(ctx,
			`UPDATE remittance_otps SET status='expired' WHERE id=$1`, id); err != nil {
			return err
		}
		return ErrRemittanceOTPExpired
	}
	if attempts >= maxAttempts {
		return ErrRemittanceOTPLocked
	}
	got := RemittanceCodeHash(salt, otp)
	if !hmac.Equal([]byte(got), []byte(hash)) {
		if attempts+1 >= maxAttempts {
			if _, err := tx.Exec(ctx,
				`UPDATE remittance_otps SET attempts=attempts+1, status='expired' WHERE id=$1`, id); err != nil {
				return err
			}
			return ErrRemittanceOTPLocked
		}
		if _, err := tx.Exec(ctx,
			`UPDATE remittance_otps SET attempts=attempts+1 WHERE id=$1`, id); err != nil {
			return err
		}
		return ErrRemittanceOTPInvalid
	}
	if _, err := tx.Exec(ctx,
		`UPDATE remittance_otps SET status='consumed', consumed_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	return nil
}

// agentProfileTx loads the agent profile inside a transaction.
func agentProfileTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID) (AgentProfile, error) {
	var p AgentProfile
	var lat, lng *float64
	var approvedBy *uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT merchant_id, region_id, tier, geo_lat, geo_lng, max_outstanding_float_kobo,
			per_payout_limit_kobo, daily_payout_limit_kobo, commission_bps, status, agreement_ref,
			approved_by, created_at, updated_at
		FROM agent_profiles WHERE merchant_id=$1`, merchantID).Scan(
		&p.MerchantID, &p.RegionID, &p.Tier, &lat, &lng, &p.MaxOutstandingFloatKobo,
		&p.PerPayoutLimitKobo, &p.DailyPayoutLimitKobo, &p.CommissionBps, &p.Status, &p.AgreementRef,
		&approvedBy, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProfile{}, ErrAgentNotFound
	}
	p.GeoLat, p.GeoLng, p.ApprovedBy = lat, lng, approvedBy
	return p, err
}

// agentPaidTodayTx sums the amounts already paid out by an agent since midnight.
func agentPaidTodayTx(ctx context.Context, tx pgx.Tx, merchantID uuid.UUID) (int64, error) {
	var total int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(r.amount_kobo),0)
		FROM remittance_payouts p
		JOIN remittances r ON r.id = p.remittance_id
		WHERE p.agent_merchant_id=$1 AND p.paid_at >= date_trunc('day', now())`, merchantID).Scan(&total)
	return total, err
}

// receiverPaidSinceTx sums a recipient's cash-outs since the start of the day or
// ISO week, keyed by the deterministic ID hash (period is "day" or "week").
func receiverPaidSinceTx(ctx context.Context, tx pgx.Tx, receiverIDHash, period string) (int64, error) {
	trunc := "day"
	if period == "week" {
		trunc = "week"
	}
	var total int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(r.amount_kobo),0)
		FROM remittance_payouts p
		JOIN remittances r ON r.id = p.remittance_id
		WHERE p.receiver_id_hash=$1 AND p.paid_at >= date_trunc('`+trunc+`', now())`, receiverIDHash).Scan(&total)
	return total, err
}

// RemittancePayoutByRemittance returns the payout record for a remittance, the
// evidence a receipt renders.
func (s *Store) RemittancePayoutByRemittance(ctx context.Context, remittanceID uuid.UUID) (RemittancePayout, error) {
	payout, err := scanRemittancePayout(s.pool.QueryRow(ctx,
		`SELECT `+remittancePayoutColumns+` FROM remittance_payouts WHERE remittance_id=$1`, remittanceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return RemittancePayout{}, ErrRemittanceNotFound
	}
	return payout, err
}

// RemittanceForAgentCode resolves the agent's pending remittance for a presented
// code, without locking or moving money. It is the display/selection helper the
// console uses to show the recipient before redemption and to drive screening;
// it is never the authority — RedeemPickupTx re-resolves and re-checks inside
// the money-moving transaction.
func (s *Store) RemittanceForAgentCode(ctx context.Context, agentMerchantID uuid.UUID, code string) (Remittance, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.code_hash, p.code_salt, p.remittance_id
		FROM remittance_pickups p
		JOIN remittances r ON r.id = p.remittance_id
		WHERE p.status='pending' AND r.status='awaiting_pickup' AND r.agent_merchant_id=$1
		ORDER BY p.created_at`, agentMerchantID)
	if err != nil {
		return Remittance{}, err
	}
	type cand struct{ hash, salt string; remittanceID uuid.UUID }
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.hash, &c.salt, &c.remittanceID); err != nil {
			rows.Close()
			return Remittance{}, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Remittance{}, err
	}
	for _, c := range cands {
		if hmac.Equal([]byte(RemittanceCodeHash(c.salt, code)), []byte(c.hash)) {
			return s.RemittanceByID(ctx, c.remittanceID)
		}
	}
	return Remittance{}, ErrRemittanceNotFound
}
