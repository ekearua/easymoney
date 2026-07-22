// Package store provides PostgreSQL persistence and transactional payment operations.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
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

// Merchant is a curated payment recipient.
type Merchant struct {
	ID             uuid.UUID
	Slug           string
	Name           string
	Category       string
	Description    string
	LogoURL        string
	Active         bool
	SearchKeywords string
	SortOrder      int
	CreatedAt      time.Time
}

// MerchantRegistration is a chat-submitted merchant onboarding request.
type MerchantRegistration struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	UserName     string
	UserEmail    string
	Reference    string
	BusinessName string
	Category     string
	Description  string
	ContactEmail string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// InvoiceItem is one line on a merchant-generated invoice.
type InvoiceItem struct {
	ID            int64
	Description   string
	Quantity      int
	UnitPriceKobo int64
	LineTotalKobo int64
	SortOrder     int
}

// InvoiceView joins invoice data with merchant and creator display fields.
type InvoiceView struct {
	ID                     uuid.UUID
	MerchantID             uuid.UUID
	CreatedByUserID        uuid.UUID
	CustomerWhatsAppNumber string
	CustomerEmail          string
	Reference              string
	Status                 string
	DeliveryFeeKobo        int64
	SubtotalKobo           int64
	TotalKobo              int64
	AmountPaidKobo         int64
	DueAt                  *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
	PaidAt                 *time.Time
	MerchantName           string
	MerchantSlug           string
	MerchantCategory       string
	CreatorName            string
	CreatorEmail           string
	Items                  []InvoiceItem
}

