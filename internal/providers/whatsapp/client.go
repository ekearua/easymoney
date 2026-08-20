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
	MediaType   string // image, audio, video, document, sticker, ""
	MediaID     string
	MediaMime   string
	Caption     string
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

// ParseInbound extracts customer text, interactive selections, and media from a webhook.
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
						Image struct {
							ID       string `json:"id"`
							MimeType string `json:"mime_type"`
							Caption  string `json:"caption"`
						} `json:"image"`
						Audio struct {
							ID       string `json:"id"`
							MimeType string `json:"mime_type"`
						} `json:"audio"`
						Video struct {
							ID       string `json:"id"`
							MimeType string `json:"mime_type"`
							Caption  string `json:"caption"`
						} `json:"video"`
						Document struct {
							ID       string `json:"id"`
							MimeType string `json:"mime_type"`
							Caption  string `json:"caption"`
						} `json:"document"`
						Sticker struct {
							ID       string `json:"id"`
							MimeType string `json:"mime_type"`
						} `json:"sticker"`
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
				// Capture media if present.
				switch message.Type {
				case "image":
					parsed.MediaType = "image"
					parsed.MediaID = message.Image.ID
					parsed.MediaMime = message.Image.MimeType
					parsed.Caption = message.Image.Caption
				case "audio":
					parsed.MediaType = "audio"
					parsed.MediaID = message.Audio.ID
					parsed.MediaMime = message.Audio.MimeType
				case "video":
					parsed.MediaType = "video"
					parsed.MediaID = message.Video.ID
					parsed.MediaMime = message.Video.MimeType
					parsed.Caption = message.Video.Caption
				case "document":
					parsed.MediaType = "document"
					parsed.MediaID = message.Document.ID
					parsed.MediaMime = message.Document.MimeType
					parsed.Caption = message.Document.Caption
				case "sticker":
					parsed.MediaType = "sticker"
					parsed.MediaID = message.Sticker.ID
					parsed.MediaMime = message.Sticker.MimeType
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

// DownloadMedia retrieves media bytes from the WhatsApp Graph API.
// It performs the two-step fetch: media ID → signed URL → bytes. The returned
// mime type indicates the content type. A size cap of 16 MiB is enforced to
// protect memory.
func (c *Client) DownloadMedia(ctx context.Context, mediaID string) ([]byte, string, error) {
	if c.accessToken == "" {
		return nil, "", errors.New("WhatsApp credentials are not configured")
	}
	if c.graphVersion == "" {
		return nil, "", errors.New("WhatsApp graph version is not configured")
	}

	// Step 1: resolve media ID to a signed download URL.
	endpoint := fmt.Sprintf("https://graph.facebook.com/%s/%s", c.graphVersion, mediaID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("WhatsApp media resolve: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", fmt.Errorf("WhatsApp media resolve read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("WhatsApp media resolve returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var mediaInfo struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
	}
	if err := json.Unmarshal(respBody, &mediaInfo); err != nil {
		return nil, "", fmt.Errorf("WhatsApp media resolve decode: %w", err)
	}
	if mediaInfo.URL == "" {
		return nil, "", errors.New("WhatsApp media resolve returned no URL")
	}

	// Step 2: download the actual bytes from the signed URL.
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaInfo.URL, nil)
	if err != nil {
		return nil, "", err
	}
	dlResp, err := c.http.Do(dlReq)
	if err != nil {
		return nil, "", fmt.Errorf("WhatsApp media download: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode < 200 || dlResp.StatusCode >= 300 {
		dlBody, _ := io.ReadAll(io.LimitReader(dlResp.Body, 1<<20))
		return nil, "", fmt.Errorf("WhatsApp media download returned %s: %s", dlResp.Status, strings.TrimSpace(string(dlBody)))
	}
	data, err := io.ReadAll(io.LimitReader(dlResp.Body, 16<<20)) // 16 MiB cap
	if err != nil {
		return nil, "", fmt.Errorf("WhatsApp media download read: %w", err)
	}
	return data, mediaInfo.MimeType, nil
}
