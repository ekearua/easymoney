package domain

import "time"

// Phase 3 event topics published onto the event bus.
const (
	// TopicPaymentSucceeded carries a PaymentSucceeded fact.
	TopicPaymentSucceeded = "payment.succeeded"
	// TopicPaymentFailed carries a PaymentFailed fact.
	TopicPaymentFailed = "payment.failed"
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
