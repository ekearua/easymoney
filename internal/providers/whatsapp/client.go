// Package whatsapp implements WhatsApp Cloud API messaging and webhook parsing.
package whatsapp

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

// Client sends messages through a WhatsApp Business phone number.
type Client struct {
	appSecret      string
	accessToken    string
	phoneID        string
	graphVersion   string
	templateLocale string
	http           *http.Client
}

// InboundMessage is the normalized subset of a WhatsApp webhook needed by the bot.
type InboundMessage struct {
	ID          string
	From        string
	Text        string
	Interactive string
	Timestamp   time.Time
}

// New creates a WhatsApp Cloud API client.
func New(appSecret, accessToken, phoneID, graphVersion, templateLocale string) *Client {
	return &Client{
		appSecret: appSecret, accessToken: accessToken, phoneID: phoneID,
		graphVersion: graphVersion, templateLocale: templateLocale, http: &http.Client{Timeout: 15 * time.Second},
	}
}

// ValidateSignature authenticates the exact raw webhook body using Meta's app secret.
func (c *Client) ValidateSignature(body []byte, signature string) error {
	const prefix = "sha256="
	if c.appSecret == "" || !strings.HasPrefix(signature, prefix) {
		return errors.New("missing or malformed Meta signature")
	}
	actual, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return errors.New("malformed Meta signature")
	}
	mac := hmac.New(sha256.New, []byte(c.appSecret))
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), actual) {
		return errors.New("invalid Meta signature")
	}
	return nil
}

