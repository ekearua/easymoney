// Package store provides PostgreSQL persistence and transactional payment operations.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-payment-demo/internal/domain"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store wraps PostgreSQL operations used by the application.
type Store struct {
	pool *pgxpool.Pool
}

// User is a WhatsApp customer profile.
type User struct {
	ID                 uuid.UUID
	WhatsAppNumber     string
	DisplayName        string
	Email              string
	OnboardingComplete bool
	WhatsAppVerifiedAt sql.NullTime
	NumberConfirmedAt  sql.NullTime
	EmailVerifiedAt    sql.NullTime
	VerificationLevel  string
	LastInboundAt      time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Merchant is a curated payment recipient.
type Merchant struct {
	ID          uuid.UUID
	Slug        string
	Name        string
	Category    string
	Description string
	LogoURL     string
	Active      bool
	CreatedAt   time.Time
}

// Session persists a user's current conversation state.
type Session struct {
	UserID    uuid.UUID
	State     string
	Data      map[string]string
	ExpiresAt time.Time
}

// PaymentView joins payment data with customer and merchant display fields.
type PaymentView struct {
	domain.Payment
	UserName       string
	UserEmail      string
	WhatsAppNumber string
	MerchantName   string
	MerchantSlug   string
	LastInboundAt  time.Time
}

// WebhookView is a sanitized operational record.
type WebhookView struct {
	ID               int64
	Provider         string
	EventKey         string
	SignatureValid   bool
	ProcessingStatus string
	ErrorMessage     string
	ReceivedAt       time.Time
	ProcessedAt      *time.Time
}

// Metrics summarizes investor-demo activity.
type Metrics struct {
	Users           int64
	Payments        int64
	Succeeded       int64
	Failed          int64
	Pending         int64
	VolumeKobo      int64
	SuccessRate     float64
	WebhookFailures int64
}

// OutboxMessage is one durable outbound WhatsApp operation.
type OutboxMessage struct {
	ID        int64
	Recipient string
	Kind      string
	Payload   json.RawMessage
	Attempts  int
}

// OutboxSpec describes an outbound message inserted in a payment transaction.
type OutboxSpec struct {
	UserID    uuid.UUID
	Recipient string
	Kind      string
	Payload   json.RawMessage
}

// InboundMessage is one durable normalized WhatsApp message.
type InboundMessage struct {
	ID          string
	Sender      string
	Text        string
	Interactive string
	Attempts    int
}

// GatewayEvent is one durable normalized Paystack webhook.
type GatewayEvent struct {
	ID        int64  `json:"-"`
	Event     string `json:"event"`
	Reference string `json:"reference"`
	Attempts  int    `json:"-"`
}

// BankTransferAccount is a demo collection account shown to customers.
type BankTransferAccount struct {
	ID            uuid.UUID
	BankName      string
	AccountName   string
	AccountNumber string
	Active        bool
	CreatedAt     time.Time
}

// BankTransferInstruction contains the bank details and simulated proof handle.
type BankTransferInstruction struct {
	PaymentID          uuid.UUID
	BankAccountID      uuid.UUID
	BankName           string
	AccountName        string
	AccountNumber      string
	SimulatedReference string
	Status             string
	CreatedAt          time.Time
}

// Open establishes a PostgreSQL connection pool.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases database connections.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping checks database readiness.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Migrate applies embedded SQL migrations in filename order.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create migration registry: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, entry.Name()).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if applied {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, entry.Name()); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// Seed refreshes the fictional merchant fixtures.
func (s *Store) Seed(ctx context.Context) error {
	body, err := migrationFiles.ReadFile("migrations/002_seed_merchants.sql")
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, string(body))
	return err
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
		RETURNING id, whatsapp_number, display_name, email, onboarding_complete,
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level,
			last_inbound_at, created_at, updated_at`
	var user User
	err := s.pool.QueryRow(ctx, query, number).Scan(
		&user.ID, &user.WhatsAppNumber, &user.DisplayName, &user.Email,
		&user.OnboardingComplete, &user.WhatsAppVerifiedAt, &user.NumberConfirmedAt,
		&user.EmailVerifiedAt, &user.VerificationLevel, &user.LastInboundAt,
		&user.CreatedAt, &user.UpdatedAt,
	)
	return user, err
}

// UpdateUserName stores the first onboarding field.
func (s *Store) UpdateUserName(ctx context.Context, id uuid.UUID, name string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET display_name=$2, updated_at=now() WHERE id=$1`, id, name)
	return err
}

