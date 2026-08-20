package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
)

// ThriftGroupView summarizes one rotational thrift group.
type ThriftGroupView struct {
	ID                     uuid.UUID
	CreatorUserID          uuid.UUID
	Name                   string
	ContributionAmountKobo int64
	Frequency              string
	TargetMemberCount      int
	InviteCode             string
	Status                 string
	CurrentCycle           int
	CreatedAt              time.Time
	UpdatedAt              time.Time
	ActivatedAt            *time.Time
	CompletedAt            *time.Time
	CreatorName            string
	MemberCount            int
}

// ThriftMemberView joins thrift membership with user display fields.
type ThriftMemberView struct {
	ID             uuid.UUID
	GroupID        uuid.UUID
	UserID         uuid.UUID
	UserName       string
	UserEmail      string
	WhatsAppNumber string
	Status         string
	PayoutPosition sql.NullInt32
	JoinedAt       time.Time
	ConfirmedAt    time.Time
}

// ThriftCycleView is one contribution/payout cycle.
type ThriftCycleView struct {
	ID                     uuid.UUID
	GroupID                uuid.UUID
	GroupName              string
	CycleNumber            int
	DueAt                  time.Time
	PayoutMemberID         uuid.UUID
	PayoutMemberName       string
	Status                 string
	ContributionAmountKobo int64
	TargetMemberCount      int
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// ThriftContributionView links one member contribution to a payment receipt.
type ThriftContributionView struct {
	ID             uuid.UUID
	CycleID        uuid.UUID
	GroupID        uuid.UUID
	GroupName      string
	CycleNumber    int
	MemberID       uuid.UUID
	UserID         uuid.UUID
	MemberName     string
	PaymentID      uuid.NullUUID
	AmountKobo     int64
	Status         string
	PaymentStatus  domain.PaymentStatus
	PaymentReceipt string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	PaidAt         *time.Time
}

// ThriftPayoutView is one simulated thrift payout obligation.
type ThriftPayoutView struct {
	ID               uuid.UUID
	CycleID          uuid.UUID
	GroupID          uuid.UUID
	GroupName        string
	CycleNumber      int
	PayoutMemberID   uuid.UUID
	PayoutMemberName string
	AmountKobo       int64
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      *time.Time
}

// ThriftCycleProgress returns summary progress for a specific cycle.
type ThriftCycleProgress struct {
	TotalMembers     int
	PaidCount        int
	DueAt            time.Time
	PayoutMemberName string
	GroupName        string
	CycleNumber      int
	ContributionAmt  int64
}

// ThriftSystemMerchant returns the inactive internal merchant used only for
// thrift contribution payment records.
func (s *Store) ThriftSystemMerchant(ctx context.Context) (Merchant, error) {
	var merchant Merchant
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at,
		       password_hash, allow_partial_payments, min_invoice_amount_kobo, upfront_percent,
		       min_installment_percent, max_installments, allow_full_pay_always
		FROM merchants WHERE slug='xego-thrift-contributions'`).Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords,
		&merchant.SortOrder, &merchant.CreatedAt,
		&merchant.PasswordHash, &merchant.AllowPartialPayments,
		&merchant.MinInvoiceAmountKobo, &merchant.UpfrontPercent,
		&merchant.MinInstallmentPercent, &merchant.MaxInstallments,
		&merchant.AllowFullPayAlways,
	)
	return merchant, err
}

// ThriftGroupNameExists reports whether an active thrift group with the given
// name already exists. Names are compared case-insensitively.
func (s *Store) ThriftGroupNameExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM thrift_groups WHERE LOWER(TRIM(name))=LOWER($1) AND status <> 'cancelled')`, name).Scan(&exists)
	return exists, err
}

// CreateThriftGroup creates an inviting thrift group and adds the creator as a
// confirmed member. The creator can later choose the full payout order.
func (s *Store) CreateThriftGroup(ctx context.Context, creatorID uuid.UUID, name string, amountKobo int64, frequency string, targetMembers int) (ThriftGroupView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftGroupView{}, err
	}
	defer tx.Rollback(ctx)
	trimmed := strings.TrimSpace(name)
	var groupID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_groups(creator_user_id,name,contribution_amount_kobo,frequency,target_member_count,invite_code,status)
		VALUES($1,$2,$3,$4,$5,'','inviting')
		RETURNING id`, creatorID, trimmed, amountKobo, frequency, targetMembers).Scan(&groupID); err != nil {
		return ThriftGroupView{}, err
	}
	var memberID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_members(group_id,user_id,status)
		VALUES($1,$2,'confirmed')
		RETURNING id`, groupID, creatorID).Scan(&memberID); err != nil {
		return ThriftGroupView{}, err
	}
	if err := insertThriftEvent(ctx, tx, groupID, uuid.Nil, memberID, "group_created", map[string]any{"name": trimmed}); err != nil {
		return ThriftGroupView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftGroupView{}, err
	}
	return s.ThriftGroupByID(ctx, groupID)
}

