package interswitch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// virtualAccountPath generates a Dynamic Virtual Account for bank-transfer
// collection on the payment gateway host.
const virtualAccountPath = "/paymentgateway/api/v1/virtualaccounts/transaction"

type virtualAccountRequest struct {
	MerchantCode         string `json:"merchantCode"`
	PayableCode          string `json:"payableCode"`
	CurrencyCode         string `json:"currencyCode"`
	Amount               string `json:"amount"`
	AccountName          string `json:"accountName"`
	TransactionReference string `json:"transactionReference"`
}

type virtualAccountResponse struct {
	AccountNumber        string `json:"accountNumber"`
	AccountName          string `json:"accountName"`
	BankName             string `json:"bankName"`
	Amount               int64  `json:"amount"`
	TransactionReference string `json:"transactionReference"`
	ResponseCode         string `json:"responseCode"`
	ValidityPeriodMins   int    `json:"validityPeriodMins"`
}

// Transfer generates a one-time dynamic virtual account for the payment so the
// customer can pay by bank transfer within the validity window. The returned
// Reference is the same provider reference the authoritative requery (Verify)
// resolves, so the existing webhook + requery confirmation path applies.
func (c *Client) Transfer(ctx context.Context, input ports.InitializePayment) (ports.TransferInstruction, error) {
	if c.merchantCode == "" || c.payItemID == "" {
		return ports.TransferInstruction{}, errors.New("interswitch: merchant code and pay item id are required for virtual accounts")
	}
	accountName := "Xego transaction"
	if slug, ok := input.Metadata["merchant_slug"]; ok && strings.TrimSpace(slug) != "" {
		accountName = "Xego " + strings.TrimSpace(slug)
	}
	var resp virtualAccountResponse
	if err := c.doAuthorized(ctx, http.MethodPost, virtualAccountPath, virtualAccountRequest{
		MerchantCode:         c.merchantCode,
		PayableCode:          c.payItemID,
		CurrencyCode:         NGN,
		Amount:               strconv.FormatInt(input.AmountKobo, 10),
		AccountName:          accountName,
		TransactionReference: input.Reference,
	}, &resp); err != nil {
		return ports.TransferInstruction{}, err
	}
	if resp.AccountNumber == "" {
		return ports.TransferInstruction{}, fmt.Errorf("interswitch virtual account returned no account number (responseCode %s)", resp.ResponseCode)
	}
	validity := resp.ValidityPeriodMins
	if validity <= 0 {
		validity = 30
	}
	return ports.TransferInstruction{
		AccountNumber: resp.AccountNumber,
		AccountName:   strings.TrimSpace(resp.AccountName),
		BankName:      strings.TrimSpace(resp.BankName),
		Reference:     strings.TrimSpace(resp.TransactionReference),
		ExpiresAt:     time.Now().Add(time.Duration(validity) * time.Minute),
		ValidityMins:  validity,
	}, nil
}