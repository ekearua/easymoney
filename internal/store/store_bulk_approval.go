package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Batch statuses introduced by the approval workflow.
const (
	BulkBatchDraft     = "draft"
	BulkBatchSubmitted = "submitted"
	BulkBatchApproved  = "approved"
	BulkBatchRejected  = "rejected"

	BulkJobPay = "bulk_pay"
)

// MerchantBulkSettings is the admin-controlled bulk configuration of a merchant.
type MerchantBulkSettings struct {
	BulkEnabled      bool
	LocationRequired bool
	PayoutMode       string
}

// MerchantBulkSettings reads the three bulk switches for a merchant.
func (s *Store) MerchantBulkSettings(ctx context.Context, merchantID uuid.UUID) (MerchantBulkSettings, error) {
	var out MerchantBulkSettings
	err := s.pool.QueryRow(ctx, `
		SELECT bulk_enabled, bulk_location_required, bulk_payout_mode
		FROM merchants WHERE id=$1`, merchantID).Scan(&out.BulkEnabled, &out.LocationRequired, &out.PayoutMode)
	return out, err
}

// SetMerchantBulkSettings updates the three bulk switches in one statement.
func (s *Store) SetMerchantBulkSettings(ctx context.Context, merchantID uuid.UUID, enabled, locationRequired bool, payoutMode string) error {
	if payoutMode != "wallet" && payoutMode != "bank" {
		return errors.New("payout mode must be wallet or bank")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE merchants
		SET bulk_enabled=$2, bulk_location_required=$3, bulk_payout_mode=$4, updated_at=now()
		WHERE id=$1`, merchantID, enabled, locationRequired, payoutMode)
	return err
}

// NewBulkDraft describes a batch built from a filtered recipient list.
type NewBulkDraft struct {
	MerchantID      uuid.UUID
	AmountKobo      int64
	Currency        string
	TTLSeconds      int
	CreatedByMember *uuid.UUID
	PayoutMode      string
	RequireLocation bool
	Filter          RecipientFilter
	CenterID        *uuid.UUID
}

// CreateBulkDraft snapshots the verified recipients matching the filter into a
// draft batch. The filter itself is stored so the approval screen can show what
// was selected even after records change.
func (s *Store) CreateBulkDraft(ctx context.Context, in NewBulkDraft) (BulkBatch, error) {
	if in.AmountKobo <= 0 {
		return BulkBatch{}, errors.New("bulk batch requires a positive amount")
	}
	if in.Currency == "" {
		in.Currency = "NGN"
	}
	if in.TTLSeconds <= 0 {
		in.TTLSeconds = 300
	}
	if in.PayoutMode == "" {
		in.PayoutMode = "wallet"
	}
	where, args := buildRecipientWhere(in.MerchantID, in.Filter, true)
	rows, err := s.pool.Query(ctx, `SELECT cu.id FROM center_users cu WHERE `+where, args...)
	if err != nil {
		return BulkBatch{}, err
	}
	ids := make([]uuid.UUID, 0, 128)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return BulkBatch{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return BulkBatch{}, err
	}
	if len(ids) == 0 {
		return BulkBatch{}, errors.New("no verified recipients match this filter")
	}

	snapshot, err := json.Marshal(map[string]any{
		"state":         in.Filter.State,
		"lga":           in.Filter.LGA,
		"ward":          in.Filter.Ward,
		"polling_unit":  in.Filter.PollingUnit,
		"verification":  RecipientVerified,
		"query":         in.Filter.Query,
		"recipient_ids": ids,
	})
	if err != nil {
		return BulkBatch{}, err
	}

	var center any
	if in.CenterID != nil && *in.CenterID != uuid.Nil {
		center = *in.CenterID
	}
	var member any
	if in.CreatedByMember != nil && *in.CreatedByMember != uuid.Nil {
		member = *in.CreatedByMember
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BulkBatch{}, err
	}
	defer tx.Rollback(ctx)

	var batch BulkBatch
	err = tx.QueryRow(ctx, `
		INSERT INTO bulk_batches(batch_no, merchant_id, center_id, amount_kobo, currency, status,
			ttl_seconds, created_by_member, payout_mode, require_location, filter_snapshot)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, batch_no, merchant_id, COALESCE(center_id,'00000000-0000-0000-0000-000000000000'::uuid),
			amount_kobo, currency, status, ttl_seconds, total_targets, created_at`,
		generateBulkBatchNo(), in.MerchantID, center, in.AmountKobo, in.Currency, BulkBatchDraft,
		in.TTLSeconds, member, in.PayoutMode, in.RequireLocation, snapshot).Scan(
		&batch.ID, &batch.BatchNo, &batch.MerchantID, &batch.CenterID, &batch.AmountKobo,
		&batch.Currency, &batch.Status, &batch.TTLSeconds, &batch.TotalTargets, &batch.CreatedAt)
	if err != nil {
		return BulkBatch{}, err
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO bulk_batch_items(batch_id, center_user_id, phone_e164, amount_kobo, status,
			center_id, state, lga, ward, polling_unit, payout_mode)
		SELECT $1, id, phone_e164, $2, $3, center_id, state, lga, ward, polling_unit, $4
		FROM center_users WHERE id = ANY($5::uuid[])`,
		batch.ID, in.AmountKobo, BulkItemPending, in.PayoutMode, ids)
	if err != nil {
		return BulkBatch{}, err
	}
	batch.TotalTargets = int(tag.RowsAffected())
	if _, err := tx.Exec(ctx, `UPDATE bulk_batches SET total_targets=$1, updated_at=now() WHERE id=$2`,
		batch.TotalTargets, batch.ID); err != nil {
		return BulkBatch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BulkBatch{}, err
	}
	return batch, nil
}

