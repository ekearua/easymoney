package app

// P1/P5: the sponsor's self-service program console. A program is a sponsor
// merchant's restricted money scheme — the sponsor funds it, the merchants its
// funds may be spent at, and enrolled beneficiaries who each hold a purse. The
// sponsor owns this configuration outright: creating a program, activating or
// deactivating it, curating the allow-list, and enrolling or removing
// beneficiaries.
//
// Everything here is scoped to the session merchant. ProgramByID is unscoped,
// so a sponsor could otherwise act on another sponsor's program by guessing its
// id; every path that names a program is gated on ProgramOwnedBySponsor.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/store"
)

// merchantPrograms renders the sponsor's program list and, when one is
// selected, its allow-list, enrollments, purses, and roll-up. It is reachable by
// any merchant so a first-time sponsor can create a program, which is what
// enables sponsor tools for the account.
func (a *App) merchantPrograms(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	merchantID := merchantIDFromContext(ctx)
	sponsor, err := a.store.IsSponsor(ctx, merchantID)
	if err != nil {
		a.logger.WarnContext(ctx, "sponsor check", "error", err)
		http.Error(w, "account unavailable", http.StatusInternalServerError)
		return
	}
	programs, err := a.store.ProgramsForSponsor(ctx, merchantID)
	if err != nil {
		a.logger.WarnContext(ctx, "list sponsored programs", "error", err)
		http.Error(w, "programs unavailable", http.StatusInternalServerError)
		return
	}
	merchants, err := a.store.ListMerchants(ctx)
	if err != nil {
		a.logger.WarnContext(ctx, "list merchants for programs", "error", err)
		http.Error(w, "merchants unavailable", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"IsSponsor": sponsor, "Programs": programs, "Merchants": merchants}
	if raw := strings.TrimSpace(r.URL.Query().Get("id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "invalid program id", http.StatusBadRequest)
			return
		}
		owned, err := a.store.ProgramOwnedBySponsor(ctx, id, merchantID)
		if err != nil {
			http.Error(w, "program unavailable", http.StatusInternalServerError)
			return
		}
		if !owned {
			http.Error(w, "program not found", http.StatusNotFound)
			return
		}
		program, err := a.store.ProgramByID(ctx, id)
		if err != nil {
			a.logger.WarnContext(ctx, "load program", "error", err)
			http.Error(w, "program not found", http.StatusNotFound)
			return
		}
		members, err := a.store.ListProgramMerchants(ctx, id)
		if err != nil {
			http.Error(w, "program merchants unavailable", http.StatusInternalServerError)
			return
		}
		enrollments, err := a.store.ListProgramEnrollments(ctx, id, 500)
		if err != nil {
			http.Error(w, "program enrollments unavailable", http.StatusInternalServerError)
			return
		}
		purses, err := a.store.ListProgramPurses(ctx, id, 500)
		if err != nil {
			http.Error(w, "program purses unavailable", http.StatusInternalServerError)
			return
		}
		summary, err := a.store.ProgramSummary(ctx, id)
		if err != nil {
			http.Error(w, "program summary unavailable", http.StatusInternalServerError)
			return
		}
		data["Program"] = program
		data["Members"] = members
		data["Enrollments"] = enrollments
		data["Purses"] = purses
		data["Summary"] = summary
	}
	a.renderMerchant(w, "merchant_programs.html", r, "Programs", data)
}

// merchantProgramCreate records a program sponsored by the session merchant and
// enables sponsor tools for the account. The program opens with an empty
// allow-list, so it is safe to create and configure afterwards.
func (a *App) merchantProgramCreate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	sponsorID := merchantIDFromContext(r.Context())
	validityDays := 0
	if raw := strings.TrimSpace(r.FormValue("default_validity_days")); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || days < 0 {
			http.Error(w, "validity days must be a non-negative whole number", http.StatusBadRequest)
			return
		}
		validityDays = days
	}
	program, err := a.store.CreateProgram(r.Context(), store.NewProgram{
		Code:                strings.TrimSpace(r.FormValue("code")),
		Name:                strings.TrimSpace(r.FormValue("name")),
		SponsorMerchantID:   sponsorID,
		PurposeLabel:        strings.TrimSpace(r.FormValue("purpose_label")),
		Description:         strings.TrimSpace(r.FormValue("description")),
		DefaultValidityDays: validityDays,
		CreatedBy:           sponsorID,
	})
	if err != nil {
		a.logger.WarnContext(r.Context(), "sponsor create program", "error", err)
		http.Error(w, "could not create program", http.StatusBadRequest)
		return
	}
	a.audit(r, "merchant.program_created", "merchant_program", program.Code, map[string]any{
		"sponsor_merchant_id": sponsorID.String(),
		"validity_days":       validityDays,
	})
	http.Redirect(w, r, "/merchant/programs?id="+program.ID.String(), http.StatusSeeOther)
}

// merchantProgramAddMerchant approves a merchant to accept the program's funds.
func (a *App) merchantProgramAddMerchant(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.sponsorProgramFromPath(w, r)
	if !ok {
		return
	}
	merchantID, err := uuid.Parse(strings.TrimSpace(r.FormValue("merchant_id")))
	if err != nil {
		http.Error(w, "a merchant is required", http.StatusBadRequest)
		return
	}
	if err := a.store.AddProgramMerchant(r.Context(), programID, merchantID, merchantIDFromContext(r.Context())); err != nil {
		a.logger.WarnContext(r.Context(), "program merchant approve", "error", err)
		http.Error(w, "could not approve merchant", http.StatusBadRequest)
		return
	}
	a.audit(r, "merchant.program_merchant_approved", "merchant_program", programID.String(), map[string]any{
		"merchant_id": merchantID.String(),
	})
	a.redirectToMerchantProgram(w, r, programID)
}

