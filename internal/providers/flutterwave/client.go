// Package flutterwave implements the provider-neutral payment gateway port
// for Flutterwave v3 collection APIs.
package flutterwave

import (
	"bytes"
	"context"
	"crypto/hmac"
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

// Client integrates with Flutterwave hosted checkout and verification.
type Client struct {
	secretKey     string
	publicKey     string
	baseURL       string
	webhookSecret string
	http          *http.Client
}

// New creates a Flutterwave client with strict request timeouts.
func New(secretKey, publicKey, baseURL, webhookSecret string) *Client {
	return &Client{
		secretKey:     secretKey,
		publicKey:     publicKey,
		baseURL:       strings.TrimRight(baseURL, "/"),
		webhookSecret: webhookSecret,
		http:          &http.Client{Timeout: 15 * time.Second},
	}
}

// Initialize creates a card-only hosted checkout from the trusted backend.
func (c *Client) Initialize(ctx context.Context, input ports.InitializePayment) (ports.Checkout, error) {
	payload := map[string]any{
		"tx_ref":       input.Reference,
		"amount":       strconv.FormatFloat(float64(input.AmountKobo)/100, 'f', 2, 64),
		"currency":     input.Currency,
		"redirect_url": input.CallbackURL,
		"payment_options": "card",
		"customer": map[string]string{
			"email": input.Email,
		},
		"meta": input.Metadata,
	}
	var response struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Data    struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v3/payments", payload, &response); err != nil {
		return ports.Checkout{}, err
	}
	if response.Status != "success" || response.Data.Link == "" {
		return ports.Checkout{}, fmt.Errorf("flutterwave initialize rejected: %s", response.Message)
	}
	return ports.Checkout{Reference: input.Reference, URL: response.Data.Link}, nil
}

// Verify retrieves the authoritative server-side transaction status.
func (c *Client) Verify(ctx context.Context, reference string) (ports.Verification, error) {
	var response struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Data    struct {
			TxRef     string  `json:"tx_ref"`
			ID        int64   `json:"id"`
			Status    string  `json:"status"`
			Amount    float64 `json:"amount"`
			Currency  string  `json:"currency"`
			CreatedAt string  `json:"created_at"`
			IP        string  `json:"ip"`
			Channel   string  `json:"channel"`
			Meta      json.RawMessage `json:"meta"`
		} `json:"data"`
	}
	path := "/v3/transactions/verify?tx_ref=" + url.QueryEscape(reference)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return ports.Verification{}, err
	}
	if response.Status != "success" {
		return ports.Verification{}, fmt.Errorf("flutterwave verify rejected: %s", response.Message)
	}
	amountKobo := int64(response.Data.Amount * 100)
	metadata := map[string]string{}
	_ = json.Unmarshal(response.Data.Meta, &metadata)
	var paidAt *time.Time
	if t, err := time.Parse(time.RFC3339, response.Data.CreatedAt); err == nil {
		paidAt = &t
	}
	return ports.Verification{
		Reference:  response.Data.TxRef,
		Status:     flutterwaveStatus(response.Data.Status),
		AmountKobo: amountKobo,
		Currency:   response.Data.Currency,
		Domain:     "",
		Channel:    flutterwaveChannel(response.Data.Channel),
		Metadata:   metadata,
		PaidAt:     paidAt,
		Message:    response.Message,
	}, nil
}

// ValidateWebhook authenticates the raw payload using Flutterwave's
// HMAC-SHA512 verif-hash signature.
func (c *Client) ValidateWebhook(body []byte, signature string) (ports.GatewayWebhook, error) {
	if c.webhookSecret == "" || signature == "" {
		return ports.GatewayWebhook{}, errors.New("missing Flutterwave webhook credentials")
	}
	mac := hmac.New(sha512.New, []byte(c.webhookSecret))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return ports.GatewayWebhook{}, errors.New("invalid Flutterwave signature")
	}
	var envelope struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ports.GatewayWebhook{}, fmt.Errorf("decode Flutterwave webhook: %w", err)
	}
	var data struct {
		TxRef string `json:"tx_ref"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return ports.GatewayWebhook{}, fmt.Errorf("decode Flutterwave webhook data: %w", err)
	}
	return ports.GatewayWebhook{Event: envelope.Event, Reference: data.TxRef, Raw: json.RawMessage(body)}, nil
}

// Health checks whether the Flutterwave API is reachable.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v3/transactions/verify?tx_ref=__health_check__", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("flutterwave health: %w", err)
	}
	resp.Body.Close()
	return nil
}

// flutterwaveStatus normalizes Flutterwave statuses to the provider-neutral
// set that mapGatewayStatus understands.
func flutterwaveStatus(status string) string {
	switch strings.ToLower(status) {
	case "successful", "success":
		return "success"
	case "failed":
		return "failed"
	case "cancelled":
		return "failed"
	case "pending":
		return "pending"
	case "processing":
		return "processing"
	case "reversed":
		return "reversed"
	default:
		return status
	}
}

// flutterwaveChannel normalizes Flutterwave channel names to the
// provider-neutral set.
func flutterwaveChannel(channel string) string {
	switch strings.ToLower(channel) {
	case "card", "card-charge":
		return "card"
	case "banktransfer", "bank_transfer":
		return "bank_transfer"
	case "account", "bank":
		return "bank"
	case "ussd":
		return "ussd"
	case "mobilemoney", "mobile_money":
		return "mobile_money"
	case "qr":
		return "qr"
	default:
		return channel
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, input any, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.secretKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("flutterwave request: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("flutterwave returned %s", response.Status)
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("decode flutterwave response: %w", err)
	}
	return nil
}
