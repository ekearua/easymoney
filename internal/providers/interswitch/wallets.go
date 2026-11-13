package interswitch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"whatsapp-payment-demo/internal/ports"
)

const (
	merchantWalletCreatePath      = "/api/v1/wallet"
	merchantWalletDetailsPath     = "/api/v1/wallet/details/"
	merchantWalletBalancePath     = "/api/v1/wallet/balance/"
	merchantWalletTransactPath    = "/api/v1/transaction/transact"
	merchantWalletReversePath     = "/api/v1/transaction/reverse"
	merchantWalletTransactionPath = "/api/v1/transaction/"
)

// MerchantWalletCreateRequest contains the identity fields Interswitch
// requires to issue a merchant-owned customer wallet. PIN is sent only to the
// provider over HTTPS and must not be persisted or logged by the caller.
type MerchantWalletCreateRequest struct {
	Name         string
	MerchantCode string
	Status       string
	MobileNumber string
	Provider     string
	FirstName    string
	PIN          string
	Email        string
}

type merchantWalletCreatePayload struct {
	Name         string `json:"name"`
	MerchantCode string `json:"merchantCode"`
	Status       string `json:"status"`
	MobileNumber string `json:"mobileNo"`
	Provider     string `json:"provider"`
	FirstName    string `json:"firstName"`
	PIN          string `json:"pin"`
	Email        string `json:"email"`
}

// MerchantWallet is the provider-created wallet and its linked receiving
// account. WalletID is the customer-facing identifier used by wallet APIs.
type MerchantWallet struct {
	ID                   int64                  `json:"id"`
	Provider             string                 `json:"provider"`
	CurrencyCode         string                 `json:"currencyCode"`
	WalletID             string                 `json:"walletId"`
	MerchantCode         string                 `json:"merchantCode"`
	Status               string                 `json:"status"`
	WalletCreationStatus string                 `json:"walletCreationStatus"`
	VirtualAccount       MerchantVirtualAccount `json:"virtualAccount"`
}

// MerchantVirtualAccount identifies the receiving bank account Interswitch
// returns with a wallet. The wallet's parent ID is retained separately from its
// external WalletID because the API examples use both identifiers.
type MerchantVirtualAccount struct {
	ID                 int64  `json:"id"`
	WalletID           int64  `json:"walletId"`
	MerchantCode       string `json:"merchantCode"`
	AccountPayableCode string `json:"accountPayableCode"`
	AccountName        string `json:"accountName"`
	AccountNumber      string `json:"accountNumber"`
	BankName           string `json:"bankName"`
	BankCode           string `json:"bankCode"`
	CurrencyCode       string `json:"currencyCode"`
}

// MerchantWalletBalance is the provider's balance view, in the provider's
// returned currency units (minor units for NGN).
type MerchantWalletBalance struct {
	ResponseCode    string `json:"responseCode"`
	ResponseMessage string `json:"responseMessage"`
	Available       int64  `json:"availableBalance"`
	Ledger          int64  `json:"ledgerBalance"`
	Successful      bool   `json:"successful"`
}

// MerchantWalletTransactionRequest describes one provider-side debit. PIN is
// never retained by this client after the HTTPS request is sent.
type MerchantWalletTransactionRequest struct {
	WalletID        string
	Amount          int64
	PIN             string
	Reference       string
	TransactionCode string
	Narration       string
}

// MerchantWalletTransaction is the normalized result of a wallet debit.
type MerchantWalletTransaction struct {
	ResponseCode    string `json:"responseCode"`
	ResponseMessage string `json:"responseMessage"`
	Reference       string `json:"reference"`
	Status          string `json:"status"`
}

type merchantWalletTransactionPayload struct {
	WalletID        string `json:"walletId"`
	Amount          string `json:"amount"`
	PIN             string `json:"pin"`
	Reference       string `json:"reference"`
	TransactionCode string `json:"transactionCode"`
	Narration       string `json:"narration"`
}

type merchantWalletTransactionResponse struct {
	ResponseCode    string `json:"responseCode"`
	ResponseMessage string `json:"responseMessage"`
	ResponseDesc    string `json:"responseDescription"`
	Reference       string `json:"reference"`
	TransactionRef  string `json:"transactionReference"`
	Status          string `json:"status"`
}

