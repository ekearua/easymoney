package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Merchant console roles. The owner keeps full access and is never stored as a
// member; staff hold exactly one scoped role.
const (
	MerchantRoleOwner    = "owner"
	MerchantRoleRecords  = "records"
	MerchantRolePayments = "payments"

	MemberStatusInvited  = "invited"
	MemberStatusActive   = "active"
	MemberStatusDisabled = "disabled"
)

var ErrMerchantMemberNotFound = errors.New("merchant member not found")

// MerchantMember is one staff account on a merchant console.
type MerchantMember struct {
	ID             uuid.UUID
	MerchantID     uuid.UUID
	MerchantName   string
	Email          string
	PasswordHash   string
	Role           string
	Status         string
	InvitedByOwner bool
	ActivatedAt    *time.Time
	LastLoginAt    *time.Time
	CreatedAt      time.Time
}

// HashMemberInviteToken hashes an invite token for storage.
func HashMemberInviteToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

const memberColumns = `m.id, m.merchant_id, COALESCE(mer.name,''), m.email, m.password_hash,
	m.role, m.status, m.invited_by_owner, m.activated_at, m.last_login_at, m.created_at`

func scanMerchantMember(row pgx.Row) (MerchantMember, error) {
	var m MerchantMember
	err := row.Scan(&m.ID, &m.MerchantID, &m.MerchantName, &m.Email, &m.PasswordHash,
		&m.Role, &m.Status, &m.InvitedByOwner, &m.ActivatedAt, &m.LastLoginAt, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MerchantMember{}, ErrMerchantMemberNotFound
	}
	return m, err
}

// CreateMerchantMember records an invited staff account. The caller generates
// the invite token and passes only its hash.
func (s *Store) CreateMerchantMember(ctx context.Context, merchantID uuid.UUID, email, role string, inviteTokenHash string, expires time.Time, byOwner bool, invitedByMember *uuid.UUID) (MerchantMember, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return MerchantMember{}, errors.New("member email is required")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO merchant_members(merchant_id, email, role, status, invite_token_hash, invite_expires_at, invited_by_owner, invited_by_member)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, merchant_id, '', email, password_hash, role, status, invited_by_owner, activated_at, last_login_at, created_at`,
		merchantID, email, role, MemberStatusInvited, inviteTokenHash, expires, byOwner, invitedByMember)
	return scanMerchantMember(row)
}

// MerchantMemberByEmail resolves a staff account for sign-in.
func (s *Store) MerchantMemberByEmail(ctx context.Context, email string) (MerchantMember, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+memberColumns+`
		FROM merchant_members m
		LEFT JOIN merchants mer ON mer.id = m.merchant_id
		WHERE lower(m.email)=lower($1)`, strings.TrimSpace(email))
	return scanMerchantMember(row)
}

// MerchantMemberByInviteToken resolves an unexpired invite.
func (s *Store) MerchantMemberByInviteToken(ctx context.Context, token string) (MerchantMember, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+memberColumns+`
		FROM merchant_members m
		LEFT JOIN merchants mer ON mer.id = m.merchant_id
		WHERE m.invite_token_hash=$1 AND m.status=$2
			AND (m.invite_expires_at IS NULL OR m.invite_expires_at > now())`,
		HashMemberInviteToken(token), MemberStatusInvited)
	return scanMerchantMember(row)
}

// ActivateMerchantMember sets the password and makes the account usable.
func (s *Store) ActivateMerchantMember(ctx context.Context, id uuid.UUID, passwordHash string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_members
		SET password_hash=$2, status=$3, activated_at=now(), invite_token_hash='', invite_expires_at=NULL, updated_at=now()
		WHERE id=$1`, id, passwordHash, MemberStatusActive)
	return err
}

// ListMerchantMembers returns a merchant's staff, newest first.
func (s *Store) ListMerchantMembers(ctx context.Context, merchantID uuid.UUID) ([]MerchantMember, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+memberColumns+`
		FROM merchant_members m
		LEFT JOIN merchants mer ON mer.id = m.merchant_id
		WHERE m.merchant_id=$1
		ORDER BY m.created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []MerchantMember
	for rows.Next() {
		var m MerchantMember
		if err := rows.Scan(&m.ID, &m.MerchantID, &m.MerchantName, &m.Email, &m.PasswordHash,
			&m.Role, &m.Status, &m.InvitedByOwner, &m.ActivatedAt, &m.LastLoginAt, &m.CreatedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// MerchantMembersByMerchant returns every member grouped by merchant, for the
// admin merchants page.
func (s *Store) MerchantMembersByMerchant(ctx context.Context) (map[uuid.UUID][]MerchantMember, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+memberColumns+`
		FROM merchant_members m
		LEFT JOIN merchants mer ON mer.id = m.merchant_id
		ORDER BY m.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[uuid.UUID][]MerchantMember)
	for rows.Next() {
		var m MerchantMember
		if err := rows.Scan(&m.ID, &m.MerchantID, &m.MerchantName, &m.Email, &m.PasswordHash,
			&m.Role, &m.Status, &m.InvitedByOwner, &m.ActivatedAt, &m.LastLoginAt, &m.CreatedAt); err != nil {
			return nil, err
		}
		out[m.MerchantID] = append(out[m.MerchantID], m)
	}
	return out, rows.Err()
}

