// C20: NDPR data-subject rights. Consent is recorded per purpose with
// timestamps; access requests are answered with a full export of the user's
// data; erasure requests place a 30-day cooling-off legal hold (so MLPA record
// keeping and dispute handling survive), then archive a snapshot to the
// append-only ledger before the user row is cascade-deleted.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ConsentPurpose values covered by consent_records.
const (
	ConsentProcessing  = "processing"
	ConsentMarketing   = "marketing"
	ConsentDataSharing = "data_sharing"
)

// ConsentRecord is one grant or withdrawal of a processing purpose (NDPR 3.1).
type ConsentRecord struct {
	ID        int64
	UserID    uuid.UUID
	Purpose   string
	Status    string
	Source    string
	Version   string
	GrantedAt time.Time
	RevokedAt *time.Time
}

// DataSubjectRequest is an access or erasure request (NDPR right to data
// portability / erasure).
type DataSubjectRequest struct {
	ID            int64
	UserID        uuid.UUID
	RequestType   string
	Status        string
	Reason        string
	RequestedBy   string
	RequestedAt   time.Time
	ReviewerEmail string
	DecisionNote  string
	ReviewedAt    *time.Time
	CompletedAt   *time.Time
}

// ErasureCoolingOff is how long an erasure request stays on a legal hold so the
// data subject can be responded to and disputes / MLPA obligations resolved.
const ErasureCoolingOff = 30 * 24 * time.Hour

// RecordConsent records (or re-grants) a purpose for a user, preserving the
// original grant timestamp. Re-granting reopens a previously withdrawn purpose.
func (s *Store) RecordConsent(ctx context.Context, userID uuid.UUID, purpose, source string) (ConsentRecord, error) {
	valid := map[string]bool{ConsentProcessing: true, ConsentMarketing: true, ConsentDataSharing: true}
	if !valid[purpose] {
		return ConsentRecord{}, fmt.Errorf("invalid consent purpose %q", purpose)
	}
	if source == "" {
		source = "chat"
	}
	var c ConsentRecord
	err := s.pool.QueryRow(ctx, `
		INSERT INTO consent_records(user_id, purpose, source)
		VALUES($1,$2,$3)
		ON CONFLICT (user_id, purpose)
		DO UPDATE SET status='granted', source=EXCLUDED.source, revoked_at=NULL
		RETURNING id, user_id, purpose, status, source, version, granted_at, revoked_at`,
		userID, purpose, source).Scan(&c.ID, &c.UserID, &c.Purpose, &c.Status, &c.Source, &c.Version, &c.GrantedAt, &c.RevokedAt)
	if err != nil {
		return ConsentRecord{}, fmt.Errorf("record consent: %w", err)
	}
	_, err = s.AppendAuditLog(ctx, AuditLog{
		ActorType: "system", Action: "dsr.consent_recorded",
		ResourceType: sqlStr("user"), ResourceID: sqlStr(userID.String()),
		Details: map[string]any{"purpose": purpose, "source": source},
	})
	if err != nil {
		return ConsentRecord{}, err
	}
	return c, nil
}

// WithdrawConsent revokes a purpose (NDPR right to withdraw consent).
func (s *Store) WithdrawConsent(ctx context.Context, userID uuid.UUID, purpose string) (ConsentRecord, error) {
	var c ConsentRecord
	err := s.pool.QueryRow(ctx, `
		INSERT INTO consent_records(user_id, purpose, source)
		VALUES($1,$2,'chat')
		ON CONFLICT (user_id, purpose)
		DO UPDATE SET status='revoked', revoked_at=now()
		RETURNING id, user_id, purpose, status, source, version, granted_at, revoked_at`,
		userID, purpose).Scan(&c.ID, &c.UserID, &c.Purpose, &c.Status, &c.Source, &c.Version, &c.GrantedAt, &c.RevokedAt)
	if err != nil {
		return ConsentRecord{}, fmt.Errorf("withdraw consent: %w", err)
	}
	_, err = s.AppendAuditLog(ctx, AuditLog{
		ActorType: "system", Action: "dsr.consent_withdrawn",
		ResourceType: sqlStr("user"), ResourceID: sqlStr(userID.String()),
		Details: map[string]any{"purpose": purpose},
	})
	if err != nil {
		return ConsentRecord{}, err
	}
	return c, nil
}