// ThriftGroupByName returns a group by its user-facing name (case-insensitive).
func (s *Store) ThriftGroupByName(ctx context.Context, name string) (ThriftGroupView, error) {
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id FROM thrift_groups WHERE LOWER(TRIM(name))=LOWER($1)`, strings.TrimSpace(name)).Scan(&id); err != nil {
		return ThriftGroupView{}, err
	}
	return s.ThriftGroupByID(ctx, id)
}

// ThriftGroupByInviteCode is a backward-compatible alias for ThriftGroupByName.
func (s *Store) ThriftGroupByInviteCode(ctx context.Context, code string) (ThriftGroupView, error) {
	return s.ThriftGroupByName(ctx, code)
}

// ThriftGroupByID returns a group summary.
func (s *Store) ThriftGroupByID(ctx context.Context, id uuid.UUID) (ThriftGroupView, error) {
	var group ThriftGroupView
	err := s.pool.QueryRow(ctx, `
		SELECT g.id,g.creator_user_id,g.name,g.contribution_amount_kobo,g.frequency,g.target_member_count,
		       g.invite_code,g.status,g.current_cycle,g.created_at,g.updated_at,g.activated_at,g.completed_at,
		       u.display_name,
		       (SELECT COUNT(*) FROM thrift_members tm WHERE tm.group_id=g.id AND tm.status IN ('confirmed','active'))
		FROM thrift_groups g
		JOIN users u ON u.id=g.creator_user_id
		WHERE g.id=$1`, id).Scan(
		&group.ID, &group.CreatorUserID, &group.Name, &group.ContributionAmountKobo, &group.Frequency,
		&group.TargetMemberCount, &group.InviteCode, &group.Status, &group.CurrentCycle,
		&group.CreatedAt, &group.UpdatedAt, &group.ActivatedAt, &group.CompletedAt,
		&group.CreatorName, &group.MemberCount,
	)
	return group, err
}

// JoinThriftGroup adds a confirmed member while the group is still inviting.
func (s *Store) JoinThriftGroup(ctx context.Context, name string, userID uuid.UUID) (ThriftGroupView, ThriftMemberView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	defer tx.Rollback(ctx)
	var groupID uuid.UUID
	var status string
	var target, count int
	if err := tx.QueryRow(ctx, `
		SELECT id,status,target_member_count,
		       (SELECT COUNT(*) FROM thrift_members WHERE group_id=thrift_groups.id AND status IN ('confirmed','active'))
		FROM thrift_groups
		WHERE LOWER(TRIM(name))=LOWER($1)
		FOR UPDATE`, strings.TrimSpace(name)).Scan(&groupID, &status, &target, &count); err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	if status != "inviting" {
		return ThriftGroupView{}, ThriftMemberView{}, fmt.Errorf("thrift group is %s", status)
	}
	if count >= target {
		return ThriftGroupView{}, ThriftMemberView{}, errors.New("thrift group is already full")
	}
	var memberID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_members(group_id,user_id,status)
		VALUES($1,$2,'confirmed')
		ON CONFLICT(group_id,user_id) DO UPDATE
		SET status=CASE WHEN thrift_members.status='removed' THEN 'confirmed' ELSE thrift_members.status END,
			confirmed_at=COALESCE(thrift_members.confirmed_at, now())
		RETURNING id`, groupID, userID).Scan(&memberID); err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	if err := insertThriftEvent(ctx, tx, groupID, uuid.Nil, memberID, "member_confirmed", map[string]any{}); err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	group, err := s.ThriftGroupByID(ctx, groupID)
	if err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	member, err := s.ThriftMemberByID(ctx, memberID)
	return group, member, err
}

// ThriftMemberByID returns one member with display fields.
func (s *Store) ThriftMemberByID(ctx context.Context, memberID uuid.UUID) (ThriftMemberView, error) {
	var member ThriftMemberView
	err := s.pool.QueryRow(ctx, `
		SELECT tm.id,tm.group_id,tm.user_id,u.display_name,u.email,COALESCE(u.whatsapp_number,''),
		       tm.status,tm.payout_position,tm.joined_at,tm.confirmed_at
		FROM thrift_members tm
		JOIN users u ON u.id=tm.user_id
		WHERE tm.id=$1`, memberID).Scan(&member.ID, &member.GroupID, &member.UserID, &member.UserName, &member.UserEmail, &member.WhatsAppNumber, &member.Status, &member.PayoutPosition, &member.JoinedAt, &member.ConfirmedAt)
	return member, err
}