type merchantWalletTransactionRecord struct {
	MerchantWalletID    int64  `json:"merchantWalletId"`
	Narration           string `json:"narration"`
	TransactionCode     string `json:"transactionCode"`
	TransactionDirection string `json:"transactionDirection"`
	Amount              int64  `json:"amount"`
	ResponseCode        string `json:"responseCode"`
	ResponseDescription string `json:"responseDescription"`
	PaymentReference    string `json:"paymentReference"`
	Reference           string `json:"reference"`
	Reversed            bool   `json:"reversed"`
	CurrencyCode        string `json:"currencyCode"`
}

type merchantWalletReversalResponse struct {
	ResponseCode string `json:"responseCode"`
	Data         struct {
		ReversalReference string `json:"reversalReference"`
		OriginalReference string `json:"originalReference"`
		Status            string `json:"status"`
	} `json:"data"`
}

// CreateMerchantWallet provisions one Interswitch wallet and its linked
// virtual account using the published Merchant Wallet API.
func (c *Client) CreateMerchantWallet(ctx context.Context, request MerchantWalletCreateRequest) (MerchantWallet, error) {
	if strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.MerchantCode) == "" ||
		strings.TrimSpace(request.MobileNumber) == "" || strings.TrimSpace(request.Provider) == "" ||
		strings.TrimSpace(request.FirstName) == "" || strings.TrimSpace(request.Email) == "" {
		return MerchantWallet{}, errors.New("interswitch wallet: name, merchant, mobile, provider, first name and email are required")
	}
	if len(request.PIN) != 4 || !digitsOnly(request.PIN) {
		return MerchantWallet{}, errors.New("interswitch wallet: PIN must be four digits")
	}
	if strings.TrimSpace(request.Status) == "" {
		request.Status = "ACTIVE"
	}

	payload := merchantWalletCreatePayload{
		Name:         strings.TrimSpace(request.Name),
		MerchantCode: strings.TrimSpace(request.MerchantCode),
		Status:       strings.ToUpper(strings.TrimSpace(request.Status)),
		MobileNumber: strings.TrimSpace(request.MobileNumber),
		Provider:     strings.ToUpper(strings.TrimSpace(request.Provider)),
		FirstName:    strings.TrimSpace(request.FirstName),
		PIN:          request.PIN,
		Email:        strings.TrimSpace(request.Email),
	}
	var wallet MerchantWallet
	if err := c.doAuthorizedOn(ctx, c.walletBaseURL, http.MethodPost, merchantWalletCreatePath, payload, &wallet); err != nil {
		// Provider error bodies can echo the submitted wallet PIN. Do not expose
		// that body through an error that a caller might log; the create outcome
		// remains ambiguous until an operator/provider lookup resolves it.
		return MerchantWallet{}, errors.New("interswitch wallet create outcome is unknown")
	}
	if strings.TrimSpace(wallet.WalletID) == "" {
		return MerchantWallet{}, errors.New("interswitch wallet create returned no wallet ID")
	}
	return wallet, nil
}

// MerchantWalletDetails returns the provider's current wallet identity and
// status without exposing the wallet PIN.
func (c *Client) MerchantWalletDetails(ctx context.Context, merchantCode, walletID string) (MerchantWallet, error) {
	merchantCode = strings.TrimSpace(merchantCode)
	walletID = strings.TrimSpace(walletID)
	if merchantCode == "" || walletID == "" {
		return MerchantWallet{}, errors.New("interswitch wallet: merchant code and wallet ID are required")
	}
	query := url.Values{"walletId": {walletID}}
	path := merchantWalletDetailsPath + url.PathEscape(merchantCode) + "?" + query.Encode()
	var wallet MerchantWallet
	if err := c.doAuthorizedOn(ctx, c.walletBaseURL, http.MethodGet, path, nil, &wallet); err != nil {
		return MerchantWallet{}, fmt.Errorf("interswitch wallet details: %w", err)
	}
	return wallet, nil
}

