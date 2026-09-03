package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Merchant is a curated payment recipient.
type Merchant struct {
	ID                    uuid.UUID
	Slug                  string
	Name                  string
	Category              string
	Description           string
	LogoURL               string
	Active                bool
	SearchKeywords        string
	SortOrder             int
	CreatedAt             time.Time
	PasswordHash          string
	AllowPartialPayments  bool
	MinInvoiceAmountKobo  int64
	UpfrontPercent        int
	MinInstallmentPercent int
	MaxInstallments       int
	AllowFullPayAlways    bool
}

// MerchantNotificationPrefs holds per-merchant notification opt-in flags.
type MerchantNotificationPrefs struct {
	NotifyPayment    bool
	NotifyRefund     bool
	NotifySettlement bool
	NotifyApproval   bool
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

// MerchantService is a service or event created by a merchant for customers to pay for.
type MerchantService struct {
	ID                uuid.UUID
	MerchantID        uuid.UUID
	Name              string
	Description       string
	UnitPriceKobo     int64
	QuantityAvailable int
	ExpiresAt         *time.Time
	IsActive          bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ServicePurchaseView links a service purchase to a payment with customer info.
type ServicePurchaseView struct {
	ID             uuid.UUID
	ServiceID      uuid.UUID
	ServiceName    string
	PaymentID      uuid.UUID
	UserID         uuid.UUID
	UserName       string
	WhatsAppNumber string
	Quantity       int
	UnitPriceKobo  int64
	TotalKobo      int64
	CreatedAt      time.Time
}

// ServiceCustomField is a custom data field defined by a merchant for a service.
type ServiceCustomField struct {
	ID           uuid.UUID
	ServiceID    uuid.UUID
	FieldName    string
	FieldType    string
	FieldOptions string
	IsRequired   bool
	SortOrder    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ServicePurchaseCustomDataValue holds one custom field value for a purchase.
type ServicePurchaseCustomDataValue struct {
	FieldName  string
	FieldValue string
}

// CustomFieldSpec is input for creating/updating a custom field definition.
type CustomFieldSpec struct {
	FieldName    string
	FieldType    string
	FieldOptions string
	IsRequired   bool
	SortOrder    int
}

func newMerchantRegistrationReference() string {
	return "XG-MER-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
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
	query := `SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at, password_hash, allow_partial_payments, min_invoice_amount_kobo, upfront_percent, min_installment_percent, max_installments, allow_full_pay_always FROM merchants`
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
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at,
		       password_hash, allow_partial_payments, min_invoice_amount_kobo, upfront_percent,
		       min_installment_percent, max_installments, allow_full_pay_always
		FROM merchants WHERE slug=$1 AND active=true`, slug).Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords,
		&merchant.SortOrder, &merchant.CreatedAt,
		&merchant.PasswordHash, &merchant.AllowPartialPayments,
		&merchant.MinInvoiceAmountKobo, &merchant.UpfrontPercent,
		&merchant.MinInstallmentPercent, &merchant.MaxInstallments,
		&merchant.AllowFullPayAlways,
	)
	return merchant, err
}

// MerchantNotificationPrefs returns the notification opt-in flags for a merchant.
func (s *Store) MerchantNotificationPrefs(ctx context.Context, merchantID uuid.UUID) (MerchantNotificationPrefs, error) {
	var p MerchantNotificationPrefs
	err := s.pool.QueryRow(ctx, `
		SELECT notify_payment, notify_refund, notify_settlement, notify_approval
		FROM merchants WHERE id=$1`, merchantID).Scan(&p.NotifyPayment, &p.NotifyRefund, &p.NotifySettlement, &p.NotifyApproval)
	return p, err
}

// UpdateMerchantNotificationPrefs sets the notification opt-in flags for a merchant.
func (s *Store) UpdateMerchantNotificationPrefs(ctx context.Context, merchantID uuid.UUID, p MerchantNotificationPrefs) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchants SET notify_payment=$2, notify_refund=$3, notify_settlement=$4, notify_approval=$5
		WHERE id=$1`, merchantID, p.NotifyPayment, p.NotifyRefund, p.NotifySettlement, p.NotifyApproval)
	return err
}

// SearchMerchants returns one customer-facing page of active merchants.
func (s *Store) SearchMerchants(ctx context.Context, query string, offset, limit int) ([]Merchant, bool, error) {
	offset, limit = normalizePageBounds(offset, limit)
	search := strings.ToLower(strings.TrimSpace(query))
	args := []any{limit + 1, offset}
	sql := `
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at,
		       password_hash, allow_partial_payments, min_invoice_amount_kobo, upfront_percent,
		       min_installment_percent, max_installments, allow_full_pay_always
		FROM merchants
		WHERE active=true`
	if search != "" {
		args = append(args, search)
		sql += ` AND (
			word_similarity(lower(name), $3) > 0.4 OR
			word_similarity(lower(category), $3) > 0.4 OR
			word_similarity(lower(description), $3) > 0.4 OR
			word_similarity(lower(search_keywords), $3) > 0.4
		)`
		sql += ` ORDER BY GREATEST(
			word_similarity(lower(name), $3),
			word_similarity(lower(category), $3),
			word_similarity(lower(description), $3),
			word_similarity(lower(search_keywords), $3)
		) DESC, sort_order, name`
	} else {
		sql += ` ORDER BY sort_order, name`
	}
	sql += ` LIMIT $1 OFFSET $2`
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
		SELECT m.id, m.slug, m.name, m.category, m.description, m.logo_url, m.active,
		       m.search_keywords, m.sort_order, m.created_at,
		       m.password_hash, m.allow_partial_payments, m.min_invoice_amount_kobo, m.upfront_percent,
		       m.min_installment_percent, m.max_installments, m.allow_full_pay_always
		FROM merchants m
		WHERE m.active=true
			AND NOT EXISTS (
				SELECT 1 FROM user_merchant_recents r
				WHERE r.user_id=$3 AND r.merchant_id=m.id
			)`
	if search != "" {
		args = append(args, search)
		sql += ` AND (
			word_similarity(lower(m.name), $4) > 0.4 OR
			word_similarity(lower(m.category), $4) > 0.4 OR
			word_similarity(lower(m.description), $4) > 0.4 OR
			word_similarity(lower(m.search_keywords), $4) > 0.4
		)`
		sql += ` ORDER BY GREATEST(
			word_similarity(lower(m.name), $4),
			word_similarity(lower(m.category), $4),
			word_similarity(lower(m.description), $4),
			word_similarity(lower(m.search_keywords), $4)
		) DESC, m.sort_order, m.name`
	} else {
		sql += ` ORDER BY m.sort_order, m.name`
	}
	sql += ` LIMIT $1 OFFSET $2`
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
			m.search_keywords, m.sort_order, m.created_at,
			m.password_hash, m.allow_partial_payments, m.min_invoice_amount_kobo, m.upfront_percent,
			m.min_installment_percent, m.max_installments, m.allow_full_pay_always
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
		RETURNING id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at,
		          password_hash, allow_partial_payments, min_invoice_amount_kobo, upfront_percent,
		          min_installment_percent, max_installments, allow_full_pay_always`,
		slug, request.BusinessName, request.Category, request.Description,
		strings.ToLower(request.BusinessName+" "+request.Category+" "+request.Description),
	).Scan(&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category, &merchant.Description,
		&merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords, &merchant.SortOrder, &merchant.CreatedAt,
		&merchant.PasswordHash, &merchant.AllowPartialPayments,
		&merchant.MinInvoiceAmountKobo, &merchant.UpfrontPercent,
		&merchant.MinInstallmentPercent, &merchant.MaxInstallments,
		&merchant.AllowFullPayAlways)
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
		       m.search_keywords, m.sort_order, m.created_at,
		       m.password_hash, m.allow_partial_payments, m.min_invoice_amount_kobo, m.upfront_percent,
		       m.min_installment_percent, m.max_installments, m.allow_full_pay_always
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
		&merchant.PasswordHash, &merchant.AllowPartialPayments,
		&merchant.MinInvoiceAmountKobo, &merchant.UpfrontPercent,
		&merchant.MinInstallmentPercent, &merchant.MaxInstallments,
		&merchant.AllowFullPayAlways,
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

// MerchantByID returns a single merchant by its ID.
func (s *Store) MerchantByID(ctx context.Context, id uuid.UUID) (Merchant, error) {
	var merchant Merchant
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, category, description, logo_url, active, search_keywords, sort_order, created_at,
		       password_hash, allow_partial_payments, min_invoice_amount_kobo, upfront_percent,
		       min_installment_percent, max_installments, allow_full_pay_always
		FROM merchants WHERE id=$1`, id).Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords,
		&merchant.SortOrder, &merchant.CreatedAt,
		&merchant.PasswordHash, &merchant.AllowPartialPayments,
		&merchant.MinInvoiceAmountKobo, &merchant.UpfrontPercent,
		&merchant.MinInstallmentPercent, &merchant.MaxInstallments,
		&merchant.AllowFullPayAlways,
	)
	return merchant, err
}

// ServicesByMerchantID returns registered services belonging to a merchant.
func (s *Store) ServicesByMerchantID(ctx context.Context, merchantID uuid.UUID) ([]RegisteredServiceView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rs.id,rs.name,rs.service_type,rs.merchant_id,COALESCE(m.name,''),
		       rs.accepted_receipt_types,rs.token_ttl_seconds,rs.active,rs.phone_whitelist,rs.created_at,rs.updated_at
		FROM registered_services rs
		LEFT JOIN merchants m ON m.id=rs.merchant_id
		WHERE rs.merchant_id=$1
		ORDER BY rs.created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var services []RegisteredServiceView
	for rows.Next() {
		var service RegisteredServiceView
		if err := rows.Scan(&service.ID, &service.Name, &service.ServiceType, &service.MerchantID, &service.MerchantName, &service.AcceptedReceiptTypes, &service.TokenTTLSeconds, &service.Active, &service.PhoneWhitelist, &service.CreatedAt, &service.UpdatedAt); err != nil {
			return nil, err
		}
		services = append(services, service)
	}
	return services, rows.Err()
}

// MerchantByEmail returns the merchant whose owner has the given email address.
func (s *Store) MerchantByEmail(ctx context.Context, email string) (Merchant, error) {
	var merchant Merchant
	err := s.pool.QueryRow(ctx, `
		SELECT m.id, m.slug, m.name, m.category, m.description, m.logo_url, m.active,
		       m.search_keywords, m.sort_order, m.created_at,
		       m.password_hash, m.allow_partial_payments, m.min_invoice_amount_kobo, m.upfront_percent,
		       m.min_installment_percent, m.max_installments, m.allow_full_pay_always
		FROM merchants m
		JOIN merchant_owners mo ON mo.merchant_id=m.id
		JOIN users u ON u.id=mo.user_id
		WHERE LOWER(u.email)=LOWER($1) AND m.active=true
		ORDER BY mo.created_at ASC
		LIMIT 1`, email).Scan(
		&merchant.ID, &merchant.Slug, &merchant.Name, &merchant.Category,
		&merchant.Description, &merchant.LogoURL, &merchant.Active, &merchant.SearchKeywords,
		&merchant.SortOrder, &merchant.CreatedAt,
		&merchant.PasswordHash, &merchant.AllowPartialPayments,
		&merchant.MinInvoiceAmountKobo, &merchant.UpfrontPercent,
		&merchant.MinInstallmentPercent, &merchant.MaxInstallments,
		&merchant.AllowFullPayAlways,
	)
	return merchant, err
}

// MerchantOwnerID returns the user ID of the primary owner of a merchant.
func (s *Store) MerchantOwnerID(ctx context.Context, merchantID uuid.UUID) (uuid.UUID, error) {
	var userID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT user_id FROM merchant_owners
		WHERE merchant_id=$1
		ORDER BY created_at ASC LIMIT 1`, merchantID).Scan(&userID)
	return userID, err
}

// CreateMerchantSession stores a hashed session token and returns the raw token.
func (s *Store) CreateMerchantSession(ctx context.Context, merchantID, userID uuid.UUID, token, csrf string, expiresAt time.Time) error {
	hash := sha256.Sum256([]byte(token))
	sealedCSRF, err := s.sealValue([]byte(csrf))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO merchant_sessions(token_hash,merchant_id,user_id,csrf_token,expires_at)
		VALUES($1,$2,$3,$4,$5)`, hash[:], merchantID, userID, sealedCSRF, expiresAt)
	return err
}

// ValidateMerchantSession checks a session token and returns the merchant_id and user_id.
func (s *Store) ValidateMerchantSession(ctx context.Context, token string) (uuid.UUID, uuid.UUID, string, error) {
	hash := sha256.Sum256([]byte(token))
	var merchantID, userID uuid.UUID
	var sealedCSRF string
	err := s.pool.QueryRow(ctx, `
		SELECT merchant_id,user_id,csrf_token FROM merchant_sessions
		WHERE token_hash=$1 AND expires_at > now()`, hash[:]).Scan(&merchantID, &userID, &sealedCSRF)
	if err != nil {
		return merchantID, userID, "", err
	}
	raw, err := s.openValue(sealedCSRF)
	if err != nil {
		return merchantID, userID, "", err
	}
	return merchantID, userID, string(raw), nil
}

// DeleteMerchantSession invalidates one merchant login.
func (s *Store) DeleteMerchantSession(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx, `DELETE FROM merchant_sessions WHERE token_hash=$1`, hash[:])
	return err
}

// CreateMerchantPasswordResetToken stores a hashed one-time token for the merchant to set their password.
func (s *Store) CreateMerchantPasswordResetToken(ctx context.Context, merchantID, userID uuid.UUID, token string, expiresAt time.Time) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO merchant_password_reset_tokens(token_hash,merchant_id,user_id,expires_at)
		VALUES($1,$2,$3,$4)`, hash[:], merchantID, userID, expiresAt)
	return err
}

