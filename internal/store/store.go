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
	ID                  uuid.UUID
	WhatsAppNumber      string
	DisplayName         string
	Email               string
	OnboardingComplete  bool
	WhatsAppVerifiedAt  sql.NullTime
	NumberConfirmedAt   sql.NullTime
	EmailVerifiedAt     sql.NullTime
	VerificationLevel   string
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
		RETURNING id, COALESCE(whatsapp_number,''), display_name, email, onboarding_complete,
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level,
			telegram_chat_id, telegram_user_id, telegram_username, telegram_verified_at, telegram_confirmed_at,
			last_inbound_at, created_at, updated_at`
	var user User
	err := s.pool.QueryRow(ctx, query, number).Scan(
		&user.ID, &user.WhatsAppNumber, &user.DisplayName, &user.Email,
		&user.OnboardingComplete, &user.WhatsAppVerifiedAt, &user.NumberConfirmedAt,
		&user.EmailVerifiedAt, &user.VerificationLevel, &user.TelegramChatID,
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
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level,
			telegram_chat_id, telegram_user_id, telegram_username, telegram_verified_at, telegram_confirmed_at,
			last_inbound_at, created_at, updated_at`
	var user User
	err := s.pool.QueryRow(ctx, query, chatID, userID, username).Scan(
		&user.ID, &user.WhatsAppNumber, &user.DisplayName, &user.Email,
		&user.OnboardingComplete, &user.WhatsAppVerifiedAt, &user.NumberConfirmedAt,
		&user.EmailVerifiedAt, &user.VerificationLevel, &user.TelegramChatID,
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
			whatsapp_verified_at, number_confirmed_at, email_verified_at, verification_level,
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
			&user.EmailVerifiedAt, &user.VerificationLevel, &user.TelegramChatID,
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
	return s.MerchantBySlug(ctx, "xego-data")
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

// DeferDataOrderFulfilment stores a pending provider reference and returns the
// order to paid so a later worker pass or webhook can resolve the final state.
func (s *Store) DeferDataOrderFulfilment(ctx context.Context, orderID uuid.UUID, providerReference, message string) error {
	_, err := s.transitionDataOrder(ctx, orderID, domain.DataOrderPaid, "data.provider.pending", map[string]any{"provider_reference": providerReference, "message": message}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE data_orders SET provider_reference=$2, failure_reason='' WHERE id=$1`, orderID, providerReference)
		return err
	})
	return err
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
	payload, _ := json.Marshal(map[string]any{"body": body})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
		VALUES($1,'whatsapp',$2,'text',$3)`, userID, recipient, payload)
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