// MerchantWalletBalance returns the provider's available and ledger balances.
func (c *Client) MerchantWalletBalance(ctx context.Context, merchantCode, walletID string) (MerchantWalletBalance, error) {
	merchantCode = strings.TrimSpace(merchantCode)
	walletID = strings.TrimSpace(walletID)
	if merchantCode == "" || walletID == "" {
		return MerchantWalletBalance{}, errors.New("interswitch wallet: merchant code and wallet ID are required")
	}
	query := url.Values{"walletId": {walletID}}
	path := merchantWalletBalancePath + url.PathEscape(merchantCode) + "?" + query.Encode()
	var balance MerchantWalletBalance
	if err := c.doAuthorizedOn(ctx, c.walletBaseURL, http.MethodGet, path, nil, &balance); err != nil {
		return MerchantWalletBalance{}, fmt.Errorf("interswitch wallet balance: %w", err)
	}
	return balance, nil
}

// MerchantWalletTransactionByReference looks up a provider wallet movement by
// its stable reference. The response is used to reconcile ambiguous debit and
// linked-virtual-account credit outcomes without repeating a money movement.
func (c *Client) MerchantWalletTransactionByReference(ctx context.Context, merchantCode, reference string) (ports.CustomerWalletTransaction, error) {
	merchantCode = strings.TrimSpace(merchantCode)
	reference = strings.TrimSpace(reference)
	if merchantCode == "" || reference == "" {
		return ports.CustomerWalletTransaction{}, errors.New("interswitch wallet: merchant code and reference are required")
	}
	query := url.Values{"merchantCode": {merchantCode}, "reference": {reference}}
	var records []merchantWalletTransactionRecord
	if err := c.doAuthorizedOn(ctx, c.walletBaseURL, http.MethodGet, merchantWalletTransactionPath+"?"+query.Encode(), nil, &records); err != nil {
		return ports.CustomerWalletTransaction{}, fmt.Errorf("interswitch wallet transaction lookup: %w", err)
	}
	for _, record := range records {
		gotReference := firstNonEmptyString(record.Reference, record.PaymentReference)
		if gotReference != reference {
			continue
		}
		return ports.CustomerWalletTransaction{
			Reference: gotReference, ResponseCode: strings.TrimSpace(record.ResponseCode),
			Direction: strings.ToUpper(strings.TrimSpace(record.TransactionDirection)),
			AmountKobo: record.Amount, CurrencyCode: strings.TrimSpace(record.CurrencyCode),
			WalletRecordID: record.MerchantWalletID, Reversed: record.Reversed,
		}, nil
	}
	return ports.CustomerWalletTransaction{}, errors.New("interswitch wallet transaction not found")
}

// CustomerWalletBalance adapts the documented Merchant Wallet balance query
// to the provider-neutral service contract.
func (c *Client) CustomerWalletBalance(ctx context.Context, merchantCode, walletID string) (ports.CustomerWalletBalance, error) {
	balance, err := c.MerchantWalletBalance(ctx, merchantCode, walletID)
	if err != nil {
		return ports.CustomerWalletBalance{}, err
	}
	if !balance.Successful || balance.ResponseCode != "00" {
		return ports.CustomerWalletBalance{}, errors.New("interswitch wallet balance was not confirmed")
	}
	return ports.CustomerWalletBalance{
		AvailableKobo: balance.Available,
		LedgerKobo:    balance.Ledger,
		CurrencyCode:  "566",
	}, nil
}

// CustomerWalletTransaction adapts the documented transaction lookup to the
// provider-neutral service contract.
func (c *Client) CustomerWalletTransaction(ctx context.Context, merchantCode, reference string) (ports.CustomerWalletTransaction, error) {
	return c.MerchantWalletTransactionByReference(ctx, merchantCode, reference)
}

// DebitCustomerWallet adapts the documented wallet debit to the provider-
// neutral service contract. PIN is never included in returned errors.
func (c *Client) DebitCustomerWallet(ctx context.Context, request ports.CustomerWalletDebitRequest) (ports.CustomerWalletTransaction, error) {
	result, err := c.TransactMerchantWallet(ctx, MerchantWalletTransactionRequest{
		WalletID: request.WalletID, Amount: request.AmountKobo, PIN: request.PIN,
		Reference: request.Reference, TransactionCode: request.TransactionCode, Narration: request.Narration,
	})
	if err != nil {
		return ports.CustomerWalletTransaction{}, err
	}
	return ports.CustomerWalletTransaction{
		Reference: result.Reference, ResponseCode: result.ResponseCode,
		Direction: "DEBIT", AmountKobo: request.AmountKobo, CurrencyCode: "566",
	}, nil
}