// ValidateMerchantPasswordResetToken checks a reset token is valid and unused.
func (s *Store) ValidateMerchantPasswordResetToken(ctx context.Context, token string) (uuid.UUID, uuid.UUID, error) {
	hash := sha256.Sum256([]byte(token))
	var merchantID, userID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT merchant_id,user_id FROM merchant_password_reset_tokens
		WHERE token_hash=$1 AND expires_at > now() AND used_at IS NULL`, hash[:]).Scan(&merchantID, &userID)
	return merchantID, userID, err
}

// UseMerchantPasswordResetToken marks a reset token as consumed.
func (s *Store) UseMerchantPasswordResetToken(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_password_reset_tokens SET used_at=now()
		WHERE token_hash=$1 AND used_at IS NULL`, hash[:])
	return err
}

// UpdateMerchantPaymentTerms updates a merchant's partial payment settings.
func (s *Store) UpdateMerchantPaymentTerms(ctx context.Context, merchantID uuid.UUID, allowPartial bool, minInvoiceKobo int64, upfrontPct, minInstallPct, maxInstallments int, allowFullAlways bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchants SET
			allow_partial_payments=$2,
			min_invoice_amount_kobo=$3,
			upfront_percent=$4,
			min_installment_percent=$5,
			max_installments=$6,
			allow_full_pay_always=$7,
			updated_at=now()
		WHERE id=$1`, merchantID, allowPartial, minInvoiceKobo, upfrontPct, minInstallPct, maxInstallments, allowFullAlways)
	return err
}

// UpdateMerchantPassword sets the password hash for a merchant.
func (s *Store) UpdateMerchantPassword(ctx context.Context, merchantID uuid.UUID, passwordHash string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchants SET password_hash=$2, updated_at=now() WHERE id=$1`, merchantID, passwordHash)
	return err
}