// InvoicePaymentView links one payment receipt to one invoice contribution.
type InvoicePaymentView struct {
	ID          uuid.UUID
	InvoiceID   uuid.UUID
	PaymentID   uuid.UUID
	PayerUserID uuid.UUID
	AmountKobo  int64
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// InvoiceSpec contains validated invoice details collected in chat.
type InvoiceSpec struct {
	MerchantID             uuid.UUID
	CreatedByUserID        uuid.UUID
	CustomerWhatsAppNumber string
	CustomerEmail          string
	DeliveryFeeKobo        int64
	DueAt                  *time.Time
	Items                  []InvoiceItem
}

// IndividualProfile is the demo KYC profile that unlocks individual-only features.
type IndividualProfile struct {
	UserID      uuid.UUID
	LegalName   string
	DateOfBirth time.Time
	Address     string
	Occupation  string
	KYCStatus   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ThriftGroupView summarizes one rotational thrift group.
type ThriftGroupView struct {
	ID                     uuid.UUID
	CreatorUserID          uuid.UUID
	Name                   string
	ContributionAmountKobo int64
	Frequency              string
	TargetMemberCount      int
	InviteCode             string
	Status                 string
	CurrentCycle           int
	CreatedAt              time.Time
	UpdatedAt              time.Time
	ActivatedAt            *time.Time
	CompletedAt            *time.Time
	CreatorName            string
	MemberCount            int
}

// ThriftMemberView joins thrift membership with user display fields.
type ThriftMemberView struct {
	ID             uuid.UUID
	GroupID        uuid.UUID
	UserID         uuid.UUID
	UserName       string
	UserEmail      string
	WhatsAppNumber string
	Status         string
	PayoutPosition sql.NullInt32
	JoinedAt       time.Time
	ConfirmedAt    time.Time
}

// ThriftCycleView is one contribution/payout cycle.
type ThriftCycleView struct {
	ID                     uuid.UUID
	GroupID                uuid.UUID
	GroupName              string
	CycleNumber            int
	DueAt                  time.Time
	PayoutMemberID         uuid.UUID
	PayoutMemberName       string
	Status                 string
	ContributionAmountKobo int64
	TargetMemberCount      int
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// ThriftContributionView links one member contribution to a payment receipt.
type ThriftContributionView struct {
	ID             uuid.UUID
	CycleID        uuid.UUID
	GroupID        uuid.UUID
	GroupName      string
	CycleNumber    int
	MemberID       uuid.UUID
	UserID         uuid.UUID
	MemberName     string
	PaymentID      uuid.NullUUID
	AmountKobo     int64
	Status         string
	PaymentStatus  domain.PaymentStatus
	PaymentReceipt string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	PaidAt         *time.Time
}

// ThriftPayoutView is one simulated thrift payout obligation.
type ThriftPayoutView struct {
	ID               uuid.UUID
	CycleID          uuid.UUID
	GroupID          uuid.UUID
	GroupName        string
	CycleNumber      int
	PayoutMemberID   uuid.UUID
	PayoutMemberName string
	AmountKobo       int64
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      *time.Time
}

// RegisteredServiceView is a merchant or Xego-owned service allowed to scan receipts.
type RegisteredServiceView struct {
	ID                   uuid.UUID
	Name                 string
	ServiceType          string
	MerchantID           uuid.NullUUID
	MerchantName         string
	AcceptedReceiptTypes string
	TokenTTLSeconds      int
	Active               bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ServiceReaderView is an external reader credential bound to one service.
type ServiceReaderView struct {
	ID          uuid.UUID
	ServiceID   uuid.UUID
	ServiceName string
	Name        string
	KeyPrefix   string
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ReceiptScanTokenView is the QR/manual token customers present to a service.
type ReceiptScanTokenView struct {
	ID          uuid.UUID
	PaymentID   uuid.UUID
	ServiceID   uuid.UUID
	ServiceName string
	Token       string
	ManualCode  string
	ReceiptType string
	ExpiresAt   time.Time
	ConsumedAt  *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
}

// ReceiptScanAttemptView is one scanner validation attempt for audit.
type ReceiptScanAttemptView struct {
	ID          int64
	TokenID     uuid.NullUUID
	ServiceID   uuid.NullUUID
	ReaderID    uuid.NullUUID
	ServiceName string
	ReaderName  string
	Status      string
	RemoteAddr  string
	CreatedAt   time.Time
}

// ReceiptScanResult is the safe response returned to external readers.
type ReceiptScanResult struct {
	Status         string `json:"status"`
	ReceiptType    string `json:"receipt_type,omitempty"`
	ServiceName    string `json:"service_name,omitempty"`
	MerchantName   string `json:"merchant_name,omitempty"`
	Amount         string `json:"amount,omitempty"`
	AmountKobo     int64  `json:"amount_kobo,omitempty"`
	PaymentStatus  string `json:"payment_status,omitempty"`
	CustomerName   string `json:"customer_name,omitempty"`
	CustomerPhone  string `json:"customer_phone,omitempty"`
	ManualCode     string `json:"manual_code,omitempty"`
	ProviderRef    string `json:"provider_reference,omitempty"`
	ConsumedAtText string `json:"consumed_at,omitempty"`
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
	DataOrders      int64
	DataFulfilled   int64
	DataFailures    int64
	SMSRequests     int64
}

// OutboxMessage is one durable outbound WhatsApp operation.
type OutboxMessage struct {
	ID        int64
	Channel   string
	Recipient string
	Kind      string
	Payload   json.RawMessage
	Attempts  int
}

// OutboxSpec describes an outbound message inserted in a payment transaction.
type OutboxSpec struct {
	UserID    uuid.UUID
	Channel   string
	Recipient string
	Kind      string
	Payload   json.RawMessage
}

// InboundMessage is one durable normalized WhatsApp message.
type InboundMessage struct {
	ID          string
	Channel     string
	Sender      string
	Recipient   string
	Text        string
	Interactive string
	Username    string
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
	ID             uuid.UUID
	BankName       string
	AccountName    string
	AccountNumber  string
	Active         bool
	SearchKeywords string
	SortOrder      int
	CreatedAt      time.Time
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

// DataNetwork is a supported Nigerian mobile network.
type DataNetwork struct {
	ID        uuid.UUID
	Code      string
	Name      string
	Active    bool
	SortOrder int
	CreatedAt time.Time
}

// DataPlan is a sellable mobile data bundle.
type DataPlan struct {
	ID          uuid.UUID
	NetworkID   uuid.UUID
	NetworkCode string
	NetworkName string
	Code        string
	DisplayName string
	DataSize    string
	Validity    string
	PriceKobo   int64
	ProviderSKU string
	Active      bool
	SortOrder   int
	CreatedAt   time.Time
}

// DataOrderView joins a data order with network and plan display fields.
type DataOrderView struct {
	ID                 uuid.UUID
	UserID             uuid.UUID
	PaymentID          uuid.NullUUID
	Channel            string
	Recipient          string
	BeneficiaryPhone   string
	NetworkID          uuid.UUID
	NetworkCode        string
	NetworkName        string
	PlanID             uuid.UUID
	PlanCode           string
	PlanName           string
	DataSize           string
	Validity           string
	ProviderSKU        string
	AmountKobo         int64
	Status             domain.DataOrderStatus
	RequestCode        string
	Provider           string
	ProviderReference  string
	FailureReason      string
	UserName           string
	UserEmail          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	FulfilledAt        *time.Time
	PaymentStatus      domain.PaymentStatus
	PaymentReceipt     string
	PaymentReference   string
	PaymentProvider    string
	PaymentCheckoutURL string
}

// SMSRequestView is one inbound SMS command and the response generated by Xego.
type SMSRequestView struct {
	ID               string
	Sender           string
	Body             string
	Command          string
	RequestCode      string
	ProcessingStatus string
	ResponseBody     string
	ErrorMessage     string
	ReceivedAt       time.Time
	ProcessedAt      *time.Time
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

// Seed refreshes the baseline merchant fixtures.
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

// CreateEmailVerificationCode stores a hashed one-time confirmation code.
func (s *Store) CreateEmailVerificationCode(ctx context.Context, userID uuid.UUID, email string, codeHash []byte, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO email_verification_codes(user_id,email,code_hash,expires_at)
		VALUES($1,lower($2),$3,$4)`, userID, email, codeHash, expiresAt)
	return err
}

// VerifyEmailCode consumes the latest valid code when the submitted hash
// matches. Mismatches increment attempts so repeated guessing is bounded.
func (s *Store) VerifyEmailCode(ctx context.Context, userID uuid.UUID, email string, codeHash []byte) (bool, error) {
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
	if attempts >= 5 || !equalBytes(stored, codeHash) {
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

func equalBytes(left, right []byte) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare(left, right) == 1
}

func newMerchantRegistrationReference() string {
	return "XG-MER-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
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

// ListActiveMerchants returns curated payment recipients in their display order.
func (s *Store) ListActiveMerchants(ctx context.Context) ([]Merchant, error) {
	return s.listMerchants(ctx, true)
}

// ListMerchants returns all recipients for the operations dashboard.
func (s *Store) ListMerchants(ctx context.Context) ([]Merchant, error) {
	return s.listMerchants(ctx, false)
}

func (s *Store) listMerchants(ctx context.Context, activeOnly bool) ([]Merchant, error) {
	query := `SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at FROM merchants`
	if activeOnly {
		query += ` WHERE active=true`
	}
	query += ` ORDER BY sort_order, name`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var merchants []Merchant
	for rows.Next() {
		merchant, err := scanMerchant(rows)
		if err != nil {
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
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at
		FROM merchants WHERE slug=$1 AND active=true`, slug).Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords,
		&merchant.SortOrder, &merchant.CreatedAt,
	)
	return merchant, err
}

// SearchMerchants returns one customer-facing page of active merchants.
func (s *Store) SearchMerchants(ctx context.Context, query string, offset, limit int) ([]Merchant, bool, error) {
	offset, limit = normalizePageBounds(offset, limit)
	search := strings.ToLower(strings.TrimSpace(query))
	args := []any{limit + 1, offset}
	sql := `
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at
		FROM merchants
		WHERE active=true`
	if search != "" {
		args = append(args, "%"+search+"%")
		sql += ` AND (
			lower(name) LIKE $3 OR lower(category) LIKE $3 OR
			lower(description) LIKE $3 OR lower(search_keywords) LIKE $3
		)`
	}
	sql += ` ORDER BY sort_order, name LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	merchants, err := collectMerchants(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(merchants) > limit
	if hasMore {
		merchants = merchants[:limit]
	}
	return merchants, hasMore, nil
}

// SearchMerchantsExcludingUserRecents pages active merchants that are not already
// shown in the customer's recent-merchant shortcut section.
func (s *Store) SearchMerchantsExcludingUserRecents(ctx context.Context, userID uuid.UUID, query string, offset, limit int) ([]Merchant, bool, error) {
	offset, limit = normalizePageBounds(offset, limit)
	search := strings.ToLower(strings.TrimSpace(query))
	args := []any{limit + 1, offset, userID}
	sql := `
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at
		FROM merchants
		WHERE active=true
			AND NOT EXISTS (
				SELECT 1 FROM user_merchant_recents r
				WHERE r.user_id=$3 AND r.merchant_id=merchants.id
			)`
	if search != "" {
		args = append(args, "%"+search+"%")
		sql += ` AND (
			lower(name) LIKE $4 OR lower(category) LIKE $4 OR
			lower(description) LIKE $4 OR lower(search_keywords) LIKE $4
		)`
	}
	sql += ` ORDER BY sort_order, name LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	merchants, err := collectMerchants(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(merchants) > limit
	if hasMore {
		merchants = merchants[:limit]
	}
	return merchants, hasMore, nil
}

// RecentMerchantsForUser returns a customer's most recently selected merchants.
func (s *Store) RecentMerchantsForUser(ctx context.Context, userID uuid.UUID, limit int) ([]Merchant, error) {
	_, limit = normalizePageBounds(0, limit)
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.slug, m.name, m.category, m.description, m.logo_url, m.active,
			m.search_keywords, m.sort_order, m.created_at
		FROM user_merchant_recents r
		JOIN merchants m ON m.id=r.merchant_id
		WHERE r.user_id=$1 AND m.active=true
		ORDER BY r.last_selected_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectMerchants(rows)
}

// TouchRecentMerchant records a merchant selection for future shortcut lists.
func (s *Store) TouchRecentMerchant(ctx context.Context, userID, merchantID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_merchant_recents (user_id, merchant_id, last_selected_at)
		VALUES ($1,$2,now())
		ON CONFLICT (user_id, merchant_id) DO UPDATE
		SET last_selected_at=EXCLUDED.last_selected_at`, userID, merchantID)
	return err
}

// CreateMerchantRegistration stores a pending merchant request submitted from chat.
func (s *Store) CreateMerchantRegistration(ctx context.Context, userID uuid.UUID, businessName, category, description, contactEmail string) (MerchantRegistration, error) {
	for i := 0; i < 5; i++ {
		request := MerchantRegistration{ID: uuid.New(), UserID: userID, Reference: newMerchantRegistrationReference()}
		err := s.pool.QueryRow(ctx, `
			INSERT INTO merchant_registrations
				(id,user_id,reference,business_name,category,description,contact_email,status)
			VALUES($1,$2,$3,$4,$5,$6,lower($7),'awaiting_approval')
			RETURNING id,user_id,reference,business_name,category,description,contact_email,status,created_at,updated_at`,
			request.ID, userID, request.Reference, strings.TrimSpace(businessName), strings.TrimSpace(category), strings.TrimSpace(description), strings.TrimSpace(contactEmail),
		).Scan(&request.ID, &request.UserID, &request.Reference, &request.BusinessName, &request.Category, &request.Description, &request.ContactEmail, &request.Status, &request.CreatedAt, &request.UpdatedAt)
		if err == nil {
			_, _ = s.pool.Exec(ctx, `UPDATE users SET account_level='pending_merchant',updated_at=now() WHERE id=$1 AND account_level='customer'`, userID)
			return request, nil
		}
		if !strings.Contains(strings.ToLower(err.Error()), "merchant_registrations_reference") {
			return MerchantRegistration{}, err
		}
	}
	return MerchantRegistration{}, errors.New("could not allocate merchant registration reference")
}

// ListMerchantRegistrations returns recent merchant onboarding requests.
func (s *Store) ListMerchantRegistrations(ctx context.Context, limit int) ([]MerchantRegistration, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id,r.user_id,COALESCE(u.display_name,''),COALESCE(u.email,''),
		       r.reference,r.business_name,r.category,r.description,r.contact_email,r.status,r.created_at,r.updated_at
		FROM merchant_registrations r
		JOIN users u ON u.id=r.user_id
		ORDER BY r.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requests []MerchantRegistration
	for rows.Next() {
		var request MerchantRegistration
		if err := rows.Scan(&request.ID, &request.UserID, &request.UserName, &request.UserEmail, &request.Reference, &request.BusinessName, &request.Category, &request.Description, &request.ContactEmail, &request.Status, &request.CreatedAt, &request.UpdatedAt); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

// ApproveMerchantRegistration turns a reviewed request into an active payable
// merchant, links the requester as owner, and upgrades the user's account level.
func (s *Store) ApproveMerchantRegistration(ctx context.Context, registrationID uuid.UUID) (Merchant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Merchant{}, err
	}
	defer tx.Rollback(ctx)

	var request MerchantRegistration
	err = tx.QueryRow(ctx, `
		SELECT id,user_id,reference,business_name,category,description,contact_email,status,created_at,updated_at
		FROM merchant_registrations
		WHERE id=$1
		FOR UPDATE`, registrationID).Scan(
		&request.ID, &request.UserID, &request.Reference, &request.BusinessName, &request.Category,
		&request.Description, &request.ContactEmail, &request.Status, &request.CreatedAt, &request.UpdatedAt,
	)
	if err != nil {
		return Merchant{}, err
	}
	if request.Status != "approved" {
		if _, err := tx.Exec(ctx, `
			UPDATE merchant_registrations
			SET status='approved',updated_at=now()
			WHERE id=$1`, request.ID); err != nil {
			return Merchant{}, err
		}
	}

	slug, err := allocateMerchantSlug(ctx, tx, request.BusinessName)
	if err != nil {
		return Merchant{}, err
	}
	var merchant Merchant
	err = tx.QueryRow(ctx, `
		INSERT INTO merchants(slug,name,category,description,active,search_keywords,sort_order)
		VALUES($1,$2,$3,$4,true,$5,900)
		ON CONFLICT(slug) DO UPDATE
		SET name=EXCLUDED.name,
			category=EXCLUDED.category,
			description=EXCLUDED.description,
			active=true,
			search_keywords=EXCLUDED.search_keywords,
			updated_at=now()
		RETURNING id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at`,
		slug, request.BusinessName, request.Category, request.Description,
		strings.ToLower(request.BusinessName+" "+request.Category+" "+request.Description),
	).Scan(&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category, &merchant.Description,
		&merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords, &merchant.SortOrder, &merchant.CreatedAt)
	if err != nil {
		return Merchant{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO merchant_owners(merchant_id,user_id)
		VALUES($1,$2)
		ON CONFLICT DO NOTHING`, merchant.ID, request.UserID); err != nil {
		return Merchant{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users SET account_level='merchant',updated_at=now()
		WHERE id=$1`, request.UserID); err != nil {
		return Merchant{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO registered_services(name,service_type,merchant_id,accepted_receipt_types,token_ttl_seconds,active)
		VALUES($1,'merchant',$2,'merchant_payment,invoice',86400,true)
		ON CONFLICT(service_type, merchant_id) DO UPDATE
		SET name=EXCLUDED.name,
			accepted_receipt_types=EXCLUDED.accepted_receipt_types,
			active=true,
			updated_at=now()`, merchant.Name, merchant.ID); err != nil {
		return Merchant{}, err
	}
	return merchant, tx.Commit(ctx)
}

func allocateMerchantSlug(ctx context.Context, tx pgx.Tx, name string) (string, error) {
	base := slugify(name)
	if base == "" {
		base = "merchant"
	}
	for i := 0; i < 20; i++ {
		slug := base
		if i > 0 {
			slug = fmt.Sprintf("%s-%d", base, i+1)
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM merchants WHERE slug=$1)`, slug).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
	}
	return "", errors.New("could not allocate merchant slug")
}

func slugify(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// ApprovedMerchantsForUser returns active merchants owned by the user.
func (s *Store) ApprovedMerchantsForUser(ctx context.Context, userID uuid.UUID) ([]Merchant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.slug, m.name, m.category, m.description, m.logo_url, m.active,
		       m.search_keywords, m.sort_order, m.created_at
		FROM merchant_owners mo
		JOIN merchants m ON m.id=mo.merchant_id
		WHERE mo.user_id=$1 AND m.active=true
		ORDER BY m.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectMerchants(rows)
}

// CreateInvoice stores a merchant invoice and its line items atomically.
func (s *Store) CreateInvoice(ctx context.Context, spec InvoiceSpec) (InvoiceView, error) {
	if len(spec.Items) == 0 {
		return InvoiceView{}, errors.New("invoice requires at least one line item")
	}
	subtotal := int64(0)
	for i := range spec.Items {
		item := &spec.Items[i]
		if item.Quantity <= 0 || item.UnitPriceKobo <= 0 || strings.TrimSpace(item.Description) == "" {
			return InvoiceView{}, errors.New("invoice item is invalid")
		}
		item.LineTotalKobo = int64(item.Quantity) * item.UnitPriceKobo
		item.SortOrder = i + 1
		subtotal += item.LineTotalKobo
	}
	total := subtotal + spec.DeliveryFeeKobo
	if total <= 0 {
		return InvoiceView{}, errors.New("invoice total must be greater than zero")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InvoiceView{}, err
	}
	defer tx.Rollback(ctx)
	reference := newInvoiceReference()
	var invoice InvoiceView
	err = tx.QueryRow(ctx, `
		INSERT INTO invoices
			(id,merchant_id,created_by_user_id,customer_whatsapp_number,customer_email,reference,status,delivery_fee_kobo,subtotal_kobo,total_kobo,due_at)
		VALUES($1,$2,$3,$4,lower($5),$6,'sent',$7,$8,$9,$10)
		RETURNING id,merchant_id,created_by_user_id,customer_whatsapp_number,customer_email,reference,status,
		          delivery_fee_kobo,subtotal_kobo,total_kobo,amount_paid_kobo,due_at,created_at,updated_at,paid_at`,
		uuid.New(), spec.MerchantID, spec.CreatedByUserID, spec.CustomerWhatsAppNumber, spec.CustomerEmail,
		reference, spec.DeliveryFeeKobo, subtotal, total, spec.DueAt,
	).Scan(&invoice.ID, &invoice.MerchantID, &invoice.CreatedByUserID, &invoice.CustomerWhatsAppNumber,
		&invoice.CustomerEmail, &invoice.Reference, &invoice.Status, &invoice.DeliveryFeeKobo,
		&invoice.SubtotalKobo, &invoice.TotalKobo, &invoice.AmountPaidKobo, &invoice.DueAt,
		&invoice.CreatedAt, &invoice.UpdatedAt, &invoice.PaidAt)
	if err != nil {
		return InvoiceView{}, err
	}
	for _, item := range spec.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO invoice_items(invoice_id,description,quantity,unit_price_kobo,line_total_kobo,sort_order)
			VALUES($1,$2,$3,$4,$5,$6)`, invoice.ID, item.Description, item.Quantity, item.UnitPriceKobo, item.LineTotalKobo, item.SortOrder); err != nil {
			return InvoiceView{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return InvoiceView{}, err
	}
	return s.InvoiceByReference(ctx, reference)
}

func newInvoiceReference() string {
	return "XG-INV-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
}

// InvoiceByReference returns an invoice with merchant details and line items.
func (s *Store) InvoiceByReference(ctx context.Context, reference string) (InvoiceView, error) {
	var invoice InvoiceView
	err := s.pool.QueryRow(ctx, `
		SELECT i.id,i.merchant_id,i.created_by_user_id,i.customer_whatsapp_number,i.customer_email,
		       i.reference,i.status,i.delivery_fee_kobo,i.subtotal_kobo,i.total_kobo,i.amount_paid_kobo,
		       i.due_at,i.created_at,i.updated_at,i.paid_at,
		       m.name,m.slug,m.category,u.display_name,u.email
		FROM invoices i
		JOIN merchants m ON m.id=i.merchant_id
		JOIN users u ON u.id=i.created_by_user_id
		WHERE i.reference=$1`, strings.ToUpper(strings.TrimSpace(reference))).Scan(
		&invoice.ID, &invoice.MerchantID, &invoice.CreatedByUserID, &invoice.CustomerWhatsAppNumber,
		&invoice.CustomerEmail, &invoice.Reference, &invoice.Status, &invoice.DeliveryFeeKobo,
		&invoice.SubtotalKobo, &invoice.TotalKobo, &invoice.AmountPaidKobo, &invoice.DueAt,
		&invoice.CreatedAt, &invoice.UpdatedAt, &invoice.PaidAt, &invoice.MerchantName,
		&invoice.MerchantSlug, &invoice.MerchantCategory, &invoice.CreatorName, &invoice.CreatorEmail,
	)
	if err != nil {
		return InvoiceView{}, err
	}
	items, err := s.invoiceItems(ctx, invoice.ID)
	if err != nil {
		return InvoiceView{}, err
	}
	invoice.Items = items
	return invoice, nil
}

func (s *Store) invoiceItems(ctx context.Context, invoiceID uuid.UUID) ([]InvoiceItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,description,quantity,unit_price_kobo,line_total_kobo,sort_order
		FROM invoice_items
		WHERE invoice_id=$1
		ORDER BY sort_order,id`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []InvoiceItem
	for rows.Next() {
		var item InvoiceItem
		if err := rows.Scan(&item.ID, &item.Description, &item.Quantity, &item.UnitPriceKobo, &item.LineTotalKobo, &item.SortOrder); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RecentInvoicesForMerchantOwner returns a chat-sized dashboard for an approved merchant.
func (s *Store) RecentInvoicesForMerchantOwner(ctx context.Context, userID uuid.UUID, limit int) ([]InvoiceView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.id,i.merchant_id,i.created_by_user_id,i.customer_whatsapp_number,i.customer_email,
		       i.reference,i.status,i.delivery_fee_kobo,i.subtotal_kobo,i.total_kobo,i.amount_paid_kobo,
		       i.due_at,i.created_at,i.updated_at,i.paid_at,
		       m.name,m.slug,m.category,u.display_name,u.email
		FROM invoices i
		JOIN merchants m ON m.id=i.merchant_id
		JOIN merchant_owners mo ON mo.merchant_id=m.id
		JOIN users u ON u.id=i.created_by_user_id
		WHERE mo.user_id=$1
		ORDER BY i.created_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invoices []InvoiceView
	for rows.Next() {
		var invoice InvoiceView
		if err := rows.Scan(&invoice.ID, &invoice.MerchantID, &invoice.CreatedByUserID, &invoice.CustomerWhatsAppNumber,
			&invoice.CustomerEmail, &invoice.Reference, &invoice.Status, &invoice.DeliveryFeeKobo,
			&invoice.SubtotalKobo, &invoice.TotalKobo, &invoice.AmountPaidKobo, &invoice.DueAt,
			&invoice.CreatedAt, &invoice.UpdatedAt, &invoice.PaidAt, &invoice.MerchantName,
			&invoice.MerchantSlug, &invoice.MerchantCategory, &invoice.CreatorName, &invoice.CreatorEmail); err != nil {
			return nil, err
		}
		invoices = append(invoices, invoice)
	}
	return invoices, rows.Err()
}

// MerchantOwnerByRegistrationID returns the user who submitted a merchant registration.
func (s *Store) MerchantOwnerByRegistrationID(ctx context.Context, registrationID uuid.UUID) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id,u.whatsapp_number,u.display_name,u.email
		FROM merchant_registrations mr
		JOIN users u ON u.id=mr.user_id
		WHERE mr.id=$1`, registrationID).Scan(&u.ID, &u.WhatsAppNumber, &u.DisplayName, &u.Email)
	return u, err
}

// MerchantOwnerByInvoiceID returns the merchant owner who owns the invoice's merchant.
func (s *Store) MerchantOwnerByInvoiceID(ctx context.Context, invoiceID uuid.UUID) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id,u.whatsapp_number,u.display_name,u.email
		FROM invoices i
		JOIN merchant_owners mo ON mo.merchant_id=i.merchant_id
		JOIN users u ON u.id=mo.user_id
		WHERE i.id=$1
		ORDER BY mo.created_at ASC
		LIMIT 1`, invoiceID).Scan(&u.ID, &u.WhatsAppNumber, &u.DisplayName, &u.Email)
	return u, err
}

// CreateInvoicePayment links a newly created payment attempt to an invoice.
func (s *Store) CreateInvoicePayment(ctx context.Context, invoiceID, paymentID, payerUserID uuid.UUID, amountKobo int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO invoice_payments(invoice_id,payment_id,payer_user_id,amount_kobo,status)
		VALUES($1,$2,$3,$4,'pending')
		ON CONFLICT(payment_id) DO NOTHING`, invoiceID, paymentID, payerUserID, amountKobo)
	return err
}

// ApplyInvoicePaymentSuccess records a successful contribution and marks the
// invoice paid only when cumulative successful payments cover the invoice total.
func (s *Store) ApplyInvoicePaymentSuccess(ctx context.Context, paymentID uuid.UUID) (InvoiceView, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InvoiceView{}, false, err
	}
	defer tx.Rollback(ctx)
	var invoiceID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT invoice_id
		FROM invoice_payments
		WHERE payment_id=$1
		FOR UPDATE`, paymentID).Scan(&invoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvoiceView{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return InvoiceView{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE invoice_payments
		SET status='succeeded',updated_at=now()
		WHERE payment_id=$1 AND status <> 'succeeded'`, paymentID); err != nil {
		return InvoiceView{}, false, err
	}
	var total, paid int64
	if err := tx.QueryRow(ctx, `
		SELECT i.total_kobo,
		       COALESCE((SELECT SUM(ip.amount_kobo) FROM invoice_payments ip WHERE ip.invoice_id=i.id AND ip.status='succeeded'),0)
		FROM invoices i
		WHERE i.id=$1
		FOR UPDATE`, invoiceID).Scan(&total, &paid); err != nil {
		return InvoiceView{}, false, err
	}
	status := "partially_paid"
	paidClause := ""
	if paid >= total {
		status = "paid"
		paidClause = ", paid_at=COALESCE(paid_at, now())"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE invoices
		SET amount_paid_kobo=$2,status=$3,updated_at=now()`+paidClause+`
		WHERE id=$1`, invoiceID, paid, status); err != nil {
		return InvoiceView{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvoiceView{}, false, err
	}
	invoice, err := s.InvoiceByID(ctx, invoiceID)
	return invoice, true, err
}

// InvoiceByID returns one invoice by primary key.
func (s *Store) InvoiceByID(ctx context.Context, id uuid.UUID) (InvoiceView, error) {
	var reference string
	if err := s.pool.QueryRow(ctx, `SELECT reference FROM invoices WHERE id=$1`, id).Scan(&reference); err != nil {
		return InvoiceView{}, err
	}
	return s.InvoiceByReference(ctx, reference)
}

// InvoiceByPaymentID resolves invoice context for a receipt contribution.
func (s *Store) InvoiceByPaymentID(ctx context.Context, paymentID uuid.UUID) (InvoiceView, error) {
	var reference string
	err := s.pool.QueryRow(ctx, `
		SELECT i.reference
		FROM invoice_payments ip
		JOIN invoices i ON i.id=ip.invoice_id
		WHERE ip.payment_id=$1`, paymentID).Scan(&reference)
	if err != nil {
		return InvoiceView{}, err
	}
	return s.InvoiceByReference(ctx, reference)
}

// EnsureReceiptScanToken creates a single-use scan token for successful
// payments that match an active registered service. It is idempotent so payment
// webhook retries cannot create multiple customer-facing scan codes.
func (s *Store) EnsureReceiptScanToken(ctx context.Context, paymentID uuid.UUID) (ReceiptScanTokenView, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReceiptScanTokenView{}, false, err
	}
	defer tx.Rollback(ctx)

	if token, err := scanTokenByPaymentTx(ctx, tx, paymentID); err == nil {
		return token, false, tx.Commit(ctx)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ReceiptScanTokenView{}, false, err
	}

	var merchantID uuid.UUID
	var status domain.PaymentStatus
	if err := tx.QueryRow(ctx, `
		SELECT merchant_id,status
		FROM payments
		WHERE id=$1
		FOR UPDATE`, paymentID).Scan(&merchantID, &status); err != nil {
		return ReceiptScanTokenView{}, false, err
	}
	if status != domain.StatusSucceeded {
		return ReceiptScanTokenView{}, false, pgx.ErrNoRows
	}
	receiptType, err := receiptTypeForPaymentTx(ctx, tx, paymentID)
	if err != nil {
		return ReceiptScanTokenView{}, false, err
	}
	var serviceID uuid.UUID
	var ttlSeconds int
	if err := tx.QueryRow(ctx, `
		SELECT id,token_ttl_seconds
		FROM registered_services
		WHERE merchant_id=$1
		  AND active=true
		  AND (',' || replace(accepted_receipt_types, ' ', '') || ',') LIKE '%,' || $2 || ',%'
		ORDER BY created_at
		LIMIT 1`, merchantID, receiptType).Scan(&serviceID, &ttlSeconds); err != nil {
		return ReceiptScanTokenView{}, false, err
	}
	token, err := randomScanToken("scan")
	if err != nil {
		return ReceiptScanTokenView{}, false, err
	}
	manualCode, err := randomScanToken("XGSCAN")
	if err != nil {
		return ReceiptScanTokenView{}, false, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO receipt_scan_tokens(payment_id,service_id,token,manual_code,receipt_type,expires_at)
		VALUES($1,$2,$3,$4,$5,now()+($6::text || ' seconds')::interval)`,
		paymentID, serviceID, token, manualCode, receiptType, fmt.Sprintf("%d", ttlSeconds))
	if err != nil {
		return ReceiptScanTokenView{}, false, err
	}
	view, err := scanTokenByPaymentTx(ctx, tx, paymentID)
	if err != nil {
		return ReceiptScanTokenView{}, false, err
	}
	return view, true, tx.Commit(ctx)
}

// ReceiptScanTokenByPaymentID returns the scan token linked to a payment.
func (s *Store) ReceiptScanTokenByPaymentID(ctx context.Context, paymentID uuid.UUID) (ReceiptScanTokenView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReceiptScanTokenView{}, err
	}
	defer tx.Rollback(ctx)
	token, err := scanTokenByPaymentTx(ctx, tx, paymentID)
	if err != nil {
		return ReceiptScanTokenView{}, err
	}
	return token, tx.Commit(ctx)
}

func scanTokenByPaymentTx(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) (ReceiptScanTokenView, error) {
	var view ReceiptScanTokenView
	err := tx.QueryRow(ctx, `
		SELECT rst.id,rst.payment_id,rst.service_id,rs.name,rst.token,rst.manual_code,
		       rst.receipt_type,rst.expires_at,rst.consumed_at,rst.revoked_at,rst.created_at
		FROM receipt_scan_tokens rst
		JOIN registered_services rs ON rs.id=rst.service_id
		WHERE rst.payment_id=$1`, paymentID).Scan(
		&view.ID, &view.PaymentID, &view.ServiceID, &view.ServiceName, &view.Token, &view.ManualCode,
		&view.ReceiptType, &view.ExpiresAt, &view.ConsumedAt, &view.RevokedAt, &view.CreatedAt,
	)
	return view, err
}

func receiptTypeForPaymentTx(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) (string, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoice_payments WHERE payment_id=$1)`, paymentID).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "invoice", nil
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM data_orders WHERE payment_id=$1)`, paymentID).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "data", nil
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM thrift_contributions WHERE payment_id=$1)`, paymentID).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "thrift", nil
	}
	return "merchant_payment", nil
}

func randomScanToken(prefix string) (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// CreateRegisteredService adds an admin-managed service or updates an existing
// service for the same type and merchant owner.
func (s *Store) CreateRegisteredService(ctx context.Context, name, serviceType string, merchantID uuid.NullUUID, acceptedTypes string, ttlSeconds int, active bool) (RegisteredServiceView, error) {
	if ttlSeconds <= 0 {
		ttlSeconds = 86400
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO registered_services(name,service_type,merchant_id,accepted_receipt_types,token_ttl_seconds,active)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(service_type, merchant_id) DO UPDATE
		SET name=EXCLUDED.name,
			accepted_receipt_types=EXCLUDED.accepted_receipt_types,
			token_ttl_seconds=EXCLUDED.token_ttl_seconds,
			active=EXCLUDED.active,
			updated_at=now()
		RETURNING id`, strings.TrimSpace(name), strings.TrimSpace(serviceType), merchantID, normalizeAcceptedReceiptTypes(acceptedTypes), ttlSeconds, active).Scan(&id)
	if err != nil {
		return RegisteredServiceView{}, err
	}
	return s.RegisteredServiceByID(ctx, id)
}

