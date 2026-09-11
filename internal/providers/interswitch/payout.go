package interswitch

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"whatsapp-payment-demo/internal/ports"
)

// transferPath is the Quickteller v5 single transfer (NIP) endpoint.
const transferPath = "/quicktellerservice/api/v5/transactions/TransferFunds"

type transferRequest struct {
	AccountFrom    string `json:"accountFrom"`
	AccountTo      string `json:"accountTo"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
	Fee            string `json:"fee"`
	SenderName     string `json:"senderName"`
	TransactionRef string `json:"transactionRef"`
	Narration      string `json:"narration"`
	TransferType   string `json:"transferType"`
	BankCode       string `json:"bankCode"`
	Source         string `json:"source"`
	RecipientName  string `json:"recipientName"`
	AccountType    string `json:"accountType"`
}

type transferResponse struct {
	ResponseCode        string `json:"responseCode"`
	ResponseDescription string `json:"responseDescription"`
	Progress            int    `json:"progress"`
	SessionID           string `json:"sessionId"`
	TransferRef         string `json:"transferRef"`
	TxnStatus           string `json:"txnStatus"`
}

// Payout pushes funds out through the Quickteller single transfer (NIP) rail.
// Request.Reference is the idempotency key; Interswitch processes a given
// transactionRef exactly once, matching the settlement dispatch contract. A
// 90000 response means the transfer was accepted by the NIP rail.
func (c *Client) Payout(ctx context.Context, request ports.PayoutRequest) (ports.PayoutResult, error) {
	if c.sourceAccount == "" {
		return ports.PayoutResult{}, errors.New("interswitch: source account is required for payouts (INTERSWITCH_SOURCE_ACCOUNT)")
	}
	if strings.TrimSpace(request.AccountNumber) == "" || strings.TrimSpace(request.BankCode) == "" {
		return ports.PayoutResult{}, errors.New("interswitch: destination account and bank code are required")
	}
	var resp transferResponse
	if err := c.doAuthorized(ctx, http.MethodPost, transferPath, transferRequest{
		AccountFrom:    c.sourceAccount,
		AccountTo:      strings.TrimSpace(request.AccountNumber),
		Amount:         strconv.FormatInt(request.AmountKobo, 10),
		Currency:       "NGN",
		Fee:            "0",
		SenderName:     "Xego",
		TransactionRef: request.Reference,
		Narration:      "Xego payout",
		TransferType:   "TOACCOUNT",
		BankCode:       strings.TrimSpace(request.BankCode),
		Source:         "BANK",
		RecipientName:  strings.TrimSpace(request.AccountName),
		AccountType:    "00",
	}, &resp); err != nil {
		return ports.PayoutResult{}, err
	}
	if resp.ResponseCode != "90000" {
		message := strings.TrimSpace(resp.ResponseDescription)
		if message == "" {
			message = "Interswitch declined the transfer (responseCode " + resp.ResponseCode + ")"
		}
		return ports.PayoutResult{Status: "failed", Message: message}, nil
	}
	externalRef := strings.TrimSpace(resp.TransferRef)
	if externalRef == "" {
		externalRef = strings.TrimSpace(resp.SessionID)
	}
	if externalRef == "" {
		externalRef = request.Reference
	}
	return ports.PayoutResult{
		ExternalRef: externalRef,
		Status:      "succeeded",
		Message:     strings.TrimSpace(resp.ResponseDescription),
	}, nil
}