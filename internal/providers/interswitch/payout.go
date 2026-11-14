package interswitch

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// Quickteller Send Money v5 rail paths, relative to the Quickteller/transfer host.
const (
	transferPath    = "/transactions/TransferFunds"
	nameEnquiryPath = "/Transactions/DoAccountNameInquiry"
	queryPath       = "/Transactions"
)

// Bank-codes directory path on the API host (legacy InterswitchAuth endpoint).
const bankCodesPath = "/api/v2/quickteller/configuration/fundstransferbanks"

// Payment method codes used by the v5 Send Money wire. The initiator pays with
// method "CA" (card/account) and the beneficiary is credited with "AC"
// (account credit), per the documented samples. NGN is the ISO 4217 numeric
// code for naira, which the rail expects in place of "NGN".
const (
	initiationPaymentMethod  = "CA"
	terminationPaymentMethod = "AC"
	initiatingEntityCode     = "PBL" // entity code in every published sample
	countryCode              = "NG"
	channelCode              = "7"
)

// defaultSenderName is used when no sender identity is configured.
const defaultSenderName = "Xego"

// sendMoneyMAC computes the message authentication code the v5 TransferFunds
// rail requires: the SHA-512 digest of the seven concatenated initiation +
// termination fields, hex-encoded.
func sendMoneyMAC(amountKobo int64) string {
	amount := strconv.FormatInt(amountKobo, 10)
	canonical := amount + NGN + initiationPaymentMethod +
		amount + NGN + terminationPaymentMethod + countryCode
	sum := sha512.Sum512([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

type transferRequest struct {
	TransferCode         string      `json:"transferCode"`
	MAC                  string      `json:"mac"`
	Termination          termination `json:"termination"`
	Sender               sender      `json:"sender"`
	InitiatingEntityCode string      `json:"initiatingEntityCode"`
	Initiation           initiation  `json:"initiation"`
	Beneficiary          beneficiary `json:"beneficiary"`
}

type termination struct {
	Amount            string     `json:"amount"`
	AccountReceivable receivable `json:"accountReceivable"`
	EntityCode        string     `json:"entityCode"`
	CurrencyCode      string     `json:"currencyCode"`
	PaymentMethodCode string     `json:"paymentMethodCode"`
	CountryCode       string     `json:"countryCode"`
}

type receivable struct {
	AccountNumber string `json:"accountNumber"`
	AccountType   string `json:"accountType"`
}

type sender struct {
	Phone      string `json:"phone"`
	Email      string `json:"email"`
	Lastname   string `json:"lastname"`
	Othernames string `json:"othernames"`
}

type initiation struct {
	Amount            string `json:"amount"`
	CurrencyCode      string `json:"currencyCode"`
	PaymentMethodCode string `json:"paymentMethodCode"`
	Channel           string `json:"channel"`
}

type beneficiary struct {
	Lastname   string `json:"lastname"`
	Othernames string `json:"othernames"`
}

type transferResponse struct {
	MAC                  string `json:"MAC"`
	TransactionDate      string `json:"TransactionDate"`
	TransactionReference string `json:"TransactionReference"`
	TransferCode         string `json:"TransferCode"`
	Pin                  string `json:"Pin"`
	ResponseCode         string `json:"ResponseCode"`
	ResponseCodeGrouping string `json:"ResponseCodeGrouping"`
}

// Payout pushes funds out through the Quickteller Send Money v5 single
// transfer (NIP) rail. Request.Reference is the idempotency key (transferCode).
func (c *Client) Payout(ctx context.Context, request ports.PayoutRequest) (ports.PayoutResult, error) {
	accountNumber := strings.TrimSpace(request.AccountNumber)
	bankCode := strings.TrimSpace(request.BankCode)
	transferCode := strings.TrimSpace(request.Reference)
	if accountNumber == "" || bankCode == "" {
		return ports.PayoutResult{}, errors.New("interswitch: destination account and bank code are required")
	}
	if transferCode == "" {
		return ports.PayoutResult{}, errors.New("interswitch: a unique transfer reference is required for the payout")
	}
	beneficiaryLast, beneficiaryOther := splitName(strings.TrimSpace(request.AccountName))
	var resp transferResponse
	if err := c.doAuthorizedOn(ctx, c.transferBaseURL, http.MethodPost, transferPath, transferRequest{
		TransferCode: transferCode,
		MAC:          sendMoneyMAC(request.AmountKobo),
		Termination: termination{
			Amount: strconv.FormatInt(request.AmountKobo, 10),
			AccountReceivable: receivable{
				AccountNumber: accountNumber,
				AccountType:   "00",
			},
			EntityCode:        bankCode,
			CurrencyCode:      NGN,
			PaymentMethodCode: terminationPaymentMethod,
			CountryCode:       countryCode,
		},
		Sender: sender{
			Phone:      c.senderPhone,
			Email:      c.senderEmail,
			Lastname:   c.senderLastname,
			Othernames: c.senderOthernames,
		},
		InitiatingEntityCode: c.initiatingEntityCode,
		Initiation: initiation{
			Amount:            strconv.FormatInt(request.AmountKobo, 10),
			CurrencyCode:      NGN,
			PaymentMethodCode: initiationPaymentMethod,
			Channel:           channelCode,
		},
		Beneficiary: beneficiary{
			Lastname:   beneficiaryLast,
			Othernames: beneficiaryOther,
		},
	}, &resp); err != nil {
		return ports.PayoutResult{}, err
	}
	code := strings.TrimSpace(resp.ResponseCode)
	if code != "90000" {
		if code == "70120" {
			return ports.PayoutResult{}, ports.ErrPayoutPending
		}
		message := strings.TrimSpace(resp.ResponseCodeGrouping)
		if message == "" {
			message = "Interswitch declined the transfer (code " + code + ")"
		}
		return ports.PayoutResult{Status: "failed", Message: message}, nil
	}
	externalRef := strings.TrimSpace(resp.TransactionReference)
	if externalRef == "" {
		externalRef = strings.TrimSpace(resp.TransferCode)
	}
	if externalRef == "" {
		externalRef = transferCode
	}
	return ports.PayoutResult{
		ExternalRef: externalRef,
		Status:      "succeeded",
		Message:     strings.TrimSpace(resp.ResponseCodeGrouping),
	}, nil
}

// NameEnquiry validates the destination account and returns the account name.
// Uses Quickteller v5 DoAccountNameInquiry (POST) with TerminalID/accountid/bankcode.
func (c *Client) NameEnquiry(ctx context.Context, accountNumber, bankCode string) (string, error) {
	token, err := c.token.AccessToken(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.transferBaseURL+nameEnquiryPath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("TerminalID", c.terminalID())
	req.Header.Set("accountid", strings.TrimSpace(accountNumber))
	req.Header.Set("bankcode", strings.TrimSpace(bankCode))
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &nameEnquiryError{StatusCode: resp.StatusCode, Body: truncate(string(raw))}
	}
	var data struct {
		AccountName          string `json:"AccountName"`
		ResponseCode         string `json:"ResponseCode"`
		ResponseCodeGrouping string `json:"ResponseCodeGrouping"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", err
	}
	if code := strings.TrimSpace(data.ResponseCode); code != "" && code != "90000" {
		return "", &nameEnquiryError{ResponseCode: code, Description: strings.TrimSpace(data.ResponseCodeGrouping)}
	}
	accountName := strings.TrimSpace(data.AccountName)
	if accountName == "" {
		return "", &nameEnquiryError{ResponseCode: "empty", Body: truncate(string(raw))}
	}
	return accountName, nil
}

type nameEnquiryError struct {
	StatusCode   int
	ResponseCode string
	Description  string
	Body         string
}

func (e *nameEnquiryError) Error() string {
	if e.StatusCode != 0 {
		return "interswitch name enquiry returned HTTP " + strconv.Itoa(e.StatusCode) + ": " + e.Body
	}
	if e.Description != "" {
		return "interswitch name enquiry: " + e.ResponseCode + " " + e.Description
	}
	return "interswitch name enquiry failed (code " + e.ResponseCode + ")"
}

// ReconcilePayout reports what the v5 rail says actually happened to the
// transfer identified by reference (requestRef/transferCode).
func (c *Client) ReconcilePayout(ctx context.Context, reference string) (ports.PayoutReconciliation, error) {
	transferCode := strings.TrimSpace(reference)
	if transferCode == "" {
		return ports.PayoutReconciliation{}, errors.New("interswitch: a transfer reference is required for reconciliation")
	}
	status, err := c.QueryTransaction(ctx, transferCode)
	if err != nil {
		return ports.PayoutReconciliation{}, err
	}
	externalRef := strings.TrimSpace(status.TransactionRef)
	if externalRef == "" {
		externalRef = transferCode
	}
	message := strings.TrimSpace(status.Status + " " + status.ResponseCode)
	return ports.PayoutReconciliation{
		Status:      normalizeTransferStatus(status.Status, status.ResponseCode),
		ExternalRef: externalRef,
		Message:     message,
		AmountKobo:  status.AmountKobo,
		Currency:    status.Currency,
	}, nil
}

func normalizeTransferStatus(status, responseCode string) string {
	s := strings.ToUpper(strings.TrimSpace(status))
	code := strings.TrimSpace(responseCode)
	switch {
	case strings.Contains(s, "COMPLET") || strings.Contains(s, "SUCCESS") || code == "90000":
		return ports.PayoutReconcileSucceeded
	case strings.Contains(s, "FAIL") || strings.Contains(s, "DECLIN") ||
		strings.Contains(s, "REJECT") || strings.Contains(s, "REVERS") ||
		strings.Contains(s, "CANCEL"):
		return ports.PayoutReconcileFailed
	case s == "":
		return ports.PayoutReconcileUnknown
	default:
		return ports.PayoutReconcilePending
	}
}

// TransactionStatus is the normalized result of a v5 query.
type TransactionStatus struct {
	Reference      string
	Status         string
	TransactionRef string
	ResponseCode   string
	AmountKobo     int64
	Currency       string
}

// QueryTransaction returns the current status of a transfer by requestRef.
func (c *Client) QueryTransaction(ctx context.Context, requestRef string) (TransactionStatus, error) {
	var data struct {
		RequestReference        string `json:"requestReference"`
		Status                  string `json:"status"`
		TransactionRef          string `json:"transactionRef"`
		TransactionResponseCode string `json:"transactionResponseCode"`
		Amount                  string `json:"amount"`
		CurrencyCode            string `json:"currencyCode"`
	}
	endpoint := queryPath + "?" + url.Values{"requestRef": {strings.TrimSpace(requestRef)}}.Encode()
	if err := c.doAuthorizedOn(ctx, c.transferBaseURL, http.MethodGet, endpoint, nil, &data); err != nil {
		return TransactionStatus{}, err
	}
	amountKobo, _ := strconv.ParseInt(strings.TrimSpace(data.Amount), 10, 64)
	return TransactionStatus{
		Reference:      strings.TrimSpace(data.RequestReference),
		Status:         strings.TrimSpace(data.Status),
		TransactionRef: strings.TrimSpace(data.TransactionRef),
		ResponseCode:   strings.TrimSpace(data.TransactionResponseCode),
		AmountKobo:     amountKobo,
		Currency:       strings.TrimSpace(data.CurrencyCode),
	}, nil
}

// FundTransferBank is one entry from the funds-transfer bank directory.
type FundTransferBank struct {
	ID       string `json:"id"`
	CBNCode  string `json:"cbnCode"`
	BankName string `json:"bankName"`
	BankCode string `json:"bankCode"`
}

// GetBankCodes returns the bank directory used on the transfer rail.
// Per the provided doc, this endpoint is served on the API host with legacy
// InterswitchAuth request signing.
func (c *Client) GetBankCodes(ctx context.Context) ([]FundTransferBank, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+bankCodesPath, nil)
	if err != nil {
		return nil, err
	}
	nonce, err := randomNonce()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	applyInterswitchAuthHeaders(req, c.clientID, c.clientSecret, http.MethodGet, bankCodesPath, now, nonce)
	if terminalID := c.terminalID(); terminalID != "" {
		req.Header.Set("TerminalID", terminalID)
	}
	req.Header.Set("accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("interswitch bank codes: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("interswitch bank codes returned %s: %s", resp.Status, truncate(string(raw)))
	}
	var data struct {
		Banks []FundTransferBank `json:"banks"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode interswitch bank codes: %w", err)
	}
	return data.Banks, nil
}

func splitName(full string) (lastname, othernames string) {
	parts := strings.Fields(full)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[len(parts)-1], strings.Join(parts[:len(parts)-1], " ")
}