// UpdateUserEmail stores the Paystack checkout and receipt email without
// treating it as verified. Email verification can be added later as a separate
// proof, but this MVP only confirms the WhatsApp number.
func (s *Store) UpdateUserEmail(ctx context.Context, id uuid.UUID, email string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET email=$2, updated_at=now() WHERE id=$1`, id, email)
	return err
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

// ListUsers returns recent customers for the read-only dashboard.
func (s *Store) ListUsers(ctx context.Context, limit int) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, whatsapp_number, display_name, email, onboarding_complete,
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level,
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
			&user.EmailVerifiedAt, &user.VerificationLevel, &user.LastInboundAt,
			&user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// ListActiveMerchants returns curated recipients shown in WhatsApp.
func (s *Store) ListActiveMerchants(ctx context.Context) ([]Merchant, error) {
	return s.listMerchants(ctx, true)
}

// ListMerchants returns all recipients for the operations dashboard.
func (s *Store) ListMerchants(ctx context.Context) ([]Merchant, error) {
	return s.listMerchants(ctx, false)
}

func (s *Store) listMerchants(ctx context.Context, activeOnly bool) ([]Merchant, error) {
	query := `SELECT id, slug, name, category, description, logo_url, active, created_at FROM merchants`
	if activeOnly {
		query += ` WHERE active=true`
	}
	query += ` ORDER BY name`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var merchants []Merchant
	for rows.Next() {
		var merchant Merchant
		if err := rows.Scan(&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category, &merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.CreatedAt); err != nil {
			return nil, err
		}
		merchants = append(merchants, merchant)
	}
	return merchants, rows.Err()
}

// MerchantBySlug resolves an active curated merchant.
func (s *Store) MerchantBySlug(ctx context.Context, slug string) (Merchant, error) {
	var merchant Merchant
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, category, description, logo_url, active, created_at
		FROM merchants WHERE slug=$1 AND active=true`, slug).Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.CreatedAt,
	)
	return merchant, err
}

