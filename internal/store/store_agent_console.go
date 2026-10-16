// Agent console identities and sessions (migration 103, POS_AGENT_NETWORK_PLAN.md
// §9). An agent_user is the human who operates an agent terminal: an owner or an
// operator bound to one agent merchant. Nothing here moves money — the console
// only authenticates the operator and lists that agent's awaiting pickups; every
// payout still runs through RedeemPickupTx, which re-checks the agent's status,
// region and limits inside the money-moving transaction.
//
// Sessions store only the hash of the token and seal the CSRF token at rest,
// mirroring merchant_sessions. No foreign keys: retention and DSR purges delete
// users and merchants, and an agent identity must never block a purge.
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrAgentUserNotFound is returned when no console identity matches a lookup.
var ErrAgentUserNotFound = errors.New("agent user not found")

// AgentUser is one console identity for an agent terminal. PasswordHash is empty
// until an operator sets it. Phone is unique across all agent identities.
type AgentUser struct {
	ID              uuid.UUID
	AgentMerchantID uuid.UUID
	Name            string
	Phone           string
	Email           string
	PasswordHash    string
	Role            string
	Status          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const agentUserColumns = `id, agent_merchant_id, name, phone, email, password_hash, role, status, created_at, updated_at`

func scanAgentUser(row pgx.Row) (AgentUser, error) {
	var u AgentUser
	err := row.Scan(&u.ID, &u.AgentMerchantID, &u.Name, &u.Phone, &u.Email,
		&u.PasswordHash, &u.Role, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

// CreateAgentUser inserts a console identity. The password hash is set by the
// caller (bcrypt); the identity is active by default.
func (s *Store) CreateAgentUser(ctx context.Context, u AgentUser) (AgentUser, error) {
	if u.AgentMerchantID == uuid.Nil {
		return AgentUser{}, errors.New("agent user requires an agent merchant")
	}
	if strings.TrimSpace(u.Phone) == "" {
		return AgentUser{}, errors.New("agent user requires a phone number")
	}
	if u.Role == "" {
		u.Role = AgentRoleOperator
	}
	if u.Status == "" {
		u.Status = AgentUserActive
	}
	created, err := scanAgentUser(s.pool.QueryRow(ctx, `
		INSERT INTO agent_users(agent_merchant_id, name, phone, email, password_hash, role, status)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		RETURNING `+agentUserColumns,
		u.AgentMerchantID, u.Name, u.Phone, u.Email, u.PasswordHash, u.Role, u.Status))
	return created, err
}

// AgentUserByLogin resolves a console identity by its email address or its
// phone number (case-insensitively for email). A blank lookup returns
// ErrAgentUserNotFound rather than matching an empty row.
func (s *Store) AgentUserByLogin(ctx context.Context, login string) (AgentUser, error) {
	login = strings.TrimSpace(login)
	if login == "" {
		return AgentUser{}, ErrAgentUserNotFound
	}
	u, err := scanAgentUser(s.pool.QueryRow(ctx, `
		SELECT `+agentUserColumns+` FROM agent_users
		WHERE LOWER(email) = LOWER($1) OR phone = $1
		ORDER BY created_at ASC LIMIT 1`, login))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentUser{}, ErrAgentUserNotFound
	}
	return u, err
}

// AgentUserByID resolves a console identity by id.
func (s *Store) AgentUserByID(ctx context.Context, id uuid.UUID) (AgentUser, error) {
	u, err := scanAgentUser(s.pool.QueryRow(ctx,
		`SELECT `+agentUserColumns+` FROM agent_users WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentUser{}, ErrAgentUserNotFound
	}
	return u, err
}

// SetAgentPassword replaces an identity's password hash (operator reset or
// self-service).
func (s *Store) SetAgentPassword(ctx context.Context, id uuid.UUID, passwordHash string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE agent_users SET password_hash=$2, updated_at=now() WHERE id=$1`, id, passwordHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAgentUserNotFound
	}
	return nil
}

// CreateAgentSession stores a hashed session token and its sealed CSRF token.
func (s *Store) CreateAgentSession(ctx context.Context, agentUserID, agentMerchantID uuid.UUID, token, csrf string, expiresAt time.Time) error {
	hash := sha256.Sum256([]byte(token))
	sealedCSRF, err := s.sealValue([]byte(csrf))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO agent_sessions(token_hash, agent_user_id, agent_merchant_id, csrf_token, expires_at)
		VALUES($1,$2,$3,$4,$5)`, hash[:], agentUserID, agentMerchantID, sealedCSRF, expiresAt)
	return err
}

// ValidateAgentSession checks a session token and returns the identity, the
// agent merchant it is bound to, and the CSRF token.
func (s *Store) ValidateAgentSession(ctx context.Context, token string) (agentUserID, agentMerchantID uuid.UUID, csrf string, err error) {
	hash := sha256.Sum256([]byte(token))
	var sealedCSRF string
	err = s.pool.QueryRow(ctx, `
		SELECT agent_user_id, agent_merchant_id, csrf_token FROM agent_sessions
		WHERE token_hash=$1 AND expires_at > now()`, hash[:]).Scan(&agentUserID, &agentMerchantID, &sealedCSRF)
	if err != nil {
		return agentUserID, agentMerchantID, "", err
	}
	raw, err := s.openValue(sealedCSRF)
	if err != nil {
		return agentUserID, agentMerchantID, "", err
	}
	return agentUserID, agentMerchantID, string(raw), nil
}

// DeleteAgentSession invalidates one agent login.
func (s *Store) DeleteAgentSession(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx, `DELETE FROM agent_sessions WHERE token_hash=$1`, hash[:])
	return err
}

// AgentPickup is one awaiting remittance on an agent's console: enough to show
// the recipient and amount before redemption. It carries no code or OTP — those
// are never readable, only verified.
type AgentPickup struct {
	RemittanceID   uuid.UUID
	RecipientName  string
	RecipientPhone string
	AmountKobo     int64
	CommissionKobo int64
	RegionID       uuid.UUID
	ExpiresAt      *time.Time
	CreatedAt      time.Time
}

// ListAgentPickups lists an agent's remittances awaiting pickup, newest first.
// It is display-only: the row a code resolves to is re-checked and locked by
// RedeemPickupTx at redemption time.
func (s *Store) ListAgentPickups(ctx context.Context, agentMerchantID uuid.UUID, limit int) ([]AgentPickup, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, recipient_name, recipient_phone, amount_kobo, commission_kobo, region_id, expires_at, created_at
		FROM remittances
		WHERE agent_merchant_id=$1 AND status='awaiting_pickup'
		ORDER BY created_at DESC
		LIMIT $2`, agentMerchantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pickups []AgentPickup
	for rows.Next() {
		var p AgentPickup
		if err := rows.Scan(&p.RemittanceID, &p.RecipientName, &p.RecipientPhone, &p.AmountKobo,
			&p.CommissionKobo, &p.RegionID, &p.ExpiresAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		pickups = append(pickups, p)
	}
	return pickups, rows.Err()
}
