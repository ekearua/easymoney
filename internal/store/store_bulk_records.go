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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
)

// Bulk recipient verification states. Only verified rows are payable.
const (
	RecipientUnverified = "unverified"
	RecipientVerified   = "verified"
	RecipientRejected   = "rejected"
)

// RecipientFilter narrows the merchant's recipient list. Empty fields are
// ignored, so the same struct drives both role views and batch selection.
type RecipientFilter struct {
	State        string
	LGA          string
	Ward         string
	PollingUnit  string
	Verification string
	Query        string
	CenterID     uuid.UUID
}

// BulkRecipient is one record on the merchant's single recipient list, shaped
// by the Records Template.
type BulkRecipient struct {
	ID                 uuid.UUID
	MerchantID         uuid.UUID
	CenterID           *uuid.UUID
	CenterName         string
	PhoneE164          string
	DisplayName        string
	UserID             *uuid.UUID
	Source             string
	SourceFile         string
	BVN                string
	NIN                string
	VIN                string
	AccountNumber      string
	BankName           string
	BankCode           string
	AccountName        string
	State              string
	LGA                string
	Ward               string
	PollingUnit        string
	VerificationStatus string
	VerificationNote   string
	VerifiedAt         *time.Time
	LastPaidAt         *time.Time
	LastPaidAmountKobo int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// BulkRecipientInput is one row of a records import or a manual edit.
type BulkRecipientInput struct {
	PhoneE164     string
	DisplayName   string
	CenterID      *uuid.UUID
	Source        string
	SourceFile    string
	BVN           string
	NIN           string
	VIN           string
	AccountNumber string
	BankName      string
	BankCode      string
	AccountName   string
	State         string
	LGA           string
	Ward          string
	PollingUnit   string
	rawRow        []string
	columns       map[string]int
}

// RecipientInputRowID returns a stable human-readable label for a parsed
// import row, so error messages coming from store-level failures can still
// name the template's row/serial identity.
func RecipientInputRowID(in BulkRecipientInput) string {
	if cols := in.columns; cols != nil {
		if idx, ok := cols["row"]; ok && idx >= 0 && idx < len(in.rawRow) && strings.TrimSpace(in.rawRow[idx]) != "" {
			return strings.TrimSpace(in.rawRow[idx])
		}
	}
	return ""
}

// RecipientPaymentSummary is the per-recipient bulk history headline.
type RecipientPaymentSummary struct {
	Count          int
	TotalKobo      int64
	LastAmountKobo int64
	LastPaidAt     *time.Time
}

// RecipientPaymentEntry is one bulk payment made to a recipient.
type RecipientPaymentEntry struct {
	BatchNo    string
	AmountKobo int64
	Status     string
	State      string
	LGA        string
	Ward       string
	CenterName string
	CreatedAt  time.Time
}

// blindIndex produces a deterministic keyed digest so duplicate identity
// numbers can be detected without storing a decryptable or enumerable value.
func (s *Store) blindIndex(scope, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	mac := hmac.New(sha256.New, s.dataKey)
	mac.Write([]byte("bulk-records:" + scope + ":" + value))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Store) sealIdentity(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return s.sealValue([]byte(strings.TrimSpace(value)))
}

func (s *Store) openIdentity(value string) string {
	if value == "" {
		return ""
	}
	raw, err := s.openValue(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func normalizeRecipientInput(in BulkRecipientInput) (BulkRecipientInput, error) {
	in.PhoneE164 = domain.CanonicalE164Phone(strings.TrimSpace(in.PhoneE164))
	if in.PhoneE164 == "" {
		return in, errors.New("phone number is required")
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Source = strings.TrimSpace(in.Source)
	if in.Source == "" {
		in.Source = "manual"
	}
	in.SourceFile = strings.TrimSpace(in.SourceFile)
	in.BVN = strings.TrimSpace(in.BVN)
	in.NIN = strings.TrimSpace(in.NIN)
	in.VIN = strings.TrimSpace(in.VIN)
	in.AccountNumber = strings.TrimSpace(in.AccountNumber)
	in.BankName = strings.TrimSpace(in.BankName)
	in.BankCode = strings.TrimSpace(in.BankCode)
	in.AccountName = strings.TrimSpace(in.AccountName)
	in.State = strings.TrimSpace(in.State)
	in.LGA = strings.TrimSpace(in.LGA)
	in.Ward = strings.TrimSpace(in.Ward)
	in.PollingUnit = strings.TrimSpace(in.PollingUnit)
	return in, nil
}

// UpsertBulkRecipient creates or edits one record. Editing resets verification,
// because a changed identity or bank value has not been attested yet.
func (s *Store) UpsertBulkRecipient(ctx context.Context, merchantID uuid.UUID, in BulkRecipientInput) (BulkRecipient, error) {
	in, err := normalizeRecipientInput(in)
	if err != nil {
		return BulkRecipient{}, err
	}
	bvnSealed, err := s.sealIdentity(in.BVN)
	if err != nil {
		return BulkRecipient{}, err
	}
	ninSealed, err := s.sealIdentity(in.NIN)
	if err != nil {
		return BulkRecipient{}, err
	}
	vinSealed, err := s.sealIdentity(in.VIN)
	if err != nil {
		return BulkRecipient{}, err
	}
	var center any
	if in.CenterID != nil && *in.CenterID != uuid.Nil {
		center = *in.CenterID
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx, `
		INSERT INTO center_users(merchant_id, center_id, phone_e164, display_name, source, source_file,
			bvn_sealed, bvn_index, nin_sealed, nin_index, vin_sealed, vin_index,
			account_number, bank_name, bank_code, account_name,
			state, lga, ward, polling_unit, verification_status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT (merchant_id, phone_e164) DO UPDATE SET
			center_id=EXCLUDED.center_id,
			display_name=EXCLUDED.display_name,
			source=EXCLUDED.source,
			source_file=EXCLUDED.source_file,
			bvn_sealed=EXCLUDED.bvn_sealed, bvn_index=EXCLUDED.bvn_index,
			nin_sealed=EXCLUDED.nin_sealed, nin_index=EXCLUDED.nin_index,
			vin_sealed=EXCLUDED.vin_sealed, vin_index=EXCLUDED.vin_index,
			account_number=EXCLUDED.account_number, bank_name=EXCLUDED.bank_name,
			bank_code=EXCLUDED.bank_code, account_name=EXCLUDED.account_name,
			state=EXCLUDED.state, lga=EXCLUDED.lga, ward=EXCLUDED.ward, polling_unit=EXCLUDED.polling_unit,
			verification_status=EXCLUDED.verification_status, verification_note='', verified_by_member=NULL, verified_at=NULL,
			updated_at=now()
		RETURNING id`,
		merchantID, center, in.PhoneE164, in.DisplayName, in.Source, in.SourceFile,
		bvnSealed, s.blindIndex("bvn", in.BVN), ninSealed, s.blindIndex("nin", in.NIN),
		vinSealed, s.blindIndex("vin", in.VIN),
		in.AccountNumber, in.BankName, in.BankCode, in.AccountName,
		in.State, in.LGA, in.Ward, in.PollingUnit, RecipientUnverified).Scan(&id)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "center_users_bvn_idx"):
			return BulkRecipient{}, errors.New("another recipient already uses this BVN")
		case strings.Contains(err.Error(), "center_users_nin_idx"):
			return BulkRecipient{}, errors.New("another recipient already uses this NIN")
		case strings.Contains(err.Error(), "center_users_vin_idx"):
			return BulkRecipient{}, errors.New("another recipient already uses this VIN")
		}
		return BulkRecipient{}, err
	}
	return s.BulkRecipientByID(ctx, merchantID, id)
}

const recipientColumns = `cu.id, cu.merchant_id, cu.center_id, COALESCE(mc.name,''), cu.phone_e164,
	cu.display_name, cu.user_id, cu.source, cu.source_file,
	cu.bvn_sealed, cu.nin_sealed, cu.vin_sealed,
	cu.account_number, cu.bank_name, cu.bank_code, cu.account_name,
	cu.state, cu.lga, cu.ward, cu.polling_unit,
	cu.verification_status, cu.verification_note, cu.verified_at,
	cu.last_paid_at, cu.last_paid_amount_kobo, cu.created_at, cu.updated_at`

func (s *Store) scanRecipient(row pgx.Row, withIdentity bool) (BulkRecipient, error) {
	var r BulkRecipient
	var bvnSealed, ninSealed, vinSealed string
	err := row.Scan(&r.ID, &r.MerchantID, &r.CenterID, &r.CenterName, &r.PhoneE164,
		&r.DisplayName, &r.UserID, &r.Source, &r.SourceFile,
		&bvnSealed, &ninSealed, &vinSealed,
		&r.AccountNumber, &r.BankName, &r.BankCode, &r.AccountName,
		&r.State, &r.LGA, &r.Ward, &r.PollingUnit,
		&r.VerificationStatus, &r.VerificationNote, &r.VerifiedAt,
		&r.LastPaidAt, &r.LastPaidAmountKobo, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return BulkRecipient{}, err
	}
	if withIdentity {
		r.BVN = s.openIdentity(bvnSealed)
		r.NIN = s.openIdentity(ninSealed)
		r.VIN = s.openIdentity(vinSealed)
	}
	return r, nil
}

func buildRecipientWhere(merchantID uuid.UUID, f RecipientFilter, forceVerified bool) (string, []any) {
	clauses := []string{"cu.merchant_id = $1"}
	args := []any{merchantID}
	add := func(expr, value string) {
		if v := strings.TrimSpace(value); v != "" {
			args = append(args, v)
			clauses = append(clauses, fmt.Sprintf("%s = $%d", expr, len(args)))
		}
	}
	add("cu.state", f.State)
	add("cu.lga", f.LGA)
	add("cu.ward", f.Ward)
	add("cu.polling_unit", f.PollingUnit)
	if forceVerified {
		add("cu.verification_status", RecipientVerified)
	} else {
		add("cu.verification_status", f.Verification)
	}
	if f.CenterID != uuid.Nil {
		args = append(args, f.CenterID)
		clauses = append(clauses, fmt.Sprintf("cu.center_id = $%d", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		clauses = append(clauses, fmt.Sprintf("(lower(cu.display_name) LIKE $%d OR cu.phone_e164 LIKE $%d)", len(args), len(args)))
	}
	return strings.Join(clauses, " AND "), args
}

// ListBulkRecipients returns one page of the merchant's recipient list.
func (s *Store) ListBulkRecipients(ctx context.Context, merchantID uuid.UUID, f RecipientFilter, limit, offset int) ([]BulkRecipient, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	where, args := buildRecipientWhere(merchantID, f, false)
	args = append(args, limit, offset)
	query := fmt.Sprintf(`
		SELECT %s
		FROM center_users cu
		LEFT JOIN merchant_centers mc ON mc.id = cu.center_id
		WHERE %s
		ORDER BY cu.created_at DESC
		LIMIT $%d OFFSET $%d`, recipientColumns, where, len(args)-1, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BulkRecipient
	for rows.Next() {
		r, err := s.scanRecipient(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountBulkRecipients counts the rows matching a filter.
func (s *Store) CountBulkRecipients(ctx context.Context, merchantID uuid.UUID, f RecipientFilter) (int, error) {
	where, args := buildRecipientWhere(merchantID, f, false)
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM center_users cu WHERE `+where, args...).Scan(&n)
	return n, err
}

// BulkRecipientByID returns one record scoped to the merchant.
func (s *Store) BulkRecipientByID(ctx context.Context, merchantID, id uuid.UUID) (BulkRecipient, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+recipientColumns+`
		FROM center_users cu
		LEFT JOIN merchant_centers mc ON mc.id = cu.center_id
		WHERE cu.id=$1 AND cu.merchant_id=$2`, id, merchantID)
	return s.scanRecipient(row, true)
}

// SetBulkRecipientVerification records a manual attestation.
func (s *Store) SetBulkRecipientVerification(ctx context.Context, merchantID, id, memberID uuid.UUID, status, note string) error {
	if status != RecipientVerified && status != RecipientRejected && status != RecipientUnverified {
		return errors.New("invalid verification status")
	}
	var member any
	if memberID != uuid.Nil {
		member = memberID
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE center_users
		SET verification_status=$3, verification_note=$4,
		    verified_by_member=$5, verified_at=CASE WHEN $3='verified' THEN now() ELSE NULL END,
		    updated_at=now()
		WHERE id=$1 AND merchant_id=$2`, id, merchantID, status, truncateBulkText(note), member)
	return err
}

// SetBulkRecipientCenter attaches a record to an existing payment location.
func (s *Store) SetBulkRecipientCenter(ctx context.Context, merchantID, id uuid.UUID, centerID *uuid.UUID) error {
	var center any
	if centerID != nil && *centerID != uuid.Nil {
		center = *centerID
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE center_users SET center_id=$3, updated_at=now() WHERE id=$1 AND merchant_id=$2`,
		id, merchantID, center)
	return err
}

// DeleteBulkRecipient removes one record from the merchant's list.
func (s *Store) DeleteBulkRecipient(ctx context.Context, merchantID, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM center_users WHERE id=$1 AND merchant_id=$2`, id, merchantID)
	return err
}

// RecipientPaymentSummary aggregates a recipient's bulk payment history.
func (s *Store) RecipientPaymentSummary(ctx context.Context, merchantID, recipientID uuid.UUID) (RecipientPaymentSummary, error) {
	var out RecipientPaymentSummary
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE i.status='paid'),
		       COALESCE(SUM(CASE WHEN i.status='paid' THEN i.amount_kobo ELSE 0 END),0),
		       COALESCE(MAX(i.amount_kobo) FILTER (WHERE i.status='paid'),0),
		       MAX(i.updated_at) FILTER (WHERE i.status='paid')
		FROM bulk_batch_items i
		JOIN bulk_batches b ON b.id = i.batch_id
		WHERE i.center_user_id=$1 AND b.merchant_id=$2`, recipientID, merchantID).Scan(
		&out.Count, &out.TotalKobo, &out.LastAmountKobo, &out.LastPaidAt)
	return out, err
}

// RecipientPaymentHistory lists the individual payments made to a recipient.
func (s *Store) RecipientPaymentHistory(ctx context.Context, merchantID, recipientID uuid.UUID, limit int) ([]RecipientPaymentEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT b.batch_no, i.amount_kobo, i.status, i.state, i.lga, i.ward, COALESCE(mc.name,''), i.created_at
		FROM bulk_batch_items i
		JOIN bulk_batches b ON b.id = i.batch_id
		LEFT JOIN merchant_centers mc ON mc.id = i.center_id
		WHERE i.center_user_id=$1 AND b.merchant_id=$2
		ORDER BY i.created_at DESC
		LIMIT $3`, recipientID, merchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecipientPaymentEntry
	for rows.Next() {
		var e RecipientPaymentEntry
		if err := rows.Scan(&e.BatchNo, &e.AmountKobo, &e.Status, &e.State, &e.LGA, &e.Ward, &e.CenterName, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
