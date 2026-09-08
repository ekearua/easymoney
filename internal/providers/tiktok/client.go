// Package tiktok implements TikTok Business Messaging as a full-parity
// ports.Messenger channel adapter. The platform has no interactive buttons,
// quick replies, or approved templates, so those primitives degrade to
// numbered text menus and plain URL text while every Xego flow (pay, invoice,
// thrift, data, top-up, individual pay) stays usable end-to-end from TikTok.
package tiktok

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// Client sends messages through the TikTok Business Messaging API.
type Client struct {
	appSecret   string
	accessToken string
	apiBase     string
	http        *http.Client
}

// InboundMessage is the normalized subset of a TikTok message webhook needed by Xego.
type InboundMessage struct {
	EventID        string // provider event id, used for dedup
	OpenID         string // customer open_id (conversation-scoped identity)
	UnionID        string // account-level union id when present
	ConversationID string // provider conversation id, the outbound reply-to address
	Text           string
	MediaType      string // image, ""
	MediaURL       string
	MediaMime      string
	Caption        string
	Username       string
	TimestampMs    int64
}

// New creates a TikTok Business Messaging client.
func New(appSecret, accessToken, apiBase string) *Client {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		apiBase = "https://open.tiktokapis.com"
	}
	return &Client{
		appSecret: appSecret, accessToken: accessToken, apiBase: apiBase,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// ValidateSignature verifies TikTok's TikTok-Signature header: "t=<unix>,s=<hex>"
// where the signature is HMAC-SHA256(clientSecret, "<t>.<body>"). Timestamp
// drift beyond maxAge is rejected to bound replay.
func (c *Client) ValidateSignature(body []byte, header string) error {
	return c.ValidateSignatureAt(body, header, time.Now())
}

// ValidateSignatureAt is ValidateSignature with an injectable clock for tests.
func (c *Client) ValidateSignatureAt(body []byte, header string, now time.Time) error {
	if c.appSecret == "" {
		return errors.New("TikTok app secret is not configured")
	}
	timestamp := ""
	signature := ""
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "t":
			timestamp = strings.TrimSpace(value)
		case "s":
			signature = strings.TrimSpace(value)
		}
	}
	if timestamp == "" || signature == "" {
		return errors.New("missing or malformed TikTok signature")
	}
	mac := hmac.New(sha256.New, []byte(c.appSecret))
	_, _ = mac.Write([]byte(timestamp + "." + string(body)))
	expected := mac.Sum(nil)
	actual, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(expected, actual) {
		return errors.New("invalid TikTok signature")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("malformed TikTok signature timestamp")
	}
	if now.Sub(time.Unix(seconds, 0)) > 5*time.Minute || time.Unix(seconds, 0).After(now.Add(time.Minute)) {
		return errors.New("stale TikTok signature timestamp")
	}
	return nil
}

// messageEvent mirrors the message event inside a TikTok webhook payload.
type messageEvent struct {
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	Timestamp int64  `json:"timestamp"`
	Data      struct {
		ConversationID string `json:"conversation_id"`
		Sender         struct {
			OpenID  string `json:"open_id"`
			UnionID string `json:"union_id"`
			StaffID string `json:"staff_id"`
		} `json:"sender"`
		Content struct {
			Text  string `json:"text"`
			Media []struct {
				URL     string `json:"url"`
				MIME    string `json:"mime_type"`
				Format  string `json:"format"`
				Caption string `json:"caption"`
			} `json:"media"`
		} `json:"content"`
	} `json:"data"`
}

// ParseInbound extracts customer text and image messages from a TikTok
// message-event webhook. Unrecognized event types parse to no messages so
// read receipts and typing indicators can be ignored safely.
func ParseInbound(body []byte) ([]InboundMessage, error) {
	var envelope struct {
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode TikTok webhook: %w", err)
	}
	var event messageEvent
	switch {
	case len(envelope.Event) == 0:
		// Some TikTok surfaces deliver the event object directly.
		if err := json.Unmarshal(body, &event); err != nil {
			return nil, fmt.Errorf("decode TikTok event: %w", err)
		}
	default:
		// Try the nested-object form first, then the JSON-string-encoded
		// form (TikTok's documented webhook shape wraps the event object
		// in a string).
		if err := json.Unmarshal(envelope.Event, &event); err != nil {
			var encoded string
			if stringErr := json.Unmarshal(envelope.Event, &encoded); stringErr != nil {
				return nil, fmt.Errorf("decode TikTok event envelope: %w", err)
			}
			if strings.TrimSpace(encoded) == "" {
				// An explicitly empty event carries nothing to process.
				return nil, nil
			}
			if err := json.Unmarshal([]byte(encoded), &event); err != nil {
				return nil, fmt.Errorf("decode TikTok event: %w", err)
			}
		}
	}
	return normalizeTikTokEvents([]messageEvent{event}), nil
}

