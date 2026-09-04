package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/redact"
)

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
	MediaType   string // image, audio, video, document, photo, voice, ""
	MediaID     string
	MediaMime   string
	Caption     string
	Attempts    int
}

// GatewayEvent is one durable normalized payment-provider notification.
type GatewayEvent struct {
	ID        int64  `json:"-"`
	Event     string `json:"event"`
	Reference string `json:"reference"`
	Message   string `json:"message"`
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

// EnqueueInboundMessage persists a normalized WhatsApp message before webhook acknowledgement.
func (s *Store) EnqueueInboundMessage(ctx context.Context, message InboundMessage) (bool, error) {
	if message.Channel == "" {
		message.Channel = "whatsapp"
	}
	if message.Recipient == "" {
		message.Recipient = message.Sender
	}
	payload, err := json.Marshal(map[string]string{"text": message.Text, "interactive": message.Interactive, "username": message.Username, "media_type": message.MediaType, "media_id": message.MediaID, "media_mime": message.MediaMime, "caption": message.Caption})
	if err != nil {
		return false, err
	}
	sealed, err := s.sealValue(payload)
	if err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO inbound_messages (provider_message_id, channel, sender, recipient, payload, media_type, media_id, media_mime, caption)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, message.ID, message.Channel, message.Sender, message.Recipient, sealed, message.MediaType, message.MediaID, message.MediaMime, message.Caption)
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
		SELECT provider_message_id,channel,sender,recipient,payload,attempts,media_type,media_id,media_mime,caption
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
		var payload string
		if err := rows.Scan(&message.ID, &message.Channel, &message.Sender, &message.Recipient, &payload, &message.Attempts, &message.MediaType, &message.MediaID, &message.MediaMime, &message.Caption); err != nil {
			rows.Close()
			return nil, err
		}
		raw, err := s.openValue(payload)
		if err != nil {
			rows.Close()
			return nil, err
		}
		var normalized map[string]string
		if err := json.Unmarshal(raw, &normalized); err != nil {
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
		WHERE provider_message_id=$1`, id, status, redact.Error(message, redact.DefaultMaxLen), time.Now().Add(delay))
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
			(id,user_id,merchant_id,amount_kobo,currency,status,provider,provider_reference,channel,recipient,receipt_token,checkout_token,merchant_reference,metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb)
		RETURNING created_at, updated_at`
	err = tx.QueryRow(ctx, insert,
		payment.ID, payment.UserID, payment.MerchantID, payment.AmountKobo, payment.Currency,
		payment.Status, payment.Provider, payment.ProviderReference, payment.Channel, payment.Recipient,
		payment.ReceiptToken, payment.CheckoutToken, payment.MerchantReference, metadataValue(payment.Metadata),
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
	var amountKobo int64
	var currency string
	var userID, merchantID uuid.UUID
	var provider, reference string
	if err := tx.QueryRow(ctx, `SELECT status, amount_kobo, currency, user_id, merchant_id, provider, provider_reference FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(&from, &amountKobo, &currency, &userID, &merchantID, &provider, &reference); err != nil {
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
	if to == domain.StatusSucceeded {
		// C16: money-in posting. Customer funds arrive into the operating bank
		// account as a customer float liability; purpose-specific allocations
		// (invoice, thrift pool, sales revenue) follow in their own hooks.
		// Plain merchant collection splits (Xego fee + merchant receivable) are
		// recorded by ApplyPaymentSplits called from PaymentService after the
		// transition succeeds.
		var isInvoice, isThrift, isData bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM invoice_payments WHERE payment_id=$1),
			       EXISTS(SELECT 1 FROM thrift_contributions WHERE payment_id=$1),
			       EXISTS(SELECT 1 FROM data_orders WHERE payment_id=$1)`, paymentID).
			Scan(&isInvoice, &isThrift, &isData); err != nil {
			return false, err
		}
		var tag *uuid.UUID
		if !isThrift && !isData {
			tag = &merchantID
		}
		// W1: wallet-funded payments (provider 'wallet') debit the payer's
		// wallet instead of the operating bank. The debit is atomic with the
		// transition: the wallet must be active (L1+) and hold at least the
		// payment amount, otherwise the whole confirmation fails and no money
		// moves. The rest of the pipeline (splits, settlement) is unchanged —
		// the funds land in the customer float exactly like a gateway payment.
		debitAccount := LedgerAccountOperatingBank
		description := "Payment received"
		if provider == ProviderWallet {
			wallet, err := s.ensureWalletTx(ctx, tx, WalletOwnerUser, userID, "", WalletStatusPending)
			if err != nil {
				return false, err
			}
			if wallet.Status != WalletStatusActive {
				return false, fmt.Errorf("%w: wallet is %s (reach L1 to pay from wallet)", ErrWalletNotActive, wallet.Status)
			}
			balance, err := walletBalanceQ(ctx, tx, wallet.AccountCode)
			if err != nil {
				return false, err
			}
			if balance < amountKobo {
				return false, fmt.Errorf("%w: have %d kobo, need %d kobo", ErrInsufficientWalletBalance, balance, amountKobo)
			}
			debitAccount = wallet.AccountCode
			description = "Payment from wallet"
		}
		if err := s.postLedgerPair(ctx, tx, paymentID.String(), "payment", paymentID.String(),
			debitAccount, LedgerAccountCustomerFloat, currency, description, "system", amountKobo, tag); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source,detail)
		VALUES($1,$2,$3,$4,$5)`, paymentID, from, to, source, raw); err != nil {
		return false, err
	}
	// C9-tiers: terminal non-success states release the money-in allowance
	// reservation taken at draft creation, so an abandoned, failed, expired, or
	// refunded attempt does not consume the payer's daily/monthly budget. The
	// reservation key is the provider reference (unique per attempt).
	if to == domain.StatusFailed || to == domain.StatusAbandoned ||
		to == domain.StatusExpired || to == domain.StatusRefunded {
		if _, err := tx.Exec(ctx, `
			DELETE FROM allowance_usage
			WHERE transaction_ref=$1 AND account_type=$2 AND direction=$3`,
			reference, AccountIndividual, kyc.DirIn); err != nil {
			return false, err
		}
	}
	// Phase 3: emit the terminal domain fact into the transactional outbox.
	// Events are drained onto the event bus by the publisher, so consumers
	// (notifications, compliance) no longer run inline in this transaction.
	if err := s.insertPaymentEvent(ctx, tx, paymentID, from, to, source, userID, merchantID, provider, reference, amountKobo, currency); err != nil {
		return false, err
	}
	if outbox != nil && outbox.Channel != ChannelAPI {
		if outbox.Channel == "" {
			outbox.Channel = "whatsapp"
		}
		sealed, err := s.sealValue(outbox.Payload)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
			VALUES($1,$2,$3,$4,$5)`, outbox.UserID, outbox.Channel, outbox.Recipient, outbox.Kind, sealed); err != nil {
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
		VALUES($1,$2,$3,'interswitch.initialize')`, paymentID, from, domain.StatusInitialized); err != nil {
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

	if !domain.CanTransition(from, domain.StatusPending) {
		return BankTransferInstruction{}, fmt.Errorf("invalid payment transition %s -> %s", from, domain.StatusPending)
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
		VALUES($1,$2,$3,'bank_transfer.initialize',$4)`, paymentID, from, domain.StatusPending, detail); err != nil {
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
	var amountKobo int64
	var currency string
	var merchantID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, provider, amount_kobo, currency, merchant_id FROM payments WHERE id=$1 FOR UPDATE`, paymentID).Scan(&from, &provider, &amountKobo, &currency, &merchantID); err != nil {
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
	// C16: money-in posting, mirroring the card-gateway success path. Thrift and
	// data-order payments stay untagged platform movements; plain merchant
	// collections are tagged and accrue the merchant payable.
	var isInvoice, isThrift, isData bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM invoice_payments WHERE payment_id=$1),
		       EXISTS(SELECT 1 FROM thrift_contributions WHERE payment_id=$1),
		       EXISTS(SELECT 1 FROM data_orders WHERE payment_id=$1)`, paymentID).
		Scan(&isInvoice, &isThrift, &isData); err != nil {
		return false, err
	}
	var tag *uuid.UUID
	if !isThrift && !isData {
		tag = &merchantID
	}
	if err := s.postLedgerPair(ctx, tx, paymentID.String(), "payment", paymentID.String(),
		LedgerAccountOperatingBank, LedgerAccountCustomerFloat, currency, "Payment received (bank transfer)", "system", amountKobo, tag); err != nil {
		return false, err
	}
	if !isInvoice && !isThrift && !isData {
		if err := s.postLedgerPair(ctx, tx, paymentID.String(), "payment", paymentID.String(),
			LedgerAccountCustomerFloat, LedgerAccountMerchantPayable, currency, "Merchant collection accrual", "system", amountKobo, &merchantID); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events(payment_id,from_status,to_status,source,detail)
		VALUES($1,$2,$3,'bank_transfer.user_confirmation',$4)`, paymentID, from, domain.StatusSucceeded, detail); err != nil {
		return false, err
	}
	if outbox.Channel == "" {
		outbox.Channel = "whatsapp"
	}
	sealed, err := s.sealValue(outbox.Payload)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
		VALUES($1,$2,$3,$4,$5)`, outbox.UserID, outbox.Channel, outbox.Recipient, outbox.Kind, sealed); err != nil {
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

// PaymentByCheckoutToken resolves a payment from its hosted-checkout capability.
func (s *Store) PaymentByCheckoutToken(ctx context.Context, token string) (PaymentView, error) {
	return s.paymentBy(ctx, "p.checkout_token=$1", token)
}

func (s *Store) paymentBy(ctx context.Context, predicate string, args ...any) (PaymentView, error) {
	query := `
		SELECT p.id,p.user_id,p.merchant_id,p.amount_kobo,p.currency,p.status,p.provider,
		       p.provider_reference,p.channel,p.recipient,p.checkout_url,p.checkout_token,p.receipt_token,
		       p.merchant_reference,p.metadata,p.failure_reason,
		       p.created_at,p.updated_at,p.paid_at,
		       u.display_name,u.email,COALESCE(u.whatsapp_number,''),m.name,m.slug,u.last_inbound_at
		FROM payments p
		JOIN users u ON u.id=p.user_id
		JOIN merchants m ON m.id=p.merchant_id
		WHERE ` + predicate
	var view PaymentView
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&view.ID, &view.UserID, &view.MerchantID, &view.AmountKobo, &view.Currency,
		&view.Status, &view.Provider, &view.ProviderReference, &view.Channel, &view.Recipient, &view.CheckoutURL,
		&view.CheckoutToken, &view.ReceiptToken, &view.MerchantReference, &view.Metadata, &view.FailureReason,
		&view.CreatedAt, &view.UpdatedAt, &view.PaidAt,
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
		       p.provider_reference,p.channel,p.recipient,p.checkout_url,p.checkout_token,p.receipt_token,
		       p.merchant_reference,p.metadata,p.failure_reason,
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
			&view.CheckoutToken, &view.ReceiptToken, &view.MerchantReference, &view.Metadata, &view.FailureReason,
			&view.CreatedAt, &view.UpdatedAt, &view.PaidAt,
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
		WHERE p.provider='interswitch' AND p.status IN ('initialized','pending') AND p.updated_at < $1
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
		WHERE id=$1`, id, processingStatus, redact.Error(errorMessage, redact.DefaultMaxLen))
	return err
}

// ClaimGatewayWebhooks leases normalized gateway events for a specific provider.
func (s *Store) ClaimGatewayWebhooks(ctx context.Context, provider string, limit int) ([]GatewayEvent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id,payload,attempts
		FROM webhook_deliveries
		WHERE provider=$1 AND processing_status IN ('received','processing')
		  AND signature_valid=true AND available_at <= now()
		ORDER BY received_at
		FOR UPDATE SKIP LOCKED LIMIT $2`, provider, limit)
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
		WHERE id=$1`, id, status, redact.Error(message, redact.DefaultMaxLen), time.Now().Add(delay))
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

// ListFailedWebhooks returns dead-lettered webhook deliveries for admin review.
func (s *Store) ListFailedWebhooks(ctx context.Context, limit int) ([]WebhookView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,provider,event_key,signature_valid,processing_status,error_message,received_at,processed_at
		FROM webhook_deliveries WHERE processing_status='failed' ORDER BY received_at DESC LIMIT $1`, limit)
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

// ReplayDeadLetter resets a dead-lettered webhook for reprocessing.
func (s *Store) ReplayDeadLetter(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET processing_status='received',attempts=0,error_message='',available_at=now()
		WHERE id=$1 AND processing_status='failed'`, id)
	return err
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
	sealed, err := s.sealValue(payload)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
		VALUES($1,$2,$3,'text',$4)`, userID, channel, recipient, sealed)
	return err
}