// UpdateMerchantProfile updates a merchant's display fields.
func (s *Store) UpdateMerchantProfile(ctx context.Context, merchantID uuid.UUID, name, category, description, logoURL string) error {
	keywords := strings.ToLower(name + " " + category + " " + description)
	_, err := s.pool.Exec(ctx, `
		UPDATE merchants SET name=$2, category=$3, description=$4, logo_url=$5,
			search_keywords=$6, updated_at=now()
		WHERE id=$1`, merchantID, name, category, description, logoURL, keywords)
	return err
}

// UpdateServicePhoneWhitelist sets the comma-separated E.164 whitelist for a service.
func (s *Store) UpdateServicePhoneWhitelist(ctx context.Context, serviceID uuid.UUID, whitelist string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE registered_services SET phone_whitelist=$2, updated_at=now() WHERE id=$1`, serviceID, whitelist)
	return err
}

// CreateMerchantService creates a new service for a merchant.
func (s *Store) CreateMerchantService(ctx context.Context, merchantID uuid.UUID, name, description string, unitPriceKobo int64, quantityAvailable int, expiresAt *time.Time) (MerchantService, error) {
	id := uuid.New()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO merchant_services(id,merchant_id,name,description,unit_price_kobo,quantity_available,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, id, merchantID, name, description, unitPriceKobo, quantityAvailable, expiresAt)
	if err != nil {
		return MerchantService{}, err
	}
	return s.MerchantServiceByID(ctx, id)
}

// UpdateMerchantService updates a merchant's service.
func (s *Store) UpdateMerchantService(ctx context.Context, serviceID uuid.UUID, name, description string, unitPriceKobo int64, quantityAvailable int, expiresAt *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_services SET
			name=$2, description=$3, unit_price_kobo=$4, quantity_available=$5,
			expires_at=$6, updated_at=now()
		WHERE id=$1`, serviceID, name, description, unitPriceKobo, quantityAvailable, expiresAt)
	return err
}

