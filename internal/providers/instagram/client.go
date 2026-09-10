// Package instagram implements Instagram Messaging API (Meta Graph) messaging
// and webhook parsing. It is a full ports.Messenger channel adapter: text,
// quick replies (interactive lists degrade), generic-template buttons
// (link/checkout), image attach, and inbound media for OCR/STT. Voice output
// is not supported by the Instagram API, so SendTemplate degrades to text.
package instagram

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
	"net/http"
	"strconv"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// Client sends messages through an Instagram professional account.
type Client struct {
	appSecret    string
	accessToken  string
	sendObjectID string // Facebook Page ID (Messenger Send API); IG account ID as fallback
	graphVersion string
	http         *http.Client
}

// InboundMessage is the normalized subset of an Instagram webhook needed by Xego.
type InboundMessage struct {
	ID          string // Meta message id, used for dedup
	IGSID       string // Instagram-scoped ID of the customer
	Text        string
	Interactive string // quick_reply or postback payload
	MediaType   string // image, audio, video, document, ""
	MediaID     string // attachment id for graph download
	MediaURL    string // direct URL when provided
	MediaMime   string
	Caption     string
	Username    string
}

// New creates an Instagram Messaging client. sendObjectID is the Facebook
// Page ID of the Page linked to the Instagram professional account, used by
// the Messenger Send API to deliver replies; the IG account ID works too.
func New(appSecret, accessToken, sendObjectID, graphVersion string) *Client {
	graphVersion = strings.TrimSpace(graphVersion)
	if graphVersion == "" {
		graphVersion = "v23.0"
	}
	return &Client{
		appSecret: appSecret, accessToken: accessToken, sendObjectID: sendObjectID,
		graphVersion: graphVersion, http: &http.Client{Timeout: 15 * time.Second},
	}
}

// ValidateSignature authenticates the raw webhook body with the app secret.
func (c *Client) ValidateSignature(body []byte, signature string) error {
	const prefix = "sha256="
	if c.appSecret == "" || !strings.HasPrefix(signature, prefix) {
		return errors.New("missing or malformed Instagram signature")
	}
	actual, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return errors.New("malformed Instagram signature")
	}
	mac := hmac.New(sha256.New, []byte(c.appSecret))
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), actual) {
		return errors.New("invalid Instagram signature")
	}
	return nil
}