// BulkBatchApproval is the approval-side view of a batch.
type BulkBatchApproval struct {
	BatchID         uuid.UUID
	MerchantID      uuid.UUID
	BatchNo         string
	Status          string
	AmountKobo      int64
	TotalTargets    int
	PayoutMode      string
	RequireLocation bool
	ComplianceFlag  bool
	SubmittedAt     *time.Time
	ApprovedAt      *time.Time
	ApprovedBy      *uuid.UUID
	RejectedReason  string
	FilterSnapshot  string
}

// BulkBatchApprovalInfo loads the approval fields of a batch.
func (s *Store) BulkBatchApprovalInfo(ctx context.Context, merchantID, batchID uuid.UUID) (BulkBatchApproval, error) {
	var out BulkBatchApproval
	var snapshot []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, merchant_id, batch_no, status, amount_kobo, total_targets, payout_mode,
		       require_location, compliance_flag, submitted_at, approved_at, approved_by_member,
		       rejected_reason, filter_snapshot
		FROM bulk_batches WHERE id=$1 AND merchant_id=$2`, batchID, merchantID).Scan(
		&out.BatchID, &out.MerchantID, &out.BatchNo, &out.Status, &out.AmountKobo, &out.TotalTargets,
		&out.PayoutMode, &out.RequireLocation, &out.ComplianceFlag, &out.SubmittedAt, &out.ApprovedAt,
		&out.ApprovedBy, &out.RejectedReason, &snapshot)
	out.FilterSnapshot = string(snapshot)
	return out, err
}

// EnqueueBulkJob schedules a job for a batch, idempotently per (kind, batch).
func (s *Store) EnqueueBulkJob(ctx context.Context, kind string, batchID uuid.UUID, after time.Duration) error {
	runAfter := time.Now().Add(after)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO bulk_jobs(kind, batch_id, run_after, status)
		VALUES($1,$2,$3,$4)
		ON CONFLICT (kind, batch_id) DO UPDATE SET run_after=EXCLUDED.run_after, status=$4, updated_at=now()`,
		kind, batchID, runAfter, BulkJobQueued)
	return err
}

// SubmitBulkBatch moves a draft into the approval queue.
func (s *Store) SubmitBulkBatch(ctx context.Context, merchantID, batchID, memberID uuid.UUID) error {
	var member any
	if memberID != uuid.Nil {
		member = memberID
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE bulk_batches SET status=$3, submitted_at=now(), created_by_member=COALESCE(created_by_member,$4), updated_at=now()
		WHERE id=$1 AND merchant_id=$2 AND status IN ('draft','submitted')`,
		batchID, merchantID, BulkBatchSubmitted, member)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("only a draft batch can be submitted")
	}
	return nil
}

// ApproveBulkBatch releases a submitted batch. The caller enqueues the work job
// so the money path stays in the service layer.
func (s *Store) ApproveBulkBatch(ctx context.Context, merchantID, batchID, memberID uuid.UUID) error {
	var member any
	if memberID != uuid.Nil {
		member = memberID
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE bulk_batches
		SET status=$3, approved_by_member=$4, approved_at=now(), updated_at=now()
		WHERE id=$1 AND merchant_id=$2 AND status IN ('draft','submitted')`,
		batchID, merchantID, BulkBatchApproved, member)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("only a draft or submitted batch can be approved")
	}
	return nil
}

// RejectBulkBatch refuses a batch with a reason.
func (s *Store) RejectBulkBatch(ctx context.Context, merchantID, batchID, memberID uuid.UUID, reason string) error {
	var member any
	if memberID != uuid.Nil {
		member = memberID
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE bulk_batches
		SET status=$3, rejected_by_member=$4, rejected_reason=$5, updated_at=now()
		WHERE id=$1 AND merchant_id=$2 AND status IN ('draft','submitted')`,
		batchID, merchantID, BulkBatchRejected, member, truncateBulkText(reason))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("only a draft or submitted batch can be rejected")
	}
	return nil
}

// MarkDirectItemsPayable lets a batch skip the location window: pending items
// become payable without captured coordinates.
func (s *Store) MarkDirectItemsPayable(ctx context.Context, batchID uuid.UUID) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE bulk_batch_items SET status=$1, updated_at=now()
		WHERE batch_id=$2 AND status=$3`, BulkItemLocationReceived, batchID, BulkItemPending)
	return int(tag.RowsAffected()), err
}

// CountBatchPayableItems counts items awaiting payment.
func (s *Store) CountBatchPayableItems(ctx context.Context, batchID uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM bulk_batch_items WHERE batch_id=$1 AND status=$2`,
		batchID, BulkItemLocationReceived).Scan(&n)
	return n, err
}

// BatchNeedsLocation reports whether the batch uses the location window.
func (s *Store) BatchNeedsLocation(ctx context.Context, batchID uuid.UUID) (bool, string, error) {
	var needs bool
	var mode string
	err := s.pool.QueryRow(ctx, `
		SELECT require_location, payout_mode FROM bulk_batches WHERE id=$1`, batchID).Scan(&needs, &mode)
	return needs, mode, err
}

// BulkBatchStatus returns a batch's current status.
func (s *Store) BulkBatchStatus(ctx context.Context, batchID uuid.UUID) (string, error) {
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM bulk_batches WHERE id=$1`, batchID).Scan(&status)
	return status, err
}

// BatchTargetsForDisplay is a small helper for the approval page: batch number
// plus the location window it will use, rendered as one sentence.
func BatchTargetsForDisplay(approval BulkBatchApproval) string {
	return fmt.Sprintf("%s · %d recipients", approval.BatchNo, approval.TotalTargets)
}
