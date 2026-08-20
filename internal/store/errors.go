package store

import "errors"

var (
	ErrNothingToSettle         = errors.New("nothing to settle")
	ErrPaymentInSettlementBatch = errors.New("payment is in a processed settlement batch")
)
