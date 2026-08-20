package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// User is a WhatsApp customer profile.
type User struct {
	ID                  uuid.UUID
	WhatsAppNumber      string
	DisplayName         string
	Email               string
	OnboardingComplete  bool
	WhatsAppVerifiedAt  sql.NullTime
	NumberConfirmedAt   sql.NullTime
	EmailVerifiedAt     sql.NullTime
	VerificationLevel   string
	AccountLevel        string
	TelegramChatID      sql.NullString
	TelegramUserID      sql.NullString
	TelegramUsername    string
	TelegramVerifiedAt  sql.NullTime
	TelegramConfirmedAt sql.NullTime
	LastInboundAt       time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Session persists a user's current conversation state.
type Session struct {
	UserID    uuid.UUID
	State     string
	Data      map[string]string
	ExpiresAt time.Time
}

// GetOrCreateUser resolves a customer by normalized WhatsApp number.
func (s *Store) GetOrCreateUser(ctx context.Context, number string) (User, error) {
	const query = `
		INSERT INTO users (whatsapp_number, whatsapp_verified_at, verification_level) VALUES ($1, now(), 'whatsapp_inbound')
		ON CONFLICT (whatsapp_number) DO UPDATE SET
			updated_at = now(),
			last_inbound_at = now(),
			whatsapp_verified_at = COALESCE(users.whatsapp_verified_at, now()),
			verification_level = CASE
				WHEN users.number_confirmed_at IS NOT NULL THEN users.verification_level
				WHEN users.verification_level = 'unverified' THEN 'whatsapp_inbound'
				ELSE users.verification_level
			END
		RETURNING id, COALESCE(whatsapp_number,''), display_name, email, onboarding_complete,
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level, account_level,
			telegram_chat_id, telegram_user_id, telegram_username, telegram_verified_at, telegram_confirmed_at,
			last_inbound_at, created_at, updated_at`
	var user User
	err := s.pool.QueryRow(ctx, query, number).Scan(
		&user.ID, &user.WhatsAppNumber, &user.DisplayName, &user.Email,
		&user.OnboardingComplete, &user.WhatsAppVerifiedAt, &user.NumberConfirmedAt,
		&user.EmailVerifiedAt, &user.VerificationLevel, &user.AccountLevel, &user.TelegramChatID,
		&user.TelegramUserID, &user.TelegramUsername, &user.TelegramVerifiedAt,
		&user.TelegramConfirmedAt, &user.LastInboundAt,
		&user.CreatedAt, &user.UpdatedAt,
	)
	return user, err
}

// GetOrCreateTelegramUser resolves a Telegram customer by stable chat ID.
func (s *Store) GetOrCreateTelegramUser(ctx context.Context, chatID, userID, username string) (User, error) {
	const query = `
		INSERT INTO users (telegram_chat_id, telegram_user_id, telegram_username, telegram_verified_at, verification_level)
		VALUES ($1,$2,$3,now(),'telegram_inbound')
		ON CONFLICT (telegram_chat_id) DO UPDATE SET
			telegram_user_id=EXCLUDED.telegram_user_id,
			telegram_username=EXCLUDED.telegram_username,
			telegram_verified_at=COALESCE(users.telegram_verified_at, now()),
			last_inbound_at=now(),
			updated_at=now(),
			verification_level = CASE
				WHEN users.telegram_confirmed_at IS NOT NULL THEN users.verification_level
				WHEN users.verification_level IN ('unverified','whatsapp_inbound') THEN 'telegram_inbound'
				ELSE users.verification_level
			END
		RETURNING id, COALESCE(whatsapp_number,''), display_name, email, onboarding_complete,
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level, account_level,
			telegram_chat_id, telegram_user_id, telegram_username, telegram_verified_at, telegram_confirmed_at,
			last_inbound_at, created_at, updated_at`
	var user User
	err := s.pool.QueryRow(ctx, query, chatID, userID, username).Scan(
		&user.ID, &user.WhatsAppNumber, &user.DisplayName, &user.Email,
		&user.OnboardingComplete, &user.WhatsAppVerifiedAt, &user.NumberConfirmedAt,
		&user.EmailVerifiedAt, &user.VerificationLevel, &user.AccountLevel, &user.TelegramChatID,
		&user.TelegramUserID, &user.TelegramUsername, &user.TelegramVerifiedAt,
		&user.TelegramConfirmedAt, &user.LastInboundAt,
		&user.CreatedAt, &user.UpdatedAt,
	)
	return user, err
}

// UpdateUserName stores the first onboarding field.
func (s *Store) UpdateUserName(ctx context.Context, id uuid.UUID, name string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET display_name=$2, updated_at=now() WHERE id=$1`, id, name)
	return err
}

// UpdateUserEmail stores the checkout and receipt email. If the address
// changes, any previous email verification is cleared.
func (s *Store) UpdateUserEmail(ctx context.Context, id uuid.UUID, email string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users
		SET email=$2,
			email_verified_at=CASE WHEN lower(email)=lower($2) THEN email_verified_at ELSE NULL END,
			updated_at=now()
		WHERE id=$1`, id, email)
	return err
}

// ErrResendTooSoon is returned when a new confirmation code is requested
// before the resend cooldown has elapsed.
var ErrResendTooSoon = errors.New("confirmation code resend is still on cooldown")

