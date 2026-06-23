// Package paystack implements the provider-neutral payment gateway port.
package paystack

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

// Client integrates only with Paystack hosted checkout and verification.
type Client struct {
	secretKey string
	baseURL   string
	http      *http.Client
}

// New creates a Paystack client with strict request timeouts.
func New(secretKey, baseURL string) *Client {
	return &Client{
		secretKey: secretKey,
		baseURL:   strings.TrimRight(baseURL, "/"),
		http:      &http.Client{Timeout: 15 * time.Second},
	}
}

// Initialize creates a card-only hosted checkout from the trusted backend.
func (c *Client) Initialize(ctx context.Context, input ports.InitializePayment) (ports.Checkout, error) {
	payload := map[string]any{
		"reference":    input.Reference,
		"email":        input.Email,
		"amount":       strconv.FormatInt(input.AmountKobo, 10),
		"currency":     input.Currency,
		"callback_url": input.CallbackURL,
		"channels":     []string{"card"},
		"metadata":     input.Metadata,
	}
	var response struct {
		Status  bool   `json:"status"`
		Message string `json:"message"`
		Data    struct {
			AuthorizationURL string `json:"authorization_url"`
			Reference        string `json:"reference"`
		} `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/transaction/initialize", payload, &response); err != nil {
		return ports.Checkout{}, err
	}
	if !response.Status || response.Data.AuthorizationURL == "" || response.Data.Reference != input.Reference {
		return ports.Checkout{}, fmt.Errorf("paystack initialize rejected: %s", response.Message)
	}
	return ports.Checkout{Reference: response.Data.Reference, URL: response.Data.AuthorizationURL}, nil
}

// Verify retrieves the authoritative server-side transaction status.
func (c *Client) Verify(ctx context.Context, reference string) (ports.Verification, error) {
	var response struct {
		Status  bool   `json:"status"`
		Message string `json:"message"`
		Data    struct {
			Reference       string          `json:"reference"`
			Status          string          `json:"status"`
			Amount          int64           `json:"amount"`
			RequestedAmount int64           `json:"requested_amount"`
			Currency        string          `json:"currency"`
			Domain          string          `json:"domain"`
			Channel         string          `json:"channel"`
			PaidAt          *time.Time      `json:"paid_at"`
			GatewayResponse string          `json:"gateway_response"`
			Metadata        json.RawMessage `json:"metadata"`
		} `json:"data"`
	}
	path := "/transaction/verify/" + url.PathEscape(reference)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return ports.Verification{}, err
	}
	if !response.Status {
		return ports.Verification{}, fmt.Errorf("paystack verify rejected: %s", response.Message)
	}
	amount := response.Data.Amount
	if response.Data.RequestedAmount > 0 {
		amount = response.Data.RequestedAmount
	}
	metadata := map[string]string{}
	_ = json.Unmarshal(response.Data.Metadata, &metadata)
	return ports.Verification{
		Reference:  response.Data.Reference,
		Status:     response.Data.Status,
		AmountKobo: amount,
		Currency:   response.Data.Currency,
		Domain:     response.Data.Domain,
		Channel:    response.Data.Channel,
		Metadata:   metadata,
		PaidAt:     response.Data.PaidAt,
		Message:    response.Data.GatewayResponse,
	}, nil
}

// ValidateWebhook authenticates the exact raw payload using Paystack's HMAC-SHA512 signature.
func (c *Client) ValidateWebhook(body []byte, signature string) (ports.GatewayWebhook, error) {
	if c.secretKey == "" || signature == "" {
		return ports.GatewayWebhook{}, errors.New("missing Paystack webhook credentials")
	}
	mac := hmac.New(sha512.New, []byte(c.secretKey))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	actual, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(expected, actual) {
		return ports.GatewayWebhook{}, errors.New("invalid Paystack signature")
	}
	var envelope struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ports.GatewayWebhook{}, fmt.Errorf("decode Paystack webhook: %w", err)
	}
	var data struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return ports.GatewayWebhook{}, fmt.Errorf("decode Paystack webhook data: %w", err)
	}
	return ports.GatewayWebhook{Event: envelope.Event, Reference: data.Reference, Raw: json.RawMessage(body)}, nil
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
		return fmt.Errorf("Paystack request: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Paystack returned %s", response.Status)
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("decode Paystack response: %w", err)
	}
	return nil
}
