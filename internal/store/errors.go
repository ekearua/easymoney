package store

import "errors"

var (
	ErrNothingToSettle          = errors.New("nothing to settle")
	ErrPaymentInSettlementBatch = errors.New("payment is in a processed settlement batch")
	// ErrPaymentNotSucceeded means a refund was requested for a payment that is
	// no longer 'succeeded' — already refunded, failed, abandoned, and so on.
	// Callers use it to skip replay-safe operations like the auto-refund of a
	// superseded web-flow attempt.
	ErrPaymentNotSucceeded = errors.New("payment is not succeeded")
)
