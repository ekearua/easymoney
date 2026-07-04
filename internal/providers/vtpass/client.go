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
	"regexp"
	"strconv"
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

// DataVariation is one sellable bundle returned by VTPass service variations.
type DataVariation struct {
	ServiceID     string
	VariationCode string
	Name          string
	AmountKobo    int64
}

// WebhookEvent is the normalized subset of a VTPass callback needed by Xego.
type WebhookEvent struct {
	Reference string
	Status    string
	Message   string
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

// ListDataVariations fetches every active VTPass variation for one data service.
func (c *Client) ListDataVariations(ctx context.Context, serviceID string) ([]DataVariation, error) {
	if c.apiKey == "" || c.publicKey == "" {
		return nil, errors.New("VTPass API key and public key are required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/service-variations?serviceID="+serviceID, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("api-key", c.apiKey)
	request.Header.Set("public-key", c.publicKey)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("VTPass variations request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("VTPass variations returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var payload variationsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode VTPass variations: %w", err)
	}
	var variations []DataVariation
	for _, variation := range payload.Content.Variations {
		amount, err := parseAmountKobo(variation.VariationAmount)
		if err != nil || strings.TrimSpace(variation.VariationCode) == "" {
			continue
		}
		variations = append(variations, DataVariation{
			ServiceID:     serviceID,
			VariationCode: strings.TrimSpace(variation.VariationCode),
			Name:          strings.TrimSpace(variation.Name),
			AmountKobo:    amount,
		})
	}
	return variations, nil
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

type variationsResponse struct {
	Content struct {
		Variations []struct {
			VariationCode   string          `json:"variation_code"`
			Name            string          `json:"name"`
			VariationAmount json.RawMessage `json:"variation_amount"`
		} `json:"variations"`
	} `json:"content"`
}

func (r apiResponse) toFulfilmentResult(fallbackReference string) ports.DataFulfilmentResult {
	reference := firstNonEmpty(r.RequestID, r.RequestIDAlt, fallbackReference, r.Content.Transactions.TransactionID)
	status := NormalizeStatus(firstNonEmpty(r.Content.Transactions.Status, r.ResponseDescription, r.Code, r.ResponseCode))
	return ports.DataFulfilmentResult{
		ProviderReference: reference,
		Status:            status,
		Message:           firstNonEmpty(r.ResponseDescription, r.Content.Transactions.ProductName, r.Code, r.ResponseCode),
	}
}

// ParseWebhook extracts request id/status from the common VTPass callback shapes.
func ParseWebhook(body []byte) (WebhookEvent, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("decode VTPass webhook: %w", err)
	}
	flattened := map[string]string{}
	flatten("", raw, flattened)
	reference := firstNonEmpty(
		flattened["requestid"], flattened["request_id"], flattened["requestid"],
		flattened["content.transactions.requestid"], flattened["content.transactions.request_id"],
		flattened["content.transactions.transactionid"], flattened["transactionid"],
	)
	statusText := firstNonEmpty(
		flattened["content.transactions.status"], flattened["status"], flattened["transactionstatus"],
		flattened["response_description"], flattened["responsecode"], flattened["code"],
	)
	message := firstNonEmpty(flattened["response_description"], flattened["message"], flattened["content.transactions.product_name"], statusText)
	if reference == "" {
		return WebhookEvent{}, errors.New("VTPass webhook missing request id")
	}
	return WebhookEvent{Reference: reference, Status: NormalizeStatus(statusText), Message: message}, nil
}

// NormalizeStatus converts VTPass wording/codes into Xego provider statuses.
func NormalizeStatus(value string) string {
	status := strings.ToLower(strings.TrimSpace(value))
	status = strings.ReplaceAll(status, "_", " ")
	status = strings.ReplaceAll(status, "-", " ")
	switch {
	case status == "000" || strings.Contains(status, "delivered") ||
		strings.Contains(status, "successful") || status == "success" ||
		strings.Contains(status, "transaction successful"):
		return "fulfilled"
	case strings.Contains(status, "failed") || strings.Contains(status, "reversed") ||
		strings.Contains(status, "cancelled") || strings.Contains(status, "canceled"):
		return "failed"
	default:
		return "pending"
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

// ServiceIDForNetwork exposes the VTPass data service mapping used by fulfilment.
func ServiceIDForNetwork(networkCode string) string {
	return serviceID(networkCode)
}

// PlanCodeFromVariation creates a stable SMS-safe Xego plan code from a VTPass variation code.
func PlanCodeFromVariation(networkCode, variationCode string) string {
	clean := regexp.MustCompile(`[^A-Z0-9]+`).ReplaceAllString(strings.ToUpper(variationCode), "_")
	clean = strings.Trim(clean, "_")
	prefix := strings.ToUpper(strings.TrimSpace(networkCode))
	if clean == "" {
		return prefix + "_DATA"
	}
	if strings.HasPrefix(clean, prefix+"_") || strings.HasPrefix(clean, prefix) {
		return clean
	}
	return prefix + "_" + clean
}

func parseAmountKobo(raw json.RawMessage) (int64, error) {
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return int64(number*100 + 0.5), nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	text = strings.ReplaceAll(strings.TrimSpace(text), ",", "")
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, err
	}
	return int64(value*100 + 0.5), nil
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

func flatten(prefix string, input map[string]any, output map[string]string) {
	for key, value := range input {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		fullKey := normalizedKey
		if prefix != "" {
			fullKey = prefix + "." + normalizedKey
		}
		switch typed := value.(type) {
		case string:
			output[fullKey] = typed
		case float64:
			output[fullKey] = strconv.FormatFloat(typed, 'f', -1, 64)
		case map[string]any:
			flatten(fullKey, typed, output)
		}
	}
}
