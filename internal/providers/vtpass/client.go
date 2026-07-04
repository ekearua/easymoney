// Package vtpass implements the VTPass data vending API behind Xego's
// provider-neutral data fulfilment boundary.
package vtpass

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// Client fulfils mobile data orders through the VTPass REST API.
type Client struct {
	baseURL   string
	apiKey    string
	publicKey string
	secretKey string
	http      *http.Client
}

// New creates a VTPass API client. Use https://sandbox.vtpass.com/api for
// sandbox and https://vtpass.com/api for live once provisioned.
func New(baseURL, apiKey, publicKey, secretKey string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://sandbox.vtpass.com/api"
	}
	return &Client{
		baseURL: baseURL, apiKey: strings.TrimSpace(apiKey),
		publicKey: strings.TrimSpace(publicKey), secretKey: strings.TrimSpace(secretKey),
		http: &http.Client{Timeout: 25 * time.Second},
	}
}

// FulfilData purchases a data bundle using the plan's VTPass variation code.
func (c *Client) FulfilData(ctx context.Context, request ports.DataFulfilmentRequest) (ports.DataFulfilmentResult, error) {
	if request.ProviderSKU == "" {
		return ports.DataFulfilmentResult{}, errors.New("VTPass variation code is required")
	}
	payload := map[string]any{
		"request_id":     vtpassRequestID(request.RequestCode),
		"serviceID":      serviceID(request.NetworkCode),
		"billersCode":    localPhone(request.BeneficiaryPhone),
		"variation_code": request.ProviderSKU,
		"amount":         float64(request.AmountKobo) / 100,
		"phone":          localPhone(request.BeneficiaryPhone),
	}
	var response apiResponse
	if err := c.post(ctx, "/pay", payload, &response); err != nil {
		return ports.DataFulfilmentResult{}, err
	}
	return response.toFulfilmentResult(payload["request_id"].(string)), nil
}

// CheckDataStatus requeries a VTPass transaction by the provider reference.
func (c *Client) CheckDataStatus(ctx context.Context, providerReference string) (ports.DataFulfilmentResult, error) {
	providerReference = strings.TrimSpace(providerReference)
	if providerReference == "" {
		return ports.DataFulfilmentResult{Status: "pending", Message: "missing VTPass request id"}, nil
	}
	var response apiResponse
	if err := c.post(ctx, "/requery", map[string]any{"request_id": providerReference}, &response); err != nil {
		return ports.DataFulfilmentResult{}, err
	}
	return response.toFulfilmentResult(providerReference), nil
}

func (c *Client) post(ctx context.Context, path string, payload map[string]any, target any) error {
	if c.apiKey == "" || c.secretKey == "" {
		return errors.New("VTPass API key and secret key are required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("api-key", c.apiKey)
	request.Header.Set("secret-key", c.secretKey)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("VTPass request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("VTPass returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode VTPass response: %w", err)
	}
	return nil
}

type apiResponse struct {
	Code                string `json:"code"`
	ResponseCode        string `json:"response_code"`
	ResponseDescription string `json:"response_description"`
	RequestID           string `json:"requestId"`
	RequestIDAlt        string `json:"request_id"`
	Content             struct {
		Transactions struct {
			Status        string `json:"status"`
			TransactionID string `json:"transactionId"`
			ProductName   string `json:"product_name"`
		} `json:"transactions"`
	} `json:"content"`
}

func (r apiResponse) toFulfilmentResult(fallbackReference string) ports.DataFulfilmentResult {
	reference := firstNonEmpty(r.RequestID, r.RequestIDAlt, fallbackReference, r.Content.Transactions.TransactionID)
	status := strings.ToLower(strings.TrimSpace(r.Content.Transactions.Status))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(r.ResponseDescription))
	}
	switch status {
	case "delivered", "successful", "transaction successful", "success":
		status = "fulfilled"
	case "failed", "reversed":
		status = "failed"
	default:
		status = "pending"
	}
	return ports.DataFulfilmentResult{
		ProviderReference: reference,
		Status:            status,
		Message:           firstNonEmpty(r.ResponseDescription, r.Content.Transactions.ProductName, r.Code, r.ResponseCode),
	}
}

func serviceID(networkCode string) string {
	switch strings.ToUpper(strings.TrimSpace(networkCode)) {
	case "MTN":
		return "mtn-data"
	case "AIRTEL":
		return "airtel-data"
	case "GLO":
		return "glo-data"
	case "9MOBILE":
		return "etisalat-data"
	default:
		return strings.ToLower(strings.TrimSpace(networkCode))
	}
}

func vtpassRequestID(requestCode string) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, requestCode)
	if clean == "" {
		clean = "XEGODATA"
	}
	location, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		location = time.FixedZone("WAT", 3600)
	}
	return time.Now().In(location).Format("200601021504") + clean
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