// ListActiveBankTransferAccounts returns demo collection banks customers can choose.
func (s *Store) ListActiveBankTransferAccounts(ctx context.Context) ([]BankTransferAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, bank_name, account_name, account_number, active, created_at
		FROM bank_transfer_accounts
		WHERE active=true
		ORDER BY bank_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []BankTransferAccount
	for rows.Next() {
		var account BankTransferAccount
		if err := rows.Scan(&account.ID, &account.BankName, &account.AccountName, &account.AccountNumber, &account.Active, &account.CreatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// BankTransferAccountByID resolves one active demo collection account.
func (s *Store) BankTransferAccountByID(ctx context.Context, id uuid.UUID) (BankTransferAccount, error) {
	var account BankTransferAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, bank_name, account_name, account_number, active, created_at
		FROM bank_transfer_accounts
		WHERE id=$1 AND active=true`, id).Scan(
		&account.ID, &account.BankName, &account.AccountName, &account.AccountNumber, &account.Active, &account.CreatedAt,
	)
	return account, err
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

// EnqueueInboundMessage persists a normalized WhatsApp message before webhook acknowledgement.
func (s *Store) EnqueueInboundMessage(ctx context.Context, message InboundMessage) (bool, error) {
	payload, err := json.Marshal(map[string]string{"text": message.Text, "interactive": message.Interactive})
	if err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO inbound_messages (provider_message_id, sender, payload)
		VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, message.ID, message.Sender, payload)
	return tag.RowsAffected() == 1, err
}

// ClaimInboundMessages leases pending messages to one worker.
func (s *Store) ClaimInboundMessages(ctx context.Context, limit int) ([]InboundMessage, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT provider_message_id,sender,payload,attempts
		FROM inbound_messages
		WHERE status IN ('pending','processing') AND available_at <= now()
		ORDER BY received_at
		FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var messages []InboundMessage
	for rows.Next() {
		var message InboundMessage
		var payload []byte
		if err := rows.Scan(&message.ID, &message.Sender, &payload, &message.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		var normalized map[string]string
		if err := json.Unmarshal(payload, &normalized); err != nil {
			rows.Close()
			return nil, err
		}
		message.Text = normalized["text"]
		message.Interactive = normalized["interactive"]
		messages = append(messages, message)
	}
	rows.Close()
	for _, message := range messages {
		if _, err := tx.Exec(ctx, `
			UPDATE inbound_messages
			SET status='processing',attempts=attempts+1,available_at=now()+interval '5 minutes'
			WHERE provider_message_id=$1`, message.ID); err != nil {
			return nil, err
		}
	}
	return messages, tx.Commit(ctx)
}

// CompleteInboundMessage marks a normalized WhatsApp message processed.
func (s *Store) CompleteInboundMessage(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE inbound_messages SET status='processed',processed_at=now() WHERE provider_message_id=$1`, id)
	return err
}

// RetryInboundMessage schedules bounded retry for a failed conversation operation.
func (s *Store) RetryInboundMessage(ctx context.Context, id string, attempts int, message string) error {
	nextAttempts := attempts + 1
	status := "pending"
	if nextAttempts >= 5 {
		status = "failed"
	}
	delay := time.Duration(1<<min(nextAttempts, 6)) * time.Minute
	_, err := s.pool.Exec(ctx, `
		UPDATE inbound_messages SET status=$2,last_error=$3,available_at=$4
		WHERE provider_message_id=$1`, id, status, truncate(message, 500), time.Now().Add(delay))
	return err
}

// CreatePayment stores a draft payment and its first audit event.
func (s *Store) CreatePayment(ctx context.Context, payment domain.Payment) (domain.Payment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Payment{}, err
	}
	defer tx.Rollback(ctx)
	const insert = `
		INSERT INTO payments
			(id,user_id,merchant_id,amount_kobo,currency,status,provider,provider_reference,receipt_token)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING created_at, updated_at`
	err = tx.QueryRow(ctx, insert,
		payment.ID, payment.UserID, payment.MerchantID, payment.AmountKobo, payment.Currency,
		payment.Status, payment.Provider, payment.ProviderReference, payment.ReceiptToken,
	).Scan(&payment.CreatedAt, &payment.UpdatedAt)
	if err != nil {
		return domain.Payment{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source)
		VALUES($1,'',$2,'conversation')`, payment.ID, payment.Status); err != nil {
		return domain.Payment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Payment{}, err
	}
	return payment, nil
}

// TransitionPayment atomically enforces payment state monotonicity and records an audit event.
func (s *Store) TransitionPayment(ctx context.Context, paymentID uuid.UUID, to domain.PaymentStatus, source string, detail map[string]any) (bool, error) {
	return s.transitionPayment(ctx, paymentID, to, source, detail, nil)
}

// TransitionPaymentWithOutbox atomically changes payment state and queues its customer notification.
func (s *Store) TransitionPaymentWithOutbox(ctx context.Context, paymentID uuid.UUID, to domain.PaymentStatus, source string, detail map[string]any, outbox OutboxSpec) (bool, error) {
	return s.transitionPayment(ctx, paymentID, to, source, detail, &outbox)
}

func (s *Store) transitionPayment(ctx context.Context, paymentID uuid.UUID, to domain.PaymentStatus, source string, detail map[string]any, outbox *OutboxSpec) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var from domain.PaymentStatus
	if err := tx.QueryRow(ctx, `SELECT status FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(&from); err != nil {
		return false, err
	}
	if from == to {
		return false, tx.Commit(ctx)
	}
	if !domain.CanTransition(from, to) {
		return false, fmt.Errorf("invalid payment transition %s -> %s", from, to)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return false, err
	}
	paidClause := ""
	if to == domain.StatusSucceeded {
		paidClause = ", paid_at=COALESCE(paid_at, now())"
	}
	if _, err := tx.Exec(ctx, `UPDATE payments SET status=$2, updated_at=now()`+paidClause+` WHERE id=$1`, paymentID, to); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source,detail)
		VALUES($1,$2,$3,$4,$5)`, paymentID, from, to, source, raw); err != nil {
		return false, err
	}
	if outbox != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO message_outbox(user_id,recipient,kind,payload)
			VALUES($1,$2,$3,$4)`, outbox.UserID, outbox.Recipient, outbox.Kind, outbox.Payload); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

// SetCheckout stores the hosted URL and advances a confirmed payment to initialized.
func (s *Store) SetCheckout(ctx context.Context, paymentID uuid.UUID, checkoutURL string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var from domain.PaymentStatus
	if err := tx.QueryRow(ctx, `SELECT status FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(&from); err != nil {
		return err
	}
	if from != domain.StatusAwaitingConfirmation {
		return fmt.Errorf("cannot initialize payment in %s", from)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payments SET checkout_url=$2,status=$3,updated_at=now() WHERE id=$1`,
		paymentID, checkoutURL, domain.StatusInitialized); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source)
		VALUES($1,$2,$3,'paystack.initialize')`, paymentID, from, domain.StatusInitialized); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InitializeBankTransferSimulation creates transfer instructions and moves the
// payment into pending. This simulates a bank rail waiting for customer action.
func (s *Store) InitializeBankTransferSimulation(ctx context.Context, paymentID, bankAccountID uuid.UUID, simulatedReference string) (BankTransferInstruction, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BankTransferInstruction{}, err
	}
	defer tx.Rollback(ctx)

	var from domain.PaymentStatus
	var provider string
	if err := tx.QueryRow(ctx, `SELECT status, provider FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(&from, &provider); err != nil {
		return BankTransferInstruction{}, err
	}
	if provider != "bank_transfer" {
		return BankTransferInstruction{}, fmt.Errorf("payment provider %q cannot use bank transfer simulation", provider)
	}
	if from != domain.StatusAwaitingConfirmation {
		return BankTransferInstruction{}, fmt.Errorf("cannot initialize bank transfer in %s", from)
	}

	var instruction BankTransferInstruction
	err = tx.QueryRow(ctx, `
		INSERT INTO bank_transfer_simulations(payment_id, bank_account_id, simulated_reference)
		VALUES($1,$2,$3)
		ON CONFLICT(payment_id) DO UPDATE
		SET bank_account_id=EXCLUDED.bank_account_id,
			simulated_reference=EXCLUDED.simulated_reference,
			updated_at=now()
		RETURNING payment_id, bank_account_id, simulated_reference, status, created_at`,
		paymentID, bankAccountID, simulatedReference,
	).Scan(&instruction.PaymentID, &instruction.BankAccountID, &instruction.SimulatedReference, &instruction.Status, &instruction.CreatedAt)
	if err != nil {
		return BankTransferInstruction{}, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT bank_name, account_name, account_number
		FROM bank_transfer_accounts
		WHERE id=$1 AND active=true`, bankAccountID).Scan(&instruction.BankName, &instruction.AccountName, &instruction.AccountNumber); err != nil {
		return BankTransferInstruction{}, err
	}

	if _, err := tx.Exec(ctx, `UPDATE payments SET status=$2, updated_at=now() WHERE id=$1`, paymentID, domain.StatusPending); err != nil {
		return BankTransferInstruction{}, err
	}
	detail, err := json.Marshal(map[string]any{
		"bank_name":           instruction.BankName,
		"account_number":      instruction.AccountNumber,
		"simulated_reference": simulatedReference,
	})
	if err != nil {
		return BankTransferInstruction{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source,detail)
		VALUES($1,$2,$3,'bank_transfer.instructions',$4)`, paymentID, from, domain.StatusInitialized, detail); err != nil {
		return BankTransferInstruction{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source,detail)
		VALUES($1,$2,$3,'bank_transfer.awaiting_user_transfer',$4)`, paymentID, domain.StatusInitialized, domain.StatusPending, detail); err != nil {
		return BankTransferInstruction{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BankTransferInstruction{}, err
	}
	return instruction, nil
}

// BankTransferInstructionByPaymentID reloads transfer instructions for retries
// or customer reminders in the WhatsApp conversation.
func (s *Store) BankTransferInstructionByPaymentID(ctx context.Context, paymentID uuid.UUID) (BankTransferInstruction, error) {
	var instruction BankTransferInstruction
	err := s.pool.QueryRow(ctx, `
		SELECT bts.payment_id, bts.bank_account_id, bta.bank_name, bta.account_name,
		       bta.account_number, bts.simulated_reference, bts.status, bts.created_at
		FROM bank_transfer_simulations bts
		JOIN bank_transfer_accounts bta ON bta.id=bts.bank_account_id
		WHERE bts.payment_id=$1`, paymentID).Scan(
		&instruction.PaymentID, &instruction.BankAccountID, &instruction.BankName,
		&instruction.AccountName, &instruction.AccountNumber, &instruction.SimulatedReference,
		&instruction.Status, &instruction.CreatedAt,
	)
	return instruction, err
}

// ConfirmBankTransferSimulation marks a pending simulated transfer successful
// after the customer taps "I have transferred" in WhatsApp.
func (s *Store) ConfirmBankTransferSimulation(ctx context.Context, paymentID uuid.UUID, outbox OutboxSpec) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var from domain.PaymentStatus
	var provider string
	if err := tx.QueryRow(ctx, `SELECT status, provider FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(&from, &provider); err != nil {
		return false, err
	}
	if provider != "bank_transfer" {
		return false, fmt.Errorf("payment provider %q cannot confirm bank transfer simulation", provider)
	}
	if from == domain.StatusSucceeded {
		return false, tx.Commit(ctx)
	}
	if !domain.CanTransition(from, domain.StatusSucceeded) {
		return false, fmt.Errorf("invalid payment transition %s -> %s", from, domain.StatusSucceeded)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bank_transfer_simulations
		SET status='user_confirmed', confirmed_at=COALESCE(confirmed_at, now()), updated_at=now()
		WHERE payment_id=$1`, paymentID); err != nil {
		return false, err
	}
	detail, err := json.Marshal(map[string]any{"simulation": true, "confirmation": "user_tapped_i_have_transferred"})
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE payments SET status=$2, paid_at=COALESCE(paid_at, now()), updated_at=now() WHERE id=$1`, paymentID, domain.StatusSucceeded); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source,detail)
		VALUES($1,$2,$3,'bank_transfer.user_confirmation',$4)`, paymentID, from, domain.StatusSucceeded, detail); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO message_outbox(user_id,recipient,kind,payload)
		VALUES($1,$2,$3,$4)`, outbox.UserID, outbox.Recipient, outbox.Kind, outbox.Payload); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// PaymentByID returns one payment with display fields.