// ListConsents returns every consent record for a user, newest first.
func (s *Store) ListConsents(ctx context.Context, userID uuid.UUID) ([]ConsentRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, purpose, status, source, version, granted_at, revoked_at
		FROM consent_records WHERE user_id=$1 ORDER BY granted_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConsentRecord
	for rows.Next() {
		var c ConsentRecord
		if err := rows.Scan(&c.ID, &c.UserID, &c.Purpose, &c.Status, &c.Source, &c.Version, &c.GrantedAt, &c.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateDataSubjectRequest opens an access or erasure request. Erasure
// requests immediately place a 30-day cooling-off legal hold on the user so
// the purge worker and the erasure step cannot run during the response window.
func (s *Store) CreateDataSubjectRequest(ctx context.Context, userID uuid.UUID, requestType, reason, requestedBy string) (DataSubjectRequest, error) {
	if requestType != "access" && requestType != "erasure" {
		return DataSubjectRequest{}, fmt.Errorf("invalid request type %q", requestType)
	}
	var req DataSubjectRequest
	err := s.pool.QueryRow(ctx, `
		INSERT INTO data_subject_requests(user_id, request_type, reason, requested_by)
		VALUES($1,$2,$3,$4)
		RETURNING id, user_id, request_type, status, reason, requested_by, requested_at,
		          COALESCE(reviewer_email,''), COALESCE(decision_note,''), reviewed_at, completed_at`,
		userID, requestType, reason, requestedBy).Scan(&req.ID, &req.UserID, &req.RequestType,
		&req.Status, &req.Reason, &req.RequestedBy, &req.RequestedAt,
		&req.ReviewerEmail, &req.DecisionNote, &req.ReviewedAt, &req.CompletedAt)
	if err != nil {
		return DataSubjectRequest{}, fmt.Errorf("create data subject request: %w", err)
	}
	if requestType == "erasure" {
		expires := time.Now().UTC().Add(ErasureCoolingOff)
		if err := s.AddLegalHold(ctx, "user", userID.String(), "DSR erasure #"+fmt.Sprint(req.ID), requestedBy, &expires); err != nil {
			return DataSubjectRequest{}, fmt.Errorf("erasure legal hold: %w", err)
		}
	}
	_, err = s.AppendAuditLog(ctx, AuditLog{
		ActorType: "system", Action: "dsr.request_created",
		ResourceType: sqlStr("user"), ResourceID: sqlStr(userID.String()),
		Details: map[string]any{"request_id": req.ID, "request_type": requestType, "reason": reason},
	})
	if err != nil {
		return DataSubjectRequest{}, err
	}
	return req, nil
}

// ListDataSubjectRequests lists requests newest first, optionally filtered to
// a status.
func (s *Store) ListDataSubjectRequests(ctx context.Context, status string, limit int) ([]DataSubjectRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT id, user_id, request_type, status, reason, requested_by, requested_at,
		       COALESCE(reviewer_email,''), COALESCE(decision_note,''), reviewed_at, completed_at
		FROM data_subject_requests`
	args := []any{}
	if status != "" {
		query += ` WHERE status=$1`
		args = append(args, status)
	}
	query += ` ORDER BY requested_at DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DataSubjectRequest
	for rows.Next() {
		var req DataSubjectRequest
		if err := rows.Scan(&req.ID, &req.UserID, &req.RequestType, &req.Status, &req.Reason,
			&req.RequestedBy, &req.RequestedAt, &req.ReviewerEmail, &req.DecisionNote,
			&req.ReviewedAt, &req.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// GetDataSubjectRequest loads one request by id.
func (s *Store) GetDataSubjectRequest(ctx context.Context, id int64) (DataSubjectRequest, error) {
	var req DataSubjectRequest
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, request_type, status, reason, requested_by, requested_at,
		       COALESCE(reviewer_email,''), COALESCE(decision_note,''), reviewed_at, completed_at
		FROM data_subject_requests WHERE id=$1`, id).Scan(&req.ID, &req.UserID, &req.RequestType,
		&req.Status, &req.Reason, &req.RequestedBy, &req.RequestedAt, &req.ReviewerEmail,
		&req.DecisionNote, &req.ReviewedAt, &req.CompletedAt)
	if err != nil {
		return DataSubjectRequest{}, err
	}
	return req, nil
}

// DataSubjectExport is a JSON-serializable snapshot of everything the system
// holds about a user, built from the audited tables (NDPR right of access).
type DataSubjectExport struct {
	ExportedAt        time.Time       `json:"exported_at"`
	User              json.RawMessage `json:"user"`
	KYCProfile        json.RawMessage `json:"kyc_profile"`
	Verifications     json.RawMessage `json:"verifications"`
	Screening         json.RawMessage `json:"screening_results"`
	RiskEvents        json.RawMessage `json:"risk_events"`
	Consents          json.RawMessage `json:"consents"`
	Payments          json.RawMessage `json:"payments"`
	DataOrders        json.RawMessage `json:"data_orders"`
	Invoices          json.RawMessage `json:"invoices"`
	ThriftGroups      json.RawMessage `json:"thrift_groups"`
	ThriftMemberships json.RawMessage `json:"thrift_memberships"`
	ReviewCases       json.RawMessage `json:"review_cases"`
	TransactionAlerts json.RawMessage `json:"transaction_alerts"`
	MerchantOwnership json.RawMessage `json:"merchant_ownership"`
}

// jsonAggSQL wraps a subquery (with %s as the user-id placeholder) into a
// `jsonb_agg(to_jsonb(...))` expression that collapses to an empty array when
// the user has no rows.
func jsonAggSQL(selectBody string) string {
	return fmt.Sprintf(
		`SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id), '[]'::jsonb)::text FROM (%s) x`,
		fmt.Sprintf(selectBody, "$1"))
}

// ExportUserData assembles a full data-subject export for one user.
func (s *Store) ExportUserData(ctx context.Context, userID uuid.UUID) (DataSubjectExport, error) {
	out := DataSubjectExport{ExportedAt: time.Now().UTC()}

	if err := s.pool.QueryRow(ctx, `
		SELECT to_jsonb(u)::text FROM users u WHERE u.id=$1`, userID).Scan(&out.User); err != nil {
		return out, fmt.Errorf("export user: %w", err)
	}

	sections := []struct {
		dest *json.RawMessage
		sql  string
	}{
		{&out.KYCProfile, jsonAggSQL(`SELECT user_id AS id, user_id, tier, tier_updated_at, evidence, last_screening_decision, review_status, risk_band, risk_updated_at, created_at, updated_at FROM kyc_profiles WHERE user_id=%s`)},
		{&out.Verifications, jsonAggSQL(`SELECT id, user_id, verification_type, status, provider, provider_reference, result, verified_at, expires_at FROM customer_verifications WHERE user_id=%s`)},
		{&out.Screening, jsonAggSQL(`SELECT id, user_id, provider, decision, matched_names, screened_at, rescreen_due FROM screening_results WHERE user_id=%s`)},
		{&out.RiskEvents, jsonAggSQL(`SELECT id, user_id, event_type, score, details, occurred_at FROM risk_events WHERE user_id=%s`)},
		{&out.Consents, jsonAggSQL(`SELECT id, user_id, purpose, status, source, version, granted_at, revoked_at FROM consent_records WHERE user_id=%s`)},
		{&out.Payments, jsonAggSQL(`SELECT p.id, p.user_id, p.merchant_id, m.name AS merchant_name, p.amount_kobo, p.currency, p.status, p.provider, p.provider_reference, p.channel, p.paid_at, p.created_at FROM payments p JOIN merchants m ON m.id=p.merchant_id WHERE p.user_id=%s`)},
		{&out.DataOrders, jsonAggSQL(`SELECT id, user_id, payment_id, channel, recipient, beneficiary_phone, amount_kobo, status, provider_reference, fulfilled_at, created_at FROM data_orders WHERE user_id=%s`)},
		{&out.Invoices, jsonAggSQL(`SELECT id, merchant_id, created_by_user_id, customer_whatsapp_number, customer_email, reference, status, subtotal_kobo, total_kobo, amount_paid_kobo, created_at, paid_at FROM invoices WHERE created_by_user_id=%s`)},
		{&out.ThriftGroups, jsonAggSQL(`SELECT id, creator_user_id, name, contribution_amount_kobo, frequency, status, current_cycle, created_at, completed_at FROM thrift_groups WHERE creator_user_id=%s`)},
		{&out.ThriftMemberships, jsonAggSQL(`SELECT id, group_id, user_id, status, payout_position, joined_at FROM thrift_members WHERE user_id=%s`)},
		{&out.ReviewCases, jsonAggSQL(`SELECT id, user_id, case_type, requested_tier, status, reason, reviewer_email, decision_note, created_at, reviewed_at FROM manual_review_cases WHERE user_id=%s`)},
		{&out.TransactionAlerts, jsonAggSQL(`SELECT id, user_id, payment_id, rule, severity, details, status, reviewer_email, created_at FROM transaction_alerts WHERE user_id=%s`)},
		{&out.MerchantOwnership, jsonAggSQL(`SELECT m.id, m.slug, m.name, mo.created_at FROM merchant_owners mo JOIN merchants m ON m.id=mo.merchant_id WHERE mo.user_id=%s`)},
	}
	for _, sec := range sections {
		if err := s.pool.QueryRow(ctx, sec.sql, userID).Scan(sec.dest); err != nil {
			return out, fmt.Errorf("export section: %w", err)
		}
	}
	return out, nil
}

// FindUserByPhone resolves a customer by WhatsApp number (exact match).
func (s *Store) FindUserByPhone(ctx context.Context, phone string) (User, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return User{}, fmt.Errorf("empty phone")
	}
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(whatsapp_number,''), display_name, email, onboarding_complete,
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level, account_level,
			telegram_chat_id, telegram_user_id, telegram_username, telegram_verified_at, telegram_confirmed_at,
			last_inbound_at, created_at, updated_at
		FROM users WHERE whatsapp_number=$1`, phone).Scan(&user.ID, &user.WhatsAppNumber,
		&user.DisplayName, &user.Email, &user.OnboardingComplete, &user.WhatsAppVerifiedAt,
		&user.NumberConfirmedAt, &user.EmailVerifiedAt, &user.VerificationLevel, &user.AccountLevel,
		&user.TelegramChatID, &user.TelegramUserID, &user.TelegramUsername, &user.TelegramVerifiedAt,
		&user.TelegramConfirmedAt, &user.LastInboundAt, &user.CreatedAt, &user.UpdatedAt)
	if err == pgx.ErrNoRows {
		return User{}, nil
	}
	return user, err
}

// GetUserByID loads a single user row.
func (s *Store) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(whatsapp_number,''), display_name, email, onboarding_complete,
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level, account_level,
			telegram_chat_id, telegram_user_id, telegram_username, telegram_verified_at, telegram_confirmed_at,
			last_inbound_at, created_at, updated_at
		FROM users WHERE id=$1`, id).Scan(&user.ID, &user.WhatsAppNumber,
		&user.DisplayName, &user.Email, &user.OnboardingComplete, &user.WhatsAppVerifiedAt,
		&user.NumberConfirmedAt, &user.EmailVerifiedAt, &user.VerificationLevel, &user.AccountLevel,
		&user.TelegramChatID, &user.TelegramUserID, &user.TelegramUsername, &user.TelegramVerifiedAt,
		&user.TelegramConfirmedAt, &user.LastInboundAt, &user.CreatedAt, &user.UpdatedAt)
	if err == pgx.ErrNoRows {
		return User{}, nil
	}
	return user, err
}

// CompleteDataSubjectRequest finalises an access or erasure request.
//   - access:  marks the request completed; the export is served out-of-band.
//   - erasure: archives a full snapshot to the append-only archive ledger,
//     removes the cooling-off legal hold, marks the request completed, and
//     cascade-deletes the user (all user-linked rows follow the FK).
func (s *Store) CompleteDataSubjectRequest(ctx context.Context, id int64, reviewerEmail, note string) (DataSubjectRequest, error) {
	req, err := s.GetDataSubjectRequest(ctx, id)
	if err != nil {
		return DataSubjectRequest{}, err
	}
	if req.Status == "completed" {
		return req, nil
	}
	if req.RequestType == "erasure" {
		return s.completeErasure(ctx, req, reviewerEmail, note)
	}
	now := time.Now().UTC()
	_, err = s.pool.Exec(ctx, `
		UPDATE data_subject_requests
		SET status='completed', reviewer_email=$2, decision_note=$3, reviewed_at=$4, completed_at=$4
		WHERE id=$1`, id, reviewerEmail, note, now)
	if err != nil {
		return DataSubjectRequest{}, fmt.Errorf("complete access request: %w", err)
	}
	req.Status = "completed"
	req.ReviewerEmail = reviewerEmail
	req.DecisionNote = note
	req.ReviewedAt = &now
	req.CompletedAt = &now
	return req, s.auditRequest(ctx, req, "dsr.request_completed")
}

// completeErasure archives and then deletes the user atomically.
func (s *Store) completeErasure(ctx context.Context, req DataSubjectRequest, reviewerEmail, note string) (DataSubjectRequest, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DataSubjectRequest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	export, err := s.ExportUserData(ctx, req.UserID)
	if err != nil {
		return DataSubjectRequest{}, fmt.Errorf("erasure export: %w", err)
	}
	payload, err := json.Marshal(export)
	if err != nil {
		return DataSubjectRequest{}, fmt.Errorf("erasure snapshot: %w", err)
	}
	if err := s.appendArchive(ctx, tx, "user_erasure", req.UserID.String(), payload); err != nil {
		return DataSubjectRequest{}, fmt.Errorf("erasure archive: %w", err)
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE data_subject_requests
		SET status='completed', reviewer_email=$2, decision_note=$3, reviewed_at=$4, completed_at=$4
		WHERE id=$1`, req.ID, reviewerEmail, note, now); err != nil {
		return DataSubjectRequest{}, fmt.Errorf("complete erasure request: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM legal_holds WHERE subject_type='user' AND subject_id=$1`, req.UserID.String()); err != nil {
		return DataSubjectRequest{}, fmt.Errorf("release erasure hold: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, req.UserID); err != nil {
		return DataSubjectRequest{}, fmt.Errorf("erase user: %w", err)
	}
	if err := appendAuditLogTx(ctx, tx, &AuditLog{
		ActorType: "system", Action: "dsr.erasure_completed",
		ResourceType: sqlStr("user"), ResourceID: sqlStr(req.UserID.String()),
		Details: map[string]any{"request_id": req.ID, "reviewer": reviewerEmail},
	}); err != nil {
		return DataSubjectRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DataSubjectRequest{}, fmt.Errorf("commit erasure: %w", err)
	}
	req.Status = "completed"
	req.ReviewerEmail = reviewerEmail
	req.DecisionNote = note
	req.ReviewedAt = &now
	req.CompletedAt = &now
	return req, nil
}

// RejectDataSubjectRequest declines a request, releasing any erasure hold.
func (s *Store) RejectDataSubjectRequest(ctx context.Context, id int64, reviewerEmail, note string) (DataSubjectRequest, error) {
	req, err := s.GetDataSubjectRequest(ctx, id)
	if err != nil {
		return DataSubjectRequest{}, err
	}
	now := time.Now().UTC()
	_, err = s.pool.Exec(ctx, `
		UPDATE data_subject_requests
		SET status='rejected', reviewer_email=$2, decision_note=$3, reviewed_at=$4
		WHERE id=$1`, id, reviewerEmail, note, now)
	if err != nil {
		return DataSubjectRequest{}, fmt.Errorf("reject request: %w", err)
	}
	if req.RequestType == "erasure" {
		if err := s.RemoveLegalHold(ctx, "user", req.UserID.String()); err != nil {
			return DataSubjectRequest{}, err
		}
	}
	req.Status = "rejected"
	req.ReviewerEmail = reviewerEmail
	req.DecisionNote = note
	req.ReviewedAt = &now
	return req, s.auditRequest(ctx, req, "dsr.request_rejected")
}

func (s *Store) auditRequest(ctx context.Context, req DataSubjectRequest, action string) error {
	_, err := s.AppendAuditLog(ctx, AuditLog{
		ActorType: "system", Action: action,
		ResourceType: sqlStr("data_subject_request"), ResourceID: sqlStr(fmt.Sprint(req.ID)),
		Details: map[string]any{"request_type": req.RequestType, "status": req.Status, "user_id": req.UserID.String()},
	})
	return err
}

func sqlStr(v string) sql.NullString {
	return sql.NullString{String: v, Valid: v != ""}
}
