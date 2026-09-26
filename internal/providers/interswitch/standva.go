package interswitch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"whatsapp-payment-demo/internal/ports"
)

// StandingVirtualAccount is a reusable virtual account reserved long-term for a
// wallet holder so bank transfers can always be received, in contrast to the
// short-lived payment-scoped Dynamic Virtual Account produced by Transfer.
type StandingVirtualAccount struct {
	AccountNumber        string
	AccountName          string
	BankName             string
	TransactionReference string
	ResponseCode         string
}

// standingVirtualAccountPath reserves a standing virtual account on the
// gateway host using the account-name and customer-reference the merchant
// supplies. The returned account number is bound to the merchant's account
// name so subsequent credits carry the payer's transaction reference.
const standingVirtualAccountPath = "/paymentgateway/api/v1/standva/transaction"

type standingVirtualAccountRequest struct {
	MerchantCode         string `json:"merchantCode"`
	PayableCode          string `json:"payableCode"`
	CurrencyCode         string `json:"currencyCode"`
	AccountName          string `json:"accountName"`
	TransactionReference string `json:"transactionReference"`
}

type standingVirtualAccountResponse struct {
	AccountNumber        string `json:"accountNumber"`
	AccountName          string `json:"accountName"`
	BankName             string `json:"bankName"`
	TransactionReference string `json:"transactionReference"`
	ResponseCode         string `json:"responseCode"`
}

// ReserveStandingAccount provisions a standing virtual account via the
// provider. Callers combine the reservation with an existing local number so a
// customer is always receivable; the returned account number is expected to be
// stored and used for all future bank-transfer credits to that customer.
func (c *Client) ReserveStandingAccount(ctx context.Context, reference, accountName string) (StandingVirtualAccount, error) {
	if c.merchantCode == "" || c.payItemID == "" {
		return StandingVirtualAccount{}, errors.New("interswitch: merchant code and pay item id are required for standing virtual accounts")
	}
	var resp standingVirtualAccountResponse
	if err := c.doAuthorized(ctx, http.MethodPost, standingVirtualAccountPath, standingVirtualAccountRequest{
		MerchantCode:         c.merchantCode,
		PayableCode:          c.payItemID,
		CurrencyCode:         NGN,
		AccountName:          strings.TrimSpace(accountName),
		TransactionReference: reference,
	}, &resp); err != nil {
		return StandingVirtualAccount{}, err
	}
	if resp.AccountNumber == "" {
		return StandingVirtualAccount{}, fmt.Errorf("interswitch standing virtual account returned no account number (responseCode %s)", resp.ResponseCode)
	}
	return StandingVirtualAccount{
		AccountNumber:        resp.AccountNumber,
		AccountName:          strings.TrimSpace(resp.AccountName),
		BankName:             strings.TrimSpace(resp.BankName),
		TransactionReference: strings.TrimSpace(resp.TransactionReference),
		ResponseCode:         resp.ResponseCode,
	}, nil
}

var _ = ports.PaymentGateway(nil)
