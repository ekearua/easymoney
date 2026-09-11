package interswitch

import (
	"context"
	"net/http"
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

// Refund reverses part or all of a payment through the Interswitch refund API.
// A refund only reaches the customer after Interswitch confirms the reversal,
// so a Z0/90000 response means the refund was accepted for processing.
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