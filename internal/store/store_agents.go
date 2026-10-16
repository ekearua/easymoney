// POS agent network (migration 103). A local agent is a merchant classified
// `merchant_type='agent'` with an agent_profiles row describing its region and
// cash limits. It fronts physical cash to a remittance recipient and Xego owes
// it on the ledger account 2400_agent_float:<merchant-id>; a remittance holds
// the sender's money on 2350_remittance_payable:<remittance-id> until it is
// redeemed or refunded.
//
// The float balance is never stored: it is summed from the append-only journal
// (mirroring walletBalanceQ), so it can never drift from the book. Like
// wallet_accounts (058) and program_purses (100), no agent row carries a
// foreign key to users/merchants, so retention and DSR purges are never
// blocked.
//
// A limit of 0 means uncapped, matching the migration-100 convention where
// default_validity_days = 0 means "no expiry". Numeric limit values are
// configuration, not schema (POS_AGENT_NETWORK_PLAN.md §12): DefaultAgentLimits
// ships them at zero so a newly created agent cannot pay out until an operator
// sets explicit limits and activates it.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Agent tiers.
const (
	AgentTierStandard = "standard"
	AgentTierSuper    = "super"
	AgentTierMax      = "max"
)

// Agent statuses.
const (
	AgentStatusPending   = "pending"
	AgentStatusActive    = "active"
	AgentStatusSuspended = "suspended"
)

// Agent region statuses.
const (
	AgentRegionActive   = "active"
	AgentRegionInactive = "inactive"
)

// Agent console roles and statuses.
const (
	AgentRoleOwner    = "owner"
	AgentRoleOperator = "operator"

	AgentUserActive   = "active"
	AgentUserDisabled = "disabled"
)

// Remittance statuses.
const (
	RemittanceAwaitingPickup = "awaiting_pickup"
	RemittancePaid           = "paid"
	RemittanceExpired        = "expired"
	RemittanceRefunded       = "refunded"
	RemittanceCancelled      = "cancelled"
)

// Pickup claim statuses.
const (
	PickupPending   = "pending"
	PickupRedeemed  = "redeemed"
	PickupExpired   = "expired"
	PickupCancelled = "cancelled"
)

// Sentinel errors surfaced so callers can render friendly copy.
var (
	ErrAgentNotFound            = errors.New("agent not found")
	ErrAgentNotActive           = errors.New("agent is not active")
	ErrAgentFloatCapExceeded    = errors.New("agent float cap exceeded")
	ErrAgentPayoutLimitExceeded = errors.New("agent payout limit exceeded")
	ErrAgentRegionNotFound      = errors.New("agent region not found")
	ErrInsufficientAgentFloat   = errors.New("insufficient agent float")
)