func normalizeAcceptedReceiptTypes(value string) string {
	allowed := map[string]bool{"merchant_payment": true, "invoice": true, "data": true, "thrift": true}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if allowed[part] && !seen[part] {
			out = append(out, part)
			seen[part] = true
		}
	}
	if len(out) == 0 {
		return "merchant_payment"
	}
	return strings.Join(out, ",")
}

// RegisteredServiceByID returns one service.
func (s *Store) RegisteredServiceByID(ctx context.Context, id uuid.UUID) (RegisteredServiceView, error) {
	var service RegisteredServiceView
	err := s.pool.QueryRow(ctx, `
		SELECT rs.id,rs.name,rs.service_type,rs.merchant_id,COALESCE(m.name,''),
		       rs.accepted_receipt_types,rs.token_ttl_seconds,rs.active,rs.created_at,rs.updated_at
		FROM registered_services rs
		LEFT JOIN merchants m ON m.id=rs.merchant_id
		WHERE rs.id=$1`, id).Scan(&service.ID, &service.Name, &service.ServiceType, &service.MerchantID, &service.MerchantName, &service.AcceptedReceiptTypes, &service.TokenTTLSeconds, &service.Active, &service.CreatedAt, &service.UpdatedAt)
	return service, err
}

// ListRegisteredServices returns services for admin scanner setup.
func (s *Store) ListRegisteredServices(ctx context.Context, limit int) ([]RegisteredServiceView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rs.id,rs.name,rs.service_type,rs.merchant_id,COALESCE(m.name,''),
		       rs.accepted_receipt_types,rs.token_ttl_seconds,rs.active,rs.created_at,rs.updated_at
		FROM registered_services rs
		LEFT JOIN merchants m ON m.id=rs.merchant_id
		ORDER BY rs.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var services []RegisteredServiceView
	for rows.Next() {
		var service RegisteredServiceView
		if err := rows.Scan(&service.ID, &service.Name, &service.ServiceType, &service.MerchantID, &service.MerchantName, &service.AcceptedReceiptTypes, &service.TokenTTLSeconds, &service.Active, &service.CreatedAt, &service.UpdatedAt); err != nil {
			return nil, err
		}
		services = append(services, service)
	}
	return services, rows.Err()
}

