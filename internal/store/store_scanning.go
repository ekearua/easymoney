package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
)

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
	PhoneWhitelist       string
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
	ID            uuid.UUID
	PaymentID     uuid.UUID
	ServiceID     uuid.UUID
	ServiceName   string
	Token         string
	ManualCode    string
	ReceiptType   string
	ExpiresAt     time.Time
	ConsumedAt    *time.Time
	RevokedAt     *time.Time
	UsesTotal     int
	UsesRemaining int
	CreatedAt     time.Time
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
	UsesRemaining  int    `json:"uses_remaining,omitempty"`
	UsesTotal      int    `json:"uses_total,omitempty"`
}

// EnsureReceiptScanToken creates a single-use scan token for successful
// payments that match an active registered service. It is idempotent so payment
// webhook retries cannot create multiple customer-facing scan codes.
func (s *Store) EnsureReceiptScanToken(ctx context.Context, paymentID uuid.UUID, uses int) (ReceiptScanTokenView, bool, error) {
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
	if uses < 1 {
		uses = 1
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO receipt_scan_tokens(payment_id,service_id,token,manual_code,receipt_type,expires_at,uses_total,uses_remaining)
		VALUES($1,$2,$3,$4,$5,now()+($6::text || ' seconds')::interval,$7,$7)`,
		paymentID, serviceID, token, manualCode, receiptType, fmt.Sprintf("%d", ttlSeconds), uses)
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
		       rst.receipt_type,rst.expires_at,rst.consumed_at,rst.revoked_at,
		       rst.uses_total,rst.uses_remaining,rst.created_at
		FROM receipt_scan_tokens rst
		JOIN registered_services rs ON rs.id=rst.service_id
		WHERE rst.payment_id=$1`, paymentID).Scan(
		&view.ID, &view.PaymentID, &view.ServiceID, &view.ServiceName, &view.Token, &view.ManualCode,
		&view.ReceiptType, &view.ExpiresAt, &view.ConsumedAt, &view.RevokedAt,
		&view.UsesTotal, &view.UsesRemaining, &view.CreatedAt,
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
		       rs.accepted_receipt_types,rs.token_ttl_seconds,rs.active,rs.phone_whitelist,rs.created_at,rs.updated_at
		FROM registered_services rs
		LEFT JOIN merchants m ON m.id=rs.merchant_id
		WHERE rs.id=$1`, id).Scan(&service.ID, &service.Name, &service.ServiceType, &service.MerchantID, &service.MerchantName, &service.AcceptedReceiptTypes, &service.TokenTTLSeconds, &service.Active, &service.PhoneWhitelist, &service.CreatedAt, &service.UpdatedAt)
	return service, err
}

// ListRegisteredServices returns services for admin scanner setup.
func (s *Store) ListRegisteredServices(ctx context.Context, limit int) ([]RegisteredServiceView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rs.id,rs.name,rs.service_type,rs.merchant_id,COALESCE(m.name,''),
		       rs.accepted_receipt_types,rs.token_ttl_seconds,rs.active,rs.phone_whitelist,rs.created_at,rs.updated_at
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
		if err := rows.Scan(&service.ID, &service.Name, &service.ServiceType, &service.MerchantID, &service.MerchantName, &service.AcceptedReceiptTypes, &service.TokenTTLSeconds, &service.Active, &service.PhoneWhitelist, &service.CreatedAt, &service.UpdatedAt); err != nil {
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
	var phoneWhitelist string
	err = tx.QueryRow(ctx, `
		SELECT rst.id,rst.payment_id,rst.service_id,rs.name,rst.token,rst.manual_code,rst.receipt_type,
		       rst.expires_at,rst.consumed_at,rst.revoked_at,rst.uses_total,rst.uses_remaining,rst.created_at,
		       p.id,p.user_id,p.merchant_id,p.amount_kobo,p.currency,p.status,p.provider,p.provider_reference,
		       p.channel,p.recipient,p.checkout_url,p.checkout_token,p.receipt_token,p.failure_reason,p.created_at,p.updated_at,p.paid_at,
		       u.display_name,u.email,COALESCE(u.whatsapp_number,''),m.name,m.slug,u.last_inbound_at,
		       rs.active,rs.phone_whitelist
		FROM receipt_scan_tokens rst
		JOIN registered_services rs ON rs.id=rst.service_id
		JOIN payments p ON p.id=rst.payment_id
		JOIN users u ON u.id=p.user_id
		JOIN merchants m ON m.id=p.merchant_id
		WHERE rst.token=$1 OR rst.manual_code=$1
		FOR UPDATE`, tokenValue).Scan(
		&token.ID, &token.PaymentID, &token.ServiceID, &token.ServiceName, &token.Token, &token.ManualCode,
		&token.ReceiptType, &token.ExpiresAt, &token.ConsumedAt, &token.RevokedAt,
		&token.UsesTotal, &token.UsesRemaining, &token.CreatedAt,
		&payment.ID, &payment.UserID, &payment.MerchantID, &payment.AmountKobo, &payment.Currency,
		&payment.Status, &payment.Provider, &payment.ProviderReference, &payment.Channel, &payment.Recipient,
		&payment.CheckoutURL, &payment.CheckoutToken, &payment.ReceiptToken, &payment.FailureReason, &payment.CreatedAt,
		&payment.UpdatedAt, &payment.PaidAt, &payment.UserName, &payment.UserEmail, &payment.WhatsAppNumber,
		&payment.MerchantName, &payment.MerchantSlug, &payment.LastInboundAt, &serviceActive, &phoneWhitelist,
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
	if status == "valid_consumed" && phoneWhitelist != "" {
		if !phoneInWhitelist(payment.WhatsAppNumber, phoneWhitelist) {
			status = "phone_not_whitelisted"
		}
	}
	if status == "valid_consumed" {
		tag, err := tx.Exec(ctx, `
			UPDATE receipt_scan_tokens
			SET uses_remaining = uses_remaining - 1,
			    consumed_at = CASE WHEN uses_remaining <= 1 THEN now() ELSE consumed_at END
			WHERE id=$1 AND uses_remaining > 0 AND consumed_at IS NULL`, token.ID)
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
		UsesRemaining: token.UsesRemaining - 1,
		UsesTotal:     token.UsesTotal,
	}
	if status == "valid_consumed" {
		result.ConsumedAtText = time.Now().Format(time.RFC3339)
	}
	return result, tx.Commit(ctx)
}

// MerchantValidateScanToken validates a scan token from the merchant portal,
// checking ownership via the merchant's services instead of a reader API key.
func (s *Store) MerchantValidateScanToken(ctx context.Context, merchantID uuid.UUID, tokenOrURL string, remoteAddr string) (ReceiptScanResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReceiptScanResult{}, err
	}
	defer tx.Rollback(ctx)
	tokenValue := ExtractScanToken(tokenOrURL)
	if tokenValue == "" {
		return ReceiptScanResult{Status: "unknown_token"}, nil
	}
	var token ReceiptScanTokenView
	var payment PaymentView
	var serviceActive bool
	var phoneWhitelist string
	err = tx.QueryRow(ctx, `
		SELECT rst.id,rst.payment_id,rst.service_id,rs.name,rst.token,rst.manual_code,rst.receipt_type,
		       rst.expires_at,rst.consumed_at,rst.revoked_at,rst.uses_total,rst.uses_remaining,rst.created_at,
		       p.id,p.user_id,p.merchant_id,p.amount_kobo,p.currency,p.status,p.provider,p.provider_reference,
		       p.channel,p.recipient,p.checkout_url,p.checkout_token,p.receipt_token,p.failure_reason,p.created_at,p.updated_at,p.paid_at,
		       u.display_name,u.email,COALESCE(u.whatsapp_number,''),m.name,m.slug,u.last_inbound_at,
		       rs.active,rs.phone_whitelist
		FROM receipt_scan_tokens rst
		JOIN registered_services rs ON rs.id=rst.service_id
		JOIN payments p ON p.id=rst.payment_id
		JOIN users u ON u.id=p.user_id
		JOIN merchants m ON m.id=p.merchant_id
		WHERE (rst.token=$1 OR rst.manual_code=$1) AND p.merchant_id=$2
		FOR UPDATE`, tokenValue, merchantID).Scan(
		&token.ID, &token.PaymentID, &token.ServiceID, &token.ServiceName, &token.Token, &token.ManualCode,
		&token.ReceiptType, &token.ExpiresAt, &token.ConsumedAt, &token.RevokedAt,
		&token.UsesTotal, &token.UsesRemaining, &token.CreatedAt,
		&payment.ID, &payment.UserID, &payment.MerchantID, &payment.AmountKobo, &payment.Currency,
		&payment.Status, &payment.Provider, &payment.ProviderReference, &payment.Channel, &payment.Recipient,
		&payment.CheckoutURL, &payment.CheckoutToken, &payment.ReceiptToken, &payment.FailureReason, &payment.CreatedAt,
		&payment.UpdatedAt, &payment.PaidAt, &payment.UserName, &payment.UserEmail, &payment.WhatsAppNumber,
		&payment.MerchantName, &payment.MerchantSlug, &payment.LastInboundAt, &serviceActive, &phoneWhitelist,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Commit(ctx)
		return ReceiptScanResult{Status: "unknown_token"}, nil
	}
	if err != nil {
		return ReceiptScanResult{}, err
	}
	now := time.Now()
	status := "valid_consumed"
	switch {
	case !serviceActive:
		status = "reader_not_authorized"
	case payment.Status != domain.StatusSucceeded:
		status = "not_paid"
	case token.RevokedAt != nil:
		status = "revoked"
	case now.After(token.ExpiresAt):
		status = "expired"
	case token.UsesRemaining <= 0:
		status = "already_used"
	}
	if status == "valid_consumed" && phoneWhitelist != "" {
		if !phoneInWhitelist(payment.WhatsAppNumber, phoneWhitelist) {
			status = "phone_not_whitelisted"
		}
	}
	if status == "valid_consumed" {
		tag, err := tx.Exec(ctx, `
			UPDATE receipt_scan_tokens
			SET uses_remaining = uses_remaining - 1,
			    consumed_at = CASE WHEN uses_remaining <= 1 THEN now() ELSE consumed_at END
			WHERE id=$1 AND uses_remaining > 0 AND consumed_at IS NULL`, token.ID)
		if err != nil {
			return ReceiptScanResult{}, err
		}
		if tag.RowsAffected() != 1 {
			status = "already_used"
		}
	}
	_ = recordScanAttemptTx(ctx, tx,
		uuid.NullUUID{UUID: token.ID, Valid: true},
		uuid.NullUUID{UUID: token.ServiceID, Valid: true},
		uuid.NullUUID{},
		status, remoteAddr, map[string]any{"receipt_type": token.ReceiptType, "source": "merchant_portal"})
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
		UsesRemaining: token.UsesRemaining - 1,
		UsesTotal:     token.UsesTotal,
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
	case token.UsesRemaining <= 0:
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
		       rst.receipt_type,rst.expires_at,rst.consumed_at,rst.revoked_at,
		       rst.uses_total,rst.uses_remaining,rst.created_at
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
		if err := rows.Scan(&token.ID, &token.PaymentID, &token.ServiceID, &token.ServiceName, &token.Token, &token.ManualCode, &token.ReceiptType, &token.ExpiresAt, &token.ConsumedAt, &token.RevokedAt, &token.UsesTotal, &token.UsesRemaining, &token.CreatedAt); err != nil {
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

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func normalizeStorePhone(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "+") {
		return value
	}
	return "+" + value
}

func phoneInWhitelist(phone, whitelist string) bool {
	phone = normalizeStorePhone(phone)
	for _, entry := range strings.Split(whitelist, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if normalizeStorePhone(entry) == phone {
			return true
		}
	}
	return false
}