// ThriftMembers returns active/confirmed members in a deterministic display order.
func (s *Store) ThriftMembers(ctx context.Context, groupID uuid.UUID) ([]ThriftMemberView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tm.id,tm.group_id,tm.user_id,u.display_name,u.email,COALESCE(u.whatsapp_number,''),
		       tm.status,tm.payout_position,tm.joined_at,tm.confirmed_at
		FROM thrift_members tm
		JOIN users u ON u.id=tm.user_id
		WHERE tm.group_id=$1 AND tm.status IN ('confirmed','active')
		ORDER BY tm.payout_position NULLS LAST, tm.joined_at`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []ThriftMemberView
	for rows.Next() {
		var member ThriftMemberView
		if err := rows.Scan(&member.ID, &member.GroupID, &member.UserID, &member.UserName, &member.UserEmail, &member.WhatsAppNumber, &member.Status, &member.PayoutPosition, &member.JoinedAt, &member.ConfirmedAt); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// ActivateThriftGroup applies the creator-selected payout order and opens the
// first contribution cycle.
func (s *Store) ActivateThriftGroup(ctx context.Context, groupID, creatorID uuid.UUID, orderedMemberIDs []uuid.UUID) (ThriftCycleView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftCycleView{}, err
	}
	defer tx.Rollback(ctx)
	var amount int64
	var frequency, status string
	var target, current int
	if err := tx.QueryRow(ctx, `
		SELECT contribution_amount_kobo,frequency,status,target_member_count,
		       (SELECT COUNT(*) FROM thrift_members WHERE group_id=thrift_groups.id AND status='confirmed')
		FROM thrift_groups
		WHERE id=$1 AND creator_user_id=$2
		FOR UPDATE`, groupID, creatorID).Scan(&amount, &frequency, &status, &target, &current); err != nil {
		return ThriftCycleView{}, err
	}
	if status != "inviting" {
		return ThriftCycleView{}, fmt.Errorf("thrift group is %s", status)
	}
	if current != target || len(orderedMemberIDs) != target {
		return ThriftCycleView{}, fmt.Errorf("rotation needs exactly %d confirmed members", target)
	}
	seen := map[uuid.UUID]bool{}
	for i, memberID := range orderedMemberIDs {
		if seen[memberID] {
			return ThriftCycleView{}, errors.New("rotation contains a duplicate member")
		}
		seen[memberID] = true
		tag, err := tx.Exec(ctx, `
			UPDATE thrift_members
			SET payout_position=$3,status='active'
			WHERE id=$1 AND group_id=$2 AND status='confirmed'`, memberID, groupID, i+1)
		if err != nil {
			return ThriftCycleView{}, err
		}
		if tag.RowsAffected() != 1 {
			return ThriftCycleView{}, errors.New("rotation contains an invalid member")
		}
	}
	var payoutMemberID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM thrift_members WHERE group_id=$1 AND payout_position=1`, groupID).Scan(&payoutMemberID); err != nil {
		return ThriftCycleView{}, err
	}
	dueAt := nextThriftDueDate(time.Now(), frequency)
	var cycleID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_cycles(group_id,cycle_number,due_at,payout_member_id,status)
		VALUES($1,1,$2,$3,'pending_contributions')
		RETURNING id`, groupID, dueAt, payoutMemberID).Scan(&cycleID); err != nil {
		return ThriftCycleView{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO thrift_contributions(cycle_id,member_id,amount_kobo,status)
		SELECT $1,id,$2,'awaiting_payment'
		FROM thrift_members
		WHERE group_id=$3 AND status='active'`, cycleID, amount, groupID); err != nil {
		return ThriftCycleView{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE thrift_groups
		SET status='active',current_cycle=1,activated_at=now(),updated_at=now()
		WHERE id=$1`, groupID); err != nil {
		return ThriftCycleView{}, err
	}
	if err := insertThriftEvent(ctx, tx, groupID, cycleID, uuid.Nil, "group_activated", map[string]any{"frequency": frequency}); err != nil {
		return ThriftCycleView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftCycleView{}, err
	}
	return s.ThriftCycleByID(ctx, cycleID)
}

func nextThriftDueDate(from time.Time, frequency string) time.Time {
	if frequency == "monthly" {
		return from.AddDate(0, 1, 0)
	}
	return from.AddDate(0, 0, 7)
}

// CurrentThriftContributionForUser returns the open contribution for a member.
func (s *Store) CurrentThriftContributionForUser(ctx context.Context, name string, userID uuid.UUID) (ThriftContributionView, error) {
	var contribution ThriftContributionView
	err := s.pool.QueryRow(ctx, `
		SELECT tc.id,tc.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,tm.user_id,u.display_name,
		       tc.payment_id,tc.amount_kobo,tc.status,COALESCE(p.status,''),COALESCE(p.receipt_token,''),
		       tc.created_at,tc.updated_at,tc.paid_at
		FROM thrift_groups tg
		JOIN thrift_cycles tcy ON tcy.group_id=tg.id AND tcy.cycle_number=tg.current_cycle
		JOIN thrift_members tm ON tm.group_id=tg.id AND tm.user_id=$2
		JOIN users u ON u.id=tm.user_id
		JOIN thrift_contributions tc ON tc.cycle_id=tcy.id AND tc.member_id=tm.id
		LEFT JOIN payments p ON p.id=tc.payment_id
		WHERE LOWER(TRIM(tg.name))=LOWER($1) AND tg.status='active'`, strings.TrimSpace(name), userID).Scan(
		&contribution.ID, &contribution.CycleID, &contribution.GroupID, &contribution.GroupName, &contribution.CycleNumber,
		&contribution.MemberID, &contribution.UserID, &contribution.MemberName, &contribution.PaymentID,
		&contribution.AmountKobo, &contribution.Status, &contribution.PaymentStatus, &contribution.PaymentReceipt,
		&contribution.CreatedAt, &contribution.UpdatedAt, &contribution.PaidAt,
	)
	return contribution, err
}

// LinkThriftContributionPayment attaches one payment attempt to a contribution.
func (s *Store) LinkThriftContributionPayment(ctx context.Context, contributionID, paymentID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE thrift_contributions
		SET payment_id=$2,updated_at=now()
		WHERE id=$1 AND status='awaiting_payment' AND payment_id IS NULL`, contributionID, paymentID)
	return err
}