// CreateServiceReader creates a reader credential and returns the plaintext key
// once for the admin to copy into the external reader.
func (s *Store) CreateServiceReader(ctx context.Context, serviceID uuid.UUID, name string) (ServiceReaderView, string, error) {
	key, err := randomScanToken("reader")
	if err != nil {
		return ServiceReaderView{}, "", err
	}
	hash := sha256.Sum256([]byte(key))
	prefix := key
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO service_readers(service_id,name,api_key_hash,key_prefix,active)
		VALUES($1,$2,$3,$4,true)
		RETURNING id`, serviceID, strings.TrimSpace(name), hash[:], prefix).Scan(&id); err != nil {
		return ServiceReaderView{}, "", err
	}
	reader, err := s.ServiceReaderByID(ctx, id)
	return reader, key, err
}

// ServiceReaderByID returns a reader display record.
func (s *Store) ServiceReaderByID(ctx context.Context, id uuid.UUID) (ServiceReaderView, error) {
	var reader ServiceReaderView
	err := s.pool.QueryRow(ctx, `
		SELECT sr.id,sr.service_id,rs.name,sr.name,sr.key_prefix,sr.active,sr.created_at,sr.updated_at
		FROM service_readers sr
		JOIN registered_services rs ON rs.id=sr.service_id
		WHERE sr.id=$1`, id).Scan(&reader.ID, &reader.ServiceID, &reader.ServiceName, &reader.Name, &reader.KeyPrefix, &reader.Active, &reader.CreatedAt, &reader.UpdatedAt)
	return reader, err
}

// ListServiceReaders returns recent external readers for admin visibility.
func (s *Store) ListServiceReaders(ctx context.Context, limit int) ([]ServiceReaderView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sr.id,sr.service_id,rs.name,sr.name,sr.key_prefix,sr.active,sr.created_at,sr.updated_at
		FROM service_readers sr
		JOIN registered_services rs ON rs.id=sr.service_id
		ORDER BY sr.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var readers []ServiceReaderView
	for rows.Next() {
		var reader ServiceReaderView
		if err := rows.Scan(&reader.ID, &reader.ServiceID, &reader.ServiceName, &reader.Name, &reader.KeyPrefix, &reader.Active, &reader.CreatedAt, &reader.UpdatedAt); err != nil {
			return nil, err
		}
		readers = append(readers, reader)
	}
	return readers, rows.Err()
}

// ValidateAndConsumeReceiptScan validates a scan token for one authenticated
// reader and consumes it atomically on success.
func (s *Store) ValidateAndConsumeReceiptScan(ctx context.Context, apiKey, tokenOrURL, remoteAddr string) (ReceiptScanResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReceiptScanResult{}, err
	}
	defer tx.Rollback(ctx)
	reader, err := readerByAPIKeyTx(ctx, tx, apiKey)
	if err != nil {
		_ = recordScanAttemptTx(ctx, tx, uuid.NullUUID{}, uuid.NullUUID{}, uuid.NullUUID{}, "reader_not_authorized", remoteAddr, map[string]any{})
		_ = tx.Commit(ctx)
		return ReceiptScanResult{Status: "reader_not_authorized"}, nil
	}
	tokenValue := ExtractScanToken(tokenOrURL)
	if tokenValue == "" {
		_ = recordScanAttemptTx(ctx, tx, uuid.NullUUID{}, uuid.NullUUID{UUID: reader.ServiceID, Valid: true}, uuid.NullUUID{UUID: reader.ID, Valid: true}, "unknown_token", remoteAddr, map[string]any{})
		_ = tx.Commit(ctx)
		return ReceiptScanResult{Status: "unknown_token"}, nil
	}

	var token ReceiptScanTokenView
	var payment PaymentView
	var serviceActive bool
	err = tx.QueryRow(ctx, `
		SELECT rst.id,rst.payment_id,rst.service_id,rs.name,rst.token,rst.manual_code,rst.receipt_type,
		       rst.expires_at,rst.consumed_at,rst.revoked_at,rst.created_at,
		       p.id,p.user_id,p.merchant_id,p.amount_kobo,p.currency,p.status,p.provider,p.provider_reference,
		       p.channel,p.recipient,p.checkout_url,p.receipt_token,p.failure_reason,p.created_at,p.updated_at,p.paid_at,
		       u.display_name,u.email,COALESCE(u.whatsapp_number,''),m.name,m.slug,u.last_inbound_at,
		       rs.active
		FROM receipt_scan_tokens rst
		JOIN registered_services rs ON rs.id=rst.service_id
		JOIN payments p ON p.id=rst.payment_id
		JOIN users u ON u.id=p.user_id
		JOIN merchants m ON m.id=p.merchant_id
		WHERE rst.token=$1 OR rst.manual_code=$1
		FOR UPDATE`, tokenValue).Scan(
		&token.ID, &token.PaymentID, &token.ServiceID, &token.ServiceName, &token.Token, &token.ManualCode,
		&token.ReceiptType, &token.ExpiresAt, &token.ConsumedAt, &token.RevokedAt, &token.CreatedAt,
		&payment.ID, &payment.UserID, &payment.MerchantID, &payment.AmountKobo, &payment.Currency,
		&payment.Status, &payment.Provider, &payment.ProviderReference, &payment.Channel, &payment.Recipient,
		&payment.CheckoutURL, &payment.ReceiptToken, &payment.FailureReason, &payment.CreatedAt,
		&payment.UpdatedAt, &payment.PaidAt, &payment.UserName, &payment.UserEmail, &payment.WhatsAppNumber,
		&payment.MerchantName, &payment.MerchantSlug, &payment.LastInboundAt, &serviceActive,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = recordScanAttemptTx(ctx, tx, uuid.NullUUID{}, uuid.NullUUID{UUID: reader.ServiceID, Valid: true}, uuid.NullUUID{UUID: reader.ID, Valid: true}, "unknown_token", remoteAddr, map[string]any{})
		_ = tx.Commit(ctx)
		return ReceiptScanResult{Status: "unknown_token"}, nil
	}
	if err != nil {
		return ReceiptScanResult{}, err
	}
	status := scanValidationStatus(reader, token, payment, serviceActive)
	if status == "valid_consumed" {
		tag, err := tx.Exec(ctx, `UPDATE receipt_scan_tokens SET consumed_at=now() WHERE id=$1 AND consumed_at IS NULL`, token.ID)
		if err != nil {
			return ReceiptScanResult{}, err
		}
		if tag.RowsAffected() != 1 {
			status = "already_used"
		}
	}
	if err := recordScanAttemptTx(ctx, tx,
		uuid.NullUUID{UUID: token.ID, Valid: true},
		uuid.NullUUID{UUID: token.ServiceID, Valid: true},
		uuid.NullUUID{UUID: reader.ID, Valid: true},
		status, remoteAddr, map[string]any{"receipt_type": token.ReceiptType}); err != nil {
		return ReceiptScanResult{}, err
	}
	result := ReceiptScanResult{
		Status:        status,
		ReceiptType:   token.ReceiptType,
		ServiceName:   token.ServiceName,
		MerchantName:  payment.MerchantName,
		Amount:        domain.FormatNGN(payment.AmountKobo),
		AmountKobo:    payment.AmountKobo,
		PaymentStatus: string(payment.Status),
		CustomerName:  payment.UserName,
		CustomerPhone: maskForScan(payment.WhatsAppNumber),
		ManualCode:    token.ManualCode,
		ProviderRef:   payment.ProviderReference,
	}
	if status == "valid_consumed" {
		result.ConsumedAtText = time.Now().Format(time.RFC3339)
	}
	return result, tx.Commit(ctx)
}

func scanValidationStatus(reader ServiceReaderView, token ReceiptScanTokenView, payment PaymentView, serviceActive bool) string {
	now := time.Now()
	switch {
	case reader.ServiceID != token.ServiceID:
		return "wrong_service"
	case !reader.Active || !serviceActive:
		return "reader_not_authorized"
	case payment.Status != domain.StatusSucceeded:
		return "not_paid"
	case token.RevokedAt != nil:
		return "revoked"
	case now.After(token.ExpiresAt):
		return "expired"
	case token.ConsumedAt != nil:
		return "already_used"
	default:
		return "valid_consumed"
	}
}

func readerByAPIKeyTx(ctx context.Context, tx pgx.Tx, apiKey string) (ServiceReaderView, error) {
	key := strings.TrimSpace(apiKey)
	hash := sha256.Sum256([]byte(key))
	var reader ServiceReaderView
	err := tx.QueryRow(ctx, `
		SELECT sr.id,sr.service_id,rs.name,sr.name,sr.key_prefix,sr.active,sr.created_at,sr.updated_at
		FROM service_readers sr
		JOIN registered_services rs ON rs.id=sr.service_id
		WHERE sr.api_key_hash=$1 AND sr.active=true AND rs.active=true`, hash[:]).Scan(&reader.ID, &reader.ServiceID, &reader.ServiceName, &reader.Name, &reader.KeyPrefix, &reader.Active, &reader.CreatedAt, &reader.UpdatedAt)
	return reader, err
}

func recordScanAttemptTx(ctx context.Context, tx pgx.Tx, tokenID, serviceID, readerID uuid.NullUUID, status, remoteAddr string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO receipt_scan_attempts(token_id,service_id,reader_id,status,detail,remote_addr)
		VALUES($1,$2,$3,$4,$5,$6)`, tokenID, serviceID, readerID, status, raw, truncate(remoteAddr, 120))
	return err
}

// ExtractScanToken accepts raw manual codes, raw tokens, or URLs containing
// /scan/{token}. This keeps external reader integrations small.
func ExtractScanToken(value string) string {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return ""
	}
	if i := strings.Index(cleaned, "/scan/"); i >= 0 {
		cleaned = cleaned[i+len("/scan/"):]
	}
	if i := strings.IndexAny(cleaned, "?#"); i >= 0 {
		cleaned = cleaned[:i]
	}
	return strings.Trim(cleaned, "/ ")
}

func maskForScan(value string) string {
	if len(value) <= 5 {
		return value
	}
	return value[:4] + strings.Repeat("*", max(0, len(value)-7)) + value[len(value)-3:]
}

// ListReceiptScanTokens returns recent customer-facing tokens for admin visibility.
func (s *Store) ListReceiptScanTokens(ctx context.Context, limit int) ([]ReceiptScanTokenView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rst.id,rst.payment_id,rst.service_id,rs.name,rst.token,rst.manual_code,
		       rst.receipt_type,rst.expires_at,rst.consumed_at,rst.revoked_at,rst.created_at
		FROM receipt_scan_tokens rst
		JOIN registered_services rs ON rs.id=rst.service_id
		ORDER BY rst.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []ReceiptScanTokenView
	for rows.Next() {
		var token ReceiptScanTokenView
		if err := rows.Scan(&token.ID, &token.PaymentID, &token.ServiceID, &token.ServiceName, &token.Token, &token.ManualCode, &token.ReceiptType, &token.ExpiresAt, &token.ConsumedAt, &token.RevokedAt, &token.CreatedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// ListReceiptScanAttempts returns recent scanner validations for audit.
func (s *Store) ListReceiptScanAttempts(ctx context.Context, limit int) ([]ReceiptScanAttemptView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rsa.id,rsa.token_id,rsa.service_id,rsa.reader_id,COALESCE(rs.name,''),COALESCE(sr.name,''),
		       rsa.status,rsa.remote_addr,rsa.created_at
		FROM receipt_scan_attempts rsa
		LEFT JOIN registered_services rs ON rs.id=rsa.service_id
		LEFT JOIN service_readers sr ON sr.id=rsa.reader_id
		ORDER BY rsa.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []ReceiptScanAttemptView
	for rows.Next() {
		var attempt ReceiptScanAttemptView
		if err := rows.Scan(&attempt.ID, &attempt.TokenID, &attempt.ServiceID, &attempt.ReaderID, &attempt.ServiceName, &attempt.ReaderName, &attempt.Status, &attempt.RemoteAddr, &attempt.CreatedAt); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

// UpsertIndividualProfile records the demo KYC profile and upgrades the user to
// the individual account level. The KYC status is simulated for the MVP.
func (s *Store) UpsertIndividualProfile(ctx context.Context, userID uuid.UUID, legalName string, dob time.Time, address, occupation string) (IndividualProfile, error) {
	var profile IndividualProfile
	err := s.pool.QueryRow(ctx, `
		INSERT INTO individual_profiles(user_id,legal_name,date_of_birth,address,occupation,kyc_status)
		VALUES($1,$2,$3,$4,$5,'approved_simulated')
		ON CONFLICT(user_id) DO UPDATE
		SET legal_name=EXCLUDED.legal_name,
			date_of_birth=EXCLUDED.date_of_birth,
			address=EXCLUDED.address,
			occupation=EXCLUDED.occupation,
			kyc_status='approved_simulated',
			updated_at=now()
		RETURNING user_id,legal_name,date_of_birth,address,occupation,kyc_status,created_at,updated_at`,
		userID, strings.TrimSpace(legalName), dob, strings.TrimSpace(address), strings.TrimSpace(occupation),
	).Scan(&profile.UserID, &profile.LegalName, &profile.DateOfBirth, &profile.Address, &profile.Occupation, &profile.KYCStatus, &profile.CreatedAt, &profile.UpdatedAt)
	if err != nil {
		return IndividualProfile{}, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE users SET account_level='individual',updated_at=now() WHERE id=$1`, userID); err != nil {
		return IndividualProfile{}, err
	}
	return profile, nil
}

// IndividualProfileByUser resolves the profile used to gate thrift creation.
func (s *Store) IndividualProfileByUser(ctx context.Context, userID uuid.UUID) (IndividualProfile, error) {
	var profile IndividualProfile
	err := s.pool.QueryRow(ctx, `
		SELECT user_id,legal_name,date_of_birth,address,occupation,kyc_status,created_at,updated_at
		FROM individual_profiles
		WHERE user_id=$1`, userID).Scan(&profile.UserID, &profile.LegalName, &profile.DateOfBirth, &profile.Address, &profile.Occupation, &profile.KYCStatus, &profile.CreatedAt, &profile.UpdatedAt)
	return profile, err
}

// ThriftSystemMerchant returns the inactive internal merchant used only for
// thrift contribution payment records.
func (s *Store) ThriftSystemMerchant(ctx context.Context) (Merchant, error) {
	var merchant Merchant
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at
		FROM merchants WHERE slug='xego-thrift-contributions'`).Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords,
		&merchant.SortOrder, &merchant.CreatedAt,
	)
	return merchant, err
}

// ThriftGroupNameExists reports whether an active thrift group with the given
// name already exists. Names are compared case-insensitively.
func (s *Store) ThriftGroupNameExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM thrift_groups WHERE LOWER(TRIM(name))=LOWER($1) AND status <> 'cancelled')`, name).Scan(&exists)
	return exists, err
}

// CreateThriftGroup creates an inviting thrift group and adds the creator as a
// confirmed member. The creator can later choose the full payout order.
func (s *Store) CreateThriftGroup(ctx context.Context, creatorID uuid.UUID, name string, amountKobo int64, frequency string, targetMembers int) (ThriftGroupView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftGroupView{}, err
	}
	defer tx.Rollback(ctx)
	trimmed := strings.TrimSpace(name)
	var groupID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_groups(creator_user_id,name,contribution_amount_kobo,frequency,target_member_count,invite_code,status)
		VALUES($1,$2,$3,$4,$5,'','inviting')
		RETURNING id`, creatorID, trimmed, amountKobo, frequency, targetMembers).Scan(&groupID); err != nil {
		return ThriftGroupView{}, err
	}
	var memberID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_members(group_id,user_id,status)
		VALUES($1,$2,'confirmed')
		RETURNING id`, groupID, creatorID).Scan(&memberID); err != nil {
		return ThriftGroupView{}, err
	}
	if err := insertThriftEvent(ctx, tx, groupID, uuid.Nil, memberID, "group_created", map[string]any{"name": trimmed}); err != nil {
		return ThriftGroupView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftGroupView{}, err
	}
	return s.ThriftGroupByID(ctx, groupID)
}

// ThriftGroupByName returns a group by its user-facing name (case-insensitive).
func (s *Store) ThriftGroupByName(ctx context.Context, name string) (ThriftGroupView, error) {
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id FROM thrift_groups WHERE LOWER(TRIM(name))=LOWER($1)`, strings.TrimSpace(name)).Scan(&id); err != nil {
		return ThriftGroupView{}, err
	}
	return s.ThriftGroupByID(ctx, id)
}

// ThriftGroupByInviteCode is a backward-compatible alias for ThriftGroupByName.
func (s *Store) ThriftGroupByInviteCode(ctx context.Context, code string) (ThriftGroupView, error) {
	return s.ThriftGroupByName(ctx, code)
}

