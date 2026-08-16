package domain

import "time"

// Phase 3 event topics published onto the event bus.
const (
	// TopicPaymentSucceeded carries a PaymentSucceeded fact.
	TopicPaymentSucceeded = "payment.succeeded"
	// TopicPaymentFailed carries a PaymentFailed fact.
	TopicPaymentFailed = "payment.failed"
)

// S1 settlement + payout topics.
const (
	// TopicSettlementBatchCreated carries a SettlementBatchCreated fact.
	TopicSettlementBatchCreated = "settlement.batch.created"
	// TopicSettlementBatchProcessed carries a SettlementBatchProcessed fact.
	TopicSettlementBatchProcessed = "settlement.batch.processed"
	// TopicPayoutSucceeded carries a PayoutSucceeded fact.
	TopicPayoutSucceeded = "payout.succeeded"
	// TopicPayoutFailed carries a PayoutFailed fact.
	TopicPayoutFailed = "payout.failed"
)

// PaymentSucceeded is the domain fact emitted when a payment reaches the
// succeeded terminal state. It carries only the facts consumers need; channel
// and recipient are derived by the notification consumer from the payment row.
type PaymentSucceeded struct {
	PaymentID  string    `json:"payment_id"`
	UserID     string    `json:"user_id"`
	MerchantID string    `json:"merchant_id"`
	Reference  string    `json:"reference"`
	Provider   string    `json:"provider"`
	Currency   string    `json:"currency"`
	AmountKobo int64     `json:"amount_kobo"`
	PaidAt     time.Time `json:"paid_at"`
}

// PaymentFailed is the domain fact emitted when a payment reaches the failed
// terminal state.
type PaymentFailed struct {
	PaymentID  string `json:"payment_id"`
	UserID     string `json:"user_id"`
	MerchantID string `json:"merchant_id"`
	Reference  string `json:"reference"`
	Provider   string `json:"provider"`
	Currency   string `json:"currency"`
	AmountKobo int64  `json:"amount_kobo"`
	Source     string `json:"source"`
}

// SettlementBatchCreated is emitted when a merchant's payable is frozen into a
// batch (dr 3100 / cr 3200 posted).
type SettlementBatchCreated struct {
	BatchID    string    `json:"batch_id"`
	BatchNo    string    `json:"batch_no"`
	MerchantID string    `json:"merchant_id"`
	TotalKobo  int64     `json:"total_kobo"`
	Currency   string    `json:"currency"`
	CutoffAt   time.Time `json:"cutoff_at"`
}

// SettlementBatchProcessed is emitted when the payout for a batch completes.
type SettlementBatchProcessed struct {
	BatchID    string `json:"batch_id"`
	BatchNo    string `json:"batch_no"`
	MerchantID string `json:"merchant_id"`
	TotalKobo  int64  `json:"total_kobo"`
	PayoutID   string `json:"payout_id"`
}

// PayoutSucceeded is emitted when funds leave the operating bank for a
// merchant's destination account.
type PayoutSucceeded struct {
	PayoutID    string    `json:"payout_id"`
	BatchNo     string    `json:"batch_no"`
	MerchantID  string    `json:"merchant_id"`
	AmountKobo  int64     `json:"amount_kobo"`
	Currency    string    `json:"currency"`
	ExternalRef string    `json:"external_ref"`
	CompletedAt time.Time `json:"completed_at"`
}

// PayoutFailed is emitted when a provider attempt is declined.
type PayoutFailed struct {
	PayoutID   string `json:"payout_id"`
	BatchNo    string `json:"batch_no"`
	MerchantID string `json:"merchant_id"`
	AmountKobo int64  `json:"amount_kobo"`
	Message    string `json:"message"`
}
