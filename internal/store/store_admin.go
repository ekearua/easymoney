package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateAdminSession persists only a hash of the bearer token.
// EnsureAdminUser upserts the bootstrap admin account from config. The role
// stays 'admin' and the account stays enabled so deployments always keep a
// recovery account; the password hash is updated when config changes.
func (s *Store) EnsureAdminUser(ctx context.Context, email, passwordHash string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO admin_users(email, password_hash, role, enabled)
		VALUES($1, $2, $3, true)
		ON CONFLICT (email) DO UPDATE SET
			password_hash=EXCLUDED.password_hash,
			role='admin',
			enabled=true,
			updated_at=now()`,
		email, passwordHash, RoleAdmin)
	return err
}

// AdminUserByEmail returns an admin account by email, including the hash.
func (s *Store) AdminUserByEmail(ctx context.Context, email string) (*AdminUser, error) {
	var user AdminUser
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, role, enabled, created_at, updated_at
		FROM admin_users WHERE email=$1`, email).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.Enabled,
		&user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &user, err
}

// AdminUserByID returns an admin account without the password hash.
func (s *Store) AdminUserByID(ctx context.Context, id uuid.UUID) (*AdminUser, error) {
	var user AdminUser
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, role, enabled, created_at, updated_at
		FROM admin_users WHERE id=$1`, id).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.Enabled,
		&user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &user, err
}

// ListAdminUsers returns all admin accounts (hashes excluded) for the
// admin management page.
func (s *Store) ListAdminUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, email, password_hash, role, enabled, created_at, updated_at
		FROM admin_users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []AdminUser{}
	for rows.Next() {
		var user AdminUser
		if err := rows.Scan(
			&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.Enabled,
			&user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// CreateAdminUser provisions a new operator account.
func (s *Store) CreateAdminUser(ctx context.Context, email, passwordHash, role string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO admin_users(email, password_hash, role)
		VALUES($1, $2, $3)`, email, passwordHash, role)
	return err
}

// UpdateAdminUserRole changes an operator's role.
func (s *Store) UpdateAdminUserRole(ctx context.Context, id uuid.UUID, role string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE admin_users SET role=$1, updated_at=now() WHERE id=$2`, role, id)
	return err
}

// SetAdminUserEnabled enables or disables an operator account.
func (s *Store) SetAdminUserEnabled(ctx context.Context, id uuid.UUID, enabled bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE admin_users SET enabled=$1, updated_at=now() WHERE id=$2`, enabled, id)
	return err
}

// UpdateAdminUserPassword replaces an operator's password hash.
func (s *Store) UpdateAdminUserPassword(ctx context.Context, id uuid.UUID, passwordHash string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE admin_users SET password_hash=$1, updated_at=now() WHERE id=$2`, passwordHash, id)
	return err
}

func (s *Store) CreateAdminSession(ctx context.Context, adminID uuid.UUID, token, csrf string, expiresAt time.Time) error {
	hash := sha256.Sum256([]byte(token))
	sealedCSRF, err := s.sealValue([]byte(csrf))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO admin_sessions(token_hash,csrf_token,expires_at,admin_id) VALUES($1,$2,$3,$4)`,
		hash[:], sealedCSRF, expiresAt, adminID)
	return err
}

// ValidateAdminSession resolves a session, its CSRF token, and the admin
// account role. Disabled accounts and sessions past expiry are rejected.
func (s *Store) ValidateAdminSession(ctx context.Context, token string) (adminID uuid.UUID, role string, email string, csrf string, err error) {
	hash := sha256.Sum256([]byte(token))
	var sealedCSRF string
	err = s.pool.QueryRow(ctx, `
		SELECT s.admin_id, u.role, u.email, s.csrf_token
		FROM admin_sessions s
		JOIN admin_users u ON u.id = s.admin_id
		WHERE s.token_hash=$1 AND s.expires_at > now() AND u.enabled`,
		hash[:]).Scan(&adminID, &role, &email, &sealedCSRF)
	if err != nil {
		return adminID, role, email, "", err
	}
	raw, err := s.openValue(sealedCSRF)
	if err != nil {
		return adminID, role, email, "", err
	}
	return adminID, role, email, string(raw), nil
}

// DeleteAdminSession invalidates one login.
func (s *Store) DeleteAdminSession(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx, `DELETE FROM admin_sessions WHERE token_hash=$1`, hash[:])
	return err
}