// MerchantServiceByID returns one service by ID.
func (s *Store) MerchantServiceByID(ctx context.Context, id uuid.UUID) (MerchantService, error) {
	var svc MerchantService
	err := s.pool.QueryRow(ctx, `
		SELECT id,merchant_id,name,description,unit_price_kobo,quantity_available,expires_at,is_active,created_at,updated_at
		FROM merchant_services WHERE id=$1`, id).Scan(
		&svc.ID, &svc.MerchantID, &svc.Name, &svc.Description, &svc.UnitPriceKobo,
		&svc.QuantityAvailable, &svc.ExpiresAt, &svc.IsActive, &svc.CreatedAt, &svc.UpdatedAt)
	return svc, err
}

// ListMerchantServices returns all services for a merchant, ordered by active+non-expired first.
func (s *Store) ListMerchantServices(ctx context.Context, merchantID uuid.UUID) ([]MerchantService, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,merchant_id,name,description,unit_price_kobo,quantity_available,expires_at,is_active,created_at,updated_at
		FROM merchant_services
		WHERE merchant_id=$1
		ORDER BY
			CASE WHEN is_active=true AND (expires_at IS NULL OR expires_at > now()) THEN 0 ELSE 1 END,
			created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var services []MerchantService
	for rows.Next() {
		var svc MerchantService
		if err := rows.Scan(&svc.ID, &svc.MerchantID, &svc.Name, &svc.Description, &svc.UnitPriceKobo,
			&svc.QuantityAvailable, &svc.ExpiresAt, &svc.IsActive, &svc.CreatedAt, &svc.UpdatedAt); err != nil {
			return nil, err
		}
		services = append(services, svc)
	}
	return services, rows.Err()
}

