// P1 (restricted-purpose program purses): a sponsor merchant funds a program;
// enrolled individuals ("beneficiaries") hold a restricted purse whose money
// can only be spent at the program's approved merchants.
//
// The design intent, in one line: a purse is a real ledger account, not a tag
// on the wallet. Everything below therefore has to respect three invariants:
//
//  1. A purse balance is the net of its ledger account (2302_program_purse:<id>),
//     never a stored number that could drift from the book.
//  2. Nothing here ever touches 2301_user_wallet, so the withdrawal rail cannot
//     reach restricted money.
//  3. The allow-list check is re-evaluated inside the same transaction that
//     moves the money (PurseSpendAllowedTx), not only where the intent was
//     recorded — a de-listed merchant must not be payable by a stale draft.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Merchant types (classification only; the allow-list authorises a spend).
const (
	MerchantTypeStandard        = "standard"
	MerchantTypeSponsor         = "sponsor"
	MerchantTypeProgramAcceptor = "program_acceptor"
)

// Program statuses.
const (
	ProgramStatusActive   = "active"
	ProgramStatusInactive = "inactive"
)

// Allow-list states. A blocked merchant keeps its row so the record of the
// previous approval (and the reason for the block) survives.
const (
	ProgramMemberApproved = "approved"
	ProgramMemberBlocked  = "blocked"
)

// Enrollment statuses.
const (
	EnrollmentActive  = "active"
	EnrollmentRemoved = "removed"
)

// Purse statuses.
const (
	PurseStatusActive = "active"
	PurseStatusClosed = "closed"
)

// Ledger source types for program postings, so the summary below can separate
// money in, money spent, and money returned from each other.
const (
	LedgerSourceProgramFunding = "program_purse"
	LedgerSourcePurseRefund    = "program_purse_return"
)

// ErrPurseMerchantNotApproved is returned when a purse-funded movement names a
// merchant the program has not approved (blocked, removed, or never added).
var ErrPurseMerchantNotApproved = errors.New("this program's funds can only be spent at its approved merchants")

// ErrPurseExpired is returned when a purse's validity window has closed.
var ErrPurseExpired = errors.New("this program purse has expired")

// ErrPurseNotActive is returned when a purse has already been closed/returned.
var ErrPurseNotActive = errors.New("this program purse is closed")

// ErrProgramNotEnrolled is returned when a beneficiary has no active enrollment
// in a program, so no purse can be opened for them. Callers must refuse the
// disbursement rather than fall back to an unrestricted credit.
var ErrProgramNotEnrolled = errors.New("recipient is not enrolled in this program")