// MerchantMemberByID returns one member scoped to the merchant.
func (s *Store) MerchantMemberByID(ctx context.Context, merchantID, id uuid.UUID) (MerchantMember, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+memberColumns+`
		FROM merchant_members m
		LEFT JOIN merchants mer ON mer.id = m.merchant_id
		WHERE m.id=$1 AND m.merchant_id=$2`, id, merchantID)
	return scanMerchantMember(row)
}

// SetMerchantMemberRole changes a staff member's role.
func (s *Store) SetMerchantMemberRole(ctx context.Context, merchantID, id uuid.UUID, role string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE merchant_members SET role=$3, updated_at=now() WHERE id=$1 AND merchant_id=$2`,
		id, merchantID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantMemberNotFound
	}
	return nil
}

// SetMerchantMemberStatus disables or re-enables a member. Disabling revokes
// every existing session immediately.
func (s *Store) SetMerchantMemberStatus(ctx context.Context, merchantID, id uuid.UUID, status string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE merchant_members SET status=$3, updated_at=now() WHERE id=$1 AND merchant_id=$2`,
		id, merchantID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrMerchantMemberNotFound
	}
	if status != MemberStatusActive {
		if _, err := tx.Exec(ctx, `DELETE FROM merchant_sessions WHERE member_id=$1`, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteMerchantMember removes a staff account and its sessions.
func (s *Store) DeleteMerchantMember(ctx context.Context, merchantID, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM merchant_members WHERE id=$1 AND merchant_id=$2`, id, merchantID)
	return err
}

// TouchMerchantMemberLogin records a successful sign-in.
func (s *Store) TouchMerchantMemberLogin(ctx context.Context, id uuid.UUID) {
	_, _ = s.pool.Exec(ctx, `UPDATE merchant_members SET last_login_at=now() WHERE id=$1`, id)
}

// CreateMerchantMemberSession stores a session that carries the member id and
// role so the console can gate routes without a second lookup.
func (s *Store) CreateMerchantMemberSession(ctx context.Context, merchantID, memberID, userID uuid.UUID, role, token, csrf string, expiresAt time.Time) error {
	hash := sha256.Sum256([]byte(token))
	sealedCSRF, err := s.sealValue([]byte(csrf))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO merchant_sessions(token_hash,merchant_id,user_id,csrf_token,expires_at,member_id,member_role)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, hash[:], merchantID, userID, sealedCSRF, expiresAt, memberID, role)
	return err
}

// MerchantSessionIdentity is the resolved actor behind a merchant cookie.
type MerchantSessionIdentity struct {
	MerchantID uuid.UUID
	UserID     uuid.UUID
	MemberID   *uuid.UUID
	Role       string
	Email      string
	CSRF       string
}

// MerchantSessionFromToken resolves a merchant session, including the staff
// role. Owner sessions (member_id NULL) resolve to MerchantRoleOwner.
func (s *Store) MerchantSessionFromToken(ctx context.Context, token string) (MerchantSessionIdentity, error) {
	hash := sha256.Sum256([]byte(token))
	var identity MerchantSessionIdentity
	var sealedCSRF string
	var memberID *uuid.UUID
	var role, email string
	err := s.pool.QueryRow(ctx, `
		SELECT s.merchant_id, COALESCE(s.user_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       s.member_id, COALESCE(NULLIF(s.member_role,''), $2), COALESCE(m.email,''), s.csrf_token
		FROM merchant_sessions s
		LEFT JOIN merchant_members m ON m.id = s.member_id
		WHERE s.token_hash=$1 AND s.expires_at > now()
		  AND (s.member_id IS NULL OR (m.status=$3 AND m.role = s.member_role))`,
		hash[:], MerchantRoleOwner, MemberStatusActive).Scan(
		&identity.MerchantID, &identity.UserID, &memberID, &role, &email, &sealedCSRF)
	if err != nil {
		return MerchantSessionIdentity{}, err
	}
	raw, err := s.openValue(sealedCSRF)
	if err != nil {
		return MerchantSessionIdentity{}, err
	}
	identity.MemberID = memberID
	identity.Role = role
	identity.Email = email
	identity.CSRF = string(raw)
	return identity, nil
}