// DeleteAdminSessionsByUser invalidates all sessions for an admin (e.g. after role change).
func (s *Store) DeleteAdminSessionsByUser(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM admin_sessions WHERE user_id=$1`, userID)
	return err
}

// AuditLog is one tamper-evident entry in the admin audit trail.
type AuditLog struct {
	ID           int64
	OccurredAt   time.Time
	ActorType    string
	ActorID      uuid.NullUUID
	ActorEmail   sql.NullString
	IP           sql.NullString
	Action       string
	ResourceType sql.NullString
	ResourceID   sql.NullString
	Details      map[string]any
	PrevHash     string
	Hash         string
}

// auditHash binds one entry to its predecessor. Any field edit changes the
// hash, and edits are also rejected by the append-only trigger.
func auditHash(prevHash string, entry AuditLog) string {
	details, err := json.Marshal(entry.Details)
	if err != nil {
		details = []byte("{}")
	}
	fields := []string{
		prevHash,
		entry.OccurredAt.UTC().Format(time.RFC3339Nano),
		entry.ActorType,
		entry.ActorID.UUID.String(),
		entry.ActorEmail.String,
		entry.IP.String,
		entry.Action,
		entry.ResourceType.String,
		entry.ResourceID.String,
		string(details),
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "|")))
	return hex.EncodeToString(sum[:])
}

// AppendAuditLog records a privileged action. The insert is serialized on the
// tail row so the hash chain cannot fork under concurrency.
func (s *Store) AppendAuditLog(ctx context.Context, entry AuditLog) (AuditLog, error) {
	if entry.ActorID.UUID == uuid.Nil {
		entry.ActorID.Valid = false
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return entry, fmt.Errorf("begin audit log: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := appendAuditLogTx(ctx, tx, &entry); err != nil {
		return entry, err
	}
	if err := tx.Commit(ctx); err != nil {
		return entry, fmt.Errorf("commit audit log: %w", err)
	}
	return entry, nil
}

// appendAuditLogTx inserts one hash-chained entry on the supplied transaction
// without committing, so callers can co-record an audit entry atomically with
// the action that produced it.
func appendAuditLogTx(ctx context.Context, tx pgx.Tx, entry *AuditLog) error {
	if entry.ActorID.UUID == uuid.Nil {
		entry.ActorID.Valid = false
	}
	var prevHash string
	if err := tx.QueryRow(ctx, `SELECT hash FROM audit_logs ORDER BY id DESC LIMIT 1 FOR UPDATE`).Scan(&prevHash); err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("read audit tail: %w", err)
	}
	var ip any
	if entry.IP.Valid {
		ip = entry.IP.String
	}
	entry.OccurredAt = time.Now().UTC().Truncate(time.Microsecond)
	entry.Hash = auditHash(prevHash, *entry)
	entry.PrevHash = prevHash
	err := tx.QueryRow(ctx, `
		INSERT INTO audit_logs(occurred_at, actor_type, actor_id, actor_email, ip, action, resource_type, resource_id, details, prev_hash, hash)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, occurred_at`,
		entry.OccurredAt, entry.ActorType, entry.ActorID, entry.ActorEmail, ip,
		entry.Action, entry.ResourceType, entry.ResourceID,
		entry.Details, prevHash, entry.Hash,
	).Scan(&entry.ID, &entry.OccurredAt)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

// ListAuditLogs returns recent entries, newest first.
func (s *Store) ListAuditLogs(ctx context.Context, limit int) ([]AuditLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, occurred_at, actor_type, COALESCE(actor_id::text,''), COALESCE(actor_email,''),
			COALESCE(ip::text,''), action, COALESCE(resource_type,''), COALESCE(resource_id,''),
			details, prev_hash, hash
		FROM audit_logs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []AuditLog
	for rows.Next() {
		var entry AuditLog
		var details []byte
		if err := rows.Scan(&entry.ID, &entry.OccurredAt, &entry.ActorType, &entry.ActorID.UUID,
			&entry.ActorEmail.String, &entry.IP.String, &entry.Action, &entry.ResourceType.String,
			&entry.ResourceID.String, &details, &entry.PrevHash, &entry.Hash); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(details, &entry.Details)
		entry.ActorID.Valid = entry.ActorID.UUID != uuid.Nil
		entry.ActorEmail.Valid = entry.ActorEmail.String != ""
		entry.IP.Valid = entry.IP.String != ""
		entry.ResourceType.Valid = entry.ResourceType.String != ""
		entry.ResourceID.Valid = entry.ResourceID.String != ""
		if entry.Details == nil {
			entry.Details = map[string]any{}
		}
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

// VerifyAuditChain recomputes every hash and checks linkage. It returns the
// number of entries and the index of the first broken entry (-1 if sound).
func (s *Store) VerifyAuditChain(ctx context.Context) (int, int, error) {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&count); err != nil {
		return 0, -1, err
	}
	var cursor int64
	var prevHash string
	expected := prevHash
	broken := -1
	seen := 0
	for {
		rows, err := s.pool.Query(ctx, `
			SELECT id, occurred_at, actor_type, COALESCE(actor_id::text,''), COALESCE(actor_email,''),
				COALESCE(ip::text,''), action, COALESCE(resource_type,''), COALESCE(resource_id,''),
				details, prev_hash, hash
			FROM audit_logs WHERE id > $1 ORDER BY id ASC LIMIT 1000`, cursor)
		if err != nil {
			return 0, -1, err
		}
		var batch []AuditLog
		for rows.Next() {
			var entry AuditLog
			var details []byte
			if err := rows.Scan(&entry.ID, &entry.OccurredAt, &entry.ActorType, &entry.ActorID.UUID,
				&entry.ActorEmail.String, &entry.IP.String, &entry.Action, &entry.ResourceType.String,
				&entry.ResourceID.String, &details, &entry.PrevHash, &entry.Hash); err != nil {
				rows.Close()
				return 0, -1, err
			}
			_ = json.Unmarshal(details, &entry.Details)
			entry.ActorID.Valid = entry.ActorID.UUID != uuid.Nil
			batch = append(batch, entry)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, -1, err
		}
		if len(batch) == 0 {
			break
		}
		for _, entry := range batch {
			recomputed := auditHash(prevHash, entry)
			if entry.PrevHash != expected || recomputed != entry.Hash {
				broken = seen
				return count, broken, nil
			}
			prevHash = entry.Hash
			expected = entry.Hash
			cursor = entry.ID
			seen++
		}
	}
	return count, broken, nil
}

// Admin rows use a NULL subject_id; merchant rows carry the merchant UUID.
func (s *Store) GetTOTPSecret(ctx context.Context, scope string, subjectID *uuid.UUID) ([]byte, error) {
	var cipher []byte
	err := s.pool.QueryRow(ctx, `
		SELECT secret_cipher FROM totp_secrets
		WHERE scope=$1 AND subject_id IS NOT DISTINCT FROM $2`, scope, subjectID).Scan(&cipher)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return cipher, err
}

// SetTOTPSecret upserts the encrypted TOTP secret for a scope/subject. Admin
// rows have a NULL subject_id and are deduplicated by the partial unique index
// totp_secrets_admin_one, which ON CONFLICT must target explicitly because
// NULLs never conflict on the plain unique constraint.
func (s *Store) SetTOTPSecret(ctx context.Context, scope string, subjectID *uuid.UUID, cipher []byte) error {
	if subjectID == nil {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO totp_secrets(scope, subject_id, secret_cipher)
			VALUES($1, NULL, $2)
			ON CONFLICT (scope) WHERE scope='admin' AND subject_id IS NULL
			DO UPDATE SET secret_cipher=EXCLUDED.secret_cipher, updated_at=now()`,
			scope, cipher)
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO totp_secrets(scope, subject_id, secret_cipher)
		VALUES($1, $2, $3)
		ON CONFLICT (scope, subject_id) DO UPDATE SET secret_cipher=EXCLUDED.secret_cipher, updated_at=now()`,
		scope, subjectID, cipher)
	return err
}

// DeleteTOTPSecret removes the TOTP secret for a scope/subject (MFA reset).
func (s *Store) DeleteTOTPSecret(ctx context.Context, scope string, subjectID *uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM totp_secrets WHERE scope=$1 AND subject_id IS NOT DISTINCT FROM $2`, scope, subjectID)
	return err
}

