// Remittance store (migration 103). A remittance is the sender's instruction
// to pay a named recipient at one assigned agent. The sender's money is held on
// a per-remittance payable (2350_remittance_payable:<id>) and the agent's
// commission is credited to its float (2400_agent_float:<merchant-id>) at
// collection. The recipient redeems with a hashed, single-use, expiring pickup
// code plus an OTP the sender forwards (D8); an unclaimed remittance expires
// after REMITTANCE_TTL and is refunded to the sender's wallet.
//
// Codes and OTPs are never stored in plaintext: only a salted HMAC-SHA256
// digest. The salt is per-code random, so two recipients with the same code do
// not share a digest. Generation, delivery and verification live in the
// service layer; this layer only mints, stores and retires the hashes.
package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PickupCodeLength is the number of decimal digits in a pickup code. Short
// enough to read over the phone; safe because the code is short-lived, bound to
// one remittance, capped at max_attempts and paired with an OTP and an ID.
const PickupCodeLength = 6

// Remittance and pickup sentinel errors.
var (
	ErrRemittanceNotFound     = errors.New("remittance not found")
	ErrRemittanceNotClaimable = errors.New("remittance is not awaiting pickup")
)

// Remittance is the sender's instruction to pay a named recipient at one agent.
type Remittance struct {
	ID              uuid.UUID
	SenderUserID    uuid.UUID
	AgentMerchantID uuid.UUID
	RegionID        uuid.UUID
	AmountKobo      int64
	FeeKobo         int64
	CommissionKobo  int64
	RecipientName   string
	RecipientPhone  string
	Status          string
	PaymentID       *uuid.UUID
	ExpiresAt       *time.Time
	PaidAt          *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// RemittancePickup is the hashed single-use code that claims a remittance.
type RemittancePickup struct {
	ID              uuid.UUID
	RemittanceID    uuid.UUID
	Status          string
	Attempts        int
	MaxAttempts     int
	ExpiresAt       time.Time
	RedeemedAt      *time.Time
	RedeemedByAgent *uuid.UUID
	CreatedAt       time.Time
}

// CreateRemittanceParams carries everything needed to open a remittance and
// post its collection splits in one transaction.
type CreateRemittanceParams struct {
	SenderUserID    uuid.UUID
	AgentMerchantID uuid.UUID
	RegionID        uuid.UUID
	AmountKobo      int64
	FeeKobo         int64
	CommissionKobo  int64
	RecipientName   string
	RecipientPhone  string
	PaymentID       uuid.UUID
	CodeHash        string
	CodeSalt        string
	ExpiresAt       time.Time
}

// remittanceColumns is the shared SELECT list so scans stay in step.
const remittanceColumns = `id, sender_user_id, agent_merchant_id, region_id, amount_kobo,
	fee_kobo, commission_kobo, recipient_name, recipient_phone, status,
	payment_id, expires_at, paid_at, created_at, updated_at`

func scanRemittance(row pgx.Row) (Remittance, error) {
	var r Remittance
	err := row.Scan(&r.ID, &r.SenderUserID, &r.AgentMerchantID, &r.RegionID, &r.AmountKobo,
		&r.FeeKobo, &r.CommissionKobo, &r.RecipientName, &r.RecipientPhone, &r.Status,
		&r.PaymentID, &r.ExpiresAt, &r.PaidAt, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// NewRemittanceSalt returns a fresh 16-byte hex salt for a code or OTP digest.
func NewRemittanceSalt() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// NewRemittanceCode returns a uniformly random decimal pickup code/OTP.
func NewRemittanceCode() (string, error) {
	max := big.NewInt(1)
	for i := 0; i < PickupCodeLength; i++ {
		max.Mul(max, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", PickupCodeLength, n.Int64()), nil
}

// RemittanceCodeHash is the salted digest stored for a pickup code or OTP. It is
// a deterministic HMAC so the verifier can recompute it from the presented code
// and the stored salt; the salt is the HMAC key.
func RemittanceCodeHash(salt, code string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil))
}

// CreateRemittanceTx opens a remittance and posts its collection splits in one
// transaction: the amount is ring-fenced on the per-remittance payable, the fee
// books to sales revenue, and the agent's commission to its float. It is
// idempotent on PaymentID (unique index), so a replayed post-success hook
// returns the existing remittance instead of double-charging. The bool reports
// whether this call created the remittance (false on a replay), so the caller
// delivers the pickup code exactly once.
func (s *Store) CreateRemittanceTx(ctx context.Context, p CreateRemittanceParams) (Remittance, bool, error) {
	if p.AmountKobo <= 0 {
		return Remittance{}, false, errors.New("remittance requires a positive amount")
	}
	if p.CodeHash == "" || p.CodeSalt == "" {
		return Remittance{}, false, errors.New("remittance requires a pickup code hash and salt")
	}
	if p.FeeKobo < 0 || p.CommissionKobo < 0 {
		return Remittance{}, false, errors.New("remittance fees must not be negative")
	}
	if p.ExpiresAt.IsZero() {
		return Remittance{}, false, errors.New("remittance requires an expiry")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Remittance{}, false, err
	}
	defer tx.Rollback(ctx)

	existing, err := scanRemittance(tx.QueryRow(ctx,
		`SELECT `+remittanceColumns+` FROM remittances WHERE payment_id=$1`, p.PaymentID))
	if err == nil {
		return existing, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Remittance{}, false, err
	}

	r := Remittance{
		ID:              uuid.New(),
		SenderUserID:    p.SenderUserID,
		AgentMerchantID: p.AgentMerchantID,
		RegionID:        p.RegionID,
		AmountKobo:      p.AmountKobo,
		FeeKobo:         p.FeeKobo,
		CommissionKobo:  p.CommissionKobo,
		RecipientName:   p.RecipientName,
		RecipientPhone:  p.RecipientPhone,
		Status:          RemittanceAwaitingPickup,
		PaymentID:       &p.PaymentID,
		ExpiresAt:       &p.ExpiresAt,
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO remittances
			(id, sender_user_id, agent_merchant_id, region_id, amount_kobo, fee_kobo,
			 commission_kobo, recipient_name, recipient_phone, status, payment_id, expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING created_at, updated_at`,
		r.ID, r.SenderUserID, r.AgentMerchantID, r.RegionID, r.AmountKobo, r.FeeKobo,
		r.CommissionKobo, r.RecipientName, r.RecipientPhone, r.Status, r.PaymentID, r.ExpiresAt)
	if err := row.Scan(&r.CreatedAt, &r.UpdatedAt); err != nil {
		return Remittance{}, false, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO remittance_pickups (remittance_id, code_hash, code_salt, expires_at)
		VALUES($1,$2,$3,$4)`, r.ID, p.CodeHash, p.CodeSalt, p.ExpiresAt); err != nil {
		return Remittance{}, false, err
	}

	ref := "remittance:" + r.ID.String()
	srcID := r.ID.String()
	if err := s.postLedgerPair(ctx, tx, ref, "remittance", srcID,
		LedgerAccountCustomerFloat, RemittancePayableAccountCode(r.ID), "NGN",
		"Remittance amount held for pickup", "system", r.AmountKobo, nil); err != nil {
		return Remittance{}, false, err
	}
	if r.FeeKobo > 0 {
		if err := s.postLedgerPair(ctx, tx, ref, "remittance", srcID,
			LedgerAccountCustomerFloat, LedgerAccountSalesRevenue, "NGN",
			"Remittance transfer fee", "system", r.FeeKobo, nil); err != nil {
			return Remittance{}, false, err
		}
	}
	if r.CommissionKobo > 0 {
		if err := s.postLedgerPair(ctx, tx, ref, "remittance", srcID,
			LedgerAccountCustomerFloat, AgentFloatAccountCode(r.AgentMerchantID), "NGN",
			"Agent commission on remittance", "system", r.CommissionKobo, nil); err != nil {
			return Remittance{}, false, err
		}
	}
	return r, true, tx.Commit(ctx)
}

// RemittanceByID loads a remittance (ErrRemittanceNotFound when absent).
func (s *Store) RemittanceByID(ctx context.Context, id uuid.UUID) (Remittance, error) {
	r, err := scanRemittance(s.pool.QueryRow(ctx,
		`SELECT `+remittanceColumns+` FROM remittances WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Remittance{}, ErrRemittanceNotFound
	}
	return r, err
}

// RemittanceByPaymentID loads the remittance a sender payment opened.
func (s *Store) RemittanceByPaymentID(ctx context.Context, paymentID uuid.UUID) (Remittance, error) {
	r, err := scanRemittance(s.pool.QueryRow(ctx,
		`SELECT `+remittanceColumns+` FROM remittances WHERE payment_id=$1`, paymentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Remittance{}, ErrRemittanceNotFound
	}
	return r, err
}

// RemittancesBySender lists a sender's remittances, newest first.
func (s *Store) RemittancesBySender(ctx context.Context, senderID uuid.UUID, limit int) ([]Remittance, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+remittanceColumns+` FROM remittances WHERE sender_user_id=$1
		 ORDER BY created_at DESC LIMIT $2`, senderID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Remittance
	for rows.Next() {
		r, err := scanRemittance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RequestRemittanceOTP retires any active OTP and stores a fresh one for a
// remittance that is still awaiting pickup and not yet expired (D8: the sender
// requests it, within the window). It returns ErrRemittanceNotClaimable when
// the remittance can no longer be claimed.
func (s *Store) RequestRemittanceOTP(ctx context.Context, remittanceID uuid.UUID, otpHash, otpSalt string, expiresAt time.Time) error {
	if otpHash == "" || otpSalt == "" {
		return errors.New("remittance OTP requires a hash and salt")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	var windowExpiry *time.Time
	err = tx.QueryRow(ctx,
		`SELECT status, expires_at FROM remittances WHERE id=$1 FOR UPDATE`, remittanceID).
		Scan(&status, &windowExpiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRemittanceNotFound
	}
	if err != nil {
		return err
	}
	if status != RemittanceAwaitingPickup || (windowExpiry != nil && !windowExpiry.After(time.Now())) {
		return ErrRemittanceNotClaimable
	}
	if _, err := tx.Exec(ctx, `
		UPDATE remittance_otps SET status='consumed', consumed_at=now()
		WHERE remittance_id=$1 AND status='active'`, remittanceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO remittance_otps (remittance_id, otp_hash, otp_salt, expires_at)
		VALUES($1,$2,$3,$4)`, remittanceID, otpHash, otpSalt, expiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReissueRemittancePickupCode supersedes the pending code with a fresh one, for
// a sender whose code was lost before it was used. The remittance window is
// unchanged.
func (s *Store) ReissueRemittancePickupCode(ctx context.Context, remittanceID uuid.UUID, codeHash, codeSalt string) error {
	if codeHash == "" || codeSalt == "" {
		return errors.New("remittance code requires a hash and salt")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	var windowExpiry *time.Time
	err = tx.QueryRow(ctx,
		`SELECT status, expires_at FROM remittances WHERE id=$1 FOR UPDATE`, remittanceID).
		Scan(&status, &windowExpiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRemittanceNotFound
	}
	if err != nil {
		return err
	}
	if status != RemittanceAwaitingPickup || windowExpiry == nil || !windowExpiry.After(time.Now()) {
		return ErrRemittanceNotClaimable
	}
	if _, err := tx.Exec(ctx, `
		UPDATE remittance_pickups SET status='cancelled'
		WHERE remittance_id=$1 AND status='pending'`, remittanceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO remittance_pickups (remittance_id, code_hash, code_salt, expires_at)
		VALUES($1,$2,$3,$4)`, remittanceID, codeHash, codeSalt, *windowExpiry); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ExpireRemittances sweeps remittances past their pickup window: each is marked
// expired, its pending code and active OTP are retired, and the sender is
// refunded the amount and the agent commission from the per-remittance payable
// and the agent's float respectively. The transfer fee is retained. Idempotent
// per remittance (guarded by the refund journal reference), so a crashed sweep
// can be re-run safely. Returns how many were expired.
func (s *Store) ExpireRemittances(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	type due struct {
		id         uuid.UUID
		sender     uuid.UUID
		agent      uuid.UUID
		amount     int64
		commission int64
	}
	rows, err := tx.Query(ctx, `
		SELECT id, sender_user_id, agent_merchant_id, amount_kobo, commission_kobo
		FROM remittances
		WHERE status='awaiting_pickup' AND expires_at IS NOT NULL AND expires_at <= now()
		ORDER BY expires_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	var dues []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.sender, &d.agent, &d.amount, &d.commission); err != nil {
			rows.Close()
			return 0, err
		}
		dues = append(dues, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	for _, d := range dues {
		ref := "remittance-refund:" + d.id.String()
		exists, err := ledgerJournalExistsTx(ctx, tx, ref)
		if err != nil {
			return count, err
		}
		if !exists {
			if err := s.creditWalletTx(ctx, tx, d.sender, RemittancePayableAccountCode(d.id),
				d.amount, ref, "Remittance refund (pickup window expired)"); err != nil {
				return count, err
			}
			if d.commission > 0 {
				if err := s.creditWalletTx(ctx, tx, d.sender, AgentFloatAccountCode(d.agent),
					d.commission, "remittance-refund-commission:"+d.id.String(),
					"Agent commission reversal (pickup window expired)"); err != nil {
					return count, err
				}
			}
		}
		if _, err := tx.Exec(ctx,
			`UPDATE remittances SET status='expired', updated_at=now() WHERE id=$1`, d.id); err != nil {
			return count, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE remittance_pickups SET status='expired' WHERE remittance_id=$1 AND status='pending'`, d.id); err != nil {
			return count, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE remittance_otps SET status='expired' WHERE remittance_id=$1 AND status='active'`, d.id); err != nil {
			return count, err
		}
		count++
	}
	return count, tx.Commit(ctx)
}
