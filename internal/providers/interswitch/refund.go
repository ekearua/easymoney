package interswitch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/ports"
)

// refundPath is the payment gateway refund endpoint.
const refundPath = "/paymentgateway/api/v1/refunds"

type refundRequest struct {
	Amount          int64  `json:"amount"`
	RefundReference string `json:"refundReference"`
	RefundType      string `json:"refundType"`
	ParentPaymentID string `json:"parentPaymentId"`
}

type refundResponse struct {
	ResponseCode        string `json:"responseCode"`
	ResponseDescription string `json:"responseDescription"`
	RefundReference     string `json:"refundReference"`
}

// RefundRecord represents a refund as returned by the Payment Gateway refund APIs.
type RefundRecord struct {
	ID                            int64  `json:"id"`
	RefundReference               string `json:"refundReference"`
	ParentPaymentID               int64  `json:"parentPaymentId"`
	ParentTransactionReference    string `json:"parentTransactionReference"`
	RefundType                    string `json:"refundType"`
	RefundAmount                  int64  `json:"refundAmount"`
	CreatedDate                   int64  `json:"createdDate"`
	MerchantCustomerID            string `json:"merchantCustomerId"`
	RefundStatus                  string `json:"refundStatus"`
	ProcessedDate                 int64  `json:"processedDate"`
	RefundTransactionReference    string `json:"refundTransactionReference"`
	RefundTransactionResponseCode string `json:"refundTransactionResponseCode"`
	CreatedBy                     string `json:"createdBy"`
	ReasonForRefund               string `json:"reasonForRefund"`
}

// RefundList is the paginated refund list response.
type RefundList struct {
	Count   int            `json:"count"`
	Content []RefundRecord `json:"content"`
}

// Refund reverses part or all of a payment through the Interswitch refund API.
func (c *Client) Refund(ctx context.Context, request ports.RefundRequest) (ports.RefundResult, error) {
	refundReference := "XGREF-" + uuid.NewString()
	refundType := "FULL"
	if request.PaymentAmountKobo > 0 && request.AmountKobo < request.PaymentAmountKobo {
		refundType = "PARTIAL"
	}
	var resp refundResponse
	if err := c.doAuthorized(ctx, http.MethodPost, refundPath, refundRequest{
		Amount:          request.AmountKobo,
		RefundReference: refundReference,
		RefundType:      refundType,
		ParentPaymentID: request.PaymentID,
	}, &resp); err != nil {
		return ports.RefundResult{}, err
	}
	code := strings.TrimSpace(resp.ResponseCode)
	if resp.RefundReference == "" && code != "Z0" && code != "90000" {
		message := strings.TrimSpace(resp.ResponseDescription)
		if message == "" {
			message = "Interswitch declined the refund (responseCode " + code + ")"
		}
		return ports.RefundResult{Status: "failed", Message: message}, nil
	}
	providerRef := strings.TrimSpace(resp.RefundReference)
	if providerRef == "" {
		providerRef = refundReference
	}
	return ports.RefundResult{
		Status:   "succeeded",
		RefundID: providerRef,
		Message:  strings.TrimSpace(resp.ResponseDescription),
	}, nil
}

// QueryRefund fetches a refund by refundReference. Per the provided docs, a
// not-found refund may return HTTP 200 with an empty body; in that case (nil,false,nil)
// is returned.
func (c *Client) QueryRefund(ctx context.Context, refundReference string) (*RefundRecord, bool, error) {
	refundReference = strings.TrimSpace(refundReference)
	if refundReference == "" {
		return nil, false, nil
	}
	path := refundPath + "?" + url.Values{"refundReference": {refundReference}}.Encode()
	raw, err := c.doAuthorizedRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, false, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" || trimmed == "[]" {
		return nil, false, nil
	}
	var rec RefundRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, false, err
	}
	if rec.ID == 0 && strings.TrimSpace(rec.RefundReference) == "" {
		return nil, false, nil
	}
	return &rec, true, nil
}

// ListRefunds returns refunds for a merchant. Parameters map to the documented
// query params (merchantCode, pageNum, pageSize, startDate, endDate).
func (c *Client) ListRefunds(ctx context.Context, merchantCode string, pageNum, pageSize int, startDate, endDate string) (RefundList, error) {
	q := url.Values{}
	if strings.TrimSpace(merchantCode) != "" {
		q.Set("merchantCode", strings.TrimSpace(merchantCode))
	}
	if pageNum > 0 {
		q.Set("pageNum", strconv.Itoa(pageNum))
	}
	if pageSize > 0 {
		q.Set("pageSize", strconv.Itoa(pageSize))
	}
	if strings.TrimSpace(startDate) != "" {
		q.Set("startDate", strings.TrimSpace(startDate))
	}
	if strings.TrimSpace(endDate) != "" {
		q.Set("endDate", strings.TrimSpace(endDate))
	}
	path := refundPath
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out RefundList
	if err := c.doAuthorized(ctx, http.MethodGet, path, nil, &out); err != nil {
		return RefundList{}, err
	}
	return out, nil
}