// CreateTOTPPendingLogin stores a hashed pending-login token issued after a
// successful password check. secretCipher is the encrypted enrollment secret,
// or nil when only code verification is pending.
func (s *Store) CreateTOTPPendingLogin(ctx context.Context, tokenHash []byte, scope string, subjectID *uuid.UUID, secretCipher []byte, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO totp_pending_logins(token_hash, scope, subject_id, secret_cipher, expires_at)
		VALUES($1,$2,$3,$4,$5)`, tokenHash, scope, subjectID, secretCipher, expiresAt)
	return err
}

// GetTOTPPendingLogin reads a pending login without consuming it, so a page
// refresh can still render the QR code. The caller must still consume the
// pending login before issuing a session.
func (s *Store) GetTOTPPendingLogin(ctx context.Context, tokenHash []byte) (scope string, subjectID *uuid.UUID, secretCipher []byte, found bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT scope, subject_id, secret_cipher FROM totp_pending_logins
		WHERE token_hash=$1 AND expires_at > now()`, tokenHash).Scan(&scope, &subjectID, &secretCipher)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil, false, nil
	}
	if err != nil {
		return "", nil, nil, false, err
	}
	return scope, subjectID, secretCipher, true, nil
}

// ConsumeTOTPPendingLogin reads and deletes a pending login in one step.
// Returns the encrypted enrollment secret (nil when code-only) and whether a
// pending login existed at all.
func (s *Store) ConsumeTOTPPendingLogin(ctx context.Context, tokenHash []byte) (scope string, subjectID *uuid.UUID, secretCipher []byte, found bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, nil, false, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `
		SELECT scope, subject_id, secret_cipher FROM totp_pending_logins
		WHERE token_hash=$1 AND expires_at > now()`, tokenHash).Scan(&scope, &subjectID, &secretCipher)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil, false, tx.Commit(ctx)
	}
	if err != nil {
		return "", nil, nil, false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM totp_pending_logins WHERE token_hash=$1`, tokenHash); err != nil {
		return "", nil, nil, false, err
	}
	return scope, subjectID, secretCipher, true, tx.Commit(ctx)
}