func (s *Store) PaymentByID(ctx context.Context, id uuid.UUID) (PaymentView, error) {
	return s.paymentBy(ctx, "p.id=$1", id)
}

// PaymentByReference returns one payment with display fields.
func (s *Store) PaymentByReference(ctx context.Context, reference string) (PaymentView, error) {
	return s.paymentBy(ctx, "p.provider_reference=$1", reference)
}

// PaymentByReceiptToken resolves a non-guessable public receipt.
func (s *Store) PaymentByReceiptToken(ctx context.Context, token string) (PaymentView, error) {
	return s.paymentBy(ctx, "p.receipt_token=$1", token)
}

func (s *Store) paymentBy(ctx context.Context, predicate string, value any) (PaymentView, error) {
	query := `
		SELECT p.id,p.user_id,p.merchant_id,p.amount_kobo,p.currency,p.status,p.provider,
		       p.provider_reference,p.checkout_url,p.receipt_token,p.failure_reason,
		       p.created_at,p.updated_at,p.paid_at,
		       u.display_name,u.email,u.whatsapp_number,m.name,m.slug,u.last_inbound_at
		FROM payments p
		JOIN users u ON u.id=p.user_id
		JOIN merchants m ON m.id=p.merchant_id
		WHERE ` + predicate
	var view PaymentView
	err := s.pool.QueryRow(ctx, query, value).Scan(
		&view.ID, &view.UserID, &view.MerchantID, &view.AmountKobo, &view.Currency,
		&view.Status, &view.Provider, &view.ProviderReference, &view.CheckoutURL,
		&view.ReceiptToken, &view.FailureReason, &view.CreatedAt, &view.UpdatedAt, &view.PaidAt,
		&view.UserName, &view.UserEmail, &view.WhatsAppNumber, &view.MerchantName, &view.MerchantSlug, &view.LastInboundAt,
	)
	return view, err
}