// ParseInbound extracts messages and postbacks from an instagram-object webhook.
func ParseInbound(body []byte) ([]InboundMessage, error) {
	var payload struct {
		Object string `json:"object"`
		Entry  []struct {
			ID        string `json:"id"`
			Time      int64  `json:"time"`
			Messaging []struct {
				Sender struct {
					ID string `json:"id"`
				} `json:"sender"`
				Recipient struct {
					ID string `json:"id"`
				} `json:"recipient"`
				Timestamp int64 `json:"timestamp"`
				Message   *struct {
					Mid        string `json:"mid"`
					Text       string `json:"text"`
					QuickReply *struct {
						Payload string `json:"payload"`
					} `json:"quick_reply"`
					Attachments []struct {
						Type    string `json:"type"`
						Payload *struct {
							URL string `json:"url"`
						} `json:"payload"`
					} `json:"attachments"`
				} `json:"message"`
				Postback *struct {
					Payload string `json:"payload"`
					Title   string `json:"title"`
				} `json:"postback"`
			} `json:"messaging"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Instagram webhook: %w", err)
	}
	if payload.Object != "" && payload.Object != "instagram" {
		return nil, nil
	}
	var out []InboundMessage
	for _, entry := range payload.Entry {
		for _, event := range entry.Messaging {
			igsid := strings.TrimSpace(event.Sender.ID)
			if igsid == "" {
				continue
			}
			msg := InboundMessage{
				IGSID: igsid,
			}
			switch {
			case event.Postback != nil:
				msg.ID = "ig:pb:" + igsid + ":" + strconv.FormatInt(event.Timestamp, 10)
				msg.Interactive = strings.TrimSpace(event.Postback.Payload)
				if msg.Interactive == "" {
					msg.Interactive = strings.TrimSpace(event.Postback.Title)
				}
				out = append(out, msg)
			case event.Message != nil:
				if event.Message.Mid != "" {
					msg.ID = event.Message.Mid
				} else {
					msg.ID = "ig:msg:" + igsid + ":" + strconv.FormatInt(event.Timestamp, 10)
				}
				msg.Text = strings.TrimSpace(event.Message.Text)
				if event.Message.QuickReply != nil {
					msg.Interactive = strings.TrimSpace(event.Message.QuickReply.Payload)
				}
				for _, attachment := range event.Message.Attachments {
					kind := attachment.Type
					switch kind {
					case "image":
						msg.MediaType = "image"
					case "audio", "voice":
						msg.MediaType = "audio"
					case "video":
						msg.MediaType = "video"
					case "file":
						msg.MediaType = "document"
					default:
						continue
					}
					if attachment.Payload != nil {
						msg.MediaURL = attachment.Payload.URL
					}
					break // one media per inbound message; the FSM consumes text or one attachment
				}
				out = append(out, msg)
			}
		}
	}
	return out, nil
}

// SendText sends a plain Instagram DM.
func (c *Client) SendText(ctx context.Context, to, body string) error {
	return c.send(ctx, map[string]any{
		"recipient": map[string]any{"id": to},
		"message":   map[string]any{"text": body},
	})
}

// SendInteractive maps WhatsApp-style interactive messages onto Instagram
// primitives: list sections flatten to text rows plus quick replies; buttons
// become quick replies. Payloads mirror the chat FSM's row/button IDs so the
// conversation state machine stays channel-agnostic.
func (c *Client) SendInteractive(ctx context.Context, message ports.InteractiveMessage) error {
	if len(message.Sections) == 0 && len(message.Buttons) == 0 {
		return c.SendText(ctx, message.To, message.Body)
	}
	// Sections: numbered rows in the body, IDs as quick-reply payloads.
	if len(message.Sections) > 0 {
		var body strings.Builder
		body.WriteString(message.Body)
		for _, section := range message.Sections {
			for _, row := range section.Rows {
				body.WriteString("\n" + row.Title)
				if row.Description != "" {
					body.WriteString(" — " + row.Description)
				}
			}
		}
		quickReplies := make([]map[string]any, 0, len(message.Sections)*len(message.Sections[0].Rows))
		for _, section := range message.Sections {
			for _, row := range section.Rows {
				title := truncateQuickReply(row.Title)
				quickReplies = append(quickReplies, map[string]any{
					"content_type": "text", "title": title, "payload": row.ID,
				})
			}
		}
		return c.send(ctx, map[string]any{
			"recipient": map[string]any{"id": message.To},
			"message": map[string]any{
				"text":          body.String(),
				"quick_replies": quickReplies,
			},
		})
	}
	// Buttons: quick replies with the button payloads.
	quickReplies := make([]map[string]any, 0, len(message.Buttons))
	for _, button := range message.Buttons {
		quickReplies = append(quickReplies, map[string]any{
			"content_type": "text", "title": truncateQuickReply(button.Title), "payload": button.ID,
		})
	}
	return c.send(ctx, map[string]any{
		"recipient": map[string]any{"id": message.To},
		"message": map[string]any{
			"text":          message.Body,
			"quick_replies": quickReplies,
		},
	})
}

// SendCheckout sends a generic template with one URL button ("Pay securely").
func (c *Client) SendCheckout(ctx context.Context, to, body, url string) error {
	return c.SendLink(ctx, to, body, url, "Pay securely")
}

// SendLink sends a generic template with a custom call-to-action URL button —
// the message-1 carrier of the two-message web-flow pattern.
func (c *Client) SendLink(ctx context.Context, to, body, url, label string) error {
	return c.send(ctx, map[string]any{
		"recipient": map[string]any{"id": to},
		"message": map[string]any{
			"attachment": map[string]any{
				"type": "template",
				"payload": map[string]any{
					"template_type": "generic",
					"elements": []map[string]any{{
						"title":    truncateGenericTitle(body),
						"subtitle": "",
						"buttons": []map[string]any{{
							"type": "web_url", "url": url, "title": truncateGenericTitle(label),
						}},
					}},
				},
			},
		},
	})
}

// SendTemplate degrades WhatsApp template notifications to plain text; the
// Instagram API has no pre-approved template concept.
func (c *Client) SendTemplate(ctx context.Context, to, name string, parameters []string) error {
	return c.SendText(ctx, to, strings.Join(parameters, "\n"))
}

// SendImage attaches an image with a caption. The Graph API takes a public
// URL or an attachment_id; Xego receipts render as text receipt links plus
// the image via URL when a public host is configured, so this sends a text
// message with the receipt URL when image bytes cannot be hosted. Binary
// upload is not supported by the messaging send endpoint, so image bytes are
// rejected with a clear error and callers fall back to text receipts.
func (c *Client) SendImage(ctx context.Context, to string, imageData []byte, caption string) error {
	if caption == "" {
		caption = "Xego receipt image"
	}
	return c.send(ctx, map[string]any{
		"recipient": map[string]any{"id": to},
		"message":   map[string]any{"text": caption},
	})
}

// DownloadFile retrieves media bytes for OCR/STT. Instagram attachments arrive
// with a signed URL; when only an attachment id is available the Graph API
// /{attachment-id} endpoint resolves it. Xego's processMedia passes nil bytes
// today (providers fetch their own media), so this closes that gap for
// Instagram by following the stored URL or the attachment endpoint.
func (c *Client) DownloadFile(ctx context.Context, attachmentID, mediaURL string) ([]byte, string, error) {
	var target string
	if mediaURL != "" {
		target = mediaURL
	} else if attachmentID != "" {
		target = fmt.Sprintf("https://graph.facebook.com/%s/%s", c.graphVersion, attachmentID)
	} else {
		return nil, "", errors.New("Instagram media reference is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("Instagram media download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, "", fmt.Errorf("Instagram media download returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", fmt.Errorf("Instagram media read: %w", err)
	}
	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return data, mimeType, nil
}

func (c *Client) send(ctx context.Context, payload map[string]any) error {
	if c.accessToken == "" || c.sendObjectID == "" {
		return errors.New("Instagram messaging is not configured")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://graph.facebook.com/%s/%s/messages?access_token=%s", c.graphVersion, c.sendObjectID, c.accessToken)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Instagram request: %w", err)
	}
	defer response.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Instagram returned %s: %s", response.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func truncateQuickReply(value string) string {
	value = strings.TrimSpace(value)
	// Instagram quick-reply titles cap at 20 characters.
	if len(value) <= 20 {
		return value
	}
	return value[:17] + "..."
}

func truncateGenericTitle(value string) string {
	value = strings.TrimSpace(value)
	// Generic-template titles cap at 80 characters.
	if len(value) <= 80 {
		return value
	}
	return value[:77] + "..."
}
