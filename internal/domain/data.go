package domain

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DataOrderStatus is the durable state of a mobile data purchase.
type DataOrderStatus string

const (
	DataOrderDraft           DataOrderStatus = "draft"
	DataOrderAwaitingPayment DataOrderStatus = "awaiting_payment"
	DataOrderPaid            DataOrderStatus = "paid"
	DataOrderFulfilling      DataOrderStatus = "fulfilling"
	DataOrderFulfilled       DataOrderStatus = "fulfilled"
	DataOrderFailed          DataOrderStatus = "failed"
	DataOrderExpired         DataOrderStatus = "expired"
	DataOrderCancelled       DataOrderStatus = "cancelled"
)

var dataTransitions = map[DataOrderStatus]map[DataOrderStatus]bool{
	DataOrderDraft:           {DataOrderAwaitingPayment: true, DataOrderCancelled: true, DataOrderExpired: true},
	DataOrderAwaitingPayment: {DataOrderPaid: true, DataOrderCancelled: true, DataOrderExpired: true},
	DataOrderPaid:            {DataOrderFulfilling: true, DataOrderFailed: true, DataOrderExpired: true},
	DataOrderFulfilling:      {DataOrderFulfilled: true, DataOrderFailed: true},
}

// CanTransitionDataOrder reports whether a data-order state change is monotonic.
func CanTransitionDataOrder(from, to DataOrderStatus) bool {
	if from == to {
		return true
	}
	return dataTransitions[from][to]
}

// NormalizeNigerianPhone converts common local input into +234 E.164 format.
func NormalizeNigerianPhone(input string) (string, error) {
	cleaned := strings.TrimSpace(input)
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, "(", "")
	cleaned = strings.ReplaceAll(cleaned, ")", "")
	if strings.HasPrefix(cleaned, "0") && len(cleaned) == 11 {
		cleaned = "+234" + cleaned[1:]
	} else if strings.HasPrefix(cleaned, "234") && len(cleaned) == 13 {
		cleaned = "+" + cleaned
	}
	if !regexp.MustCompile(`^\+234[789][01][0-9]{8}$`).MatchString(cleaned) {
		return "", errors.New("enter a valid Nigerian mobile number, like 08031234567")
	}
	return cleaned, nil
}

// NewDataRequestCode returns a short human-readable code for SMS status checks.
func NewDataRequestCode() (string, error) {
	var raw [5]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate data request code: %w", err)
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	return "XG-DATA-" + code[:4] + code[4:8], nil
}
