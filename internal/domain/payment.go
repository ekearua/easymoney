// Package domain defines provider-neutral payment concepts and invariants.
package domain

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PaymentStatus is the durable state of a payment attempt.
type PaymentStatus string

const (
	StatusDraft                PaymentStatus = "draft"
	StatusAwaitingConfirmation PaymentStatus = "awaiting_confirmation"
	StatusInitialized          PaymentStatus = "initialized"
	StatusPending              PaymentStatus = "pending"
	StatusSucceeded            PaymentStatus = "succeeded"
	StatusFailed               PaymentStatus = "failed"
	StatusAbandoned            PaymentStatus = "abandoned"
	StatusExpired              PaymentStatus = "expired"
)

var validTransitions = map[PaymentStatus]map[PaymentStatus]bool{
	StatusDraft:                {StatusAwaitingConfirmation: true, StatusExpired: true},
	StatusAwaitingConfirmation: {StatusInitialized: true, StatusAbandoned: true, StatusExpired: true},
	StatusInitialized:          {StatusPending: true, StatusSucceeded: true, StatusFailed: true, StatusAbandoned: true, StatusExpired: true},
	StatusPending:              {StatusSucceeded: true, StatusFailed: true, StatusAbandoned: true, StatusExpired: true},
}

// CanTransition reports whether a state change preserves the monotonic payment lifecycle.
func CanTransition(from, to PaymentStatus) bool {
	if from == to {
		return true
	}
	return validTransitions[from][to]
}

// ParseNGNAmount converts a human-entered naira amount into integer kobo.
func ParseNGNAmount(input string, minKobo, maxKobo int64) (int64, error) {
	cleaned := strings.NewReplacer(",", "", "₦", "", "NGN", "", "ngn", "", " ", "").Replace(input)
	if cleaned == "" {
		return 0, errors.New("enter an amount")
	}
	parts := strings.Split(cleaned, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, errors.New("enter a valid NGN amount")
	}
	naira, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || naira < 0 {
		return 0, errors.New("enter a valid NGN amount")
	}
	kobo := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) > 2 {
			return 0, errors.New("amount can have at most two decimal places")
		}
		fraction := parts[1] + strings.Repeat("0", 2-len(parts[1]))
		kobo, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, errors.New("enter a valid NGN amount")
		}
	}
	total := naira*100 + kobo
	if total < minKobo || total > maxKobo {
		return 0, fmt.Errorf("amount must be between %s and %s", FormatNGN(minKobo), FormatNGN(maxKobo))
	}
	return total, nil
}

// FormatNGN formats integer kobo for customer-facing copy.
func FormatNGN(kobo int64) string {
	naira := kobo / 100
	fraction := kobo % 100
	grouped := strconv.FormatInt(naira, 10)
	for i := len(grouped) - 3; i > 0; i -= 3 {
		grouped = grouped[:i] + "," + grouped[i:]
	}
	if fraction == 0 {
		return "₦" + grouped
	}
	return fmt.Sprintf("₦%s.%02d", grouped, fraction)
}

// NewProviderReference returns an opaque, unique reference suitable for Paystack.
func NewProviderReference() string {
	return "wpd_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// NewReceiptToken returns a cryptographically random URL-safe receipt capability.
func NewReceiptToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate receipt token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// Payment is the provider-neutral aggregate persisted by the service.
type Payment struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	MerchantID        uuid.UUID
	AmountKobo        int64
	Currency          string
	Status            PaymentStatus
	Provider          string
	ProviderReference string
	Channel           string
	Recipient         string
	CheckoutURL       string
	ReceiptToken      string
	FailureReason     string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	PaidAt            *time.Time
}