// ApplyThriftContributionPaymentSuccess credits a contribution and advances
// cycle readiness only once all active members have paid.
func (s *Store) ApplyThriftContributionPaymentSuccess(ctx context.Context, paymentID uuid.UUID) (ThriftContributionView, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftContributionView{}, false, err
	}
	defer tx.Rollback(ctx)
	var contributionID, cycleID, groupID uuid.UUID
	var contributionKobo int64
	err = tx.QueryRow(ctx, `
		SELECT tc.id,tc.cycle_id,tcy.group_id,tc.amount_kobo
		FROM thrift_contributions tc
		JOIN thrift_cycles tcy ON tcy.id=tc.cycle_id
		WHERE tc.payment_id=$1
		FOR UPDATE OF tc`, paymentID).Scan(&contributionID, &cycleID, &groupID, &contributionKobo)
	if errors.Is(err, pgx.ErrNoRows) {
		return ThriftContributionView{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return ThriftContributionView{}, false, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE thrift_contributions
		SET status='paid',paid_at=COALESCE(paid_at, now()),updated_at=now()
		WHERE id=$1 AND status <> 'paid'`, contributionID)
	if err != nil {
		return ThriftContributionView{}, false, err
	}
	changed := tag.RowsAffected() == 1
	if changed {
		// C16: contribution moves from the customer float into the thrift pool.
		if err := s.postLedgerPair(ctx, tx, paymentID.String(), "thrift_contribution", contributionID.String(),
			LedgerAccountCustomerFloat, LedgerAccountThriftPool, "NGN", "Thrift contribution", "system", contributionKobo, nil); err != nil {
			return ThriftContributionView{}, false, err
		}
	}
	var unpaid int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM thrift_contributions
		WHERE cycle_id=$1 AND status <> 'paid'`, cycleID).Scan(&unpaid); err != nil {
		return ThriftContributionView{}, false, err
	}
	if unpaid == 0 {
		var cycleStatus string
		var payoutMemberID uuid.UUID
		var total int64
		if err := tx.QueryRow(ctx, `
			SELECT status,payout_member_id,
			       (SELECT COALESCE(SUM(amount_kobo),0) FROM thrift_contributions WHERE cycle_id=thrift_cycles.id)
			FROM thrift_cycles
			WHERE id=$1
			FOR UPDATE`, cycleID).Scan(&cycleStatus, &payoutMemberID, &total); err != nil {
			return ThriftContributionView{}, false, err
		}
		if cycleStatus == "pending_contributions" {
			if _, err := tx.Exec(ctx, `UPDATE thrift_cycles SET status='ready_for_payout',updated_at=now() WHERE id=$1`, cycleID); err != nil {
				return ThriftContributionView{}, false, err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO thrift_payouts(cycle_id,payout_member_id,amount_kobo,status)
				VALUES($1,$2,$3,'pending')
				ON CONFLICT(cycle_id) DO NOTHING`, cycleID, payoutMemberID, total); err != nil {
				return ThriftContributionView{}, false, err
			}
			if err := insertThriftEvent(ctx, tx, groupID, cycleID, payoutMemberID, "cycle_ready_for_payout", map[string]any{"amount_kobo": total}); err != nil {
				return ThriftContributionView{}, false, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftContributionView{}, false, err
	}
	view, err := s.ThriftContributionByPaymentID(ctx, paymentID)
	return view, changed, err
}

// ThriftContributionByPaymentID returns thrift context for a receipt.
func (s *Store) ThriftContributionByPaymentID(ctx context.Context, paymentID uuid.UUID) (ThriftContributionView, error) {
	return s.thriftContributionBy(ctx, "tc.payment_id=$1", paymentID)
}

// ThriftContributionByID returns one contribution by primary key.
func (s *Store) ThriftContributionByID(ctx context.Context, id uuid.UUID) (ThriftContributionView, error) {
	return s.thriftContributionBy(ctx, "tc.id=$1", id)
}

func (s *Store) thriftContributionBy(ctx context.Context, predicate string, value any) (ThriftContributionView, error) {
	var contribution ThriftContributionView
	err := s.pool.QueryRow(ctx, `
		SELECT tc.id,tc.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,tm.user_id,u.display_name,
		       tc.payment_id,tc.amount_kobo,tc.status,COALESCE(p.status,''),COALESCE(p.receipt_token,''),
		       tc.created_at,tc.updated_at,tc.paid_at
		FROM thrift_contributions tc
		JOIN thrift_cycles tcy ON tcy.id=tc.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tc.member_id
		JOIN users u ON u.id=tm.user_id
		LEFT JOIN payments p ON p.id=tc.payment_id
		WHERE `+predicate, value).Scan(
		&contribution.ID, &contribution.CycleID, &contribution.GroupID, &contribution.GroupName, &contribution.CycleNumber,
		&contribution.MemberID, &contribution.UserID, &contribution.MemberName, &contribution.PaymentID,
		&contribution.AmountKobo, &contribution.Status, &contribution.PaymentStatus, &contribution.PaymentReceipt,
		&contribution.CreatedAt, &contribution.UpdatedAt, &contribution.PaidAt,
	)
	return contribution, err
}

// MarkThriftPayoutCompleted simulates payout completion and opens the next
// rotation cycle until each member has received one payout.
func (s *Store) MarkThriftPayoutCompleted(ctx context.Context, payoutID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cycleID, groupID uuid.UUID
	var cycleNumber, target int
	var frequency string
	var status string
	var payoutKobo int64
	if err := tx.QueryRow(ctx, `
		SELECT tp.cycle_id,tcy.group_id,tcy.cycle_number,tg.target_member_count,tg.frequency,tp.status,tp.amount_kobo
		FROM thrift_payouts tp
		JOIN thrift_cycles tcy ON tcy.id=tp.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		WHERE tp.id=$1
		FOR UPDATE OF tp`, payoutID).Scan(&cycleID, &groupID, &cycleNumber, &target, &frequency, &status, &payoutKobo); err != nil {
		return err
	}
	if status == "completed_simulated" {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE thrift_payouts SET status='completed_simulated',completed_at=now(),updated_at=now()
		WHERE id=$1`, payoutID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE thrift_cycles SET status='payout_completed',updated_at=now() WHERE id=$1`, cycleID); err != nil {
		return err
	}
	// C16: the pool disburses to the winning member via the operating bank.
	if err := s.postLedgerPair(ctx, tx, payoutID.String(), "thrift_payout", payoutID.String(),
		LedgerAccountThriftPool, LedgerAccountOperatingBank, "NGN", "Thrift payout disbursement", "system", payoutKobo, nil); err != nil {
		return err
	}
	if err := insertThriftEvent(ctx, tx, groupID, cycleID, uuid.Nil, "payout_completed_simulated", map[string]any{}); err != nil {
		return err
	}
	if cycleNumber >= target {
		if _, err := tx.Exec(ctx, `UPDATE thrift_groups SET status='completed',completed_at=now(),updated_at=now() WHERE id=$1`, groupID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	nextCycle := cycleNumber + 1
	var payoutMemberID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM thrift_members WHERE group_id=$1 AND payout_position=$2`, groupID, nextCycle).Scan(&payoutMemberID); err != nil {
		return err
	}
	var amount int64
	if err := tx.QueryRow(ctx, `SELECT contribution_amount_kobo FROM thrift_groups WHERE id=$1`, groupID).Scan(&amount); err != nil {
		return err
	}
	dueAt := nextThriftDueDate(time.Now(), frequency)
	var newCycleID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_cycles(group_id,cycle_number,due_at,payout_member_id,status)
		VALUES($1,$2,$3,$4,'pending_contributions')
		RETURNING id`, groupID, nextCycle, dueAt, payoutMemberID).Scan(&newCycleID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO thrift_contributions(cycle_id,member_id,amount_kobo,status)
		SELECT $1,id,$2,'awaiting_payment'
		FROM thrift_members
		WHERE group_id=$3 AND status='active'`, newCycleID, amount, groupID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE thrift_groups SET current_cycle=$2,updated_at=now() WHERE id=$1`, groupID, nextCycle); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateThriftGroup updates mutable fields of a thrift group that is still in
// inviting status. Only the creator can update. Returns the updated group.
func (s *Store) UpdateThriftGroup(ctx context.Context, groupID, creatorID uuid.UUID, name *string, amountKobo *int64, frequency *string, targetMembers *int) (ThriftGroupView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftGroupView{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM thrift_groups
		WHERE id=$1 AND creator_user_id=$2
		FOR UPDATE`, groupID, creatorID).Scan(&status); err != nil {
		return ThriftGroupView{}, err
	}
	if status != "inviting" {
		return ThriftGroupView{}, fmt.Errorf("thrift group is %s and can no longer be edited", status)
	}
	setClauses := []string{}
	args := []any{}
	argIdx := 3
	if name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name=$%d", argIdx))
		args = append(args, strings.TrimSpace(*name))
		argIdx++
	}
	if amountKobo != nil {
		setClauses = append(setClauses, fmt.Sprintf("contribution_amount_kobo=$%d", argIdx))
		args = append(args, *amountKobo)
		argIdx++
	}
	if frequency != nil {
		setClauses = append(setClauses, fmt.Sprintf("frequency=$%d", argIdx))
		args = append(args, *frequency)
		argIdx++
	}
	if targetMembers != nil {
		setClauses = append(setClauses, fmt.Sprintf("target_member_count=$%d", argIdx))
		args = append(args, *targetMembers)
		argIdx++
	}
	if len(setClauses) == 0 {
		return s.ThriftGroupByID(ctx, groupID)
	}
	setClauses = append(setClauses, "updated_at=now()")
	query := fmt.Sprintf("UPDATE thrift_groups SET %s WHERE id=$1 AND creator_user_id=$2", strings.Join(setClauses, ","))
	args = append([]any{groupID, creatorID}, args...)
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		return ThriftGroupView{}, err
	}
	if err := insertThriftEvent(ctx, tx, groupID, uuid.Nil, uuid.Nil, "group_updated", map[string]any{}); err != nil {
		return ThriftGroupView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftGroupView{}, err
	}
	return s.ThriftGroupByID(ctx, groupID)
}