// MerchantProgram is one sponsor-funded program.
type MerchantProgram struct {
	ID                  uuid.UUID
	Code                string
	Name                string
	SponsorMerchantID   uuid.UUID
	SponsorName         string
	PurposeLabel        string
	Description         string
	DefaultValidityDays int
	Status              string
	CreatedBy           *uuid.UUID
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// NewProgram is the input to CreateProgram.
type NewProgram struct {
	Code                string
	Name                string
	SponsorMerchantID   uuid.UUID
	PurposeLabel        string
	Description         string
	DefaultValidityDays int
	CreatedBy           uuid.UUID
}

// ProgramMember is one allow-list row with its merchant's display name.
type ProgramMember struct {
	ProgramID    uuid.UUID
	MerchantID   uuid.UUID
	MerchantName string
	MerchantSlug string
	Status       string
	BlockReason  string
	AddedBy      *uuid.UUID
	AddedAt      time.Time
	RemovedAt    *time.Time
}

// ProgramEnrollment links a program to an individual account.
type ProgramEnrollment struct {
	ID         uuid.UUID
	ProgramID  uuid.UUID
	UserID     uuid.UUID
	UserName   string
	UserPhone  string
	Status     string
	EnrolledBy *uuid.UUID
	EnrolledAt time.Time
}

// ProgramPurse is one beneficiary's restricted balance. BalanceKobo is derived
// from the ledger account, never passed in.
type ProgramPurse struct {
	ID           uuid.UUID
	ProgramID    uuid.UUID
	ProgramCode  string
	ProgramName  string
	UserID       uuid.UUID
	UserName     string
	UserPhone    string
	AccountCode  string
	Status       string
	OpenedAt     time.Time
	ExpiresAt    *time.Time
	ClosedAt     *time.Time
	ReturnedKobo int64
	BalanceKobo  int64
}

// ExpiredPurse is a purse past its validity window with money still in it,
// awaiting return to the sponsor.
type ExpiredPurse struct {
	Purse             ProgramPurse
	SponsorMerchantID uuid.UUID
}

// ProgramSummary is the per-program roll-up for the admin console.
type ProgramSummary struct {
	EnrolledCount     int
	ActivePurseCount  int
	ApprovedMerchants int
	DisbursedKobo     int64
	SpentKobo         int64
	ReturnedKobo      int64
	HeldKobo          int64
}

const programPurseColumns = `
	pu.id, pu.program_id, mp.code, mp.name, pu.user_id,
	COALESCE(u.display_name,''), COALESCE(u.whatsapp_number,''),
	pu.account_code, pu.status, pu.opened_at, pu.expires_at,
	pu.closed_at, pu.returned_kobo,
	COALESCE((SELECT SUM(CASE WHEN e.entry_type='credit' THEN e.amount_kobo ELSE -e.amount_kobo END)
	          FROM ledger_entries e WHERE e.account = pu.account_code), 0)`

func scanProgramPurse(row pgx.Row) (ProgramPurse, error) {
	var p ProgramPurse
	err := row.Scan(&p.ID, &p.ProgramID, &p.ProgramCode, &p.ProgramName, &p.UserID,
		&p.UserName, &p.UserPhone, &p.AccountCode, &p.Status, &p.OpenedAt,
		&p.ExpiresAt, &p.ClosedAt, &p.ReturnedKobo, &p.BalanceKobo)
	return p, err
}

// PurseAccountCode mints the ledger account code for a purse. The code is the
// only binding between the purse row and its balance.
func PurseAccountCode(purseID uuid.UUID) string {
	return LedgerAccountProgramPurse + ":" + purseID.String()
}

// CreateProgram records a new sponsored program.
func (s *Store) CreateProgram(ctx context.Context, in NewProgram) (MerchantProgram, error) {
	code := strings.ToLower(strings.TrimSpace(in.Code))
	if code == "" {
		return MerchantProgram{}, errors.New("program code is required")
	}
	if strings.TrimSpace(in.Name) == "" {
		return MerchantProgram{}, errors.New("program name is required")
	}
	if in.SponsorMerchantID == uuid.Nil {
		return MerchantProgram{}, errors.New("program requires a sponsor merchant")
	}
	if in.DefaultValidityDays < 0 {
		return MerchantProgram{}, errors.New("default validity days cannot be negative")
	}
	var createdBy *uuid.UUID
	if in.CreatedBy != uuid.Nil {
		createdBy = &in.CreatedBy
	}
	var p MerchantProgram
	err := s.pool.QueryRow(ctx, `
		INSERT INTO merchant_programs(code, name, sponsor_merchant_id, purpose_label,
			description, default_validity_days, created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, code, name, sponsor_merchant_id, purpose_label, description,
		          default_validity_days, status, created_by, created_at, updated_at`,
		code, strings.TrimSpace(in.Name), in.SponsorMerchantID,
		strings.TrimSpace(in.PurposeLabel), strings.TrimSpace(in.Description),
		in.DefaultValidityDays, createdBy).Scan(
		&p.ID, &p.Code, &p.Name, &p.SponsorMerchantID, &p.PurposeLabel,
		&p.Description, &p.DefaultValidityDays, &p.Status, &p.CreatedBy,
		&p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ProgramByID resolves a program with its sponsor's display name.
func (s *Store) ProgramByID(ctx context.Context, id uuid.UUID) (MerchantProgram, error) {
	return s.programWhere(ctx, `mp.id=$1`, id)
}

// ProgramByCode resolves a program by its operator-chosen code.
func (s *Store) ProgramByCode(ctx context.Context, code string) (MerchantProgram, error) {
	return s.programWhere(ctx, `mp.code=$1`, strings.ToLower(strings.TrimSpace(code)))
}

func (s *Store) programWhere(ctx context.Context, where string, arg any) (MerchantProgram, error) {
	var p MerchantProgram
	err := s.pool.QueryRow(ctx, `
		SELECT mp.id, mp.code, mp.name, mp.sponsor_merchant_id, COALESCE(m.name,''),
		       mp.purpose_label, mp.description, mp.default_validity_days, mp.status,
		       mp.created_by, mp.created_at, mp.updated_at
		FROM merchant_programs mp
		LEFT JOIN merchants m ON m.id = mp.sponsor_merchant_id
		WHERE `+where, arg).Scan(
		&p.ID, &p.Code, &p.Name, &p.SponsorMerchantID, &p.SponsorName,
		&p.PurposeLabel, &p.Description, &p.DefaultValidityDays, &p.Status,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ListPrograms returns programs, newest first, for the admin console.
func (s *Store) ListPrograms(ctx context.Context, limit int) ([]MerchantProgram, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT mp.id, mp.code, mp.name, mp.sponsor_merchant_id, COALESCE(m.name,''),
		       mp.purpose_label, mp.description, mp.default_validity_days, mp.status,
		       mp.created_by, mp.created_at, mp.updated_at
		FROM merchant_programs mp
		LEFT JOIN merchants m ON m.id = mp.sponsor_merchant_id
		ORDER BY mp.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var programs []MerchantProgram
	for rows.Next() {
		var p MerchantProgram
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.SponsorMerchantID, &p.SponsorName,
			&p.PurposeLabel, &p.Description, &p.DefaultValidityDays, &p.Status,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		programs = append(programs, p)
	}
	return programs, rows.Err()
}

// ProgramsForSponsor lists the programs a sponsor merchant owns.
func (s *Store) ProgramsForSponsor(ctx context.Context, merchantID uuid.UUID) ([]MerchantProgram, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT mp.id, mp.code, mp.name, mp.sponsor_merchant_id, COALESCE(m.name,''),
		       mp.purpose_label, mp.description, mp.default_validity_days, mp.status,
		       mp.created_by, mp.created_at, mp.updated_at
		FROM merchant_programs mp
		LEFT JOIN merchants m ON m.id = mp.sponsor_merchant_id
		WHERE mp.sponsor_merchant_id=$1
		ORDER BY mp.created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var programs []MerchantProgram
	for rows.Next() {
		var p MerchantProgram
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.SponsorMerchantID, &p.SponsorName,
			&p.PurposeLabel, &p.Description, &p.DefaultValidityDays, &p.Status,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		programs = append(programs, p)
	}
	return programs, rows.Err()
}

// SetProgramStatus activates or deactivates a program. Deactivation stops new
// purse spends immediately (PurseSpendAllowedTx requires an active program) but
// does not touch balances: money already granted stays until spent or expires.
func (s *Store) SetProgramStatus(ctx context.Context, id uuid.UUID, status string) error {
	if status != ProgramStatusActive && status != ProgramStatusInactive {
		return fmt.Errorf("invalid program status %q", status)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE merchant_programs SET status=$2, updated_at=now() WHERE id=$1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("program not found")
	}
	return nil
}

// SetMerchantType classifies a merchant (standard | sponsor | program_acceptor).
// Classification only: it never affects what a program allows.
func (s *Store) SetMerchantType(ctx context.Context, merchantID uuid.UUID, merchantType string) error {
	switch merchantType {
	case MerchantTypeStandard, MerchantTypeSponsor, MerchantTypeProgramAcceptor:
	default:
		return fmt.Errorf("invalid merchant type %q", merchantType)
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE merchants SET merchant_type=$2, updated_at=now() WHERE id=$1`, merchantID, merchantType)
	return err
}

// ---------------------------------------------------------------------------
// Allow-list
// ---------------------------------------------------------------------------

// AddProgramMerchant approves a merchant to accept a program's funds. The
// insert is filtered on an active merchant, which is what keeps the internal
// system merchants (the individual-pay and wallet-topup recipients, both
// seeded inactive) off every allow-list: a purse must never fund a
// person-to-person transfer or a wallet top-up.
func (s *Store) AddProgramMerchant(ctx context.Context, programID, merchantID uuid.UUID, addedBy uuid.UUID) error {
	var by *uuid.UUID
	if addedBy != uuid.Nil {
		by = &addedBy
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO merchant_program_members(program_id, merchant_id, status, block_reason, added_by, added_at, removed_at)
		SELECT $1, m.id, 'approved', '', $3, now(), NULL
		FROM merchants m
		WHERE m.id=$2 AND m.active=true
		ON CONFLICT (program_id, merchant_id) DO UPDATE
		SET status='approved', block_reason='', added_by=EXCLUDED.added_by,
		    added_at=now(), removed_at=NULL`,
		programID, merchantID, by)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("merchant not found or not active")
	}
	return nil
}

// BlockProgramMerchant refuses one merchant for a program without losing the
// record that it was once approved. Effective immediately on the next spend.
func (s *Store) BlockProgramMerchant(ctx context.Context, programID, merchantID uuid.UUID, reason string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE merchant_program_members
		SET status='blocked', block_reason=$3
		WHERE program_id=$1 AND merchant_id=$2`,
		programID, merchantID, strings.TrimSpace(reason))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("merchant is not on this program's allow-list")
	}
	return nil
}

// RemoveProgramMerchant takes a merchant off the allow-list. The row is kept
// (removed_at is stamped) so the history of the approval survives.
func (s *Store) RemoveProgramMerchant(ctx context.Context, programID, merchantID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_program_members SET removed_at=now()
		WHERE program_id=$1 AND merchant_id=$2`, programID, merchantID)
	return err
}

// ListProgramMerchants returns a program's allow-list, approved first.
func (s *Store) ListProgramMerchants(ctx context.Context, programID uuid.UUID) ([]ProgramMember, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT mm.program_id, mm.merchant_id, COALESCE(m.name,''), COALESCE(m.slug,''),
		       mm.status, mm.block_reason, mm.added_by, mm.added_at, mm.removed_at
		FROM merchant_program_members mm
		LEFT JOIN merchants m ON m.id = mm.merchant_id
		WHERE mm.program_id=$1
		ORDER BY (mm.removed_at IS NULL) DESC, (mm.status='approved') DESC, m.name`, programID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []ProgramMember
	for rows.Next() {
		var m ProgramMember
		if err := rows.Scan(&m.ProgramID, &m.MerchantID, &m.MerchantName, &m.MerchantSlug,
			&m.Status, &m.BlockReason, &m.AddedBy, &m.AddedAt, &m.RemovedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// ---------------------------------------------------------------------------
// Enrollment
// ---------------------------------------------------------------------------

// EnrollUser links an individual account to a program. Idempotent: re-enrolling
// a removed user reactivates the row rather than creating a second one.
func (s *Store) EnrollUser(ctx context.Context, programID, userID uuid.UUID, enrolledBy uuid.UUID) (ProgramEnrollment, error) {
	var by *uuid.UUID
	if enrolledBy != uuid.Nil {
		by = &enrolledBy
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO program_enrollments(program_id, user_id, status, enrolled_by, enrolled_at, updated_at)
		VALUES($1,$2,'active',$3,now(),now())
		ON CONFLICT (program_id, user_id) DO UPDATE
		SET status='active', enrolled_by=EXCLUDED.enrolled_by,
		    enrolled_at=now(), updated_at=now()`,
		programID, userID, by); err != nil {
		return ProgramEnrollment{}, err
	}
	return s.EnrollmentForUser(ctx, programID, userID)
}

// EnrollmentForUser resolves one enrollment with its beneficiary's identity.
func (s *Store) EnrollmentForUser(ctx context.Context, programID, userID uuid.UUID) (ProgramEnrollment, error) {
	var e ProgramEnrollment
	err := s.pool.QueryRow(ctx, `
		SELECT e.id, e.program_id, e.user_id, COALESCE(u.display_name,''),
		       COALESCE(u.whatsapp_number,''), e.status, e.enrolled_by, e.enrolled_at
		FROM program_enrollments e
		LEFT JOIN users u ON u.id = e.user_id
		WHERE e.program_id=$1 AND e.user_id=$2`, programID, userID).Scan(
		&e.ID, &e.ProgramID, &e.UserID, &e.UserName, &e.UserPhone,
		&e.Status, &e.EnrolledBy, &e.EnrolledAt)
	return e, err
}

// RemoveEnrollment marks an enrollment removed. Existing purse money is
// deliberately untouched: revoking membership is not a clawback.
func (s *Store) RemoveEnrollment(ctx context.Context, programID, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE program_enrollments SET status='removed', updated_at=now()
		WHERE program_id=$1 AND user_id=$2`, programID, userID)
	return err
}

// ListProgramEnrollments returns a program's beneficiaries, newest first.
func (s *Store) ListProgramEnrollments(ctx context.Context, programID uuid.UUID, limit int) ([]ProgramEnrollment, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.program_id, e.user_id, COALESCE(u.display_name,''),
		       COALESCE(u.whatsapp_number,''), e.status, e.enrolled_by, e.enrolled_at
		FROM program_enrollments e
		LEFT JOIN users u ON u.id = e.user_id
		WHERE e.program_id=$1
		ORDER BY e.enrolled_at DESC LIMIT $2`, programID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var enrollments []ProgramEnrollment
	for rows.Next() {
		var e ProgramEnrollment
		if err := rows.Scan(&e.ID, &e.ProgramID, &e.UserID, &e.UserName, &e.UserPhone,
			&e.Status, &e.EnrolledBy, &e.EnrolledAt); err != nil {
			return nil, err
		}
		enrollments = append(enrollments, e)
	}
	return enrollments, rows.Err()
}

// ---------------------------------------------------------------------------
// Purses
// ---------------------------------------------------------------------------

// EnsureProgramPurse opens (or returns) the beneficiary's purse for a program.
// Enrollment must be active, otherwise there is no standing consent to hold
// that program's money on the beneficiary's behalf.
func (s *Store) EnsureProgramPurse(ctx context.Context, programID, userID uuid.UUID) (ProgramPurse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProgramPurse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	purse, err := s.ensureProgramPurseTx(ctx, tx, programID, userID)
	if err != nil {
		return ProgramPurse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProgramPurse{}, err
	}
	return purse, nil
}

// ensureProgramPurseTx is the transactional variant: the caller already holds a
// transaction (a disbursement item), so the purse row and the ledger credit
// that funds it commit together or not at all.
func (s *Store) ensureProgramPurseTx(ctx context.Context, tx pgx.Tx, programID, userID uuid.UUID) (ProgramPurse, error) {
	var (
		programStatus string
		validityDays  int
		enrollmentID  uuid.UUID
	)
	err := tx.QueryRow(ctx, `
		SELECT mp.status, mp.default_validity_days, e.id
		FROM merchant_programs mp
		JOIN program_enrollments e ON e.program_id = mp.id AND e.user_id=$2 AND e.status='active'
		WHERE mp.id=$1`, programID, userID).Scan(&programStatus, &validityDays, &enrollmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProgramPurse{}, fmt.Errorf("%w: user %s in program %s", ErrProgramNotEnrolled, userID, programID)
	}
	if err != nil {
		return ProgramPurse{}, err
	}
	if programStatus != ProgramStatusActive {
		return ProgramPurse{}, errors.New("program is not active")
	}
	purseID := uuid.New()
	var expiresAt any
	if validityDays > 0 {
		expiresAt = time.Now().AddDate(0, 0, validityDays)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO program_purses(id, program_id, user_id, enrollment_id, account_code, status, expires_at)
		VALUES($1,$2,$3,$4,$5,'active',$6)
		ON CONFLICT (program_id, user_id) DO NOTHING`,
		purseID, programID, userID, enrollmentID, PurseAccountCode(purseID), expiresAt); err != nil {
		return ProgramPurse{}, fmt.Errorf("open program purse: %w", err)
	}
	return scanProgramPurse(tx.QueryRow(ctx, `
		SELECT `+programPurseColumns+`
		FROM program_purses pu
		JOIN merchant_programs mp ON mp.id = pu.program_id
		LEFT JOIN users u ON u.id = pu.user_id
		WHERE pu.program_id=$1 AND pu.user_id=$2`, programID, userID))
}

// ProgramPurseByID loads one purse with its ledger balance.
func (s *Store) ProgramPurseByID(ctx context.Context, id uuid.UUID) (ProgramPurse, error) {
	return scanProgramPurse(s.pool.QueryRow(ctx, `
		SELECT `+programPurseColumns+`
		FROM program_purses pu
		JOIN merchant_programs mp ON mp.id = pu.program_id
		LEFT JOIN users u ON u.id = pu.user_id
		WHERE pu.id=$1`, id))
}

// SetBulkBatchProgram marks a bulk batch as a program disbursement, so its
// eligible recipients are paid into their program purse rather than their
// ordinary wallet. Set before review/run, and only for a batch that has not
// started paying: changing it later would mix restricted and unrestricted
// disbursements in one batch.
func (s *Store) SetBulkBatchProgram(ctx context.Context, batchID, programID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE bulk_batches SET program_id=$2, updated_at=now()
		WHERE id=$1 AND status IN ($3, $4, $5)`,
		batchID, programID, BulkBatchPending, BulkBatchStage1, BulkBatchStage2)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("bulk batch not found, or it has already finished")
	}
	return nil
}

// ProgramForBulkBatch reports which program a bulk batch disburses for. ok is
// false for an ordinary bulk batch, which must keep crediting ordinary wallets.
func (s *Store) ProgramForBulkBatch(ctx context.Context, batchID uuid.UUID) (uuid.UUID, bool, error) {
	var programID *uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT program_id FROM bulk_batches WHERE id=$1`, batchID).Scan(&programID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if programID == nil || *programID == uuid.Nil {
		return uuid.Nil, false, nil
	}
	return *programID, true, nil
}

// PostBulkProgramPaymentItem debits the sponsor's business wallet and credits
// the recipient's program purse for one eligible item of a program batch, in a
// single transaction. It is idempotent on the item: a replay returns the
// original credit reference.
//
// The recipient must be enrolled (ensureProgramPurseTx refuses otherwise), which
// is the whole point: money that reaches a beneficiary on behalf of a program
// lands somewhere it can only be spent on that program's goods, never in a
// balance that could be withdrawn.
func (s *Store) PostBulkProgramPaymentItem(ctx context.Context, itemID uuid.UUID) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		batchID    uuid.UUID
		batchNo    string
		merchantID uuid.UUID
		programID  *uuid.UUID
		status     string
		creditRef  string
		amount     int64
		userID     *uuid.UUID
		phone      string
	)
	err = tx.QueryRow(ctx, `
		SELECT i.batch_id, b.batch_no, b.merchant_id, b.program_id, i.status, i.credit_ref,
		       i.amount_kobo, i.user_id, i.phone_e164
		FROM bulk_batch_items i
		JOIN bulk_batches b ON b.id = i.batch_id
		WHERE i.id=$1 FOR UPDATE OF i`, itemID).Scan(
		&batchID, &batchNo, &merchantID, &programID, &status, &creditRef, &amount, &userID, &phone)
	if err != nil {
		return "", err
	}
	if programID == nil || *programID == uuid.Nil {
		return "", errors.New("bulk batch is not a program disbursement")
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

	purse, err := s.ensureProgramPurseTx(ctx, tx, *programID, *userID)
	if err != nil {
		return "", err
	}
	sponsorWallet, err := s.ensureWalletTx(ctx, tx, WalletOwnerBusiness, merchantID, "", WalletStatusActive)
	if err != nil {
		return "", err
	}
	balance, err := walletBalanceQ(ctx, tx, sponsorWallet.AccountCode)
	if err != nil {
		return "", err
	}
	if balance < amount {
		return "", fmt.Errorf("%w: sponsor wallet has %d kobo, need %d kobo", ErrInsufficientWalletBalance, balance, amount)
	}

	ref := fmt.Sprintf("bulk:%s:%s", batchNo, itemID)
	if err := s.CreditProgramPurseTx(ctx, tx, purse, sponsorWallet.AccountCode, amount, ref,
		fmt.Sprintf("Program disbursement %s to %s", batchNo, phone)); err != nil {
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

// ProgramPurseForUser resolves one beneficiary's purse for one program.
func (s *Store) ProgramPurseForUser(ctx context.Context, programID, userID uuid.UUID) (ProgramPurse, error) {
	return scanProgramPurse(s.pool.QueryRow(ctx, `
		SELECT `+programPurseColumns+`
		FROM program_purses pu
		JOIN merchant_programs mp ON mp.id = pu.program_id
		LEFT JOIN users u ON u.id = pu.user_id
		WHERE pu.program_id=$1 AND pu.user_id=$2`, programID, userID))
}

// ProgramPursesForUser lists every purse a beneficiary holds, across programs.
// Ordering puts the soonest expiry first, because that is the money most at
// risk of being returned unspent.
func (s *Store) ProgramPursesForUser(ctx context.Context, userID uuid.UUID) ([]ProgramPurse, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+programPurseColumns+`
		FROM program_purses pu
		JOIN merchant_programs mp ON mp.id = pu.program_id
		LEFT JOIN users u ON u.id = pu.user_id
		WHERE pu.user_id=$1
		ORDER BY (pu.status='active') DESC, pu.expires_at ASC NULLS LAST`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var purses []ProgramPurse
	for rows.Next() {
		p, err := scanProgramPurse(rows)
		if err != nil {
			return nil, err
		}
		purses = append(purses, p)
	}
	return purses, rows.Err()
}

// ListProgramPurses lists a program's purses for the admin console.
func (s *Store) ListProgramPurses(ctx context.Context, programID uuid.UUID, limit int) ([]ProgramPurse, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+programPurseColumns+`
		FROM program_purses pu
		JOIN merchant_programs mp ON mp.id = pu.program_id
		LEFT JOIN users u ON u.id = pu.user_id
		WHERE pu.program_id=$1
		ORDER BY pu.opened_at DESC LIMIT $2`, programID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var purses []ProgramPurse
	for rows.Next() {
		p, err := scanProgramPurse(rows)
		if err != nil {
			return nil, err
		}
		purses = append(purses, p)
	}
	return purses, rows.Err()
}

// ApprovedPurseForPayee picks the purse that may legitimately fund a payment
// from this payer to this merchant, or ok=false when the merchant is not on any
// program the payer has money in. Called when a draft is created so the
// customer can be told early; the authoritative re-check happens in
// PurseSpendAllowedTx inside the money-movement transaction.
//
// Selection order is soonest-expiry-first: restricted money that is about to be
// returned to the sponsor is spent before money with a longer life, and a purse
// with no remaining balance is never selected.
func (s *Store) ApprovedPurseForPayee(ctx context.Context, userID, merchantID uuid.UUID) (ProgramPurse, bool, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+programPurseColumns+`
		FROM program_purses pu
		JOIN merchant_programs mp ON mp.id = pu.program_id
		JOIN merchant_program_members mm ON mm.program_id = pu.program_id AND mm.merchant_id=$2
		LEFT JOIN users u ON u.id = pu.user_id
		WHERE pu.user_id=$1
		  AND mp.status='active'
		  AND pu.status='active'
		  AND (pu.expires_at IS NULL OR pu.expires_at > now())
		  AND mm.status='approved'
		  AND mm.removed_at IS NULL
		  AND COALESCE((SELECT SUM(CASE WHEN e.entry_type='credit' THEN e.amount_kobo ELSE -e.amount_kobo END)
		                FROM ledger_entries e WHERE e.account = pu.account_code), 0) > 0
		ORDER BY pu.expires_at ASC NULLS LAST
		LIMIT 1`, userID, merchantID)
	purse, err := scanProgramPurse(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProgramPurse{}, false, nil
	}
	if err != nil {
		return ProgramPurse{}, false, err
	}
	return purse, true, nil
}

// PurseSpendAllowedTx is the authoritative spend gate: it runs inside the same
// transaction that debits the purse, so a merchant de-listed between draft and
// confirmation cannot be paid. It reports why a movement was refused so the
// caller can render a specific message instead of a generic failure.
func (s *Store) PurseSpendAllowedTx(ctx context.Context, tx pgx.Tx, purseID, merchantID uuid.UUID) error {
	var (
		purseStatus   string
		programStatus string
		expired       bool
		approved      bool
	)
	err := tx.QueryRow(ctx, `
		SELECT pu.status, mp.status,
		       (pu.expires_at IS NOT NULL AND pu.expires_at <= now()),
		       EXISTS(
		           SELECT 1 FROM merchant_program_members mm
		           WHERE mm.program_id = pu.program_id
		             AND mm.merchant_id = $2
		             AND mm.status = 'approved'
		             AND mm.removed_at IS NULL
		       )
		FROM program_purses pu
		JOIN merchant_programs mp ON mp.id = pu.program_id
		WHERE pu.id=$1`, purseID, merchantID).Scan(&purseStatus, &programStatus, &expired, &approved)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("program purse not found")
	}
	if err != nil {
		return err
	}
	if purseStatus != PurseStatusActive {
		return ErrPurseNotActive
	}
	if programStatus != ProgramStatusActive {
		return ErrPurseMerchantNotApproved
	}
	if expired {
		return ErrPurseExpired
	}
	if !approved {
		return ErrPurseMerchantNotApproved
	}
	return nil
}

// CreditProgramPurseTx moves money from a known ledger account (the sponsor's
// business wallet) into a purse, keyed by journalRef so a replayed batch cannot
// fund the same purse twice. Callers already hold a tx (the disbursement) so the
// credit commits atomically with the item that caused it.
func (s *Store) CreditProgramPurseTx(ctx context.Context, tx pgx.Tx, purse ProgramPurse, fromAccount string, amountKobo int64, journalRef, description string) error {
	if amountKobo <= 0 {
		return errors.New("purse credit requires a positive amount")
	}
	if journalRef == "" {
		return errors.New("purse credit requires a journal reference")
	}
	if purse.Status != PurseStatusActive {
		return ErrPurseNotActive
	}
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ledger_entries WHERE journal_ref=$1)`, journalRef).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.postLedgerPair(ctx, tx, journalRef, LedgerSourceProgramFunding, purse.ID.String(),
		fromAccount, purse.AccountCode, "NGN", description, "system", amountKobo, nil)
}

