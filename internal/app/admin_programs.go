package app

// P1/P5: the program console. A program is a sponsor merchant's restricted
// money scheme — a sponsor, the merchants its funds may be spent at, and
// enrolled beneficiaries who each hold a purse. Everything here is admin-only
// and audited, because changing the allow-list changes what beneficiaries can
// legitimately buy with the money they were given.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/store"
)

// adminPrograms renders the program list and, when one is selected, its
// allow-list, enrollments, purses, and roll-up.
func (a *App) adminPrograms(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	programs, err := a.store.ListPrograms(ctx, 200)
	if err != nil {
		a.logger.WarnContext(ctx, "list programs", "error", err)
		http.Error(w, "programs unavailable", http.StatusInternalServerError)
		return
	}
	merchants, err := a.store.ListMerchants(ctx)
	if err != nil {
		a.logger.WarnContext(ctx, "list merchants for programs", "error", err)
		http.Error(w, "merchants unavailable", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Programs": programs, "Merchants": merchants}
	if raw := strings.TrimSpace(r.URL.Query().Get("id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "invalid program id", http.StatusBadRequest)
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
	a.renderAdmin(w, "admin_programs.html", r, "Programs", data)
}

// adminProgramCreate records a new program. The program starts with an empty
// allow-list, so it cannot fund anything until merchants are added: a program
// is safe to create and then configure.
func (a *App) adminProgramCreate(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	sponsorID, err := uuid.Parse(strings.TrimSpace(r.FormValue("sponsor_merchant_id")))
	if err != nil {
		http.Error(w, "a sponsor merchant is required", http.StatusBadRequest)
		return
	}
	validityDays := 0
	if raw := strings.TrimSpace(r.FormValue("default_validity_days")); raw != "" {
		validityDays, err = strconv.Atoi(raw)
		if err != nil || validityDays < 0 {
			http.Error(w, "validity days must be a non-negative whole number", http.StatusBadRequest)
			return
		}
	}
	program, err := a.store.CreateProgram(r.Context(), store.NewProgram{
		Code:                strings.TrimSpace(r.FormValue("code")),
		Name:                strings.TrimSpace(r.FormValue("name")),
		SponsorMerchantID:   sponsorID,
		PurposeLabel:        strings.TrimSpace(r.FormValue("purpose_label")),
		Description:         strings.TrimSpace(r.FormValue("description")),
		DefaultValidityDays: validityDays,
	})
	if err != nil {
		a.logger.WarnContext(r.Context(), "create program", "error", err)
		http.Error(w, "could not create program", http.StatusBadRequest)
		return
	}
	a.audit(r, "admin.program_created", "merchant_program", program.Code, map[string]any{
		"sponsor_merchant_id": sponsorID.String(),
		"validity_days":       validityDays,
		"by":                  adminEmailFromContext(r.Context()),
	})
	http.Redirect(w, r, "/admin/programs?id="+program.ID.String(), http.StatusSeeOther)
}

// adminProgramAddMerchant approves a merchant to accept the program's funds.
func (a *App) adminProgramAddMerchant(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.programIDFromPath(w, r)
	if !ok {
		return
	}
	merchantID, err := uuid.Parse(strings.TrimSpace(r.FormValue("merchant_id")))
	if err != nil {
		http.Error(w, "a merchant is required", http.StatusBadRequest)
		return
	}
	if err := a.store.AddProgramMerchant(r.Context(), programID, merchantID, uuid.Nil); err != nil {
		a.logger.WarnContext(r.Context(), "program merchant approve", "error", err)
		http.Error(w, "could not approve merchant", http.StatusBadRequest)
		return
	}
	a.audit(r, "admin.program_merchant_approved", "merchant_program", programID.String(), map[string]any{
		"merchant_id": merchantID.String(),
		"by":          adminEmailFromContext(r.Context()),
	})
	a.redirectToProgram(w, r, programID)
}

// adminProgramBlockMerchant refuses one merchant without dropping the record
// that it was once approved.
func (a *App) adminProgramBlockMerchant(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.programIDFromPath(w, r)
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
	a.audit(r, "admin.program_merchant_blocked", "merchant_program", programID.String(), map[string]any{
		"merchant_id": merchantID.String(),
		"reason":      reason,
		"by":          adminEmailFromContext(r.Context()),
	})
	a.redirectToProgram(w, r, programID)
}

// adminProgramRemoveMerchant takes a merchant off the allow-list entirely.
func (a *App) adminProgramRemoveMerchant(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.programIDFromPath(w, r)
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
	a.audit(r, "admin.program_merchant_removed", "merchant_program", programID.String(), map[string]any{
		"merchant_id": merchantID.String(),
		"by":          adminEmailFromContext(r.Context()),
	})
	a.redirectToProgram(w, r, programID)
}

// adminProgramEnroll enrolls a beneficiary by phone number. Enrollment is the
// precondition for a purse, and a purse is the only place program money can
// land, so this is the step that makes a number payable by the program.
func (a *App) adminProgramEnroll(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.programIDFromPath(w, r)
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
	if _, err := a.store.EnrollUser(r.Context(), programID, user.ID, uuid.Nil); err != nil {
		a.logger.WarnContext(r.Context(), "program enroll", "error", err)
		http.Error(w, "could not enroll beneficiary", http.StatusInternalServerError)
		return
	}
	a.audit(r, "admin.program_enrolled", "merchant_program", programID.String(), map[string]any{
		"user_id": user.ID.String(),
		"phone":   phone,
		"by":      adminEmailFromContext(r.Context()),
	})
	a.redirectToProgram(w, r, programID)
}

// adminProgramSetStatus activates or deactivates a program. Deactivation stops
// new purse spends immediately without touching existing balances.
func (a *App) adminProgramSetStatus(w http.ResponseWriter, r *http.Request) {
	programID, ok := a.programIDFromPath(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.FormValue("status"))
	if err := a.store.SetProgramStatus(r.Context(), programID, status); err != nil {
		a.logger.WarnContext(r.Context(), "program status", "error", err)
		http.Error(w, "could not change program status", http.StatusBadRequest)
		return
	}
	a.audit(r, "admin.program_status_changed", "merchant_program", programID.String(), map[string]any{
		"status": status,
		"by":     adminEmailFromContext(r.Context()),
	})
	a.redirectToProgram(w, r, programID)
}

// programIDFromPath parses the program id and enforces CSRF, since every
// mutating program route is a form post.
func (a *App) programIDFromPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if r.FormValue("csrf_token") != csrfFromContext(r.Context()) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid program id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (a *App) redirectToProgram(w http.ResponseWriter, r *http.Request, programID uuid.UUID) {
	http.Redirect(w, r, "/admin/programs?id="+programID.String(), http.StatusSeeOther)
}