// ListActiveMerchantServices returns active, non-expired services for a merchant.
func (s *Store) ListActiveMerchantServices(ctx context.Context, merchantID uuid.UUID) ([]MerchantService, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,merchant_id,name,description,unit_price_kobo,quantity_available,expires_at,is_active,created_at,updated_at
		FROM merchant_services
		WHERE merchant_id=$1 AND is_active=true AND (expires_at IS NULL OR expires_at > now())
		ORDER BY created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var services []MerchantService
	for rows.Next() {
		var svc MerchantService
		if err := rows.Scan(&svc.ID, &svc.MerchantID, &svc.Name, &svc.Description, &svc.UnitPriceKobo,
			&svc.QuantityAvailable, &svc.ExpiresAt, &svc.IsActive, &svc.CreatedAt, &svc.UpdatedAt); err != nil {
			return nil, err
		}
		services = append(services, svc)
	}
	return services, rows.Err()
}

// ToggleMerchantService toggles is_active for a service, verifying the merchant owns it.
func (s *Store) ToggleMerchantService(ctx context.Context, serviceID, merchantID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_services SET is_active=NOT is_active, updated_at=now()
		WHERE id=$1 AND merchant_id=$2`, serviceID, merchantID)
	return err
}

// CreateServicePurchase records a service purchase.
func (s *Store) CreateServicePurchase(ctx context.Context, serviceID, paymentID uuid.UUID, quantity int, unitPriceKobo, totalKobo int64) (uuid.UUID, error) {
	id := uuid.New()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO service_purchases(id,service_id,payment_id,quantity,unit_price_kobo,total_kobo)
		VALUES($1,$2,$3,$4,$5,$6)`, id, serviceID, paymentID, quantity, unitPriceKobo, totalKobo)
	return id, err
}