// merchantProgramBlockMerchant refuses one merchant without dropping the record
// that it was once approved.
func (a *App) merchantProgramBlockMerchant(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.sponsorProgramFromPath(w, r)
	if !ok {
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "merchantID"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if err := a.store.BlockProgramMerchant(r.Context(), programID, merchantID, reason); err != nil {
		a.logger.WarnContext(r.Context(), "program merchant block", "error", err)
		http.Error(w, "could not block merchant", http.StatusBadRequest)
		return
	}
	a.audit(r, "merchant.program_merchant_blocked", "merchant_program", programID.String(), map[string]any{
		"merchant_id": merchantID.String(),
		"reason":      reason,
	})
	a.redirectToMerchantProgram(w, r, programID)
}

// merchantProgramRemoveMerchant takes a merchant off the allow-list entirely.
func (a *App) merchantProgramRemoveMerchant(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.sponsorProgramFromPath(w, r)
	if !ok {
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "merchantID"))
	if err != nil {
		http.Error(w, "invalid merchant id", http.StatusBadRequest)
		return
	}
	if err := a.store.RemoveProgramMerchant(r.Context(), programID, merchantID); err != nil {
		a.logger.WarnContext(r.Context(), "program merchant remove", "error", err)
		http.Error(w, "could not remove merchant", http.StatusInternalServerError)
		return
	}
	a.audit(r, "merchant.program_merchant_removed", "merchant_program", programID.String(), map[string]any{
		"merchant_id": merchantID.String(),
	})
	a.redirectToMerchantProgram(w, r, programID)
}

// merchantProgramEnroll enrolls a beneficiary by phone number. Enrollment is the
// precondition for a purse, and a purse is the only place program money can
// land, so this is the step that makes a number payable by the program.
func (a *App) merchantProgramEnroll(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.sponsorProgramFromPath(w, r)
	if !ok {
		return
	}
	phone := domain.CanonicalE164Phone(strings.TrimSpace(r.FormValue("phone")))
	if len(strings.TrimPrefix(phone, "+")) < 10 {
		http.Error(w, "enter a valid phone number", http.StatusBadRequest)
		return
	}
	user, err := a.store.FindUserByPhone(r.Context(), phone)
	if err != nil {
		http.Error(w, "no Faya account for that phone number", http.StatusNotFound)
		return
	}
	if _, err := a.store.EnrollUser(r.Context(), programID, user.ID, merchantIDFromContext(r.Context())); err != nil {
		a.logger.WarnContext(r.Context(), "program enroll", "error", err)
		http.Error(w, "could not enroll beneficiary", http.StatusInternalServerError)
		return
	}
	a.audit(r, "merchant.program_enrolled", "merchant_program", programID.String(), map[string]any{
		"user_id": user.ID.String(),
		"phone":   phone,
	})
	a.redirectToMerchantProgram(w, r, programID)
}

// merchantProgramRemoveEnrollment removes a beneficiary. Removing an enrollment
// does not claw back money already granted: it stops future grants and leaves
// the purse and its balance intact.
func (a *App) merchantProgramRemoveEnrollment(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.sponsorProgramFromPath(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		http.Error(w, "invalid beneficiary id", http.StatusBadRequest)
		return
	}
	if err := a.store.RemoveEnrollment(r.Context(), programID, userID); err != nil {
		a.logger.WarnContext(r.Context(), "program unenroll", "error", err)
		http.Error(w, "could not remove beneficiary", http.StatusInternalServerError)
		return
	}
	a.audit(r, "merchant.program_beneficiary_removed", "merchant_program", programID.String(), map[string]any{
		"user_id": userID.String(),
	})
	a.redirectToMerchantProgram(w, r, programID)
}

// merchantProgramSetStatus activates or deactivates a program. Deactivation
// stops new purse spends immediately without touching existing balances.
func (a *App) merchantProgramSetStatus(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.sponsorProgramFromPath(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.FormValue("status"))
	if err := a.store.SetProgramStatus(r.Context(), programID, status); err != nil {
		a.logger.WarnContext(r.Context(), "program status", "error", err)
		http.Error(w, "could not change program status", http.StatusBadRequest)
		return
	}
	a.audit(r, "merchant.program_status_changed", "merchant_program", programID.String(), map[string]any{
		"status": status,
	})
	a.redirectToMerchantProgram(w, r, programID)
}

// sponsorProgramFromPath parses the program id, enforces CSRF (every mutating
// program route is a form post), and confirms the session merchant sponsors the
// program. It writes the error response itself and reports whether the caller
// should continue.
func (a *App) sponsorProgramFromPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if r.FormValue("csrf_token") != merchantCSRFFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid program id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	owned, err := a.store.ProgramOwnedBySponsor(r.Context(), id, merchantIDFromContext(r.Context()))
	if err != nil {
		http.Error(w, "program unavailable", http.StatusInternalServerError)
		return uuid.Nil, false
	}
	if !owned {
		http.Error(w, "program not found", http.StatusNotFound)
		return uuid.Nil, false
	}
	return id, true
}

func (a *App) redirectToMerchantProgram(w http.ResponseWriter, r *http.Request, programID uuid.UUID) {
	http.Redirect(w, r, "/merchant/programs?id="+programID.String(), http.StatusSeeOther)
}