// ThriftCycleProgressForGroup returns the current cycle's progress for a group.
func (s *Store) ThriftCycleProgressForGroup(ctx context.Context, groupID uuid.UUID) (ThriftCycleProgress, error) {
	var p ThriftCycleProgress
	err := s.pool.QueryRow(ctx, `
		SELECT tg.name,tcy.cycle_number,tcy.due_at,u.display_name,tg.contribution_amount_kobo,
		       tg.target_member_count,
		       (SELECT COUNT(*) FROM thrift_contributions tc2 WHERE tc2.cycle_id=tcy.id AND tc2.status='paid')
		FROM thrift_groups tg
		JOIN thrift_cycles tcy ON tcy.group_id=tg.id AND tcy.cycle_number=tg.current_cycle
		JOIN thrift_members tm ON tm.id=tcy.payout_member_id
		JOIN users u ON u.id=tm.user_id
		WHERE tg.id=$1`, groupID).Scan(
		&p.GroupName, &p.CycleNumber, &p.DueAt, &p.PayoutMemberName, &p.ContributionAmt,
		&p.TotalMembers, &p.PaidCount,
	)
	return p, err
}

// ThriftCyclesForGroup returns all cycles for a group in order.
func (s *Store) ThriftCyclesForGroup(ctx context.Context, groupID uuid.UUID) ([]ThriftCycleView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tcy.id,tcy.group_id,tg.name,tcy.cycle_number,tcy.due_at,tm.id,u.display_name,
		       tcy.status,tg.contribution_amount_kobo,tg.target_member_count,tcy.created_at,tcy.updated_at
		FROM thrift_cycles tcy
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tcy.payout_member_id
		JOIN users u ON u.id=tm.user_id
		WHERE tcy.group_id=$1
		ORDER BY tcy.cycle_number`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cycles []ThriftCycleView
	for rows.Next() {
		var c ThriftCycleView
		if err := rows.Scan(&c.ID, &c.GroupID, &c.GroupName, &c.CycleNumber, &c.DueAt, &c.PayoutMemberID, &c.PayoutMemberName,
			&c.Status, &c.ContributionAmountKobo, &c.TargetMemberCount, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		cycles = append(cycles, c)
	}
	return cycles, rows.Err()
}

// ThriftContributionsForCycle returns all contributions for a cycle with member names.
func (s *Store) ThriftContributionsForCycle(ctx context.Context, cycleID uuid.UUID) ([]ThriftContributionView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tc.id,tc.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,tm.user_id,u.display_name,
		       tc.payment_id,tc.amount_kobo,tc.status,COALESCE(p.status,''),COALESCE(p.receipt_token,''),
		       tc.created_at,tc.updated_at,tc.paid_at
		FROM thrift_contributions tc
		JOIN thrift_cycles tcy ON tcy.id=tc.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tc.member_id
		JOIN users u ON u.id=tm.user_id
		LEFT JOIN payments p ON p.id=tc.payment_id
		WHERE tc.cycle_id=$1
		ORDER BY tm.payout_position NULLS LAST, tm.joined_at`, cycleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contributions []ThriftContributionView
	for rows.Next() {
		var c ThriftContributionView
		if err := rows.Scan(&c.ID, &c.CycleID, &c.GroupID, &c.GroupName, &c.CycleNumber,
			&c.MemberID, &c.UserID, &c.MemberName, &c.PaymentID,
			&c.AmountKobo, &c.Status, &c.PaymentStatus, &c.PaymentReceipt,
			&c.CreatedAt, &c.UpdatedAt, &c.PaidAt); err != nil {
			return nil, err
		}
		contributions = append(contributions, c)
	}
	return contributions, rows.Err()
}