// purseAccountCodeTx resolves a purse's ledger account inside a caller's
// transaction. Kept separate from the spend gate so the gate stays a pure
// yes/no decision that is easy to reason about and to test.
func (s *Store) purseAccountCodeTx(ctx context.Context, tx pgx.Tx, purseID uuid.UUID) (string, error) {
	var code string
	err := tx.QueryRow(ctx, `SELECT account_code FROM program_purses WHERE id=$1`, purseID).Scan(&code)
	return code, err
}

// CreditProgramPurse is the standalone variant of CreditProgramPurseTx.
func (s *Store) CreditProgramPurse(ctx context.Context, purseID uuid.UUID, fromAccount string, amountKobo int64, journalRef, description string) error {
	purse, err := s.ProgramPurseByID(ctx, purseID)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.CreditProgramPurseTx(ctx, tx, purse, fromAccount, amountKobo, journalRef, description); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Expiry
// ---------------------------------------------------------------------------

// ExpiredPurses lists active purses past their validity window that still hold
// money, oldest expiry first, for the return sweep.
func (s *Store) ExpiredPurses(ctx context.Context, limit int) ([]ExpiredPurse, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+programPurseColumns+`, mp.sponsor_merchant_id
		FROM program_purses pu
		JOIN merchant_programs mp ON mp.id = pu.program_id
		LEFT JOIN users u ON u.id = pu.user_id
		WHERE pu.status='active'
		  AND pu.expires_at IS NOT NULL
		  AND pu.expires_at <= now()
		  AND COALESCE((SELECT SUM(CASE WHEN e.entry_type='credit' THEN e.amount_kobo ELSE -e.amount_kobo END)
		                FROM ledger_entries e WHERE e.account = pu.account_code), 0) > 0
		ORDER BY pu.expires_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var expired []ExpiredPurse
	for rows.Next() {
		var (
			p         ProgramPurse
			sponsorID uuid.UUID
		)
		if err := rows.Scan(&p.ID, &p.ProgramID, &p.ProgramCode, &p.ProgramName, &p.UserID,
			&p.UserName, &p.UserPhone, &p.AccountCode, &p.Status, &p.OpenedAt,
			&p.ExpiresAt, &p.ClosedAt, &p.ReturnedKobo, &p.BalanceKobo, &sponsorID); err != nil {
			return nil, err
		}
		expired = append(expired, ExpiredPurse{Purse: p, SponsorMerchantID: sponsorID})
	}
	return expired, rows.Err()
}

// ReturnExpiredPurse repatriates a purse's remaining balance to the sponsor and
// closes the purse. It is idempotent: a purse that is already closed, or whose
// window has not closed, is left alone and returns 0.
//
// The return is a normal balanced posting (debit the purse, credit the
// sponsor's business wallet), so the money never leaves the book and the
// journal keeps proving where it went.
func (s *Store) ReturnExpiredPurse(ctx context.Context, purseID uuid.UUID, actor string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		sponsorID uuid.UUID
		status    string
		expired   bool
		account   string
		programID uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT mp.sponsor_merchant_id, pu.status,
		       (pu.expires_at IS NOT NULL AND pu.expires_at <= now()),
		       pu.account_code, pu.program_id
		FROM program_purses pu
		JOIN merchant_programs mp ON mp.id = pu.program_id
		WHERE pu.id=$1
		FOR UPDATE OF pu`, purseID).Scan(&sponsorID, &status, &expired, &account, &programID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errors.New("program purse not found")
	}
	if err != nil {
		return 0, err
	}
	if status != PurseStatusActive || !expired {
		return 0, tx.Commit(ctx)
	}

	balance, err := walletBalanceQ(ctx, tx, account)
	if err != nil {
		return 0, err
	}
	sponsorWallet, err := s.ensureWalletTx(ctx, tx, WalletOwnerBusiness, sponsorID, "", WalletStatusActive)
	if err != nil {
		return 0, fmt.Errorf("ensure sponsor wallet: %w", err)
	}
	if balance > 0 {
		journalRef := "purse-return:" + purseID.String()
		if err := s.postLedgerPair(ctx, tx, journalRef, LedgerSourcePurseRefund, purseID.String(),
			account, sponsorWallet.AccountCode, "NGN",
			"Unspent program funds returned to sponsor", "system", balance, nil); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE program_purses
		SET status='closed', closed_at=now(), returned_kobo=returned_kobo+$2
		WHERE id=$1`, purseID, balance); err != nil {
		return 0, err
	}
	entry := AuditLog{
		ActorType:    "system",
		Action:       "program.purse.returned",
		ResourceType: sql.NullString{String: "program_purse", Valid: true},
		ResourceID:   sql.NullString{String: purseID.String(), Valid: true},
		Details: map[string]any{
			"program_id":    programID.String(),
			"sponsor_id":    sponsorID.String(),
			"returned_kobo": balance,
			"actor":         actor,
		},
	}
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return 0, fmt.Errorf("audit purse return: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return balance, nil
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

// ProgramSummary rolls a program up for the admin console: how many
// beneficiaries, how much was granted, spent, returned, and how much is still
// held in purses. Disbursed/spent/returned are read off the journal by source
// type, so they can never disagree with the balances.
func (s *Store) ProgramSummary(ctx context.Context, programID uuid.UUID) (ProgramSummary, error) {
	var sum ProgramSummary
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM program_enrollments e WHERE e.program_id=$1 AND e.status='active'),
			(SELECT count(*) FROM program_purses pu WHERE pu.program_id=$1 AND pu.status='active'),
			(SELECT count(*) FROM merchant_program_members mm
			  WHERE mm.program_id=$1 AND mm.status='approved' AND mm.removed_at IS NULL),
			COALESCE((SELECT SUM(l.amount_kobo) FROM ledger_entries l
			  JOIN program_purses pu2 ON pu2.account_code = l.account
			  WHERE pu2.program_id=$1 AND l.entry_type='credit'
			    AND l.source_type=$2), 0),
			COALESCE((SELECT SUM(l.amount_kobo) FROM ledger_entries l
			  JOIN program_purses pu3 ON pu3.account_code = l.account
			  WHERE pu3.program_id=$1 AND l.entry_type='debit'
			    AND l.source_type='payment'), 0),
			COALESCE((SELECT SUM(l.amount_kobo) FROM ledger_entries l
			  JOIN program_purses pu4 ON pu4.account_code = l.account
			  WHERE pu4.program_id=$1 AND l.entry_type='debit'
			    AND l.source_type=$3), 0),
			COALESCE((SELECT SUM(CASE WHEN l.entry_type='credit' THEN l.amount_kobo ELSE -l.amount_kobo END)
			  FROM ledger_entries l
			  JOIN program_purses pu5 ON pu5.account_code = l.account
			  WHERE pu5.program_id=$1), 0)`,
		programID, LedgerSourceProgramFunding, LedgerSourcePurseRefund).Scan(
		&sum.EnrolledCount, &sum.ActivePurseCount, &sum.ApprovedMerchants,
		&sum.DisbursedKobo, &sum.SpentKobo, &sum.ReturnedKobo, &sum.HeldKobo)
	return sum, err
}
