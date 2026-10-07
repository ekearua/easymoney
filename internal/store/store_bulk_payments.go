package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
)

// Bulk payment item/batch statuses. Items progress pending → location_requested
// → location_received → (paid | a terminal ineligibility/expiry reason).
const (
	BulkItemPending          = "pending"
	BulkItemLocationSent     = "location_requested"
	BulkItemLocationReceived = "location_received"
	BulkItemOutOfRange       = "out_of_range"
	BulkItemPoorAccuracy     = "poor_accuracy"
	BulkItemKYCIneligible    = "kyc_ineligible"
	BulkItemNotRegistered    = "not_registered"
	BulkItemExpired          = "expired"
	BulkItemPaid             = "paid"
	BulkItemFailed           = "failed"

	BulkBatchPending   = "pending"
	BulkBatchStage1    = "stage1"
	BulkBatchStage2    = "stage2"
	BulkBatchCompleted = "completed"
	BulkBatchPartial   = "partial"
	BulkBatchFailed    = "failed"
	BulkBatchCancelled = "cancelled"

	BulkJobStage1 = "bulk_stage1"
	BulkJobStage2 = "bulk_stage2"

	BulkJobQueued  = "queued"
	BulkJobRunning = "running"
	BulkJobDone    = "done"
	BulkJobFailed  = "failed"
)