// EnqueueImageForChannel adds a durable outbound image notification. The image
// data is base64-encoded so the outbox worker can decode and upload it later.
func (s *Store) EnqueueImageForChannel(ctx context.Context, userID uuid.UUID, channel, recipient, imageDataB64, caption string) error {
	payload, _ := json.Marshal(map[string]any{"image_data": imageDataB64, "caption": caption})
	sealed, err := s.sealValue(payload)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
		VALUES($1,$2,$3,'image',$4)`, userID, channel, recipient, sealed)
	return err
}

// EnqueueTemplate adds a durable template notification for use outside the service window.
func (s *Store) EnqueueTemplate(ctx context.Context, userID uuid.UUID, recipient, name, locale string, parameters []string) error {
	payload, _ := json.Marshal(map[string]any{"name": name, "locale": locale, "parameters": parameters})
	sealed, err := s.sealValue(payload)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO message_outbox(user_id,channel,recipient,kind,payload)
		VALUES($1,'whatsapp',$2,'template',$3)`, userID, recipient, sealed)
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
		raw, err := s.openValue(string(message.Payload))
		if err != nil {
			rows.Close()
			return nil, err
		}
		message.Payload = raw
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
		WHERE id=$1`, id, status, nextAttempts, redact.Error(message, redact.DefaultMaxLen), time.Now().Add(delay))
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

// PaymentsByMerchantID returns payments linked to a merchant, most recent first.
func (s *Store) PaymentsByMerchantID(ctx context.Context, merchantID uuid.UUID, limit, offset int) ([]PaymentView, error) {
	offset, limit = normalizePageBounds(offset, limit)
	rows, err := s.pool.Query(ctx, `
		SELECT p.id,p.user_id,p.merchant_id,p.amount_kobo,p.currency,p.status,p.provider,
		       p.provider_reference,p.channel,p.recipient,p.checkout_url,p.checkout_token,p.receipt_token,
		       p.merchant_reference,p.metadata,p.failure_reason,
		       p.created_at,p.updated_at,p.paid_at,
		       u.display_name,u.email,COALESCE(u.whatsapp_number,''),m.name,m.slug,u.last_inbound_at
		FROM payments p
		JOIN users u ON u.id=p.user_id
		JOIN merchants m ON m.id=p.merchant_id
		WHERE p.merchant_id=$1
		ORDER BY p.created_at DESC
		LIMIT $2 OFFSET $3`, merchantID, limit, offset)
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
			&view.CheckoutToken, &view.ReceiptToken, &view.MerchantReference, &view.Metadata, &view.FailureReason,
			&view.CreatedAt, &view.UpdatedAt, &view.PaidAt,
			&view.UserName, &view.UserEmail, &view.WhatsAppNumber, &view.MerchantName, &view.MerchantSlug, &view.LastInboundAt,
		); err != nil {
			return nil, err
		}
		payments = append(payments, view)
	}
	return payments, rows.Err()
}
