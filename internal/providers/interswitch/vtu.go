package interswitch

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"whatsapp-payment-demo/internal/ports"
)

// Quickteller v5 VTU endpoints.
const vtuPurchasePath = "/quicktellerservice/api/v5/Transactions"

type vtuPurchaseRequest struct {
	TerminalID        string `json:"terminalId"`
	TransactionRef    string `json:"transactionRef"`
	ItemCode          string `json:"itemCode"`
	Amount            string `json:"amount"`
	SourceAccount     string `json:"sourceAccount"`
	DestinationAccount string `json:"destinationAccount"`
	PhoneNumber       string `json:"phoneNumber"`
	AmountDeducted    string `json:"amountDeducted"`
}

type vtuResponse struct {
	RequestRef          string `json:"requestRef"`
	TransactionRef      string `json:"transactionRef"`
	ResponseCode        string `json:"responseCode"`
	ResponseDescription string `json:"responseDescription"`
	TxnStatus           string `json:"txnStatus"`
}

// FulfilData purchases a data bundle through the Quickteller VTU rail. The plan's
// ProviderSKU is the Interswitch bundle item code; the request is keyed by
// transactionRef so CheckDataStatus can resolve the outcome later.
func (c *Client) FulfilData(ctx context.Context, request ports.DataFulfilmentRequest) (ports.DataFulfilmentResult, error) {
	if strings.TrimSpace(request.ProviderSKU) == "" {
		return ports.DataFulfilmentResult{}, errors.New("interswitch VTU: plan item code is required")
	}
	if strings.TrimSpace(request.BeneficiaryPhone) == "" {
		return ports.DataFulfilmentResult{}, errors.New("interswitch VTU: beneficiary phone is required")
	}
	transactionRef := strings.TrimSpace(request.ProviderReference)
	if transactionRef == "" {
		transactionRef = vtuRequestRef(request.RequestCode)
	}
	if c.sourceAccount == "" {
		return ports.DataFulfilmentResult{}, errors.New("interswitch VTU: source account is required (INTERSWITCH_SOURCE_ACCOUNT)")
	}
	amount := strconv.FormatInt(request.AmountKobo, 10)
	var resp vtuResponse
	if err := c.doAuthorized(ctx, http.MethodPost, vtuPurchasePath, vtuPurchaseRequest{
		TerminalID:         c.terminalID(),
		TransactionRef:     transactionRef,
		ItemCode:           strings.TrimSpace(request.ProviderSKU),
		Amount:             amount,
		SourceAccount:      c.sourceAccount,
		DestinationAccount: localPhone(request.BeneficiaryPhone),
		PhoneNumber:        localPhone(request.BeneficiaryPhone),
		AmountDeducted:     amount,
	}, &resp); err != nil {
		return ports.DataFulfilmentResult{}, err
	}
	reference := strings.TrimSpace(resp.RequestRef)
	if reference == "" {
		reference = strings.TrimSpace(resp.TransactionRef)
	}
	if reference == "" {
		reference = transactionRef
	}
	return ports.DataFulfilmentResult{
		ProviderReference: reference,
		Status:            normalizeVTUStatus(resp.ResponseCode, resp.TxnStatus, resp.ResponseDescription),
		Message:           firstNonEmptyString(resp.ResponseDescription, resp.TxnStatus, resp.ResponseCode),
	}, nil
}

// CheckDataStatus requeries a VTU transaction by its provider reference.
func (c *Client) CheckDataStatus(ctx context.Context, providerReference string) (ports.DataFulfilmentResult, error) {
	providerReference = strings.TrimSpace(providerReference)
	if providerReference == "" {
		return ports.DataFulfilmentResult{Status: "pending", Message: "missing Interswitch VTU request reference"}, nil
	}
	query := url.Values{}
	query.Set("requestRef", providerReference)
	var resp vtuResponse
	if err := c.doAuthorized(ctx, http.MethodGet, vtuPurchasePath+"?"+query.Encode(), nil, &resp); err != nil {
		return ports.DataFulfilmentResult{}, err
	}
	return ports.DataFulfilmentResult{
		ProviderReference: providerReference,
		Status:            normalizeVTUStatus(resp.ResponseCode, resp.TxnStatus, resp.ResponseDescription),
		Message:           firstNonEmptyString(resp.ResponseDescription, resp.TxnStatus, resp.ResponseCode),
	}, nil
}

func normalizeVTUStatus(responseCode, txnStatus, description string) string {
	code := strings.TrimSpace(responseCode)
	statusText := strings.ToLower(strings.TrimSpace(txnStatus))
	description = strings.ToLower(strings.TrimSpace(description))
	if code == "90000" && statusText == "" && description == "" {
		return "pending"
	}
	switch {
	case strings.Contains(statusText, "completed") || strings.Contains(statusText, "successful") ||
		strings.Contains(statusText, "delivered") || strings.Contains(description, "successful"):
		return "fulfilled"
	case strings.Contains(statusText, "failed") || strings.Contains(statusText, "cancelled") ||
		strings.Contains(statusText, "canceled") || strings.Contains(statusText, "reversed"):
		return "failed"
	default:
		return "pending"
	}
}

func vtuRequestRef(requestCode string) string {
	return "XG-DATA-" + strings.TrimSpace(requestCode)
}

func localPhone(phone string) string {
	phone = strings.TrimSpace(phone)
	if strings.HasPrefix(phone, "+234") {
		return "0" + strings.TrimPrefix(phone, "+234")
	}
	if strings.HasPrefix(phone, "234") {
		return "0" + strings.TrimPrefix(phone, "234")
	}
	return phone
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}