// defaultEmailResendCooldown applies when no explicit cooldown is configured.
const defaultEmailResendCooldown = 60 * time.Second

// CreateEmailVerificationCode stores a hashed one-time confirmation code.
// Previous unconsumed codes for the same user and email are invalidated so
// a resend cannot reset the attempt counter of an older active code.
func (s *Store) CreateEmailVerificationCode(ctx context.Context, userID uuid.UUID, email string, codeHash []byte, expiresAt time.Time) error {
	cooldown := s.EmailResendCooldown
	if cooldown <= 0 {
		cooldown = defaultEmailResendCooldown
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var recent bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM email_verification_codes
			WHERE user_id=$1 AND lower(email)=lower($2) AND consumed_at IS NULL
			  AND created_at > now() - $3::interval
		)`, userID, email, cooldown).Scan(&recent)
	if err != nil {
		return err
	}
	if recent {
		return ErrResendTooSoon
	}
	if _, err := tx.Exec(ctx, `
		UPDATE email_verification_codes SET consumed_at=now()
		WHERE user_id=$1 AND lower(email)=lower($2) AND consumed_at IS NULL`, userID, email); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO email_verification_codes(user_id,email,code_hash,expires_at)
		VALUES($1,lower($2),$3,$4)`, userID, email, codeHash, expiresAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// VerifyEmailCode consumes the latest valid code when the submitted
// candidate matches the stored bcrypt hash. Mismatches increment attempts
// so repeated guessing is bounded.
func (s *Store) VerifyEmailCode(ctx context.Context, userID uuid.UUID, email string, candidate []byte) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var stored []byte
	var attempts int
	err = tx.QueryRow(ctx, `
		SELECT id, code_hash, attempts
		FROM email_verification_codes
		WHERE user_id=$1 AND lower(email)=lower($2) AND consumed_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC
		LIMIT 1
		FOR UPDATE`, userID, email).Scan(&id, &stored, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if attempts >= 5 || bcrypt.CompareHashAndPassword(stored, candidate) != nil {
		_, err = tx.Exec(ctx, `UPDATE email_verification_codes SET attempts=attempts+1 WHERE id=$1`, id)
		if err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE email_verification_codes SET consumed_at=now() WHERE id=$1`, id); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE users
		SET email_verified_at=now(),
			verification_level=CASE WHEN verification_level='unverified' THEN 'email_confirmed' ELSE verification_level END,
			updated_at=now()
		WHERE id=$1 AND lower(email)=lower($2)`, userID, email)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE email_verification_codes
		SET consumed_at=COALESCE(consumed_at, now())
		WHERE user_id=$1 AND lower(email)=lower($2) AND consumed_at IS NULL`, userID, email)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// ConfirmUserNumber records the user's explicit confirmation that their
// WhatsApp number should be used as the demo account identity.
func (s *Store) ConfirmUserNumber(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users
		SET onboarding_complete=true,
			number_confirmed_at=COALESCE(number_confirmed_at, now()),
			verification_level='whatsapp_confirmed',
			updated_at=now()
		WHERE id=$1`, id)
	return err
}

// ConfirmTelegramAccount records the customer's explicit Telegram confirmation.
func (s *Store) ConfirmTelegramAccount(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users
		SET onboarding_complete=true,
			telegram_confirmed_at=COALESCE(telegram_confirmed_at, now()),
			verification_level='telegram_confirmed',
			updated_at=now()
		WHERE id=$1`, id)
	return err
}

// ListUsers returns recent customers for the read-only dashboard.
func (s *Store) ListUsers(ctx context.Context, limit int) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(whatsapp_number,''), display_name, email, onboarding_complete,
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level, account_level,
			telegram_chat_id, telegram_user_id, telegram_username, telegram_verified_at, telegram_confirmed_at,
			last_inbound_at, created_at, updated_at
		FROM users ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.WhatsAppNumber, &user.DisplayName, &user.Email,
			&user.OnboardingComplete, &user.WhatsAppVerifiedAt, &user.NumberConfirmedAt,
			&user.EmailVerifiedAt, &user.VerificationLevel, &user.AccountLevel, &user.TelegramChatID,
			&user.TelegramUserID, &user.TelegramUsername, &user.TelegramVerifiedAt,
			&user.TelegramConfirmedAt, &user.LastInboundAt,
			&user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// LoadSession returns an unexpired conversation session, or a fresh "menu" state.
func (s *Store) LoadSession(ctx context.Context, userID uuid.UUID) (Session, error) {
	var session Session
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, state, data, expires_at
		FROM conversation_sessions WHERE user_id=$1 AND expires_at > now()`, userID).
		Scan(&session.UserID, &session.State, &raw, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{UserID: userID, State: "menu", Data: map[string]string{}}, nil
	}
	if err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal(raw, &session.Data); err != nil {
		return Session{}, fmt.Errorf("decode session: %w", err)
	}
	return session, nil
}

// SaveSession replaces the current durable conversation state.
func (s *Store) SaveSession(ctx context.Context, session Session) error {
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	raw, err := json.Marshal(session.Data)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO conversation_sessions (user_id, state, data, expires_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (user_id) DO UPDATE
		SET state=EXCLUDED.state, data=EXCLUDED.data, expires_at=EXCLUDED.expires_at, updated_at=now()`,
		session.UserID, session.State, raw, session.ExpiresAt)
	return err
}