// RecentPaymentsForUser returns customer-visible history.
func (s *Store) RecentPaymentsForUser(ctx context.Context, userID uuid.UUID, limit int) ([]PaymentView, error) {
	return s.listPayments(ctx, `WHERE p.user_id=$1 ORDER BY p.created_at DESC LIMIT $2`, userID, limit)
}

// ListPayments returns recent attempts for the dashboard.
func (s *Store) ListPayments(ctx context.Context, limit int) ([]PaymentView, error) {
	return s.listPayments(ctx, `ORDER BY p.created_at DESC LIMIT $1`, limit)
}

func (s *Store) listPayments(ctx context.Context, suffix string, args ...any) ([]PaymentView, error) {
	query := `
		SELECT p.id,p.user_id,p.merchant_id,p.amount_kobo,p.currency,p.status,p.provider,
		       p.provider_reference,p.checkout_url,p.receipt_token,p.failure_reason,
		       p.created_at,p.updated_at,p.paid_at,
		       u.display_name,u.email,u.whatsapp_number,m.name,m.slug,u.last_inbound_at
		FROM payments p
		JOIN users u ON u.id=p.user_id
		JOIN merchants m ON m.id=p.merchant_id ` + suffix
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payments []PaymentView
	for rows.Next() {
		var view PaymentView
		if err := rows.Scan(
			&view.ID, &view.UserID, &view.MerchantID, &view.AmountKobo, &view.Currency,
			&view.Status, &view.Provider, &view.ProviderReference, &view.CheckoutURL,
			&view.ReceiptToken, &view.FailureReason, &view.CreatedAt, &view.UpdatedAt, &view.PaidAt,
			&view.UserName, &view.UserEmail, &view.WhatsAppNumber, &view.MerchantName, &view.MerchantSlug, &view.LastInboundAt,
		); err != nil {
			return nil, err
		}
		payments = append(payments, view)
	}
	return payments, rows.Err()
}

