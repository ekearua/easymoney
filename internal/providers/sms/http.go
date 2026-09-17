// Package sms provides provider adapters for outbound SMS. The HTTP adapter
// sends through a generic REST endpoint so a live gateway (Termii, Africa's
// Talking, Twilio, SMS360, ...) can be enabled with the same contract.
//
// Request (POST {SMS_API_BASE}, Content-Type: application/json):
//
//	{
//	  "to":        "+2348012345678",
//	  "message":   "Your Xego request XG-1234 is fulfilled.",
//	  "sender_id": "Xego",
//	  "reference": "xego-outbox-42"
//	}
//
// When an API key is configured it is sent as both "Authorization: Bearer ..."
// and "X-API-Key: ..." so common gateway schemes work unchanged. The response
// should carry the provider's message identifier in one of:
// message_id | messageId | msg_id | id | reference.
package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTP sends outbound SMS through a generic REST endpoint.
type HTTP struct {
	baseURL    string
	apiKey     string
	senderID   string
	httpClient *http.Client
}

// NewHTTP creates the generic SMS sender. An empty timeout defaults to 30
// seconds.
func NewHTTP(baseURL, apiKey, senderID string, timeout time.Duration) *HTTP {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTP{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     strings.TrimSpace(apiKey),
		senderID:   strings.TrimSpace(senderID),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// sendRequest is the wire body sent to the gateway.
type sendRequest struct {
	To        string `json:"to"`
	Message   string `json:"message"`
	SenderID  string `json:"sender_id,omitempty"`
	Reference string `json:"reference,omitempty"`
}

// sendResponse is the normalized response envelope we accept.
type sendResponse struct {
	MessageID string `json:"message_id"`
	MessageId string `json:"messageId"`
	MsgID     string `json:"msg_id"`
	ID        string `json:"id"`
	Reference string `json:"reference"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	Message   string `json:"message"`
}

// Send delivers one SMS and returns the provider message identifier.
func (h *HTTP) Send(ctx context.Context, to, body string) (string, error) {
	to = strings.TrimSpace(to)
	body = strings.TrimSpace(body)
	if to == "" {
		return "", fmt.Errorf("recipient is required for SMS")
	}
	if body == "" {
		return "", fmt.Errorf("message body is required for SMS")
	}
	if h.baseURL == "" {
		return "", fmt.Errorf("SMS API base URL is not configured")
	}

	resp, err := h.send(ctx, to, body)
	if err != nil {
		return "", err
	}
	return firstNonEmpty(resp.MessageID, resp.MessageId, resp.MsgID, resp.ID, resp.Reference), nil
}

// send performs the HTTP POST and decodes the JSON response.
func (h *HTTP) send(ctx context.Context, to, body string) (sendResponse, error) {
	payload := sendRequest{
		To:        to,
		Message:   body,
		SenderID:  h.senderID,
		Reference: "xego-outbox",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return sendResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL, bytes.NewReader(data))
	if err != nil {
		return sendResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if h.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
		req.Header.Set("X-API-Key", h.apiKey)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return sendResponse{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return sendResponse{}, fmt.Errorf("read response body: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
		// fall through to parse
	case http.StatusUnauthorized:
		return sendResponse{}, fmt.Errorf("SMS API key rejected (401)")
	case http.StatusTooManyRequests:
		return sendResponse{}, fmt.Errorf("SMS rate limit exceeded (429)")
	default:
		var errResp sendResponse
		if json.Unmarshal(respBody, &errResp) == nil && strings.TrimSpace(errResp.Error) != "" {
			return sendResponse{}, fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, errResp.Error)
		}
		return sendResponse{}, fmt.Errorf("API error HTTP %d", resp.StatusCode)
	}

	var parsed sendResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return sendResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return parsed, nil
}

// firstNonEmpty returns the first trimmed non-empty value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