// AgentRegion is a destination a sender chooses and an agent is scoped to.
type AgentRegion struct {
	ID        uuid.UUID
	Code      string
	Name      string
	State     string
	LGA       string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AgentProfile extends a merchant into a cash agent.
type AgentProfile struct {
	MerchantID              uuid.UUID
	RegionID                uuid.UUID
	Tier                    string
	GeoLat                  *float64
	GeoLng                  *float64
	MaxOutstandingFloatKobo int64
	PerPayoutLimitKobo      int64
	DailyPayoutLimitKobo    int64
	CommissionBps           int
	Status                  string
	AgreementRef            string
	ApprovedBy              *uuid.UUID
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// AgentFloatAccountCode is the ledger account holding what Xego owes an agent.
func AgentFloatAccountCode(merchantID uuid.UUID) string {
	return LedgerAccountAgentFloat + ":" + merchantID.String()
}

// AgentDepositAccountCode is the ledger account holding an agent's collateral.
func AgentDepositAccountCode(merchantID uuid.UUID) string {
	return LedgerAccountAgentDeposit + ":" + merchantID.String()
}

// RemittancePayableAccountCode is the per-remittance liability to a recipient.
func RemittancePayableAccountCode(remittanceID uuid.UUID) string {
	return LedgerAccountRemittancePayable + ":" + remittanceID.String()
}

// DefaultAgentLimits returns the limits a newly created agent starts with. All
// values are zero (uncapped) by schema convention, but the agent starts
// `pending` and cannot pay out until an operator sets limits and activates it,
// so "ship with no number" is a deny-by-default posture rather than an open
// one. The ratified per-tier numbers (POS_AGENT_NETWORK_PLAN.md §12) live in
// config.Config.AgentTierDefaults; an operator applies them — via SetAgentLimits
// — when activating an agent.
func DefaultAgentLimits(tier string) (maxOutstandingKobo, perPayoutKobo, dailyKobo int64, commissionBps int) {
	switch tier {
	case AgentTierStandard, AgentTierSuper, AgentTierMax:
	default:
		tier = AgentTierStandard
	}
	return 0, 0, 0, 0
}

// agentPayoutAllowed is the pure authorisation predicate: it says whether an
// active agent with the given profile may pay out amountKobo given its current
// outstanding float and today's paid total. It is the logic RedeemPickupTx
// re-runs inside the money-moving transaction, kept pure so it is unit-testable
// without a database. A limit of 0 means uncapped.
func agentPayoutAllowed(p AgentProfile, amountKobo, currentFloatKobo, paidTodayKobo int64) error {
	if amountKobo <= 0 {
		return errors.New("payout amount must be positive")
	}
	if p.Status != AgentStatusActive {
		return fmt.Errorf("%w: agent is %s", ErrAgentNotActive, p.Status)
	}
	if p.PerPayoutLimitKobo > 0 && amountKobo > p.PerPayoutLimitKobo {
		return fmt.Errorf("%w: %d exceeds per-payout cap %d", ErrAgentPayoutLimitExceeded, amountKobo, p.PerPayoutLimitKobo)
	}
	if p.DailyPayoutLimitKobo > 0 && paidTodayKobo+amountKobo > p.DailyPayoutLimitKobo {
		return fmt.Errorf("%w: %d would exceed daily cap %d", ErrAgentPayoutLimitExceeded, paidTodayKobo+amountKobo, p.DailyPayoutLimitKobo)
	}
	if p.MaxOutstandingFloatKobo > 0 && currentFloatKobo+amountKobo > p.MaxOutstandingFloatKobo {
		return fmt.Errorf("%w: %d would exceed outstanding cap %d", ErrAgentFloatCapExceeded, currentFloatKobo+amountKobo, p.MaxOutstandingFloatKobo)
	}
	return nil
}

// EnsureAgentRegion creates or returns a region by its unique code.
func (s *Store) EnsureAgentRegion(ctx context.Context, region AgentRegion) (AgentRegion, error) {
	if region.Code == "" || region.Name == "" {
		return AgentRegion{}, errors.New("agent region requires a code and a name")
	}
	if region.Status == "" {
		region.Status = AgentRegionActive
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO agent_regions(code, name, state, lga, status)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (code) DO UPDATE SET name=EXCLUDED.name, state=EXCLUDED.state,
			lga=EXCLUDED.lga, status=EXCLUDED.status, updated_at=now()`,
		region.Code, region.Name, region.State, region.LGA, region.Status); err != nil {
		return AgentRegion{}, err
	}
	return s.AgentRegionByCode(ctx, region.Code)
}

// AgentRegionByCode resolves a region by its unique code.
func (s *Store) AgentRegionByCode(ctx context.Context, code string) (AgentRegion, error) {
	return s.agentRegionQ(ctx, s.pool, `WHERE code=$1`, code)
}

// AgentRegionByID resolves a region by id.
func (s *Store) AgentRegionByID(ctx context.Context, id uuid.UUID) (AgentRegion, error) {
	return s.agentRegionQ(ctx, s.pool, `WHERE id=$1`, id)
}

// AgentRegions lists regions, active first, optionally limited to active ones.
func (s *Store) AgentRegions(ctx context.Context, activeOnly bool) ([]AgentRegion, error) {
	query := `SELECT id, code, name, state, lga, status, created_at, updated_at FROM agent_regions`
	if activeOnly {
		query += ` WHERE status='active'`
	}
	query += ` ORDER BY state, name`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var regions []AgentRegion
	for rows.Next() {
		var r AgentRegion
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.State, &r.LGA, &r.Status, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		regions = append(regions, r)
	}
	return regions, rows.Err()
}

// AssignAgentInRegion picks the active agent in a region that currently has the
// fewest remittances awaiting pickup, balancing fronted-cash load across agents
// so one agent's float cap is not reached first. It returns ErrAgentNotFound
// when the region has no active agent.
func (s *Store) AssignAgentInRegion(ctx context.Context, regionID uuid.UUID) (uuid.UUID, error) {
	var merchantID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT p.merchant_id
		FROM agent_profiles p
		LEFT JOIN remittances r
		    ON r.agent_merchant_id = p.merchant_id AND r.status = 'awaiting_pickup'
		WHERE p.region_id = $1 AND p.status = 'active'
		GROUP BY p.merchant_id, p.created_at
		ORDER BY count(r.id) ASC, p.created_at ASC
		LIMIT 1`, regionID).Scan(&merchantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrAgentNotFound
	}
	return merchantID, err
}

type agentRegionQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Store) agentRegionQ(ctx context.Context, q agentRegionQuerier, where string, arg any) (AgentRegion, error) {
	var r AgentRegion
	err := q.QueryRow(ctx, `
		SELECT id, code, name, state, lga, status, created_at, updated_at
		FROM agent_regions `+where, arg).Scan(
		&r.ID, &r.Code, &r.Name, &r.State, &r.LGA, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRegion{}, ErrAgentRegionNotFound
	}
	return r, err
}

// EnsureAgentProfile creates an agent profile (idempotent on the merchant id)
// and returns it. An existing profile is returned unchanged: region moves, tier
// changes and limit changes go through their own setters so an ensure can never
// silently widen an agent's powers.
func (s *Store) EnsureAgentProfile(ctx context.Context, merchantID, regionID uuid.UUID, tier string) (AgentProfile, error) {
	switch tier {
	case AgentTierStandard, AgentTierSuper, AgentTierMax:
	default:
		tier = AgentTierStandard
	}
	maxOutstanding, perPayout, daily, commission := DefaultAgentLimits(tier)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO agent_profiles(merchant_id, region_id, tier, max_outstanding_float_kobo,
			per_payout_limit_kobo, daily_payout_limit_kobo, commission_bps, status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (merchant_id) DO NOTHING`,
		merchantID, regionID, tier, maxOutstanding, perPayout, daily, commission, AgentStatusPending); err != nil {
		return AgentProfile{}, err
	}
	return s.AgentProfileByMerchant(ctx, merchantID)
}

// AgentProfileByMerchant resolves an agent profile (ErrAgentNotFound when absent).
func (s *Store) AgentProfileByMerchant(ctx context.Context, merchantID uuid.UUID) (AgentProfile, error) {
	var p AgentProfile
	var lat, lng *float64
	var approvedBy *uuid.UUID
	err := s.pool.QueryRow(ctx, `
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

// SetAgentStatus moves an agent between pending, active and suspended. It
// returns ErrAgentNotFound when there is no profile.
func (s *Store) SetAgentStatus(ctx context.Context, merchantID uuid.UUID, status string) error {
	switch status {
	case AgentStatusPending, AgentStatusActive, AgentStatusSuspended:
	default:
		return fmt.Errorf("invalid agent status %q", status)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE agent_profiles SET status=$2, updated_at=now() WHERE merchant_id=$1`, merchantID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// SetAgentLimits sets the cash limits and commission. A limit of 0 is uncapped.
func (s *Store) SetAgentLimits(ctx context.Context, merchantID uuid.UUID, maxOutstandingKobo, perPayoutKobo, dailyKobo int64, commissionBps int) error {
	if maxOutstandingKobo < 0 || perPayoutKobo < 0 || dailyKobo < 0 || commissionBps < 0 {
		return errors.New("agent limits cannot be negative")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE agent_profiles SET max_outstanding_float_kobo=$2, per_payout_limit_kobo=$3,
			daily_payout_limit_kobo=$4, commission_bps=$5, updated_at=now()
		WHERE merchant_id=$1`,
		merchantID, maxOutstandingKobo, perPayoutKobo, dailyKobo, commissionBps)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// AgentFloatBalance returns what Xego currently owes the agent, summed from the
// append-only journal (a liability: credits increase it, debits decrease it).
func (s *Store) AgentFloatBalance(ctx context.Context, merchantID uuid.UUID) (int64, error) {
	return walletBalanceQ(ctx, s.pool, AgentFloatAccountCode(merchantID))
}

// FundAgentFloat posts money into the agent's float from a known ledger account
// (a security deposit or a settlement correction): dr fromAccount / cr agent
// float. Idempotent on journalRef.
func (s *Store) FundAgentFloat(ctx context.Context, merchantID uuid.UUID, fromAccount string, amountKobo int64, journalRef, description string) error {
	if amountKobo <= 0 {
		return errors.New("agent float funding requires a positive amount")
	}
	if journalRef == "" {
		return errors.New("agent float funding requires a journal reference")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	exists, err := ledgerJournalExistsTx(ctx, tx, journalRef)
	if err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	if err := s.postLedgerPair(ctx, tx, journalRef, "agent", merchantID.String(),
		fromAccount, AgentFloatAccountCode(merchantID), "NGN", description, "system", amountKobo, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SettleAgentFloat pays the agent's outstanding float out to the operating bank:
// dr agent float / cr operating bank. It refuses to overdraw (the agent cannot
// be settled for more than it is owed) and is idempotent on journalRef.
func (s *Store) SettleAgentFloat(ctx context.Context, merchantID uuid.UUID, amountKobo int64, journalRef, description string) error {
	if amountKobo <= 0 {
		return errors.New("agent settlement requires a positive amount")
	}
	if journalRef == "" {
		return errors.New("agent settlement requires a journal reference")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	exists, err := ledgerJournalExistsTx(ctx, tx, journalRef)
	if err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	balance, err := walletBalanceQ(ctx, tx, AgentFloatAccountCode(merchantID))
	if err != nil {
		return err
	}
	if balance < amountKobo {
		return fmt.Errorf("%w: owe %d kobo, settling %d kobo", ErrInsufficientAgentFloat, balance, amountKobo)
	}
	if err := s.postLedgerPair(ctx, tx, journalRef, "agent", merchantID.String(),
		AgentFloatAccountCode(merchantID), LedgerAccountOperatingBank, "NGN", description, "system", amountKobo, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ledgerJournalExistsTx reports whether any ledger entry already carries a
// journal reference, the idempotency guard shared by agent postings.
func ledgerJournalExistsTx(ctx context.Context, tx pgx.Tx, journalRef string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ledger_entries WHERE journal_ref=$1)`, journalRef).Scan(&exists)
	return exists, err
}