// UnresolvedPayments returns initialized attempts that need provider reconciliation.
func (s *Store) UnresolvedPayments(ctx context.Context, olderThan time.Time, limit int) ([]PaymentView, error) {
	return s.listPayments(ctx, `
		WHERE p.provider='paystack' AND p.status IN ('initialized','pending') AND p.updated_at < $1
		ORDER BY p.updated_at LIMIT $2`, olderThan, limit)
}

// ExpirablePayments returns stale pre-checkout attempts whose lifecycle can safely end.
func (s *Store) ExpirablePayments(ctx context.Context, customerCutoff time.Time, limit int) ([]PaymentView, error) {
	return s.listPayments(ctx, `
		WHERE p.status IN ('draft','awaiting_confirmation') AND p.updated_at < $1
		ORDER BY p.updated_at LIMIT $2`, customerCutoff, limit)
}

// RecordWebhook deduplicates external events before processing.
func (s *Store) RecordWebhook(ctx context.Context, provider, eventKey string, valid bool, payload json.RawMessage) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO webhook_deliveries(provider,event_key,signature_valid,payload)
		VALUES($1,$2,$3,$4)
		ON CONFLICT(provider,event_key) DO NOTHING
		RETURNING id`, provider, eventKey, valid, payload).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if lookupErr := s.pool.QueryRow(ctx, `
			SELECT id FROM webhook_deliveries WHERE provider=$1 AND event_key=$2`, provider, eventKey).Scan(&id); lookupErr != nil {
			return 0, false, lookupErr
		}
		return id, false, nil
	}
	return id, err == nil, err
}

// CompleteWebhook records the processing outcome for operators.
func (s *Store) CompleteWebhook(ctx context.Context, id int64, processingStatus, errorMessage string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET processing_status=$2,error_message=$3,processed_at=now()
		WHERE id=$1`, id, processingStatus, errorMessage)
	return err
}

