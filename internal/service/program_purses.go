// Program purses (P1/P4): the service-side helpers for restricted program
// money. Two jobs live here, and they are deliberately the only two:
//
//   - telling a beneficiary what they hold and where it may be spent, so the
//     restriction is visible before a purchase rather than discovered at the
//     point of payment; and
//   - the expiry sweep, which returns money a beneficiary never spent to the
//     sponsor that granted it.
//
// The spend decision itself is NOT here: it lives in
// store.PurseSpendAllowedTx, inside the transaction that moves the money, so
// there is exactly one place that can authorise a purse payment.
package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// ProgramPursesService exposes restricted program balances and runs the expiry
// sweep that repatriates unspent grants.
type ProgramPursesService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewProgramPursesService constructs the service.
func NewProgramPursesService(repository *store.Store, logger *slog.Logger) *ProgramPursesService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProgramPursesService{store: repository, logger: logger}
}

// PurseSummary is one purse plus the merchants it may be spent at, which is
// what a beneficiary actually needs to see ("₦12,500 of Agric funds, usable at
// these three input dealers, expires 30 June").
type PurseSummary struct {
	Purse     store.ProgramPurse
	Merchants []store.ProgramMember
}

// PurseSummariesForUser returns every purse a beneficiary holds, active first
// and soonest-expiring first (the money most at risk of being returned).
func (s *ProgramPursesService) PurseSummariesForUser(ctx context.Context, userID uuid.UUID) ([]PurseSummary, error) {
	purses, err := s.store.ProgramPursesForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	summaries := make([]PurseSummary, 0, len(purses))
	for _, purse := range purses {
		var members []store.ProgramMember
		if purse.Status == store.PurseStatusActive {
			members, err = s.SpendableMerchants(ctx, purse.ProgramID)
			if err != nil {
				return nil, err
			}
		}
		summaries = append(summaries, PurseSummary{Purse: purse, Merchants: members})
	}
	return summaries, nil
}

// SpendableMerchants returns the merchants a program's funds may currently be
// spent at: approved and not removed. A blocked or removed merchant is absent,
// which is the same rule the spend gate applies.
func (s *ProgramPursesService) SpendableMerchants(ctx context.Context, programID uuid.UUID) ([]store.ProgramMember, error) {
	members, err := s.store.ListProgramMerchants(ctx, programID)
	if err != nil {
		return nil, err
	}
	active := make([]store.ProgramMember, 0, len(members))
	for _, m := range members {
		if m.Status == store.ProgramMemberApproved && m.RemovedAt == nil {
			active = append(active, m)
		}
	}
	return active, nil
}

// sweepBatchSize is how many purses one pass fetches. The sweep loops over
// batches until it catches up or runs out of budget, so a program lapsing with
// thousands of purses repatriates within a tick instead of 200 at a time.
const sweepBatchSize = 200

// sweepBudget bounds how long one sweep runs, so a large backlog cannot hold a
// worker slot indefinitely; the next tick resumes where this one stopped.
const sweepBudget = 2 * time.Minute

// RunExpirySweep returns unspent balances on lapsed purses to their sponsors.
// It reports how many purses were closed and the total repatriated, in kobo.
//
// A single purse failing does not stop the sweep: the next tick retries it, and
// ReturnExpiredPurse is idempotent, so a partial failure can never double-return.
func (s *ProgramPursesService) RunExpirySweep(ctx context.Context) (int, int64, error) {
	var (
		closed   int
		returned int64
	)
	deadline := time.Now().Add(sweepBudget)
	for {
		if ctx.Err() != nil {
			return closed, returned, ctx.Err()
		}
		closedBefore := closed
		expired, err := s.store.ExpiredPurses(ctx, sweepBatchSize)
		if err != nil {
			return closed, returned, err
		}
		for _, item := range expired {
			if ctx.Err() != nil {
				return closed, returned, ctx.Err()
			}
			amount, err := s.store.ReturnExpiredPurse(ctx, item.Purse.ID, "expiry_sweep")
			if err != nil {
				s.logger.ErrorContext(ctx, "program purse return failed",
					"purse", item.Purse.ID, "program", item.Purse.ProgramCode, "error", err)
				continue
			}
			if amount <= 0 {
				// Either it was already closed by a concurrent sweep, or it held
				// nothing. Either way there is no money to report.
				continue
			}
			closed++
			returned += amount
			s.logger.InfoContext(ctx, "program purse returned to sponsor",
				"purse", item.Purse.ID, "program", item.Purse.ProgramCode,
				"beneficiary", item.Purse.UserID, "amount_kobo", amount)
		}
		// A short batch means we have caught up; a full one may hide more.
		if len(expired) < sweepBatchSize {
			break
		}
		// A full batch that closed nothing is not draining (every return failed
		// or was already handled): stop rather than re-fetch the same purses for
		// the rest of the budget. The next tick retries them.
		if closed == closedBefore {
			break
		}
		if time.Now().After(deadline) {
			s.logger.InfoContext(ctx, "program purse expiry sweep budget reached",
				"closed", closed, "returned_kobo", returned)
			break
		}
	}
	return closed, returned, nil
}