// MerchantCenter is a merchant-owned point used as the origin for the 50 m
// (configurable) distance check.
type MerchantCenter struct {
	ID         uuid.UUID
	MerchantID uuid.UUID
	Name       string
	Latitude   float64
	Longitude  float64
	RadiusM    int
	Active     bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// CenterUser is a recipient the merchant attached to a center. Exactly one
// center per (merchant, phone).
type CenterUser struct {
	ID          uuid.UUID
	MerchantID  uuid.UUID
	CenterID    uuid.UUID
	PhoneE164   string
	DisplayName string
	UserID      *uuid.UUID
	Source      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// BulkBatch is one merchant-triggered bulk payment run.
type BulkBatch struct {
	ID               uuid.UUID
	BatchNo          string
	MerchantID       uuid.UUID
	CenterID         uuid.UUID
	AmountKobo       int64
	Currency         string
	Status           string
	TTLSeconds       int
	TotalTargets     int
	RequestedCount   int
	RespondedCount   int
	EligibleCount    int
	PaidCount        int
	FailedCount      int
	ExpiredCount     int
	TotalDebitedKobo int64
	ComplianceFlag   bool
	CreatedBy        *uuid.UUID
	CreatedAt        time.Time
	Stage1At         *time.Time
	Stage2DueAt      *time.Time
	CompletedAt      *time.Time
	FailureReason    string
}

// BulkBatchItem is one recipient's progress within a batch.
type BulkBatchItem struct {
	ID                 uuid.UUID
	BatchID            uuid.UUID
	CenterUserID       *uuid.UUID
	UserID             *uuid.UUID
	PhoneE164          string
	Channel            string
	Status             string
	Latitude           *float64
	Longitude          *float64
	AccuracyM          *float64
	DistanceM          *float64
	LocationCapturedAt *time.Time
	RequestSentAt      *time.Time
	AmountKobo         int64
	CreditRef          string
	FailureCode        string
	FailureReason      string
	Attempts           int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// BulkJob is a delayed one-off unit of work claimed by the worker ticker.
type BulkJob struct {
	ID          uuid.UUID
	Kind        string
	BatchID     uuid.UUID
	RunAfter    time.Time
	Status      string
	Attempts    int
	MaxAttempts int
	LastError   string
}

// NewBulkBatch is the input to CreateBulkBatch. Items are snapshotted from the
// center's current recipients.
type NewBulkBatch struct {
	MerchantID uuid.UUID
	CenterID   uuid.UUID
	AmountKobo int64
	Currency   string
	TTLSeconds int
	CreatedBy  uuid.UUID
}

// OpenBulkLocationTarget describes the batch item and center a location reply
// from a phone number should be matched against.
type OpenBulkLocationTarget struct {
	ItemID     uuid.UUID
	BatchID    uuid.UUID
	PhoneE164  string
	CenterLat  float64
	CenterLng  float64
	RadiusM    int
	Stage1At   time.Time
	TTLSeconds int
}

func generateBulkBatchNo() string {
	ref := strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
	return fmt.Sprintf("BP%s%s", time.Now().UTC().Format("20060102150405"), ref)
}

// CreateMerchantCenter stores a new center for a merchant.
func (s *Store) CreateMerchantCenter(ctx context.Context, merchantID uuid.UUID, name string, lat, lng float64, radiusM int) (MerchantCenter, error) {
	if radiusM <= 0 {
		radiusM = 50
	}
	var c MerchantCenter
	err := s.pool.QueryRow(ctx, `
		INSERT INTO merchant_centers(merchant_id, name, latitude, longitude, radius_m)
		VALUES($1,$2,$3,$4,$5)
		RETURNING id, merchant_id, name, latitude, longitude, radius_m, active, created_at, updated_at`,
		merchantID, name, lat, lng, radiusM).Scan(
		&c.ID, &c.MerchantID, &c.Name, &c.Latitude, &c.Longitude, &c.RadiusM, &c.Active, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// ListMerchantCenters returns a merchant's centers, newest first.
func (s *Store) ListMerchantCenters(ctx context.Context, merchantID uuid.UUID) ([]MerchantCenter, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, merchant_id, name, latitude, longitude, radius_m, active, created_at, updated_at
		FROM merchant_centers WHERE merchant_id=$1 ORDER BY created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var centers []MerchantCenter
	for rows.Next() {
		var c MerchantCenter
		if err := rows.Scan(&c.ID, &c.MerchantID, &c.Name, &c.Latitude, &c.Longitude, &c.RadiusM, &c.Active, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		centers = append(centers, c)
	}
	return centers, rows.Err()
}

// MerchantCenterByID returns a center scoped to the merchant.
func (s *Store) MerchantCenterByID(ctx context.Context, merchantID, id uuid.UUID) (MerchantCenter, error) {
	var c MerchantCenter
	err := s.pool.QueryRow(ctx, `
		SELECT id, merchant_id, name, latitude, longitude, radius_m, active, created_at, updated_at
		FROM merchant_centers WHERE id=$1 AND merchant_id=$2`, id, merchantID).Scan(
		&c.ID, &c.MerchantID, &c.Name, &c.Latitude, &c.Longitude, &c.RadiusM, &c.Active, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// DeleteMerchantCenter removes a center (and its recipient list) for a merchant.
func (s *Store) DeleteMerchantCenter(ctx context.Context, merchantID, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM merchant_centers WHERE id=$1 AND merchant_id=$2`, id, merchantID)
	return err
}

// UpsertCenterUser attaches (or re-points) a recipient on a merchant. A phone
// number belongs to exactly one center per merchant.
func (s *Store) UpsertCenterUser(ctx context.Context, merchantID, centerID uuid.UUID, phone, name, source string) (CenterUser, error) {
	if source == "" {
		source = "manual"
	}
	phone = domain.CanonicalE164Phone(phone)
	var cu CenterUser
	err := s.pool.QueryRow(ctx, `
		INSERT INTO center_users(merchant_id, center_id, phone_e164, display_name, source)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (merchant_id, phone_e164) DO UPDATE SET
			center_id=EXCLUDED.center_id,
			display_name=EXCLUDED.display_name,
			source=EXCLUDED.source,
			updated_at=now()
		RETURNING id, merchant_id, center_id, phone_e164, display_name, user_id, source, created_at, updated_at`,
		merchantID, centerID, phone, name, source).Scan(
		&cu.ID, &cu.MerchantID, &cu.CenterID, &cu.PhoneE164, &cu.DisplayName, &cu.UserID, &cu.Source, &cu.CreatedAt, &cu.UpdatedAt)
	return cu, err
}

// ListCenterUsers returns the recipients attached to a center.
func (s *Store) ListCenterUsers(ctx context.Context, centerID uuid.UUID) ([]CenterUser, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, merchant_id, center_id, phone_e164, display_name, user_id, source, created_at, updated_at
		FROM center_users WHERE center_id=$1 ORDER BY created_at ASC`, centerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []CenterUser
	for rows.Next() {
		var cu CenterUser
		if err := rows.Scan(&cu.ID, &cu.MerchantID, &cu.CenterID, &cu.PhoneE164, &cu.DisplayName, &cu.UserID, &cu.Source, &cu.CreatedAt, &cu.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, cu)
	}
	return users, rows.Err()
}

// DeleteCenterUser removes a recipient from the merchant's list.
func (s *Store) DeleteCenterUser(ctx context.Context, merchantID, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM center_users WHERE id=$1 AND merchant_id=$2`, id, merchantID)
	return err
}

// CountCenterUsers returns how many recipients a center has (pre-create check).
func (s *Store) CountCenterUsers(ctx context.Context, centerID uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM center_users WHERE center_id=$1`, centerID).Scan(&n)
	return n, err
}

// ScheduleBulkStage2 throws the stage-2 job onto the delayed queue at the TTL
// boundary. It is idempotent (one stage-2 job per batch).
func (s *Store) ScheduleBulkStage2(ctx context.Context, batchID uuid.UUID, due time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO bulk_jobs(kind, batch_id, run_after, status)
		VALUES($1,$2,$3,$4)
		ON CONFLICT (kind, batch_id) DO NOTHING`, BulkJobStage2, batchID, due, BulkJobQueued)
	return err
}

// CreateBulkBatch snapshots the center's recipients into a batch, records the
// stage-1 job, and returns the batch. It is atomic: an empty center or a
// missing amount inserts nothing.
func (s *Store) CreateBulkBatch(ctx context.Context, in NewBulkBatch) (BulkBatch, error) {
	if in.AmountKobo <= 0 {
		return BulkBatch{}, errors.New("bulk batch requires a positive amount")
	}
	if in.Currency == "" {
		in.Currency = "NGN"
	}
	if in.TTLSeconds <= 0 {
		in.TTLSeconds = 300
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BulkBatch{}, err
	}
	defer tx.Rollback(ctx)

	var centerActive bool
	if err := tx.QueryRow(ctx, `SELECT active FROM merchant_centers WHERE id=$1 AND merchant_id=$2`, in.CenterID, in.MerchantID).Scan(&centerActive); err != nil {
		return BulkBatch{}, fmt.Errorf("center not found: %w", err)
	}
	if !centerActive {
		return BulkBatch{}, errors.New("center is inactive")
	}

	batchNo := generateBulkBatchNo()
	var createdBy *uuid.UUID
	if in.CreatedBy != uuid.Nil {
		createdBy = &in.CreatedBy
	}
	var b BulkBatch
	err = tx.QueryRow(ctx, `
		INSERT INTO bulk_batches(batch_no, merchant_id, center_id, amount_kobo, currency, status, ttl_seconds, created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, batch_no, merchant_id, center_id, amount_kobo, currency, status, ttl_seconds,
			total_targets, created_at`,
		batchNo, in.MerchantID, in.CenterID, in.AmountKobo, in.Currency, BulkBatchPending, in.TTLSeconds, createdBy).Scan(
		&b.ID, &b.BatchNo, &b.MerchantID, &b.CenterID, &b.AmountKobo, &b.Currency, &b.Status, &b.TTLSeconds,
		&b.TotalTargets, &b.CreatedAt)
	if err != nil {
		return BulkBatch{}, err
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO bulk_batch_items(batch_id, center_user_id, phone_e164, amount_kobo, status)
		SELECT $1, id, phone_e164, $2, $3 FROM center_users WHERE center_id=$4 AND merchant_id=$5`,
		b.ID, in.AmountKobo, BulkItemPending, in.CenterID, in.MerchantID)
	if err != nil {
		return BulkBatch{}, err
	}
	b.TotalTargets = int(tag.RowsAffected())
	if b.TotalTargets == 0 {
		return BulkBatch{}, errors.New("center has no recipients")
	}
	if _, err := tx.Exec(ctx, `UPDATE bulk_batches SET total_targets=$1 WHERE id=$2`, b.TotalTargets, b.ID); err != nil {
		return BulkBatch{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bulk_jobs(kind, batch_id, run_after, status) VALUES($1,$2,now(),$3)`,
		BulkJobStage1, b.ID, BulkJobQueued); err != nil {
		return BulkBatch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BulkBatch{}, err
	}
	return b, nil
}

// BulkBatchByID returns a batch scoped to the merchant.
func (s *Store) BulkBatchByID(ctx context.Context, merchantID, id uuid.UUID) (BulkBatch, error) {
	var b BulkBatch
	err := s.pool.QueryRow(ctx, `
		SELECT id, batch_no, merchant_id, center_id, amount_kobo, currency, status, ttl_seconds,
			total_targets, requested_count, responded_count, eligible_count, paid_count, failed_count,
			expired_count, total_debited_kobo, compliance_flag,
			created_by, created_at, stage1_at, stage2_due_at, completed_at, failure_reason
		FROM bulk_batches WHERE id=$1 AND merchant_id=$2`, id, merchantID).Scan(
		&b.ID, &b.BatchNo, &b.MerchantID, &b.CenterID, &b.AmountKobo, &b.Currency, &b.Status, &b.TTLSeconds,
		&b.TotalTargets, &b.RequestedCount, &b.RespondedCount, &b.EligibleCount, &b.PaidCount, &b.FailedCount,
		&b.ExpiredCount, &b.TotalDebitedKobo, &b.ComplianceFlag,
		&b.CreatedBy, &b.CreatedAt, &b.Stage1At, &b.Stage2DueAt, &b.CompletedAt, &b.FailureReason)
	return b, err
}

// BulkBatchByIDAny returns a batch regardless of merchant (worker use).
func (s *Store) BulkBatchByIDAny(ctx context.Context, id uuid.UUID) (BulkBatch, error) {
	var b BulkBatch
	err := s.pool.QueryRow(ctx, `
		SELECT id, batch_no, merchant_id, center_id, amount_kobo, currency, status, ttl_seconds,
			total_targets, requested_count, responded_count, eligible_count, paid_count, failed_count,
			expired_count, total_debited_kobo, compliance_flag,
			created_by, created_at, stage1_at, stage2_due_at, completed_at, failure_reason
		FROM bulk_batches WHERE id=$1`, id).Scan(
		&b.ID, &b.BatchNo, &b.MerchantID, &b.CenterID, &b.AmountKobo, &b.Currency, &b.Status, &b.TTLSeconds,
		&b.TotalTargets, &b.RequestedCount, &b.RespondedCount, &b.EligibleCount, &b.PaidCount, &b.FailedCount,
		&b.ExpiredCount, &b.TotalDebitedKobo, &b.ComplianceFlag,
		&b.CreatedBy, &b.CreatedAt, &b.Stage1At, &b.Stage2DueAt, &b.CompletedAt, &b.FailureReason)
	return b, err
}

// ListBulkBatches returns a merchant's batches, newest first.
func (s *Store) ListBulkBatches(ctx context.Context, merchantID uuid.UUID, limit int) ([]BulkBatch, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, batch_no, merchant_id, center_id, amount_kobo, currency, status, ttl_seconds,
			total_targets, requested_count, responded_count, eligible_count, paid_count, failed_count,
			expired_count, total_debited_kobo, compliance_flag,
			created_by, created_at, stage1_at, stage2_due_at, completed_at, failure_reason
		FROM bulk_batches WHERE merchant_id=$1 ORDER BY created_at DESC LIMIT $2`, merchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []BulkBatch
	for rows.Next() {
		var b BulkBatch
		if err := rows.Scan(
			&b.ID, &b.BatchNo, &b.MerchantID, &b.CenterID, &b.AmountKobo, &b.Currency, &b.Status, &b.TTLSeconds,
			&b.TotalTargets, &b.RequestedCount, &b.RespondedCount, &b.EligibleCount, &b.PaidCount, &b.FailedCount,
			&b.ExpiredCount, &b.TotalDebitedKobo, &b.ComplianceFlag,
			&b.CreatedBy, &b.CreatedAt, &b.Stage1At, &b.Stage2DueAt, &b.CompletedAt, &b.FailureReason); err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

// ListBulkBatchItems returns a batch's items, optionally filtered by status.
func (s *Store) ListBulkBatchItems(ctx context.Context, batchID uuid.UUID, status string, limit int) ([]BulkBatchItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, batch_id, center_user_id, user_id, phone_e164, channel, status,
			latitude, longitude, accuracy_m, distance_m, location_captured_at, request_sent_at,
			amount_kobo, credit_ref, failure_code, failure_reason, attempts, created_at, updated_at
		FROM bulk_batch_items
		WHERE batch_id=$1 AND ($2='' OR status=$2)
		ORDER BY phone_e164 ASC LIMIT $3`, batchID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []BulkBatchItem
	for rows.Next() {
		var it BulkBatchItem
		if err := rows.Scan(&it.ID, &it.BatchID, &it.CenterUserID, &it.UserID, &it.PhoneE164, &it.Channel, &it.Status,
			&it.Latitude, &it.Longitude, &it.AccuracyM, &it.DistanceM, &it.LocationCapturedAt, &it.RequestSentAt,
			&it.AmountKobo, &it.CreditRef, &it.FailureCode, &it.FailureReason, &it.Attempts, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// PendingBulkItems returns a batch's not-yet-requested items.
func (s *Store) PendingBulkItems(ctx context.Context, batchID uuid.UUID, limit int) ([]BulkBatchItem, error) {
	return s.ListBulkBatchItems(ctx, batchID, BulkItemPending, limit)
}

// MarkBulkItemRequested records a successful stage-1 location request.
func (s *Store) MarkBulkItemRequested(ctx context.Context, itemID uuid.UUID, channel string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE bulk_batch_items
		SET status=$1, channel=$2, request_sent_at=now(), attempts=attempts+1, updated_at=now()
		WHERE id=$3`, BulkItemLocationSent, channel, itemID)
	return err
}

// RecordBulkItemSendFailure records a failed stage-1 send attempt.
func (s *Store) RecordBulkItemSendFailure(ctx context.Context, itemID uuid.UUID, message string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE bulk_batch_items SET attempts=attempts+1, failure_reason=$1, updated_at=now()
		WHERE id=$2`, truncateBulkText(message), itemID)
	return err
}

// SetBulkItemLocation stores a captured position and its computed distance,
// moving the item to the outcome status.
func (s *Store) SetBulkItemLocation(ctx context.Context, itemID uuid.UUID, lat, lng, accuracyM, distanceM float64, status string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE bulk_batch_items
		SET latitude=$1, longitude=$2, accuracy_m=$3, distance_m=$4,
		    location_captured_at=now(), status=$5, updated_at=now()
		WHERE id=$6`, lat, lng, accuracyM, distanceM, status, itemID)
	return err
}

// SetBulkItemStatus moves an item to a terminal outcome with a reason.
func (s *Store) SetBulkItemStatus(ctx context.Context, itemID uuid.UUID, status, code, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE bulk_batch_items SET status=$1, failure_code=$2, failure_reason=$3, updated_at=now()
		WHERE id=$4`, status, code, truncateBulkText(reason), itemID)
	return err
}

// FindOpenBulkItemByPhone returns the most recent item awaiting a location from
// this phone number, with the center it is measured against. found is false
// when no open request exists.
func (s *Store) FindOpenBulkItemByPhone(ctx context.Context, phone string) (OpenBulkLocationTarget, bool, error) {
	var t OpenBulkLocationTarget
	err := s.pool.QueryRow(ctx, `
		SELECT i.id, i.batch_id, i.phone_e164, c.latitude, c.longitude, c.radius_m, b.stage1_at, b.ttl_seconds
		FROM bulk_batch_items i
		JOIN bulk_batches b ON b.id = i.batch_id
		JOIN merchant_centers c ON c.id = b.center_id
		WHERE i.phone_e164=$1 AND i.status=$2 AND b.status IN ($3,$4)
		ORDER BY i.request_sent_at DESC NULLS LAST
		LIMIT 1`, phone, BulkItemLocationSent, BulkBatchStage1, BulkBatchStage2).Scan(
		&t.ItemID, &t.BatchID, &t.PhoneE164, &t.CenterLat, &t.CenterLng, &t.RadiusM, &t.Stage1At, &t.TTLSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return OpenBulkLocationTarget{}, false, nil
	}
	if err != nil {
		return OpenBulkLocationTarget{}, false, err
	}
	return t, true, nil
}

// FindOpenBulkItemByID resolves an open request by item id, used by the
// browser-geolocation fallback link.
func (s *Store) FindOpenBulkItemByID(ctx context.Context, itemID uuid.UUID) (OpenBulkLocationTarget, bool, error) {
	var t OpenBulkLocationTarget
	err := s.pool.QueryRow(ctx, `
		SELECT i.id, i.batch_id, i.phone_e164, c.latitude, c.longitude, c.radius_m, b.stage1_at, b.ttl_seconds
		FROM bulk_batch_items i
		JOIN bulk_batches b ON b.id = i.batch_id
		JOIN merchant_centers c ON c.id = b.center_id
		WHERE i.id=$1 AND i.status=$2 AND b.status IN ($3,$4)
		LIMIT 1`, itemID, BulkItemLocationSent, BulkBatchStage1, BulkBatchStage2).Scan(
		&t.ItemID, &t.BatchID, &t.PhoneE164, &t.CenterLat, &t.CenterLng, &t.RadiusM, &t.Stage1At, &t.TTLSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return OpenBulkLocationTarget{}, false, nil
	}
	if err != nil {
		return OpenBulkLocationTarget{}, false, err
	}
	return t, true, nil
}

// MarkBulkBatchStage1 records that location requests went out and schedules
// stage 2 at the TTL boundary.
func (s *Store) MarkBulkBatchStage1(ctx context.Context, batchID uuid.UUID, requested int, stage2Due time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE bulk_batches SET status=$1, stage1_at=now(), requested_count=$2, stage2_due_at=$3, updated_at=now()
		WHERE id=$4`, BulkBatchStage2, requested, stage2Due, batchID)
	return err
}

// MarkBulkBatchStage2 marks a batch as paying.
func (s *Store) MarkBulkBatchStage2(ctx context.Context, batchID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE bulk_batches SET status=$1, updated_at=now() WHERE id=$2`, BulkBatchStage2, batchID)
	return err
}

// ExpireUnrespondedBulkItems moves still-pending/requested items to expired.
func (s *Store) ExpireUnrespondedBulkItems(ctx context.Context, batchID uuid.UUID) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE bulk_batch_items SET status=$1, failure_code='expired', failure_reason='No location received within the window', updated_at=now()
		WHERE batch_id=$2 AND status IN ($3,$4)`,
		BulkItemExpired, batchID, BulkItemPending, BulkItemLocationSent)
	return int(tag.RowsAffected()), err
}

// FinalizeBulkBatch sets the terminal batch status and refreshes counters from
// the items, so a crash mid-run still reports the truth.
func (s *Store) FinalizeBulkBatch(ctx context.Context, batchID uuid.UUID, failureReason string) (BulkBatch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BulkBatch{}, err
	}
	defer tx.Rollback(ctx)

	var requested, responded, eligible, paid, failed, expired int
	var debited int64
	if err := tx.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status IN ('location_requested','location_received','out_of_range','poor_accuracy','kyc_ineligible','expired','paid','failed')),
			count(*) FILTER (WHERE status IN ('location_received','out_of_range','poor_accuracy')),
			count(*) FILTER (WHERE status IN ('location_received','paid')),
			count(*) FILTER (WHERE status='paid'),
			count(*) FILTER (WHERE status IN ('failed','not_registered','kyc_ineligible','out_of_range','poor_accuracy')),
			count(*) FILTER (WHERE status='expired'),
			COALESCE(SUM(CASE WHEN status='paid' THEN amount_kobo ELSE 0 END),0)
		FROM bulk_batch_items WHERE batch_id=$1`, batchID).Scan(
		&requested, &responded, &eligible, &paid, &failed, &expired, &debited); err != nil {
		return BulkBatch{}, err
	}
	status := BulkBatchCompleted
	if paid == 0 {
		status = BulkBatchFailed
	} else if failed+expired > 0 {
		status = BulkBatchPartial
	}
	var b BulkBatch
	err = tx.QueryRow(ctx, `
		UPDATE bulk_batches SET status=$1, requested_count=$2, responded_count=$3, eligible_count=$4,
			paid_count=$5, failed_count=$6, expired_count=$7, total_debited_kobo=$8,
			completed_at=now(), failure_reason=$9, updated_at=now()
		WHERE id=$10
		RETURNING id, batch_no, merchant_id, center_id, amount_kobo, currency, status, ttl_seconds,
			total_targets, requested_count, responded_count, eligible_count, paid_count, failed_count,
			expired_count, total_debited_kobo, compliance_flag,
			created_by, created_at, stage1_at, stage2_due_at, completed_at, failure_reason`,
		status, requested, responded, eligible, paid, failed, expired, debited, truncateBulkText(failureReason), batchID).Scan(
		&b.ID, &b.BatchNo, &b.MerchantID, &b.CenterID, &b.AmountKobo, &b.Currency, &b.Status, &b.TTLSeconds,
		&b.TotalTargets, &b.RequestedCount, &b.RespondedCount, &b.EligibleCount, &b.PaidCount, &b.FailedCount,
		&b.ExpiredCount, &b.TotalDebitedKobo, &b.ComplianceFlag,
		&b.CreatedBy, &b.CreatedAt, &b.Stage1At, &b.Stage2DueAt, &b.CompletedAt, &b.FailureReason)
	if err != nil {
		return BulkBatch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BulkBatch{}, err
	}
	return b, nil
}

// SetBulkBatchComplianceFlag flags a batch whose total exceeds the merchant's
// KYB payout ceiling. Flag-only: payment proceeds, compliance follows up.
func (s *Store) SetBulkBatchComplianceFlag(ctx context.Context, batchID uuid.UUID, flag bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE bulk_batches SET compliance_flag=$1, updated_at=now() WHERE id=$2`, flag, batchID)
	return err
}

// PostBulkPaymentItem debits the merchant's business wallet and credits the
// recipient's wallet for one eligible item, in a single transaction. It is
// idempotent on the item: a replay returns the original credit reference.
func (s *Store) PostBulkPaymentItem(ctx context.Context, itemID uuid.UUID) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var (
		batchID    uuid.UUID
		batchNo    string
		merchantID uuid.UUID
		status     string
		creditRef  string
		amount     int64
		userID     *uuid.UUID
		phone      string
	)
	err = tx.QueryRow(ctx, `
		SELECT i.batch_id, b.batch_no, b.merchant_id, i.status, i.credit_ref, i.amount_kobo, i.user_id, i.phone_e164
		FROM bulk_batch_items i
		JOIN bulk_batches b ON b.id = i.batch_id
		WHERE i.id=$1 FOR UPDATE OF i`, itemID).Scan(
		&batchID, &batchNo, &merchantID, &status, &creditRef, &amount, &userID, &phone)
	if err != nil {
		return "", err
	}
	if status == BulkItemPaid && creditRef != "" {
		return creditRef, nil
	}
	if status != BulkItemLocationReceived {
		return "", fmt.Errorf("bulk item %s is %s, not payable", itemID, status)
	}
	if userID == nil {
		return "", errors.New("bulk item has no resolved recipient account")
	}

	wallet, err := s.ensureWalletTx(ctx, tx, WalletOwnerBusiness, merchantID, "", WalletStatusActive)
	if err != nil {
		return "", err
	}
	balance, err := walletBalanceQ(ctx, tx, wallet.AccountCode)
	if err != nil {
		return "", err
	}
	if balance < amount {
		return "", fmt.Errorf("%w: business wallet has %d kobo, need %d kobo", ErrInsufficientWalletBalance, balance, amount)
	}

	ref := fmt.Sprintf("bulk:%s:%s", batchNo, itemID)
	description := fmt.Sprintf("Bulk payment %s to %s", batchNo, phone)
	if err := s.creditWalletTx(ctx, tx, *userID, wallet.AccountCode, amount, ref, description); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bulk_batch_items SET status=$1, credit_ref=$2, updated_at=now() WHERE id=$3`,
		BulkItemPaid, ref, itemID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return ref, nil
}

// BusinessWalletBalance returns the merchant's spendable business wallet
// balance, and whether a wallet row exists.
func (s *Store) BusinessWalletBalance(ctx context.Context, merchantID uuid.UUID) (int64, bool, error) {
	wallet, err := s.WalletByOwner(ctx, WalletOwnerBusiness, merchantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	balance, err := s.WalletBalance(ctx, wallet.ID)
	if err != nil {
		return 0, false, err
	}
	return balance, true, nil
}

// ClaimBulkJobs atomically claims due jobs for one worker.
func (s *Store) ClaimBulkJobs(ctx context.Context, limit int) ([]BulkJob, error) {
	if limit <= 0 {
		limit = 5
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id, kind, batch_id, run_after, status, attempts, max_attempts, last_error
		FROM bulk_jobs
		WHERE status=$1 AND run_after <= now()
		ORDER BY run_after ASC
		FOR UPDATE SKIP LOCKED
		LIMIT $2`, BulkJobQueued, limit)
	if err != nil {
		return nil, err
	}
	var jobs []BulkJob
	for rows.Next() {
		var j BulkJob
		if err := rows.Scan(&j.ID, &j.Kind, &j.BatchID, &j.RunAfter, &j.Status, &j.Attempts, &j.MaxAttempts, &j.LastError); err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range jobs {
		if _, err := tx.Exec(ctx, `UPDATE bulk_jobs SET status=$1, attempts=attempts+1, updated_at=now() WHERE id=$2`, BulkJobRunning, jobs[i].ID); err != nil {
			return nil, err
		}
		jobs[i].Status = BulkJobRunning
		jobs[i].Attempts++
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return jobs, nil
}

// CompleteBulkJob marks a job done.
func (s *Store) CompleteBulkJob(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE bulk_jobs SET status=$1, last_error='', updated_at=now() WHERE id=$2`, BulkJobDone, id)
	return err
}

// RetryBulkJob requeues a job with an exponential backoff, or marks it failed
// once it exhausts max_attempts.
func (s *Store) RetryBulkJob(ctx context.Context, id uuid.UUID, jobErr string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var attempts, maxAttempts int
	if err := tx.QueryRow(ctx, `SELECT attempts, max_attempts FROM bulk_jobs WHERE id=$1 FOR UPDATE`, id).Scan(&attempts, &maxAttempts); err != nil {
		return err
	}
	jobErr = truncateBulkText(jobErr)
	if attempts >= maxAttempts {
		if _, err := tx.Exec(ctx, `UPDATE bulk_jobs SET status=$1, last_error=$2, updated_at=now() WHERE id=$3`, BulkJobFailed, jobErr, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	backoff := time.Duration(1<<uint(attempts)) * 15 * time.Second
	if _, err := tx.Exec(ctx, `
		UPDATE bulk_jobs SET status=$1, run_after=now()+$2::interval, last_error=$3, updated_at=now() WHERE id=$4`,
		BulkJobQueued, backoff.String(), jobErr, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetBulkItemUserID links a center user's resolved Xego account.
func (s *Store) SetBulkItemUserID(ctx context.Context, itemID, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE bulk_batch_items SET user_id=$1, updated_at=now() WHERE id=$2`, userID, itemID)
	return err
}

// ResolveCenterUserByPhone returns the center-user row for a phone number
// scoped to a merchant, if present.
func (s *Store) ResolveCenterUserByPhone(ctx context.Context, merchantID uuid.UUID, phone string) (CenterUser, bool, error) {
	var cu CenterUser
	err := s.pool.QueryRow(ctx, `
		SELECT id, merchant_id, center_id, phone_e164, display_name, user_id, source, created_at, updated_at
		FROM center_users WHERE merchant_id=$1 AND phone_e164=$2`, merchantID, phone).Scan(
		&cu.ID, &cu.MerchantID, &cu.CenterID, &cu.PhoneE164, &cu.DisplayName, &cu.UserID, &cu.Source, &cu.CreatedAt, &cu.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CenterUser{}, false, nil
	}
	if err != nil {
		return CenterUser{}, false, err
	}
	return cu, true, nil
}

func truncateBulkText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 500 {
		return value
	}
	return value[:500]
}