// ClaimPaystackWebhooks leases normalized charge events for verification.
func (s *Store) ClaimPaystackWebhooks(ctx context.Context, limit int) ([]GatewayEvent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id,payload,attempts
		FROM webhook_deliveries
		WHERE provider='paystack' AND processing_status IN ('received','processing')
		  AND signature_valid=true AND available_at <= now()
		ORDER BY received_at
		FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var events []GatewayEvent
	for rows.Next() {
		var event GatewayEvent
		var payload []byte
		if err := rows.Scan(&event.ID, &payload, &event.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, event)
	}
	rows.Close()
	for _, event := range events {
		if _, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries
			SET processing_status='processing',attempts=attempts+1,available_at=now()+interval '5 minutes'
			WHERE id=$1`, event.ID); err != nil {
			return nil, err
		}
	}
	return events, tx.Commit(ctx)
}

// RetryWebhook schedules bounded retry for a provider verification failure.
func (s *Store) RetryWebhook(ctx context.Context, id int64, attempts int, message string) error {
	nextAttempts := attempts + 1
	status := "received"
	if nextAttempts >= 5 {
		status = "failed"
	}
	delay := time.Duration(1<<min(nextAttempts, 6)) * time.Minute
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET processing_status=$2,error_message=$3,available_at=$4
		WHERE id=$1`, id, status, truncate(message, 500), time.Now().Add(delay))
	return err
}