// ConfirmServicePurchase decrements inventory when a service payment succeeds.
func (s *Store) ConfirmServicePurchase(ctx context.Context, paymentID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// The applied flag (added in 056) makes the hook idempotent: a retried
	// hook cannot decrement inventory a second time for the same purchase.
	if _, err := tx.Exec(ctx, `
		UPDATE merchant_services ms
		SET quantity_available = ms.quantity_available - sp.quantity, updated_at=now()
		FROM service_purchases sp
		WHERE sp.payment_id=$1 AND ms.id=sp.service_id
		  AND NOT sp.applied
		  AND ms.quantity_available >= sp.quantity`, paymentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE service_purchases
		SET applied = true
		WHERE payment_id=$1 AND NOT applied`, paymentID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ServicePurchasesByMerchantID returns all purchases for a merchant's services.
func (s *Store) ServicePurchasesByMerchantID(ctx context.Context, merchantID uuid.UUID) ([]ServicePurchaseView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sp.id,sp.service_id,ms.name,sp.payment_id,p.user_id,
		       u.display_name,COALESCE(u.whatsapp_number,''),
		       sp.quantity,sp.unit_price_kobo,sp.total_kobo,sp.created_at
		FROM service_purchases sp
		JOIN merchant_services ms ON ms.id=sp.service_id
		JOIN payments p ON p.id=sp.payment_id
		JOIN users u ON u.id=p.user_id
		WHERE ms.merchant_id=$1
		ORDER BY sp.created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var purchases []ServicePurchaseView
	for rows.Next() {
		var p ServicePurchaseView
		if err := rows.Scan(&p.ID, &p.ServiceID, &p.ServiceName, &p.PaymentID, &p.UserID,
			&p.UserName, &p.WhatsAppNumber, &p.Quantity, &p.UnitPriceKobo, &p.TotalKobo, &p.CreatedAt); err != nil {
			return nil, err
		}
		purchases = append(purchases, p)
	}
	return purchases, rows.Err()
}

// ServicePurchasesByServiceID returns purchases for a specific service.
func (s *Store) ServicePurchasesByServiceID(ctx context.Context, serviceID uuid.UUID) ([]ServicePurchaseView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sp.id,sp.service_id,ms.name,sp.payment_id,p.user_id,
		       u.display_name,COALESCE(u.whatsapp_number,''),
		       sp.quantity,sp.unit_price_kobo,sp.total_kobo,sp.created_at
		FROM service_purchases sp
		JOIN merchant_services ms ON ms.id=sp.service_id
		JOIN payments p ON p.id=sp.payment_id
		JOIN users u ON u.id=p.user_id
		WHERE sp.service_id=$1
		ORDER BY sp.created_at DESC`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var purchases []ServicePurchaseView
	for rows.Next() {
		var p ServicePurchaseView
		if err := rows.Scan(&p.ID, &p.ServiceID, &p.ServiceName, &p.PaymentID, &p.UserID,
			&p.UserName, &p.WhatsAppNumber, &p.Quantity, &p.UnitPriceKobo, &p.TotalKobo, &p.CreatedAt); err != nil {
			return nil, err
		}
		purchases = append(purchases, p)
	}
	return purchases, rows.Err()
}

// UpdateMerchantServiceTTL updates the token_ttl_seconds on a merchant's registered service.
func (s *Store) UpdateMerchantServiceTTL(ctx context.Context, merchantID uuid.UUID, ttlSeconds int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE registered_services SET token_ttl_seconds=$2, updated_at=now()
		WHERE merchant_id=$1 AND service_type='merchant'`, merchantID, ttlSeconds)
	return err
}

// MerchantServiceTTL returns the current token TTL in seconds for a merchant's registered service.
func (s *Store) MerchantServiceTTL(ctx context.Context, merchantID uuid.UUID) (int, error) {
	var ttl int
	err := s.pool.QueryRow(ctx, `
		SELECT token_ttl_seconds FROM registered_services
		WHERE merchant_id=$1 AND service_type='merchant'`, merchantID).Scan(&ttl)
	return ttl, err
}

// ServicePurchaseQuantityByPaymentID returns the quantity purchased for a payment, or 1 if none.
func (s *Store) ServicePurchaseQuantityByPaymentID(ctx context.Context, paymentID uuid.UUID) (int, error) {
	var qty int
	err := s.pool.QueryRow(ctx, `
		SELECT quantity FROM service_purchases WHERE payment_id=$1`, paymentID).Scan(&qty)
	if errors.Is(err, pgx.ErrNoRows) {
		return 1, nil
	}
	return qty, err
}

// SetServiceCustomFields replaces all custom fields for a service (delete + insert in a transaction).
func (s *Store) SetServiceCustomFields(ctx context.Context, serviceID uuid.UUID, fields []CustomFieldSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM service_custom_fields WHERE service_id=$1`, serviceID); err != nil {
		return err
	}
	for i, f := range fields {
		if f.FieldName == "" {
			continue
		}
		fieldType := f.FieldType
		if fieldType == "" {
			fieldType = "text"
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO service_custom_fields(service_id,field_name,field_type,field_options,is_required,sort_order)
			VALUES($1,$2,$3,$4,$5,$6)`, serviceID, f.FieldName, fieldType, f.FieldOptions, f.IsRequired, i)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ListServiceCustomFields returns all custom fields for a service ordered by sort_order.
func (s *Store) ListServiceCustomFields(ctx context.Context, serviceID uuid.UUID) ([]ServiceCustomField, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,service_id,field_name,field_type,field_options,is_required,sort_order,created_at,updated_at
		FROM service_custom_fields
		WHERE service_id=$1
		ORDER BY sort_order`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fields []ServiceCustomField
	for rows.Next() {
		var f ServiceCustomField
		if err := rows.Scan(&f.ID, &f.ServiceID, &f.FieldName, &f.FieldType, &f.FieldOptions, &f.IsRequired, &f.SortOrder, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return fields, rows.Err()
}

// SavePurchaseCustomData stores custom field values for a service purchase.
func (s *Store) SavePurchaseCustomData(ctx context.Context, purchaseID uuid.UUID, fieldMap map[string]string) error {
	for fieldName, fieldValue := range fieldMap {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO service_purchase_custom_data(purchase_id,field_id,field_name,field_value)
			SELECT $1, id, $3, $4 FROM service_custom_fields WHERE service_id=(
				SELECT service_id FROM service_purchases WHERE id=$1
			) AND field_name=$2
			LIMIT 1`, purchaseID, fieldName, fieldName, fieldValue)
		if err != nil {
			return err
		}
	}
	return nil
}

// PurchaseCustomDataByPurchaseIDs returns custom data for a set of purchase IDs.
// Returns a map of purchaseID → []ServicePurchaseCustomDataValue.
func (s *Store) PurchaseCustomDataByPurchaseIDs(ctx context.Context, purchaseIDs []uuid.UUID) (map[uuid.UUID][]ServicePurchaseCustomDataValue, error) {
	if len(purchaseIDs) == 0 {
		return map[uuid.UUID][]ServicePurchaseCustomDataValue{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT purchase_id, field_name, field_value
		FROM service_purchase_custom_data
		WHERE purchase_id = ANY($1)
		ORDER BY purchase_id, field_name`, purchaseIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[uuid.UUID][]ServicePurchaseCustomDataValue)
	for rows.Next() {
		var pid uuid.UUID
		var v ServicePurchaseCustomDataValue
		if err := rows.Scan(&pid, &v.FieldName, &v.FieldValue); err != nil {
			return nil, err
		}
		result[pid] = append(result[pid], v)
	}
	return result, rows.Err()
}