// ListThriftGroups returns recent groups for the admin dashboard.
func (s *Store) ListThriftGroups(ctx context.Context, limit int) ([]ThriftGroupView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id,g.creator_user_id,g.name,g.contribution_amount_kobo,g.frequency,g.target_member_count,
		       g.invite_code,g.status,g.current_cycle,g.created_at,g.updated_at,g.activated_at,g.completed_at,
		       u.display_name,
		       (SELECT COUNT(*) FROM thrift_members tm WHERE tm.group_id=g.id AND tm.status IN ('confirmed','active'))
		FROM thrift_groups g
		JOIN users u ON u.id=g.creator_user_id
		ORDER BY g.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []ThriftGroupView
	for rows.Next() {
		var group ThriftGroupView
		if err := rows.Scan(&group.ID, &group.CreatorUserID, &group.Name, &group.ContributionAmountKobo, &group.Frequency,
			&group.TargetMemberCount, &group.InviteCode, &group.Status, &group.CurrentCycle, &group.CreatedAt,
			&group.UpdatedAt, &group.ActivatedAt, &group.CompletedAt, &group.CreatorName, &group.MemberCount); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// ListThriftPayouts returns recent simulated payouts for admin operations.
func (s *Store) ListThriftPayouts(ctx context.Context, limit int) ([]ThriftPayoutView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tp.id,tp.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,u.display_name,
		       tp.amount_kobo,tp.status,tp.created_at,tp.updated_at,tp.completed_at
		FROM thrift_payouts tp
		JOIN thrift_cycles tcy ON tcy.id=tp.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tp.payout_member_id
		JOIN users u ON u.id=tm.user_id
		ORDER BY tp.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payouts []ThriftPayoutView
	for rows.Next() {
		var payout ThriftPayoutView
		if err := rows.Scan(&payout.ID, &payout.CycleID, &payout.GroupID, &payout.GroupName, &payout.CycleNumber,
			&payout.PayoutMemberID, &payout.PayoutMemberName, &payout.AmountKobo, &payout.Status,
			&payout.CreatedAt, &payout.UpdatedAt, &payout.CompletedAt); err != nil {
			return nil, err
		}
		payouts = append(payouts, payout)
	}
	return payouts, rows.Err()
}

// ListThriftContributions returns recent contribution attempts for admin visibility.
func (s *Store) ListThriftContributions(ctx context.Context, limit int) ([]ThriftContributionView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tc.id,tc.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,tm.user_id,u.display_name,
		       tc.payment_id,tc.amount_kobo,tc.status,COALESCE(p.status,''),COALESCE(p.receipt_token,''),
		       tc.created_at,tc.updated_at,tc.paid_at
		FROM thrift_contributions tc
		JOIN thrift_cycles tcy ON tcy.id=tc.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tc.member_id
		JOIN users u ON u.id=tm.user_id
		LEFT JOIN payments p ON p.id=tc.payment_id
		ORDER BY tc.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contributions []ThriftContributionView
	for rows.Next() {
		var contribution ThriftContributionView
		if err := rows.Scan(&contribution.ID, &contribution.CycleID, &contribution.GroupID, &contribution.GroupName, &contribution.CycleNumber,
			&contribution.MemberID, &contribution.UserID, &contribution.MemberName, &contribution.PaymentID,
			&contribution.AmountKobo, &contribution.Status, &contribution.PaymentStatus, &contribution.PaymentReceipt,
			&contribution.CreatedAt, &contribution.UpdatedAt, &contribution.PaidAt); err != nil {
			return nil, err
		}
		contributions = append(contributions, contribution)
	}
	return contributions, rows.Err()
}

// RecentThriftGroupsForUser returns created or joined groups for chat dashboard.
func (s *Store) RecentThriftGroupsForUser(ctx context.Context, userID uuid.UUID, limit int) ([]ThriftGroupView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT g.id,g.creator_user_id,g.name,g.contribution_amount_kobo,g.frequency,g.target_member_count,
		       g.invite_code,g.status,g.current_cycle,g.created_at,g.updated_at,g.activated_at,g.completed_at,
		       u.display_name,
		       (SELECT COUNT(*) FROM thrift_members tm2 WHERE tm2.group_id=g.id AND tm2.status IN ('confirmed','active'))
		FROM thrift_groups g
		JOIN thrift_members tm ON tm.group_id=g.id
		JOIN users u ON u.id=g.creator_user_id
		WHERE g.creator_user_id=$1 OR tm.user_id=$1
		ORDER BY g.created_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []ThriftGroupView
	for rows.Next() {
		var group ThriftGroupView
		if err := rows.Scan(&group.ID, &group.CreatorUserID, &group.Name, &group.ContributionAmountKobo, &group.Frequency,
			&group.TargetMemberCount, &group.InviteCode, &group.Status, &group.CurrentCycle, &group.CreatedAt,
			&group.UpdatedAt, &group.ActivatedAt, &group.CompletedAt, &group.CreatorName, &group.MemberCount); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// ThriftCycleByID returns a cycle with group and payout display fields.
func (s *Store) ThriftCycleByID(ctx context.Context, cycleID uuid.UUID) (ThriftCycleView, error) {
	var cycle ThriftCycleView
	err := s.pool.QueryRow(ctx, `
		SELECT tcy.id,tcy.group_id,tg.name,tcy.cycle_number,tcy.due_at,tcy.payout_member_id,u.display_name,
		       tcy.status,tg.contribution_amount_kobo,tg.target_member_count,tcy.created_at,tcy.updated_at
		FROM thrift_cycles tcy
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tcy.payout_member_id
		JOIN users u ON u.id=tm.user_id
		WHERE tcy.id=$1`, cycleID).Scan(&cycle.ID, &cycle.GroupID, &cycle.GroupName, &cycle.CycleNumber,
		&cycle.DueAt, &cycle.PayoutMemberID, &cycle.PayoutMemberName, &cycle.Status,
		&cycle.ContributionAmountKobo, &cycle.TargetMemberCount, &cycle.CreatedAt, &cycle.UpdatedAt)
	return cycle, err
}

func insertThriftEvent(ctx context.Context, tx pgx.Tx, groupID, cycleID, memberID uuid.UUID, eventType string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var group any = nil
	if groupID != uuid.Nil {
		group = groupID
	}
	var cycle any = nil
	if cycleID != uuid.Nil {
		cycle = cycleID
	}
	var member any = nil
	if memberID != uuid.Nil {
		member = memberID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO thrift_events(group_id,cycle_id,member_id,event_type,detail)
		VALUES($1,$2,$3,$4,$5)`, group, cycle, member, eventType, raw)
	return err
}