// ListWebhooks returns recent provider deliveries.
func (s *Store) ListWebhooks(ctx context.Context, limit int) ([]WebhookView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,provider,event_key,signature_valid,processing_status,error_message,received_at,processed_at
		FROM webhook_deliveries ORDER BY received_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deliveries []WebhookView
	for rows.Next() {
		var delivery WebhookView
		if err := rows.Scan(&delivery.ID, &delivery.Provider, &delivery.EventKey, &delivery.SignatureValid, &delivery.ProcessingStatus, &delivery.ErrorMessage, &delivery.ReceivedAt, &delivery.ProcessedAt); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

// EnqueueText adds a durable outbound text notification.
func (s *Store) EnqueueText(ctx context.Context, userID uuid.UUID, recipient, body string) error {
	payload, _ := json.Marshal(map[string]any{"body": body})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO message_outbox(user_id,recipient,kind,payload)
		VALUES($1,$2,'text',$3)`, userID, recipient, payload)
	return err
}

// EnqueueTemplate adds a durable template notification for use outside the service window.
func (s *Store) EnqueueTemplate(ctx context.Context, userID uuid.UUID, recipient, name, locale string, parameters []string) error {
	payload, _ := json.Marshal(map[string]any{"name": name, "locale": locale, "parameters": parameters})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO message_outbox(user_id,recipient,kind,payload)
		VALUES($1,$2,'template',$3)`, userID, recipient, payload)
	return err
}

// ClaimOutbox atomically leases pending messages to one worker.
func (s *Store) ClaimOutbox(ctx context.Context, limit int) ([]OutboxMessage, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id,recipient,kind,payload,attempts
		FROM message_outbox
		WHERE status IN ('pending','sending') AND available_at <= now()
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var messages []OutboxMessage
	for rows.Next() {
		var message OutboxMessage
		if err := rows.Scan(&message.ID, &message.Recipient, &message.Kind, &message.Payload, &message.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		messages = append(messages, message)
	}
	rows.Close()
	for _, message := range messages {
		if _, err := tx.Exec(ctx, `
			UPDATE message_outbox SET status='sending',available_at=now()+interval '5 minutes' WHERE id=$1`, message.ID); err != nil {
			return nil, err
		}
	}
	return messages, tx.Commit(ctx)
}

// CompleteOutbox marks a message delivered.
func (s *Store) CompleteOutbox(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE message_outbox SET status='sent',sent_at=now(),attempts=attempts+1 WHERE id=$1`, id)
	return err
}

// RetryOutbox schedules a bounded exponential retry or permanently fails the message.
func (s *Store) RetryOutbox(ctx context.Context, id int64, attempts int, message string) error {
	nextAttempts := attempts + 1
	status := "pending"
	if nextAttempts >= 5 {
		status = "failed"
	}
	delay := time.Duration(1<<min(nextAttempts, 6)) * time.Minute
	_, err := s.pool.Exec(ctx, `
		UPDATE message_outbox
		SET status=$2,attempts=$3,last_error=$4,available_at=$5
		WHERE id=$1`, id, status, nextAttempts, truncate(message, 500), time.Now().Add(delay))
	return err
}

// CreateAdminSession persists only a hash of the bearer token.
func (s *Store) CreateAdminSession(ctx context.Context, token, csrf string, expiresAt time.Time) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO admin_sessions(token_hash,csrf_token,expires_at) VALUES($1,$2,$3)`,
		hash[:], csrf, expiresAt)
	return err
}

// ValidateAdminSession resolves a session and its CSRF token.
func (s *Store) ValidateAdminSession(ctx context.Context, token string) (string, error) {
	hash := sha256.Sum256([]byte(token))
	var csrf string
	err := s.pool.QueryRow(ctx, `
		SELECT csrf_token FROM admin_sessions WHERE token_hash=$1 AND expires_at > now()`, hash[:]).Scan(&csrf)
	return csrf, err
}

// DeleteAdminSession invalidates one login.
func (s *Store) DeleteAdminSession(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx, `DELETE FROM admin_sessions WHERE token_hash=$1`, hash[:])
	return err
}

// Metrics returns a compact operational summary.
func (s *Store) Metrics(ctx context.Context) (Metrics, error) {
	var metrics Metrics
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM users),
			count(*),
			count(*) FILTER (WHERE status='succeeded'),
			count(*) FILTER (WHERE status='failed'),
			count(*) FILTER (WHERE status IN ('draft','awaiting_confirmation','initialized','pending')),
			COALESCE(sum(amount_kobo) FILTER (WHERE status='succeeded'),0),
			(SELECT count(*) FROM webhook_deliveries WHERE processing_status='failed')
		FROM payments`).Scan(
		&metrics.Users, &metrics.Payments, &metrics.Succeeded, &metrics.Failed,
		&metrics.Pending, &metrics.VolumeKobo, &metrics.WebhookFailures,
	)
	if metrics.Payments > 0 {
		metrics.SuccessRate = float64(metrics.Succeeded) / float64(metrics.Payments) * 100
	}
	return metrics, err
}

// PurgeBefore removes demo personal data and operational records older than the cutoff.
func (s *Store) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM admin_sessions WHERE expires_at < now()`); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM webhook_deliveries WHERE received_at < $1`, cutoff); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM inbound_messages WHERE received_at < $1`, cutoff); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM conversation_sessions WHERE expires_at < now() OR updated_at < $1`, cutoff); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM message_outbox WHERE created_at < $1`, cutoff); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM payments WHERE created_at < $1`, cutoff); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM users u
		WHERE u.updated_at < $1
		  AND NOT EXISTS (SELECT 1 FROM payments p WHERE p.user_id=u.id)`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