func normalizeTikTokEvents(events []messageEvent) []InboundMessage {
	var out []InboundMessage
	for _, event := range events {
		sender := strings.TrimSpace(event.Data.Sender.OpenID)
		if sender == "" {
			continue
		}
		text := strings.TrimSpace(event.Data.Content.Text)
		var mediaURL, mediaMime, caption, mediaType string
		if len(event.Data.Content.Media) > 0 && text == "" {
			m := event.Data.Content.Media[0]
			mediaURL = m.URL
			mediaMime = m.MIME
			if mediaMime == "" {
				mediaMime = "image/jpeg"
			}
			caption = strings.TrimSpace(m.Caption)
			mediaType = "image"
		}
		eventID := strings.TrimSpace(event.EventID)
		if eventID == "" {
			eventID = "tiktok:" + sender + ":" + strconv.FormatInt(event.Timestamp, 10)
		}
		out = append(out, InboundMessage{
			EventID:        eventID,
			OpenID:         sender,
			UnionID:        strings.TrimSpace(event.Data.Sender.UnionID),
			ConversationID: strings.TrimSpace(event.Data.ConversationID),
			Text:           text,
			MediaType:      mediaType,
			MediaURL:       mediaURL,
			MediaMime:      mediaMime,
			Caption:        caption,
			TimestampMs:    event.Timestamp,
		})
	}
	return out
}

// SendText sends a plain TikTok DM.
func (c *Client) SendText(ctx context.Context, to, body string) error {
	return c.send(ctx, to, body)
}

// SendInteractive degrades WhatsApp-style interactive messages into numbered
// text menus: rows become "1. Title — Description" lines and the FSM's row
// IDs are preserved as plain-text choices the customer types back (the FSM's
// numeric parsing accepts the index; row IDs that are not numeric still work
// because handleMenu matches titles case-insensitively too).
func (c *Client) SendInteractive(ctx context.Context, message ports.InteractiveMessage) error {
	var body strings.Builder
	body.WriteString(message.Body)
	index := 0
	appendRow := func(title, description string) {
		index++
		line := fmt.Sprintf("\n%d. %s", index, title)
		if description != "" {
			line += " — " + description
		}
		body.WriteString(line)
	}
	for _, section := range message.Sections {
		for _, row := range section.Rows {
			appendRow(row.Title, row.Description)
		}
	}
	for _, button := range message.Buttons {
		appendRow(button.Title, "")
	}
	return c.send(ctx, message.To, body.String())
}

// SendCheckout degrades the hosted-checkout button to a text link.
func (c *Client) SendCheckout(ctx context.Context, to, body, url string) error {
	return c.SendText(ctx, to, body+"\n\n"+url)
}

// SendLink degrades the web-flow message-1 button to a text link.
func (c *Client) SendLink(ctx context.Context, to, body, url, label string) error {
	return c.SendText(ctx, to, body+"\n\n"+label+": "+url)
}

// SendTemplate degrades WhatsApp template notifications to plain text.
func (c *Client) SendTemplate(ctx context.Context, to, name string, parameters []string) error {
	return c.SendText(ctx, to, strings.Join(parameters, "\n"))
}

// SendImage sends an image message when the platform accepts the media
// upload path; Xego receipts always carry a text receipt link, so on upload
// failure the caption (which includes the receipt URL) is sent as text.
func (c *Client) SendImage(ctx context.Context, to string, imageData []byte, caption string) error {
	if err := c.sendImageMedia(ctx, to, imageData, caption); err == nil {
		return nil
	}
	if caption == "" {
		caption = "Xego receipt"
	}
	return c.SendText(ctx, to, caption)
}

func (c *Client) sendImageMedia(ctx context.Context, to string, imageData []byte, caption string) error {
	if c.accessToken == "" {
		return errors.New("TikTok access token is not configured")
	}
	// Business Messaging image send accepts a multipart upload keyed by
	// conversation id.
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("conversation_id", to)
	if caption != "" {
		_ = writer.WriteField("text", caption)
	}
	part, err := writer.CreateFormFile("image", "receipt.jpg")
	if err != nil {
		return err
	}
	if _, err := part.Write(imageData); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/v1/message/image/", &form)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("TikTok request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("TikTok returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (c *Client) send(ctx context.Context, to, body string) error {
	if c.accessToken == "" {
		return errors.New("TikTok access token is not configured")
	}
	payload := map[string]any{
		"conversation_id": to,
		"content":         map[string]any{"text": body},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/v1/message/text/", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("TikTok request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("TikTok returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}