// ParseInbound extracts customer text and interactive selections from a webhook.
func ParseInbound(body []byte) ([]InboundMessage, error) {
	var envelope struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Messages []struct {
						ID        string `json:"id"`
						From      string `json:"from"`
						Timestamp string `json:"timestamp"`
						Type      string `json:"type"`
						Text      struct {
							Body string `json:"body"`
						} `json:"text"`
						Interactive struct {
							Type        string `json:"type"`
							ButtonReply struct {
								ID    string `json:"id"`
								Title string `json:"title"`
							} `json:"button_reply"`
							ListReply struct {
								ID    string `json:"id"`
								Title string `json:"title"`
							} `json:"list_reply"`
						} `json:"interactive"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode Meta webhook: %w", err)
	}
	var result []InboundMessage
	for _, entry := range envelope.Entry {
		for _, change := range entry.Changes {
			for _, message := range change.Value.Messages {
				parsed := InboundMessage{ID: message.ID, From: message.From, Text: strings.TrimSpace(message.Text.Body)}
				if message.Interactive.ButtonReply.ID != "" {
					parsed.Interactive = message.Interactive.ButtonReply.ID
				}
				if message.Interactive.ListReply.ID != "" {
					parsed.Interactive = message.Interactive.ListReply.ID
				}
				if unix, err := strconv.ParseInt(message.Timestamp, 10, 64); err == nil {
					parsed.Timestamp = time.Unix(unix, 0)
				}
				result = append(result, parsed)
			}
		}
	}
	return result, nil
}

// SendText sends a plain customer-service-window message.
func (c *Client) SendText(ctx context.Context, to, body string) error {
	return c.send(ctx, map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                recipientForCloudAPI(to),
		"type":              "text",
		"text":              map[string]any{"preview_url": false, "body": body},
	})
}

// SendCheckout sends a call-to-action URL message for hosted payment.
func (c *Client) SendCheckout(ctx context.Context, to, body, url string) error {
	return c.send(ctx, map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                recipientForCloudAPI(to),
		"type":              "interactive",
		"interactive": map[string]any{
			"type": "cta_url",
			"body": map[string]any{"text": body},
			"action": map[string]any{
				"name":       "cta_url",
				"parameters": map[string]any{"display_text": "Pay securely", "url": url},
			},
		},
	})
}

// SendInteractive sends either reply buttons or a merchant/menu list.
func (c *Client) SendInteractive(ctx context.Context, message ports.InteractiveMessage) error {
	interactive := map[string]any{"body": map[string]any{"text": message.Body}}
	if len(message.Sections) > 0 {
		var sections []map[string]any
		for _, section := range message.Sections {
			var rows []map[string]any
			for _, row := range section.Rows {
				rows = append(rows, map[string]any{"id": row.ID, "title": row.Title, "description": row.Description})
			}
			sections = append(sections, map[string]any{"title": section.Title, "rows": rows})
		}
		interactive["type"] = "list"
		interactive["action"] = map[string]any{
			"button":   message.ButtonLabel,
			"sections": sections,
		}
	} else {
		var buttons []map[string]any
		for _, button := range message.Buttons {
			buttons = append(buttons, map[string]any{
				"type":  "reply",
				"reply": map[string]any{"id": button.ID, "title": button.Title},
			})
		}
		interactive["type"] = "button"
		interactive["action"] = map[string]any{"buttons": buttons}
	}
	return c.send(ctx, map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                recipientForCloudAPI(message.To),
		"type":              "interactive",
		"interactive":       interactive,
	})
}

// SendTemplate sends an approved utility template outside the service window.
func (c *Client) SendTemplate(ctx context.Context, to, name string, parameters []string) error {
	var values []map[string]any
	for _, parameter := range parameters {
		values = append(values, map[string]any{"type": "text", "text": parameter})
	}
	return c.send(ctx, map[string]any{
		"messaging_product": "whatsapp",
		"to":                recipientForCloudAPI(to),
		"type":              "template",
		"template": map[string]any{
			"name":     name,
			"language": map[string]any{"code": c.templateLocale},
			"components": []map[string]any{{
				"type":       "body",
				"parameters": values,
			}},
		},
	})
}

// SendImage uploads an image to WhatsApp media and sends it as an image message.
func (c *Client) SendImage(ctx context.Context, to string, imageData []byte, caption string) error {
	if c.accessToken == "" || c.phoneID == "" {
		return errors.New("WhatsApp credentials are not configured")
	}
	mediaID, err := c.uploadMedia(ctx, imageData)
	if err != nil {
		return fmt.Errorf("WhatsApp media upload: %w", err)
	}
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                recipientForCloudAPI(to),
		"type":              "image",
		"image":             map[string]any{"id": mediaID},
	}
	if caption != "" {
		payload["image"] = map[string]any{"id": mediaID, "caption": caption}
	}
	return c.send(ctx, payload)
}

// uploadMedia posts image bytes to the WhatsApp media endpoint and returns the media ID.
func (c *Client) uploadMedia(ctx context.Context, imageData []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("messaging_product", "whatsapp")
	_ = writer.WriteField("type", "image/jpeg")
	part, err := writer.CreateFormFile("file", "qr.jpg")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(imageData); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("https://graph.facebook.com/%s/%s/media", c.graphVersion, c.phoneID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("WhatsApp media request: %w", err)
	}
	defer response.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("WhatsApp media returned %s: %s", response.Status, strings.TrimSpace(string(respBody)))
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode media response: %w", err)
	}
	if result.ID == "" {
		return "", errors.New("WhatsApp media upload returned empty ID")
	}
	return result.ID, nil
}

func (c *Client) send(ctx context.Context, payload map[string]any) error {
	if c.accessToken == "" || c.phoneID == "" {
		return errors.New("WhatsApp credentials are not configured")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("https://graph.facebook.com/%s/%s/messages", c.graphVersion, c.phoneID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("WhatsApp request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("WhatsApp returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	return nil
}

// recipientForCloudAPI converts our stored E.164 identity into the Cloud API
// recipient format. We keep phone numbers in the database as +234... for human
// clarity, but Meta message sends expect digits only, including country code.
func recipientForCloudAPI(to string) string {
	replacer := strings.NewReplacer("+", "", " ", "", "-", "", "(", "", ")", "")
	return replacer.Replace(strings.TrimSpace(to))
}