// ThriftGroupByID returns a group summary.
func (s *Store) ThriftGroupByID(ctx context.Context, id uuid.UUID) (ThriftGroupView, error) {
	var group ThriftGroupView
	err := s.pool.QueryRow(ctx, `
		SELECT g.id,g.creator_user_id,g.name,g.contribution_amount_kobo,g.frequency,g.target_member_count,
		       g.invite_code,g.status,g.current_cycle,g.created_at,g.updated_at,g.activated_at,g.completed_at,
		       u.display_name,
		       (SELECT COUNT(*) FROM thrift_members tm WHERE tm.group_id=g.id AND tm.status IN ('confirmed','active'))
		FROM thrift_groups g
		JOIN users u ON u.id=g.creator_user_id
		WHERE g.id=$1`, id).Scan(
		&group.ID, &group.CreatorUserID, &group.Name, &group.ContributionAmountKobo, &group.Frequency,
		&group.TargetMemberCount, &group.InviteCode, &group.Status, &group.CurrentCycle,
		&group.CreatedAt, &group.UpdatedAt, &group.ActivatedAt, &group.CompletedAt,
		&group.CreatorName, &group.MemberCount,
	)
	return group, err
}

// JoinThriftGroup adds a confirmed member while the group is still inviting.
func (s *Store) JoinThriftGroup(ctx context.Context, name string, userID uuid.UUID) (ThriftGroupView, ThriftMemberView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	defer tx.Rollback(ctx)
	var groupID uuid.UUID
	var status string
	var target, count int
	if err := tx.QueryRow(ctx, `
		SELECT id,status,target_member_count,
		       (SELECT COUNT(*) FROM thrift_members WHERE group_id=thrift_groups.id AND status IN ('confirmed','active'))
		FROM thrift_groups
		WHERE LOWER(TRIM(name))=LOWER($1)
		FOR UPDATE`, strings.TrimSpace(name)).Scan(&groupID, &status, &target, &count); err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	if status != "inviting" {
		return ThriftGroupView{}, ThriftMemberView{}, fmt.Errorf("thrift group is %s", status)
	}
	if count >= target {
		return ThriftGroupView{}, ThriftMemberView{}, errors.New("thrift group is already full")
	}
	var memberID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_members(group_id,user_id,status)
		VALUES($1,$2,'confirmed')
		ON CONFLICT(group_id,user_id) DO UPDATE
		SET status=CASE WHEN thrift_members.status='removed' THEN 'confirmed' ELSE thrift_members.status END,
			confirmed_at=COALESCE(thrift_members.confirmed_at, now())
		RETURNING id`, groupID, userID).Scan(&memberID); err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	if err := insertThriftEvent(ctx, tx, groupID, uuid.Nil, memberID, "member_confirmed", map[string]any{}); err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	group, err := s.ThriftGroupByID(ctx, groupID)
	if err != nil {
		return ThriftGroupView{}, ThriftMemberView{}, err
	}
	member, err := s.ThriftMemberByID(ctx, memberID)
	return group, member, err
}

// ThriftMemberByID returns one member with display fields.
func (s *Store) ThriftMemberByID(ctx context.Context, memberID uuid.UUID) (ThriftMemberView, error) {
	var member ThriftMemberView
	err := s.pool.QueryRow(ctx, `
		SELECT tm.id,tm.group_id,tm.user_id,u.display_name,u.email,COALESCE(u.whatsapp_number,''),
		       tm.status,tm.payout_position,tm.joined_at,tm.confirmed_at
		FROM thrift_members tm
		JOIN users u ON u.id=tm.user_id
		WHERE tm.id=$1`, memberID).Scan(&member.ID, &member.GroupID, &member.UserID, &member.UserName, &member.UserEmail, &member.WhatsAppNumber, &member.Status, &member.PayoutPosition, &member.JoinedAt, &member.ConfirmedAt)
	return member, err
}

// ThriftMembers returns active/confirmed members in a deterministic display order.
func (s *Store) ThriftMembers(ctx context.Context, groupID uuid.UUID) ([]ThriftMemberView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tm.id,tm.group_id,tm.user_id,u.display_name,u.email,COALESCE(u.whatsapp_number,''),
		       tm.status,tm.payout_position,tm.joined_at,tm.confirmed_at
		FROM thrift_members tm
		JOIN users u ON u.id=tm.user_id
		WHERE tm.group_id=$1 AND tm.status IN ('confirmed','active')
		ORDER BY tm.payout_position NULLS LAST, tm.joined_at`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []ThriftMemberView
	for rows.Next() {
		var member ThriftMemberView
		if err := rows.Scan(&member.ID, &member.GroupID, &member.UserID, &member.UserName, &member.UserEmail, &member.WhatsAppNumber, &member.Status, &member.PayoutPosition, &member.JoinedAt, &member.ConfirmedAt); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// ActivateThriftGroup applies the creator-selected payout order and opens the
// first contribution cycle.
func (s *Store) ActivateThriftGroup(ctx context.Context, groupID, creatorID uuid.UUID, orderedMemberIDs []uuid.UUID) (ThriftCycleView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftCycleView{}, err
	}
	defer tx.Rollback(ctx)
	var amount int64
	var frequency, status string
	var target, current int
	if err := tx.QueryRow(ctx, `
		SELECT contribution_amount_kobo,frequency,status,target_member_count,
		       (SELECT COUNT(*) FROM thrift_members WHERE group_id=thrift_groups.id AND status='confirmed')
		FROM thrift_groups
		WHERE id=$1 AND creator_user_id=$2
		FOR UPDATE`, groupID, creatorID).Scan(&amount, &frequency, &status, &target, &current); err != nil {
		return ThriftCycleView{}, err
	}
	if status != "inviting" {
		return ThriftCycleView{}, fmt.Errorf("thrift group is %s", status)
	}
	if current != target || len(orderedMemberIDs) != target {
		return ThriftCycleView{}, fmt.Errorf("rotation needs exactly %d confirmed members", target)
	}
	seen := map[uuid.UUID]bool{}
	for i, memberID := range orderedMemberIDs {
		if seen[memberID] {
			return ThriftCycleView{}, errors.New("rotation contains a duplicate member")
		}
		seen[memberID] = true
		tag, err := tx.Exec(ctx, `
			UPDATE thrift_members
			SET payout_position=$3,status='active'
			WHERE id=$1 AND group_id=$2 AND status='confirmed'`, memberID, groupID, i+1)
		if err != nil {
			return ThriftCycleView{}, err
		}
		if tag.RowsAffected() != 1 {
			return ThriftCycleView{}, errors.New("rotation contains an invalid member")
		}
	}
	var payoutMemberID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM thrift_members WHERE group_id=$1 AND payout_position=1`, groupID).Scan(&payoutMemberID); err != nil {
		return ThriftCycleView{}, err
	}
	dueAt := nextThriftDueDate(time.Now(), frequency)
	var cycleID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_cycles(group_id,cycle_number,due_at,payout_member_id,status)
		VALUES($1,1,$2,$3,'pending_contributions')
		RETURNING id`, groupID, dueAt, payoutMemberID).Scan(&cycleID); err != nil {
		return ThriftCycleView{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO thrift_contributions(cycle_id,member_id,amount_kobo,status)
		SELECT $1,id,$2,'awaiting_payment'
		FROM thrift_members
		WHERE group_id=$3 AND status='active'`, cycleID, amount, groupID); err != nil {
		return ThriftCycleView{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE thrift_groups
		SET status='active',current_cycle=1,activated_at=now(),updated_at=now()
		WHERE id=$1`, groupID); err != nil {
		return ThriftCycleView{}, err
	}
	if err := insertThriftEvent(ctx, tx, groupID, cycleID, uuid.Nil, "group_activated", map[string]any{"frequency": frequency}); err != nil {
		return ThriftCycleView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftCycleView{}, err
	}
	return s.ThriftCycleByID(ctx, cycleID)
}

func nextThriftDueDate(from time.Time, frequency string) time.Time {
	if frequency == "monthly" {
		return from.AddDate(0, 1, 0)
	}
	return from.AddDate(0, 0, 7)
}

// CurrentThriftContributionForUser returns the open contribution for a member.
func (s *Store) CurrentThriftContributionForUser(ctx context.Context, name string, userID uuid.UUID) (ThriftContributionView, error) {
	var contribution ThriftContributionView
	err := s.pool.QueryRow(ctx, `
		SELECT tc.id,tc.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,tm.user_id,u.display_name,
		       tc.payment_id,tc.amount_kobo,tc.status,COALESCE(p.status,''),COALESCE(p.receipt_token,''),
		       tc.created_at,tc.updated_at,tc.paid_at
		FROM thrift_groups tg
		JOIN thrift_cycles tcy ON tcy.group_id=tg.id AND tcy.cycle_number=tg.current_cycle
		JOIN thrift_members tm ON tm.group_id=tg.id AND tm.user_id=$2
		JOIN users u ON u.id=tm.user_id
		JOIN thrift_contributions tc ON tc.cycle_id=tcy.id AND tc.member_id=tm.id
		LEFT JOIN payments p ON p.id=tc.payment_id
		WHERE LOWER(TRIM(tg.name))=LOWER($1) AND tg.status='active'`, strings.TrimSpace(name), userID).Scan(
		&contribution.ID, &contribution.CycleID, &contribution.GroupID, &contribution.GroupName, &contribution.CycleNumber,
		&contribution.MemberID, &contribution.UserID, &contribution.MemberName, &contribution.PaymentID,
		&contribution.AmountKobo, &contribution.Status, &contribution.PaymentStatus, &contribution.PaymentReceipt,
		&contribution.CreatedAt, &contribution.UpdatedAt, &contribution.PaidAt,
	)
	return contribution, err
}

// LinkThriftContributionPayment attaches one payment attempt to a contribution.
func (s *Store) LinkThriftContributionPayment(ctx context.Context, contributionID, paymentID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE thrift_contributions
		SET payment_id=$2,updated_at=now()
		WHERE id=$1 AND status='awaiting_payment' AND payment_id IS NULL`, contributionID, paymentID)
	return err
}

// ApplyThriftContributionPaymentSuccess credits a contribution and advances
// cycle readiness only once all active members have paid.
func (s *Store) ApplyThriftContributionPaymentSuccess(ctx context.Context, paymentID uuid.UUID) (ThriftContributionView, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftContributionView{}, false, err
	}
	defer tx.Rollback(ctx)
	var contributionID, cycleID, groupID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT tc.id,tc.cycle_id,tcy.group_id
		FROM thrift_contributions tc
		JOIN thrift_cycles tcy ON tcy.id=tc.cycle_id
		WHERE tc.payment_id=$1
		FOR UPDATE`, paymentID).Scan(&contributionID, &cycleID, &groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ThriftContributionView{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return ThriftContributionView{}, false, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE thrift_contributions
		SET status='paid',paid_at=COALESCE(paid_at, now()),updated_at=now()
		WHERE id=$1 AND status <> 'paid'`, contributionID)
	if err != nil {
		return ThriftContributionView{}, false, err
	}
	changed := tag.RowsAffected() == 1
	var unpaid int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM thrift_contributions
		WHERE cycle_id=$1 AND status <> 'paid'`, cycleID).Scan(&unpaid); err != nil {
		return ThriftContributionView{}, false, err
	}
	if unpaid == 0 {
		var cycleStatus string
		var payoutMemberID uuid.UUID
		var total int64
		if err := tx.QueryRow(ctx, `
			SELECT status,payout_member_id,
			       (SELECT COALESCE(SUM(amount_kobo),0) FROM thrift_contributions WHERE cycle_id=thrift_cycles.id)
			FROM thrift_cycles
			WHERE id=$1
			FOR UPDATE`, cycleID).Scan(&cycleStatus, &payoutMemberID, &total); err != nil {
			return ThriftContributionView{}, false, err
		}
		if cycleStatus == "pending_contributions" {
			if _, err := tx.Exec(ctx, `UPDATE thrift_cycles SET status='ready_for_payout',updated_at=now() WHERE id=$1`, cycleID); err != nil {
				return ThriftContributionView{}, false, err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO thrift_payouts(cycle_id,payout_member_id,amount_kobo,status)
				VALUES($1,$2,$3,'pending')
				ON CONFLICT(cycle_id) DO NOTHING`, cycleID, payoutMemberID, total); err != nil {
				return ThriftContributionView{}, false, err
			}
			if err := insertThriftEvent(ctx, tx, groupID, cycleID, payoutMemberID, "cycle_ready_for_payout", map[string]any{"amount_kobo": total}); err != nil {
				return ThriftContributionView{}, false, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftContributionView{}, false, err
	}
	view, err := s.ThriftContributionByPaymentID(ctx, paymentID)
	return view, changed, err
}

// ThriftContributionByPaymentID returns thrift context for a receipt.
func (s *Store) ThriftContributionByPaymentID(ctx context.Context, paymentID uuid.UUID) (ThriftContributionView, error) {
	return s.thriftContributionBy(ctx, "tc.payment_id=$1", paymentID)
}

// ThriftContributionByID returns one contribution by primary key.
func (s *Store) ThriftContributionByID(ctx context.Context, id uuid.UUID) (ThriftContributionView, error) {
	return s.thriftContributionBy(ctx, "tc.id=$1", id)
}

func (s *Store) thriftContributionBy(ctx context.Context, predicate string, value any) (ThriftContributionView, error) {
	var contribution ThriftContributionView
	err := s.pool.QueryRow(ctx, `
		SELECT tc.id,tc.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,tm.user_id,u.display_name,
		       tc.payment_id,tc.amount_kobo,tc.status,COALESCE(p.status,''),COALESCE(p.receipt_token,''),
		       tc.created_at,tc.updated_at,tc.paid_at
		FROM thrift_contributions tc
		JOIN thrift_cycles tcy ON tcy.id=tc.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tc.member_id
		JOIN users u ON u.id=tm.user_id
		LEFT JOIN payments p ON p.id=tc.payment_id
		WHERE `+predicate, value).Scan(
		&contribution.ID, &contribution.CycleID, &contribution.GroupID, &contribution.GroupName, &contribution.CycleNumber,
		&contribution.MemberID, &contribution.UserID, &contribution.MemberName, &contribution.PaymentID,
		&contribution.AmountKobo, &contribution.Status, &contribution.PaymentStatus, &contribution.PaymentReceipt,
		&contribution.CreatedAt, &contribution.UpdatedAt, &contribution.PaidAt,
	)
	return contribution, err
}

// MarkThriftPayoutCompleted simulates payout completion and opens the next
// rotation cycle until each member has received one payout.
func (s *Store) MarkThriftPayoutCompleted(ctx context.Context, payoutID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cycleID, groupID uuid.UUID
	var cycleNumber, target int
	var frequency string
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT tp.cycle_id,tcy.group_id,tcy.cycle_number,tg.target_member_count,tg.frequency,tp.status
		FROM thrift_payouts tp
		JOIN thrift_cycles tcy ON tcy.id=tp.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		WHERE tp.id=$1
		FOR UPDATE`, payoutID).Scan(&cycleID, &groupID, &cycleNumber, &target, &frequency, &status); err != nil {
		return err
	}
	if status == "completed_simulated" {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE thrift_payouts SET status='completed_simulated',completed_at=now(),updated_at=now()
		WHERE id=$1`, payoutID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE thrift_cycles SET status='payout_completed',updated_at=now() WHERE id=$1`, cycleID); err != nil {
		return err
	}
	if err := insertThriftEvent(ctx, tx, groupID, cycleID, uuid.Nil, "payout_completed_simulated", map[string]any{}); err != nil {
		return err
	}
	if cycleNumber >= target {
		if _, err := tx.Exec(ctx, `UPDATE thrift_groups SET status='completed',completed_at=now(),updated_at=now() WHERE id=$1`, groupID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	nextCycle := cycleNumber + 1
	var payoutMemberID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM thrift_members WHERE group_id=$1 AND payout_position=$2`, groupID, nextCycle).Scan(&payoutMemberID); err != nil {
		return err
	}
	var amount int64
	if err := tx.QueryRow(ctx, `SELECT contribution_amount_kobo FROM thrift_groups WHERE id=$1`, groupID).Scan(&amount); err != nil {
		return err
	}
	dueAt := nextThriftDueDate(time.Now(), frequency)
	var newCycleID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO thrift_cycles(group_id,cycle_number,due_at,payout_member_id,status)
		VALUES($1,$2,$3,$4,'pending_contributions')
		RETURNING id`, groupID, nextCycle, dueAt, payoutMemberID).Scan(&newCycleID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO thrift_contributions(cycle_id,member_id,amount_kobo,status)
		SELECT $1,id,$2,'awaiting_payment'
		FROM thrift_members
		WHERE group_id=$3 AND status='active'`, newCycleID, amount, groupID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE thrift_groups SET current_cycle=$2,updated_at=now() WHERE id=$1`, groupID, nextCycle); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateThriftGroup updates mutable fields of a thrift group that is still in
// inviting status. Only the creator can update. Returns the updated group.
func (s *Store) UpdateThriftGroup(ctx context.Context, groupID, creatorID uuid.UUID, name *string, amountKobo *int64, frequency *string, targetMembers *int) (ThriftGroupView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ThriftGroupView{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM thrift_groups
		WHERE id=$1 AND creator_user_id=$2
		FOR UPDATE`, groupID, creatorID).Scan(&status); err != nil {
		return ThriftGroupView{}, err
	}
	if status != "inviting" {
		return ThriftGroupView{}, fmt.Errorf("thrift group is %s and can no longer be edited", status)
	}
	setClauses := []string{}
	args := []any{}
	argIdx := 3
	if name != nil {
		setClauses = append(setClauses, fmt.Sprintf("name=$%d", argIdx))
		args = append(args, strings.TrimSpace(*name))
		argIdx++
	}
	if amountKobo != nil {
		setClauses = append(setClauses, fmt.Sprintf("contribution_amount_kobo=$%d", argIdx))
		args = append(args, *amountKobo)
		argIdx++
	}
	if frequency != nil {
		setClauses = append(setClauses, fmt.Sprintf("frequency=$%d", argIdx))
		args = append(args, *frequency)
		argIdx++
	}
	if targetMembers != nil {
		setClauses = append(setClauses, fmt.Sprintf("target_member_count=$%d", argIdx))
		args = append(args, *targetMembers)
		argIdx++
	}
	if len(setClauses) == 0 {
		return s.ThriftGroupByID(ctx, groupID)
	}
	setClauses = append(setClauses, "updated_at=now()")
	query := fmt.Sprintf("UPDATE thrift_groups SET %s WHERE id=$1 AND creator_user_id=$2", strings.Join(setClauses, ","))
	args = append([]any{groupID, creatorID}, args...)
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		return ThriftGroupView{}, err
	}
	if err := insertThriftEvent(ctx, tx, groupID, uuid.Nil, uuid.Nil, "group_updated", map[string]any{}); err != nil {
		return ThriftGroupView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ThriftGroupView{}, err
	}
	return s.ThriftGroupByID(ctx, groupID)
}

// ThriftCycleProgress returns summary progress for a specific cycle.
type ThriftCycleProgress struct {
	TotalMembers     int
	PaidCount        int
	DueAt            time.Time
	PayoutMemberName string
	GroupName        string
	CycleNumber      int
	ContributionAmt  int64
}

// ThriftCycleProgressForGroup returns the current cycle's progress for a group.
func (s *Store) ThriftCycleProgressForGroup(ctx context.Context, groupID uuid.UUID) (ThriftCycleProgress, error) {
	var p ThriftCycleProgress
	err := s.pool.QueryRow(ctx, `
		SELECT tg.name,tcy.cycle_number,tcy.due_at,u.display_name,tg.contribution_amount_kobo,
		       tg.target_member_count,
		       (SELECT COUNT(*) FROM thrift_contributions tc2 WHERE tc2.cycle_id=tcy.id AND tc2.status='paid')
		FROM thrift_groups tg
		JOIN thrift_cycles tcy ON tcy.group_id=tg.id AND tcy.cycle_number=tg.current_cycle
		JOIN thrift_members tm ON tm.id=tcy.payout_member_id
		JOIN users u ON u.id=tm.user_id
		WHERE tg.id=$1`, groupID).Scan(
		&p.GroupName, &p.CycleNumber, &p.DueAt, &p.PayoutMemberName, &p.ContributionAmt,
		&p.TotalMembers, &p.PaidCount,
	)
	return p, err
}

// ThriftCyclesForGroup returns all cycles for a group in order.
func (s *Store) ThriftCyclesForGroup(ctx context.Context, groupID uuid.UUID) ([]ThriftCycleView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tcy.id,tcy.group_id,tg.name,tcy.cycle_number,tcy.due_at,tm.id,u.display_name,
		       tcy.status,tg.contribution_amount_kobo,tg.target_member_count,tcy.created_at,tcy.updated_at
		FROM thrift_cycles tcy
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tcy.payout_member_id
		JOIN users u ON u.id=tm.user_id
		WHERE tcy.group_id=$1
		ORDER BY tcy.cycle_number`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cycles []ThriftCycleView
	for rows.Next() {
		var c ThriftCycleView
		if err := rows.Scan(&c.ID, &c.GroupID, &c.GroupName, &c.CycleNumber, &c.DueAt, &c.PayoutMemberID, &c.PayoutMemberName,
			&c.Status, &c.ContributionAmountKobo, &c.TargetMemberCount, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		cycles = append(cycles, c)
	}
	return cycles, rows.Err()
}

// ThriftContributionsForCycle returns all contributions for a cycle with member names.
func (s *Store) ThriftContributionsForCycle(ctx context.Context, cycleID uuid.UUID) ([]ThriftContributionView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tc.id,tc.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,tm.user_id,u.display_name,
		       tc.payment_id,tc.amount_kobo,tc.status,COALESCE(p.status,''),COALESCE(p.receipt_token,''),
		       tc.created_at,tc.updated_at,tc.paid_at
		FROM thrift_contributions tc
		JOIN thrift_cycles tcy ON tcy.id=tc.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tc.member_id
		JOIN users u ON u.id=tm.user_id
		LEFT JOIN payments p ON p.id=tc.payment_id
		WHERE tc.cycle_id=$1
		ORDER BY tm.payout_position NULLS LAST, tm.joined_at`, cycleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contributions []ThriftContributionView
	for rows.Next() {
		var c ThriftContributionView
		if err := rows.Scan(&c.ID, &c.CycleID, &c.GroupID, &c.GroupName, &c.CycleNumber,
			&c.MemberID, &c.UserID, &c.MemberName, &c.PaymentID,
			&c.AmountKobo, &c.Status, &c.PaymentStatus, &c.PaymentReceipt,
			&c.CreatedAt, &c.UpdatedAt, &c.PaidAt); err != nil {
			return nil, err
		}
		contributions = append(contributions, c)
	}
	return contributions, rows.Err()
}

// ListThriftGroups returns recent groups for the admin dashboard.
func (s *Store) ListThriftGroups(ctx context.Context, limit int) ([]ThriftGroupView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id,g.creator_user_id,g.name,g.contribution_amount_kobo,g.frequency,g.target_member_count,
		       g.invite_code,g.status,g.current_cycle,g.created_at,g.updated_at,g.activated_at,g.completed_at,
		       u.display_name,
		       (SELECT COUNT(*) FROM thrift_members tm WHERE tm.group_id=g.id AND tm.status IN ('confirmed','active'))
		FROM thrift_groups g
		JOIN users u ON u.id=g.creator_user_id
		ORDER BY g.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []ThriftGroupView
	for rows.Next() {
		var group ThriftGroupView
		if err := rows.Scan(&group.ID, &group.CreatorUserID, &group.Name, &group.ContributionAmountKobo, &group.Frequency,
			&group.TargetMemberCount, &group.InviteCode, &group.Status, &group.CurrentCycle, &group.CreatedAt,
			&group.UpdatedAt, &group.ActivatedAt, &group.CompletedAt, &group.CreatorName, &group.MemberCount); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// ListThriftPayouts returns recent simulated payouts for admin operations.
func (s *Store) ListThriftPayouts(ctx context.Context, limit int) ([]ThriftPayoutView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tp.id,tp.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,u.display_name,
		       tp.amount_kobo,tp.status,tp.created_at,tp.updated_at,tp.completed_at
		FROM thrift_payouts tp
		JOIN thrift_cycles tcy ON tcy.id=tp.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tp.payout_member_id
		JOIN users u ON u.id=tm.user_id
		ORDER BY tp.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payouts []ThriftPayoutView
	for rows.Next() {
		var payout ThriftPayoutView
		if err := rows.Scan(&payout.ID, &payout.CycleID, &payout.GroupID, &payout.GroupName, &payout.CycleNumber,
			&payout.PayoutMemberID, &payout.PayoutMemberName, &payout.AmountKobo, &payout.Status,
			&payout.CreatedAt, &payout.UpdatedAt, &payout.CompletedAt); err != nil {
			return nil, err
		}
		payouts = append(payouts, payout)
	}
	return payouts, rows.Err()
}

// ListThriftContributions returns recent contribution attempts for admin visibility.
func (s *Store) ListThriftContributions(ctx context.Context, limit int) ([]ThriftContributionView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tc.id,tc.cycle_id,tg.id,tg.name,tcy.cycle_number,tm.id,tm.user_id,u.display_name,
		       tc.payment_id,tc.amount_kobo,tc.status,COALESCE(p.status,''),COALESCE(p.receipt_token,''),
		       tc.created_at,tc.updated_at,tc.paid_at
		FROM thrift_contributions tc
		JOIN thrift_cycles tcy ON tcy.id=tc.cycle_id
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tc.member_id
		JOIN users u ON u.id=tm.user_id
		LEFT JOIN payments p ON p.id=tc.payment_id
		ORDER BY tc.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contributions []ThriftContributionView
	for rows.Next() {
		var contribution ThriftContributionView
		if err := rows.Scan(&contribution.ID, &contribution.CycleID, &contribution.GroupID, &contribution.GroupName, &contribution.CycleNumber,
			&contribution.MemberID, &contribution.UserID, &contribution.MemberName, &contribution.PaymentID,
			&contribution.AmountKobo, &contribution.Status, &contribution.PaymentStatus, &contribution.PaymentReceipt,
			&contribution.CreatedAt, &contribution.UpdatedAt, &contribution.PaidAt); err != nil {
			return nil, err
		}
		contributions = append(contributions, contribution)
	}
	return contributions, rows.Err()
}

// RecentThriftGroupsForUser returns created or joined groups for chat dashboard.
func (s *Store) RecentThriftGroupsForUser(ctx context.Context, userID uuid.UUID, limit int) ([]ThriftGroupView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT g.id,g.creator_user_id,g.name,g.contribution_amount_kobo,g.frequency,g.target_member_count,
		       g.invite_code,g.status,g.current_cycle,g.created_at,g.updated_at,g.activated_at,g.completed_at,
		       u.display_name,
		       (SELECT COUNT(*) FROM thrift_members tm2 WHERE tm2.group_id=g.id AND tm2.status IN ('confirmed','active'))
		FROM thrift_groups g
		JOIN thrift_members tm ON tm.group_id=g.id
		JOIN users u ON u.id=g.creator_user_id
		WHERE g.creator_user_id=$1 OR tm.user_id=$1
		ORDER BY g.created_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []ThriftGroupView
	for rows.Next() {
		var group ThriftGroupView
		if err := rows.Scan(&group.ID, &group.CreatorUserID, &group.Name, &group.ContributionAmountKobo, &group.Frequency,
			&group.TargetMemberCount, &group.InviteCode, &group.Status, &group.CurrentCycle, &group.CreatedAt,
			&group.UpdatedAt, &group.ActivatedAt, &group.CompletedAt, &group.CreatorName, &group.MemberCount); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// ThriftCycleByID returns a cycle with group and payout display fields.
func (s *Store) ThriftCycleByID(ctx context.Context, cycleID uuid.UUID) (ThriftCycleView, error) {
	var cycle ThriftCycleView
	err := s.pool.QueryRow(ctx, `
		SELECT tcy.id,tcy.group_id,tg.name,tcy.cycle_number,tcy.due_at,tcy.payout_member_id,u.display_name,
		       tcy.status,tg.contribution_amount_kobo,tg.target_member_count,tcy.created_at,tcy.updated_at
		FROM thrift_cycles tcy
		JOIN thrift_groups tg ON tg.id=tcy.group_id
		JOIN thrift_members tm ON tm.id=tcy.payout_member_id
		JOIN users u ON u.id=tm.user_id
		WHERE tcy.id=$1`, cycleID).Scan(&cycle.ID, &cycle.GroupID, &cycle.GroupName, &cycle.CycleNumber,
		&cycle.DueAt, &cycle.PayoutMemberID, &cycle.PayoutMemberName, &cycle.Status,
		&cycle.ContributionAmountKobo, &cycle.TargetMemberCount, &cycle.CreatedAt, &cycle.UpdatedAt)
	return cycle, err
}

func insertThriftEvent(ctx context.Context, tx pgx.Tx, groupID, cycleID, memberID uuid.UUID, eventType string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var group any = nil
	if groupID != uuid.Nil {
		group = groupID
	}
	var cycle any = nil
	if cycleID != uuid.Nil {
		cycle = cycleID
	}
	var member any = nil
	if memberID != uuid.Nil {
		member = memberID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO thrift_events(group_id,cycle_id,member_id,event_type,detail)
		VALUES($1,$2,$3,$4,$5)`, group, cycle, member, eventType, raw)
	return err
}

// ListActiveBankTransferAccounts returns collection banks customers can choose.
func (s *Store) ListActiveBankTransferAccounts(ctx context.Context) ([]BankTransferAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, bank_name, account_name, account_number, active, search_keywords, sort_order, created_at
		FROM bank_transfer_accounts
		WHERE active=true
		ORDER BY sort_order, bank_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []BankTransferAccount
	for rows.Next() {
		account, err := scanBankTransferAccount(rows)
		if err != nil {
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
		SELECT id, bank_name, account_name, account_number, active, search_keywords, sort_order, created_at
		FROM bank_transfer_accounts
		WHERE id=$1 AND active=true`, id).Scan(
		&account.ID, &account.BankName, &account.AccountName, &account.AccountNumber,
		&account.Active, &account.SearchKeywords, &account.SortOrder, &account.CreatedAt,
	)
	return account, err
}

// RecommendedBankTransferAccount returns the default collection bank promoted first.
func (s *Store) RecommendedBankTransferAccount(ctx context.Context) (BankTransferAccount, error) {
	var account BankTransferAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, bank_name, account_name, account_number, active, search_keywords, sort_order, created_at
		FROM bank_transfer_accounts
		WHERE active=true
		ORDER BY sort_order, bank_name
		LIMIT 1`).Scan(
		&account.ID, &account.BankName, &account.AccountName, &account.AccountNumber,
		&account.Active, &account.SearchKeywords, &account.SortOrder, &account.CreatedAt,
	)
	return account, err
}

// SearchBankTransferAccounts returns one customer-facing page of active banks.
func (s *Store) SearchBankTransferAccounts(ctx context.Context, query string, offset, limit int) ([]BankTransferAccount, bool, error) {
	offset, limit = normalizePageBounds(offset, limit)
	search := strings.ToLower(strings.TrimSpace(query))
	args := []any{limit + 1, offset}
	sql := `
		SELECT id, bank_name, account_name, account_number, active, search_keywords, sort_order, created_at
		FROM bank_transfer_accounts
		WHERE active=true`
	if search != "" {
		args = append(args, "%"+search+"%")
		sql += ` AND (
			lower(bank_name) LIKE $3 OR lower(account_name) LIKE $3 OR
			lower(account_number) LIKE $3 OR lower(search_keywords) LIKE $3
		)`
	}
	sql += ` ORDER BY sort_order, bank_name LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var accounts []BankTransferAccount
	for rows.Next() {
		account, err := scanBankTransferAccount(rows)
		if err != nil {
			return nil, false, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(accounts) > limit
	if hasMore {
		accounts = accounts[:limit]
	}
	return accounts, hasMore, nil
}

func collectMerchants(rows pgx.Rows) ([]Merchant, error) {
	var merchants []Merchant
	for rows.Next() {
		merchant, err := scanMerchant(rows)
		if err != nil {
			return nil, err
		}
		merchants = append(merchants, merchant)
	}
	return merchants, rows.Err()
}

func scanMerchant(rows pgx.Rows) (Merchant, error) {
	var merchant Merchant
	err := rows.Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active,
		&merchant.SearchKeywords, &merchant.SortOrder, &merchant.CreatedAt,
	)
	return merchant, err
}

func scanBankTransferAccount(rows pgx.Rows) (BankTransferAccount, error) {
	var account BankTransferAccount
	err := rows.Scan(
		&account.ID, &account.BankName, &account.AccountName, &account.AccountNumber,
		&account.Active, &account.SearchKeywords, &account.SortOrder, &account.CreatedAt,
	)
	return account, err
}

func normalizePageBounds(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 25 {
		limit = 10
	}
	return offset, limit
}

// ListActiveDataNetworks returns the mobile networks available for data sales.
func (s *Store) ListActiveDataNetworks(ctx context.Context) ([]DataNetwork, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, code, name, active, sort_order, created_at
		FROM data_networks
		WHERE active=true
		ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var networks []DataNetwork
	for rows.Next() {
		var network DataNetwork
		if err := rows.Scan(&network.ID, &network.Code, &network.Name, &network.Active, &network.SortOrder, &network.CreatedAt); err != nil {
			return nil, err
		}
		networks = append(networks, network)
	}
	return networks, rows.Err()
}

// DataNetworkByCode resolves an active mobile network by its short code.
func (s *Store) DataNetworkByCode(ctx context.Context, code string) (DataNetwork, error) {
	var network DataNetwork
	err := s.pool.QueryRow(ctx, `
		SELECT id, code, name, active, sort_order, created_at
		FROM data_networks
		WHERE upper(code)=upper($1) AND active=true`, code).Scan(
		&network.ID, &network.Code, &network.Name, &network.Active, &network.SortOrder, &network.CreatedAt,
	)
	return network, err
}

// ListActiveDataPlans returns active plans for one network.
func (s *Store) ListActiveDataPlans(ctx context.Context, networkCode string) ([]DataPlan, error) {
	rows, err := s.pool.Query(ctx, dataPlanSelect()+`
		WHERE n.active=true AND p.active=true AND upper(n.code)=upper($1)
		ORDER BY p.sort_order, p.price_kobo`, networkCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []DataPlan
	for rows.Next() {
		plan, err := scanDataPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

// SearchDataPlans returns one customer-facing page of active plans for a network.
func (s *Store) SearchDataPlans(ctx context.Context, networkCode, query string, offset, limit int) ([]DataPlan, bool, error) {
	offset, limit = normalizePageBounds(offset, limit)
	search := strings.ToLower(strings.TrimSpace(query))
	args := []any{limit + 1, offset, networkCode}
	sql := dataPlanSelect() + `
		WHERE n.active=true AND p.active=true AND upper(n.code)=upper($3)`
	if search != "" {
		args = append(args, "%"+search+"%")
		sql += ` AND (
			lower(p.code) LIKE $4 OR lower(p.display_name) LIKE $4 OR
			lower(p.data_size) LIKE $4 OR lower(p.validity) LIKE $4 OR
			lower(p.provider_sku) LIKE $4
		)`
	}
	sql += ` ORDER BY p.sort_order, p.price_kobo, p.display_name LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var plans []DataPlan
	for rows.Next() {
		plan, err := scanDataPlan(rows)
		if err != nil {
			return nil, false, err
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(plans) > limit
	if hasMore {
		plans = plans[:limit]
	}
	return plans, hasMore, nil
}

// DataPlanByCode resolves an active sellable data bundle.
func (s *Store) DataPlanByCode(ctx context.Context, planCode string) (DataPlan, error) {
	var plan DataPlan
	err := s.pool.QueryRow(ctx, dataPlanSelect()+`
		WHERE n.active=true AND p.active=true AND upper(p.code)=upper($1)`, planCode).Scan(
		&plan.ID, &plan.NetworkID, &plan.NetworkCode, &plan.NetworkName, &plan.Code,
		&plan.DisplayName, &plan.DataSize, &plan.Validity, &plan.PriceKobo,
		&plan.ProviderSKU, &plan.Active, &plan.SortOrder, &plan.CreatedAt,
	)
	return plan, err
}

// UpsertDataPlanFromProvider inserts or updates one provider catalog plan.
func (s *Store) UpsertDataPlanFromProvider(ctx context.Context, networkCode, code, displayName, dataSize, validity string, priceKobo int64, providerSKU string, sortOrder int) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO data_plans (network_id, code, display_name, data_size, validity, price_kobo, provider_sku, active, sort_order)
		SELECT n.id, $2, $3, $4, $5, $6, $7, true, $8
		FROM data_networks n
		WHERE upper(n.code)=upper($1)
		ON CONFLICT (code) DO UPDATE SET
			display_name=EXCLUDED.display_name,
			data_size=EXCLUDED.data_size,
			validity=EXCLUDED.validity,
			price_kobo=EXCLUDED.price_kobo,
			provider_sku=EXCLUDED.provider_sku,
			active=true,
			sort_order=EXCLUDED.sort_order,
			updated_at=now()`,
		networkCode, code, displayName, dataSize, validity, priceKobo, providerSKU, sortOrder)
	return err
}

// XegoDataMerchant returns the internal merchant used to reuse the payment rail.
func (s *Store) XegoDataMerchant(ctx context.Context) (Merchant, error) {
	var merchant Merchant
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at
		FROM merchants WHERE slug=$1`, "xego-data").Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords,
		&merchant.SortOrder, &merchant.CreatedAt,
	)
	return merchant, err
}

// CreateDataOrder stores a new draft data purchase with a unique request code.
func (s *Store) CreateDataOrder(ctx context.Context, userID uuid.UUID, channel, recipient, beneficiaryPhone string, plan DataPlan, requestCode string) (DataOrderView, error) {
	id := uuid.New()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DataOrderView{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO data_orders
			(id,user_id,channel,recipient,beneficiary_phone,network_id,plan_id,amount_kobo,status,request_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		id, userID, channel, recipient, beneficiaryPhone, plan.NetworkID, plan.ID, plan.PriceKobo,
		domain.DataOrderDraft, requestCode); err != nil {
		return DataOrderView{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO data_order_events(data_order_id,from_status,to_status,source)
		VALUES($1,'',$2,'conversation')`, id, domain.DataOrderDraft); err != nil {
		return DataOrderView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DataOrderView{}, err
	}
	return s.DataOrderByID(ctx, id)
}

// AttachDataOrderPayment links a data order to its payment and starts waiting for settlement.
func (s *Store) AttachDataOrderPayment(ctx context.Context, orderID, paymentID uuid.UUID) (DataOrderView, error) {
	if _, err := s.transitionDataOrder(ctx, orderID, domain.DataOrderAwaitingPayment, "payment.created", map[string]any{"payment_id": paymentID.String()}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE data_orders SET payment_id=$2 WHERE id=$1`, orderID, paymentID)
		return err
	}); err != nil {
		return DataOrderView{}, err
	}
	return s.DataOrderByID(ctx, orderID)
}

// DataOrderByID returns one data order with display fields.
func (s *Store) DataOrderByID(ctx context.Context, id uuid.UUID) (DataOrderView, error) {
	return s.dataOrderBy(ctx, "d.id=$1", id)
}

// DataOrderByPaymentID returns the data order linked to a payment.
func (s *Store) DataOrderByPaymentID(ctx context.Context, paymentID uuid.UUID) (DataOrderView, error) {
	return s.dataOrderBy(ctx, "d.payment_id=$1", paymentID)
}

// DataOrderByRequestCode returns the data order used for SMS status checks.
func (s *Store) DataOrderByRequestCode(ctx context.Context, code string) (DataOrderView, error) {
	return s.dataOrderBy(ctx, "upper(d.request_code)=upper($1)", code)
}

// DataOrderByProviderReference resolves an order from a VTPass request id or transaction id.
func (s *Store) DataOrderByProviderReference(ctx context.Context, providerReference string) (DataOrderView, error) {
	return s.dataOrderBy(ctx, "d.provider_reference=$1", providerReference)
}

func (s *Store) dataOrderBy(ctx context.Context, predicate string, value any) (DataOrderView, error) {
	query := dataOrderSelect() + " WHERE " + predicate
	var order DataOrderView
	err := s.pool.QueryRow(ctx, query, value).Scan(dataOrderScanTargets(&order)...)
	return order, err
}

// ListDataOrders returns recent data purchases for the read-only dashboard.
func (s *Store) ListDataOrders(ctx context.Context, limit int) ([]DataOrderView, error) {
	rows, err := s.pool.Query(ctx, dataOrderSelect()+` ORDER BY d.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orders []DataOrderView
	for rows.Next() {
		var order DataOrderView
		if err := rows.Scan(dataOrderScanTargets(&order)...); err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

// ClaimFulfillableDataOrders leases paid data orders for provider fulfilment.
func (s *Store) ClaimFulfillableDataOrders(ctx context.Context, limit int) ([]DataOrderView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT d.id
		FROM data_orders d
		JOIN payments p ON p.id=d.payment_id
		WHERE d.status IN ('awaiting_payment','paid') AND p.status='succeeded'
		ORDER BY d.created_at
		FOR UPDATE SKIP LOCKED
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.transitionDataOrderTx(ctx, tx, id, domain.DataOrderPaid, "payment.succeeded", nil, nil); err != nil {
			return nil, err
		}
		if _, err := s.transitionDataOrderTx(ctx, tx, id, domain.DataOrderFulfilling, "data.fulfilment.claim", nil, nil); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	orders := make([]DataOrderView, 0, len(ids))
	for _, id := range ids {
		order, err := s.DataOrderByID(ctx, id)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, nil
}

// CompleteDataOrderFulfilment records a provider result and queues the customer update.
func (s *Store) CompleteDataOrderFulfilment(ctx context.Context, orderID uuid.UUID, target domain.DataOrderStatus, providerReference, message string, outbox OutboxSpec) error {
	return s.transitionDataOrderWithOutbox(ctx, orderID, target, "data.provider", map[string]any{"provider_reference": providerReference, "message": message}, providerReference, message, outbox)
}

// DeferDataOrderFulfilment stores a pending provider reference, returns the
// order to paid for retry/webhook resolution, and queues one customer
// processing update the first time a provider result is inconclusive.
func (s *Store) DeferDataOrderFulfilment(ctx context.Context, orderID uuid.UUID, providerReference, message string, outbox OutboxSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var alreadyNotified bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM message_outbox m
			JOIN data_orders d ON d.user_id=m.user_id
			WHERE d.id=$1
			  AND m.payload->>'body' ILIKE '%Status: PROCESSING%'
			  AND m.payload->>'body' ILIKE '%' || d.request_code || '%'
		)`, orderID).Scan(&alreadyNotified); err != nil {
		return err
	}
	changed, err := s.transitionDataOrderTx(ctx, tx, orderID, domain.DataOrderPaid, "data.provider.pending", map[string]any{"provider_reference": providerReference, "message": message}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE data_orders SET provider_reference=$2, failure_reason='' WHERE id=$1`, orderID, providerReference)
		return err
	})
	if err != nil {
		return err
	}
	if changed && !alreadyNotified && outbox.UserID != uuid.Nil {
		if outbox.Channel == "" {
			outbox.Channel = "whatsapp"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
			VALUES($1,$2,$3,$4,$5)`, outbox.UserID, outbox.Channel, outbox.Recipient, outbox.Kind, outbox.Payload); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) transitionDataOrder(ctx context.Context, orderID uuid.UUID, to domain.DataOrderStatus, source string, detail map[string]any, extra func(pgx.Tx) error) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	changed, err := s.transitionDataOrderTx(ctx, tx, orderID, to, source, detail, extra)
	if err != nil {
		return false, err
	}
	return changed, tx.Commit(ctx)
}

func (s *Store) transitionDataOrderWithOutbox(ctx context.Context, orderID uuid.UUID, to domain.DataOrderStatus, source string, detail map[string]any, providerReference, failureReason string, outbox OutboxSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = s.transitionDataOrderTx(ctx, tx, orderID, to, source, detail, func(tx pgx.Tx) error {
		fulfilledClause := ""
		if to == domain.DataOrderFulfilled {
			fulfilledClause = ", fulfilled_at=COALESCE(fulfilled_at, now())"
		}
		_, err := tx.Exec(ctx, `UPDATE data_orders SET provider_reference=$2, failure_reason=$3`+fulfilledClause+` WHERE id=$1`, orderID, providerReference, failureReason)
		return err
	})
	if err != nil {
		return err
	}
	if outbox.Channel == "" {
		outbox.Channel = "whatsapp"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
		VALUES($1,$2,$3,$4,$5)`, outbox.UserID, outbox.Channel, outbox.Recipient, outbox.Kind, outbox.Payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) transitionDataOrderTx(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, to domain.DataOrderStatus, source string, detail map[string]any, extra func(pgx.Tx) error) (bool, error) {
	var from domain.DataOrderStatus
	if err := tx.QueryRow(ctx, `SELECT status FROM data_orders WHERE id=$1 FOR UPDATE`, orderID).Scan(&from); err != nil {
		return false, err
	}
	if from == to {
		return false, nil
	}
	if !domain.CanTransitionDataOrder(from, to) {
		return false, fmt.Errorf("invalid data order transition %s -> %s", from, to)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE data_orders SET status=$2, updated_at=now() WHERE id=$1`, orderID, to); err != nil {
		return false, err
	}
	if extra != nil {
		if err := extra(tx); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO data_order_events(data_order_id,from_status,to_status,source,detail)
		VALUES($1,$2,$3,$4,$5)`, orderID, from, to, source, raw); err != nil {
		return false, err
	}
	return true, nil
}

// RecordSMSRequest stores an inbound SMS command and its generated response.
func (s *Store) RecordSMSRequest(ctx context.Context, id, sender, body, command, requestCode, status, responseBody, errorMessage string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO sms_requests(provider_message_id,sender,body,command,request_code,processing_status,response_body,error_message,processed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT(provider_message_id) DO NOTHING`, id, sender, body, command, requestCode, status, responseBody, errorMessage)
	return tag.RowsAffected() == 1, err
}

// ReserveSMSRequest deduplicates an inbound SMS before any order side effects.
func (s *Store) ReserveSMSRequest(ctx context.Context, id, sender, body string) (bool, SMSRequestView, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO sms_requests(provider_message_id,sender,body,processing_status)
		VALUES($1,$2,$3,'processing')
		ON CONFLICT(provider_message_id) DO NOTHING`, id, sender, body)
	if err != nil {
		return false, SMSRequestView{}, err
	}
	if tag.RowsAffected() == 1 {
		return true, SMSRequestView{ID: id, Sender: sender, Body: body, ProcessingStatus: "processing"}, nil
	}
	var request SMSRequestView
	err = s.pool.QueryRow(ctx, `
		SELECT provider_message_id,sender,body,command,request_code,processing_status,response_body,error_message,received_at,processed_at
		FROM sms_requests WHERE provider_message_id=$1`, id).Scan(
		&request.ID, &request.Sender, &request.Body, &request.Command, &request.RequestCode,
		&request.ProcessingStatus, &request.ResponseBody, &request.ErrorMessage,
		&request.ReceivedAt, &request.ProcessedAt,
	)
	return false, request, err
}

// CompleteSMSRequest records the command result for operators and duplicate replies.
func (s *Store) CompleteSMSRequest(ctx context.Context, id, command, requestCode, status, responseBody, errorMessage string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sms_requests
		SET command=$2,request_code=$3,processing_status=$4,response_body=$5,error_message=$6,processed_at=now()
		WHERE provider_message_id=$1`, id, command, requestCode, status, responseBody, errorMessage)
	return err
}

// ListSMSRequests returns recent SMS commands for operations review.
func (s *Store) ListSMSRequests(ctx context.Context, limit int) ([]SMSRequestView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT provider_message_id,sender,body,command,request_code,processing_status,response_body,error_message,received_at,processed_at
		FROM sms_requests ORDER BY received_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requests []SMSRequestView
	for rows.Next() {
		var request SMSRequestView
		if err := rows.Scan(&request.ID, &request.Sender, &request.Body, &request.Command, &request.RequestCode, &request.ProcessingStatus, &request.ResponseBody, &request.ErrorMessage, &request.ReceivedAt, &request.ProcessedAt); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func dataPlanSelect() string {
	return `
		SELECT p.id, p.network_id, n.code, n.name, p.code, p.display_name, p.data_size,
			p.validity, p.price_kobo, p.provider_sku, p.active, p.sort_order, p.created_at
		FROM data_plans p
		JOIN data_networks n ON n.id=p.network_id `
}

func scanDataPlan(rows pgx.Rows) (DataPlan, error) {
	var plan DataPlan
	err := rows.Scan(
		&plan.ID, &plan.NetworkID, &plan.NetworkCode, &plan.NetworkName, &plan.Code,
		&plan.DisplayName, &plan.DataSize, &plan.Validity, &plan.PriceKobo,
		&plan.ProviderSKU, &plan.Active, &plan.SortOrder, &plan.CreatedAt,
	)
	return plan, err
}

func dataOrderSelect() string {
	return `
		SELECT d.id,d.user_id,d.payment_id,d.channel,d.recipient,d.beneficiary_phone,
			d.network_id,n.code,n.name,d.plan_id,p.code,p.display_name,p.data_size,p.validity,p.provider_sku,
			d.amount_kobo,d.status,d.request_code,d.provider,d.provider_reference,d.failure_reason,
			u.display_name,u.email,d.created_at,d.updated_at,d.fulfilled_at,
			COALESCE(pay.status,''),COALESCE(pay.receipt_token,''),COALESCE(pay.provider_reference,''),
			COALESCE(pay.provider,''),COALESCE(pay.checkout_url,'')
		FROM data_orders d
		JOIN users u ON u.id=d.user_id
		JOIN data_networks n ON n.id=d.network_id
		JOIN data_plans p ON p.id=d.plan_id
		LEFT JOIN payments pay ON pay.id=d.payment_id`
}

func dataOrderScanTargets(order *DataOrderView) []any {
	return []any{
		&order.ID, &order.UserID, &order.PaymentID, &order.Channel, &order.Recipient,
		&order.BeneficiaryPhone, &order.NetworkID, &order.NetworkCode, &order.NetworkName,
		&order.PlanID, &order.PlanCode, &order.PlanName, &order.DataSize, &order.Validity,
		&order.ProviderSKU, &order.AmountKobo, &order.Status, &order.RequestCode,
		&order.Provider, &order.ProviderReference, &order.FailureReason,
		&order.UserName, &order.UserEmail, &order.CreatedAt, &order.UpdatedAt,
		&order.FulfilledAt, &order.PaymentStatus, &order.PaymentReceipt,
		&order.PaymentReference, &order.PaymentProvider, &order.PaymentCheckoutURL,
	}
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
	if message.Channel == "" {
		message.Channel = "whatsapp"
	}
	if message.Recipient == "" {
		message.Recipient = message.Sender
	}
	payload, err := json.Marshal(map[string]string{"text": message.Text, "interactive": message.Interactive, "username": message.Username})
	if err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO inbound_messages (provider_message_id, channel, sender, recipient, payload)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, message.ID, message.Channel, message.Sender, message.Recipient, payload)
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
		SELECT provider_message_id,channel,sender,recipient,payload,attempts
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
		if err := rows.Scan(&message.ID, &message.Channel, &message.Sender, &message.Recipient, &payload, &message.Attempts); err != nil {
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
		message.Username = normalized["username"]
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
			(id,user_id,merchant_id,amount_kobo,currency,status,provider,provider_reference,channel,recipient,receipt_token)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING created_at, updated_at`
	err = tx.QueryRow(ctx, insert,
		payment.ID, payment.UserID, payment.MerchantID, payment.AmountKobo, payment.Currency,
		payment.Status, payment.Provider, payment.ProviderReference, payment.Channel, payment.Recipient, payment.ReceiptToken,
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
		if outbox.Channel == "" {
			outbox.Channel = "whatsapp"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
			VALUES($1,$2,$3,$4,$5)`, outbox.UserID, outbox.Channel, outbox.Recipient, outbox.Kind, outbox.Payload); err != nil {
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
	if outbox.Channel == "" {
		outbox.Channel = "whatsapp"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
		VALUES($1,$2,$3,$4,$5)`, outbox.UserID, outbox.Channel, outbox.Recipient, outbox.Kind, outbox.Payload); err != nil {
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
		       p.provider_reference,p.channel,p.recipient,p.checkout_url,p.receipt_token,p.failure_reason,
		       p.created_at,p.updated_at,p.paid_at,
		       u.display_name,u.email,COALESCE(u.whatsapp_number,''),m.name,m.slug,u.last_inbound_at
		FROM payments p
		JOIN users u ON u.id=p.user_id
		JOIN merchants m ON m.id=p.merchant_id
		WHERE ` + predicate
	var view PaymentView
	err := s.pool.QueryRow(ctx, query, value).Scan(
		&view.ID, &view.UserID, &view.MerchantID, &view.AmountKobo, &view.Currency,
		&view.Status, &view.Provider, &view.ProviderReference, &view.Channel, &view.Recipient, &view.CheckoutURL,
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
		       p.provider_reference,p.channel,p.recipient,p.checkout_url,p.receipt_token,p.failure_reason,
		       p.created_at,p.updated_at,p.paid_at,
		       u.display_name,u.email,COALESCE(u.whatsapp_number,''),m.name,m.slug,u.last_inbound_at
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
			&view.Status, &view.Provider, &view.ProviderReference, &view.Channel, &view.Recipient, &view.CheckoutURL,
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
	return s.EnqueueTextForChannel(ctx, userID, "whatsapp", recipient, body)
}

// EnqueueTextForChannel adds a durable outbound text notification on the same
// customer channel that originated an action. This keeps scanner instructions
// aligned with WhatsApp and Telegram payment flows.
func (s *Store) EnqueueTextForChannel(ctx context.Context, userID uuid.UUID, channel, recipient, body string) error {
	payload, _ := json.Marshal(map[string]any{"body": body})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
		VALUES($1,$2,$3,'text',$4)`, userID, channel, recipient, payload)
	return err
}

// EnqueueTemplate adds a durable template notification for use outside the service window.
func (s *Store) EnqueueTemplate(ctx context.Context, userID uuid.UUID, recipient, name, locale string, parameters []string) error {
	payload, _ := json.Marshal(map[string]any{"name": name, "locale": locale, "parameters": parameters})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
		VALUES($1,'whatsapp',$2,'template',$3)`, userID, recipient, payload)
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
		SELECT id,channel,recipient,kind,payload,attempts
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
		if err := rows.Scan(&message.ID, &message.Channel, &message.Recipient, &message.Kind, &message.Payload, &message.Attempts); err != nil {
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
			(SELECT count(*) FROM webhook_deliveries WHERE processing_status='failed'),
			(SELECT count(*) FROM data_orders),
			(SELECT count(*) FROM data_orders WHERE status='fulfilled'),
			(SELECT count(*) FROM data_orders WHERE status='failed'),
			(SELECT count(*) FROM sms_requests)
		FROM payments`).Scan(
		&metrics.Users, &metrics.Payments, &metrics.Succeeded, &metrics.Failed,
		&metrics.Pending, &metrics.VolumeKobo, &metrics.WebhookFailures,
		&metrics.DataOrders, &metrics.DataFulfilled, &metrics.DataFailures, &metrics.SMSRequests,
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
	if _, err := tx.Exec(ctx, `DELETE FROM email_verification_codes WHERE expires_at < now() OR created_at < $1`, cutoff); err != nil {
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
	if _, err := tx.Exec(ctx, `DELETE FROM sms_requests WHERE received_at < $1`, cutoff); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM data_orders WHERE created_at < $1`, cutoff); err != nil {
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