// ReverseCustomerWallet adapts the documented transaction reversal and only
// succeeds when Interswitch confirms response code 00 and REVERSED status.
func (c *Client) ReverseCustomerWallet(ctx context.Context, request ports.CustomerWalletReversalRequest) error {
	if strings.TrimSpace(request.WalletID) == "" || strings.TrimSpace(request.Reference) == "" || strings.TrimSpace(request.ReversalReference) == "" {
		return errors.New("interswitch wallet: wallet ID and both transaction references are required")
	}
	payload := map[string]string{
		"walletId": request.WalletID,
		"reference": request.Reference,
		"reversalReference": request.ReversalReference,
	}
	var response merchantWalletReversalResponse
	if err := c.doAuthorizedOn(ctx, c.walletBaseURL, http.MethodPost, merchantWalletReversePath, payload, &response); err != nil {
		return errors.New("interswitch wallet reversal outcome is unknown")
	}
	if response.ResponseCode != "00" || !strings.EqualFold(response.Data.Status, "REVERSED") {
		return errors.New("interswitch wallet reversal was not confirmed")
	}
	return nil
}

// TransactMerchantWallet debits an Interswitch wallet using the wallet PIN and
// caller-supplied stable reference. Callers must not log or persist PIN.
func (c *Client) TransactMerchantWallet(ctx context.Context, request MerchantWalletTransactionRequest) (MerchantWalletTransaction, error) {
	if strings.TrimSpace(request.WalletID) == "" || strings.TrimSpace(request.Reference) == "" ||
		strings.TrimSpace(request.TransactionCode) == "" || strings.TrimSpace(request.Narration) == "" {
		return MerchantWalletTransaction{}, errors.New("interswitch wallet: wallet ID, reference, transaction code and narration are required")
	}
	if request.Amount <= 0 {
		return MerchantWalletTransaction{}, errors.New("interswitch wallet: amount must be positive")
	}
	if len(request.PIN) != 4 || !digitsOnly(request.PIN) {
		return MerchantWalletTransaction{}, errors.New("interswitch wallet: PIN must be four digits")
	}
	payload := merchantWalletTransactionPayload{
		WalletID:        strings.TrimSpace(request.WalletID),
		Amount:          fmt.Sprintf("%d", request.Amount),
		PIN:             request.PIN,
		Reference:       strings.TrimSpace(request.Reference),
		TransactionCode: strings.TrimSpace(request.TransactionCode),
		Narration:       strings.TrimSpace(request.Narration),
	}
	var response merchantWalletTransactionResponse
	if err := c.doAuthorizedOn(ctx, c.walletBaseURL, http.MethodPost, merchantWalletTransactPath, payload, &response); err != nil {
		// As with wallet creation, never let an error response echo the PIN into
		// logs. The caller must reconcile this stable reference before retrying.
		return MerchantWalletTransaction{}, errors.New("interswitch wallet debit outcome is unknown")
	}
	reference := strings.TrimSpace(response.Reference)
	if reference == "" {
		reference = strings.TrimSpace(response.TransactionRef)
	}
	return MerchantWalletTransaction{
		ResponseCode: response.ResponseCode, ResponseMessage: firstNonEmptyString(response.ResponseMessage, response.ResponseDesc),
		Reference: reference, Status: response.Status,
	}, nil
}

// ReverseMerchantWalletTransaction reverses a completed wallet debit using a
// new provider reference so the compensating action is independently auditable.
func (c *Client) ReverseMerchantWalletTransaction(ctx context.Context, walletID, reference, reversalReference string) error {
	if strings.TrimSpace(walletID) == "" || strings.TrimSpace(reference) == "" || strings.TrimSpace(reversalReference) == "" {
		return errors.New("interswitch wallet: wallet ID and both transaction references are required")
	}
	payload := map[string]string{
		"walletId": walletID, "reference": reference, "reversalReference": reversalReference,
	}
	if err := c.doAuthorizedOn(ctx, c.walletBaseURL, http.MethodPost, merchantWalletReversePath, payload, nil); err != nil {
		return fmt.Errorf("interswitch wallet reversal: %w", err)
	}
	return nil
}

func digitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
