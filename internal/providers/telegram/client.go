// Package telegram implements Telegram Bot API messaging and webhook parsing.
package telegram

import (
	"bytes"
	"context"
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

// Client sends messages through a Telegram bot.
type Client struct {
	botToken string
	apiBase  string
	secret   string
	http     *http.Client
}

// InboundMessage is the normalized subset of a Telegram update needed by Xego.
type InboundMessage struct {
	UpdateID        int64
	MessageID       string
	ChatID          string
	UserID          string
	Username        string
	Text            string
	Interactive     string
	MediaType       string // voice, audio, photo, document, video, ""
	MediaID         string
	MediaMime       string
	Caption         string
	CallbackQueryID string
}

// New creates a Telegram Bot API client.
func New(botToken, apiBase, secret string) *Client {
	apiBase = strings.TrimRight(apiBase, "/")
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	return &Client{botToken: botToken, apiBase: apiBase, secret: secret, http: &http.Client{Timeout: 15 * time.Second}}
}

// ValidateSecret authenticates Telegram webhooks using the configured secret token.
func (c *Client) ValidateSecret(header string) error {
	if c.secret == "" {
		return errors.New("Telegram webhook secret is not configured")
	}
	if header != c.secret {
		return errors.New("invalid Telegram webhook secret")
	}
	return nil
}

// ParseInbound extracts text messages, media, and callback-query button selections.
func ParseInbound(body []byte) ([]InboundMessage, error) {
	var update struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			MessageID int64  `json:"message_id"`
			Text      string `json:"text"`
			Caption   string `json:"caption"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			From struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			} `json:"from"`
			Voice *struct {
				FileID   string `json:"file_id"`
				MimeType string `json:"mime_type"`
			} `json:"voice"`
			Audio *struct {
				FileID   string `json:"file_id"`
				MimeType string `json:"mime_type"`
			} `json:"audio"`
			Photo []struct {
				FileID string `json:"file_id"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"photo"`
			Document *struct {
				FileID   string `json:"file_id"`
				MimeType string `json:"mime_type"`
				FileName string `json:"file_name"`
			} `json:"document"`
			Video *struct {
				FileID   string `json:"file_id"`
				MimeType string `json:"mime_type"`
			} `json:"video"`
		} `json:"message"`
		CallbackQuery *struct {
			ID      string `json:"id"`
			Data    string `json:"data"`
			Message struct {
				MessageID int64 `json:"message_id"`
				Chat      struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
			From struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			} `json:"from"`
		} `json:"callback_query"`
	}
	if err := json.Unmarshal(body, &update); err != nil {
		return nil, fmt.Errorf("decode Telegram update: %w", err)
	}
	if update.UpdateID == 0 {
		return nil, nil
	}
	if update.Message != nil {
		msg := InboundMessage{
			UpdateID:  update.UpdateID,
			MessageID: strconv.FormatInt(update.Message.MessageID, 10),
			ChatID:    strconv.FormatInt(update.Message.Chat.ID, 10),
			UserID:    strconv.FormatInt(update.Message.From.ID, 10),
			Username:  strings.TrimSpace(update.Message.From.Username),
			Text:      strings.TrimSpace(update.Message.Text),
			Caption:   strings.TrimSpace(update.Message.Caption),
		}
		// Capture media if present.
		if update.Message.Voice != nil {
			msg.MediaType = "voice"
			msg.MediaID = update.Message.Voice.FileID
			msg.MediaMime = update.Message.Voice.MimeType
		} else if update.Message.Audio != nil {
			msg.MediaType = "audio"
			msg.MediaID = update.Message.Audio.FileID
			msg.MediaMime = update.Message.Audio.MimeType
		} else if len(update.Message.Photo) > 0 {
			// Pick the largest photo size.
			largest := update.Message.Photo[len(update.Message.Photo)-1]
			msg.MediaType = "photo"
			msg.MediaID = largest.FileID
			msg.MediaMime = "image/jpeg"
		} else if update.Message.Document != nil {
			msg.MediaType = "document"
			msg.MediaID = update.Message.Document.FileID
			msg.MediaMime = update.Message.Document.MimeType
		} else if update.Message.Video != nil {
			msg.MediaType = "video"
			msg.MediaID = update.Message.Video.FileID
			msg.MediaMime = update.Message.Video.MimeType
		}
		return []InboundMessage{msg}, nil
	}
	if update.CallbackQuery != nil {
		return []InboundMessage{{
			UpdateID:        update.UpdateID,
			MessageID:       strconv.FormatInt(update.CallbackQuery.Message.MessageID, 10),
			ChatID:          strconv.FormatInt(update.CallbackQuery.Message.Chat.ID, 10),
			UserID:          strconv.FormatInt(update.CallbackQuery.From.ID, 10),
			Username:        strings.TrimSpace(update.CallbackQuery.From.Username),
			Interactive:     strings.TrimSpace(update.CallbackQuery.Data),
			CallbackQueryID: update.CallbackQuery.ID,
		}}, nil
	}
	return nil, nil
}

// SendText sends a plain Telegram message.
func (c *Client) SendText(ctx context.Context, to, body string) error {
	return c.send(ctx, "sendMessage", map[string]any{
		"chat_id": to,
		"text":    body,
	})
}

// SendInteractive sends an inline-keyboard message.
func (c *Client) SendInteractive(ctx context.Context, message ports.InteractiveMessage) error {
	var keyboard [][]map[string]string
	if len(message.Sections) > 0 {
		for _, section := range message.Sections {
			for _, row := range section.Rows {
				text := row.Title
				if row.Description != "" {
					text = text + " — " + row.Description
				}
				keyboard = append(keyboard, []map[string]string{{"text": truncateButton(text), "callback_data": row.ID}})
			}
		}
	} else {
		var line []map[string]string
		for _, button := range message.Buttons {
			line = append(line, map[string]string{"text": truncateButton(button.Title), "callback_data": button.ID})
		}
		if len(line) > 0 {
			keyboard = append(keyboard, line)
		}
	}
	return c.send(ctx, "sendMessage", map[string]any{
		"chat_id":      message.To,
		"text":         message.Body,
		"reply_markup": map[string]any{"inline_keyboard": keyboard},
	})
}

// SendCheckout sends a Telegram message with a URL button for hosted checkout.
func (c *Client) SendCheckout(ctx context.Context, to, body, url string) error {
	return c.SendLink(ctx, to, body, url, "Pay securely")
}

// SendLink sends a Telegram message with a URL button using the given label.
func (c *Client) SendLink(ctx context.Context, to, body, url, label string) error {
	return c.send(ctx, "sendMessage", map[string]any{
		"chat_id": to,
		"text":    body,
		"reply_markup": map[string]any{"inline_keyboard": [][]map[string]string{{
			{"text": label, "url": url},
		}}},
	})
}

// SendTemplate degrades WhatsApp template notifications into normal Telegram messages.
func (c *Client) SendTemplate(ctx context.Context, to, name string, parameters []string) error {
	return c.SendText(ctx, to, strings.Join(parameters, "\n"))
}

// SendImage sends a photo with caption via the Telegram Bot API.
func (c *Client) SendImage(ctx context.Context, to string, imageData []byte, caption string) error {
	if c.botToken == "" {
		return errors.New("Telegram bot token is not configured")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", to)
	if caption != "" {
		_ = writer.WriteField("caption", caption)
	}
	part, err := writer.CreateFormFile("photo", "qr.jpg")
	if err != nil {
		return err
	}
	if _, err := part.Write(imageData); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/bot"+c.botToken+"/sendPhoto", &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Telegram request: %w", err)
	}
	defer response.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Telegram returned %s: %s", response.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// AnswerCallback stops Telegram clients from showing a loading spinner after button taps.
func (c *Client) AnswerCallback(ctx context.Context, callbackQueryID string) error {
	if strings.TrimSpace(callbackQueryID) == "" {
		return nil
	}
	return c.send(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackQueryID})
}

func (c *Client) send(ctx context.Context, method string, payload map[string]any) error {
	if c.botToken == "" {
		return errors.New("Telegram bot token is not configured")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/bot"+c.botToken+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Telegram request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Telegram returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	return nil
}

func truncateButton(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 64 {
		return value
	}
	return value[:61] + "..."
}

// DownloadFile retrieves file bytes from the Telegram Bot API using the file_id.
func (c *Client) DownloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
	if c.botToken == "" {
		return nil, "", errors.New("Telegram bot token is not configured")
	}
	// Step 1: get the file path from Telegram.
	getFileURL := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", c.apiBase, c.botToken, fileID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getFileURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("Telegram getFile: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", fmt.Errorf("Telegram getFile read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("Telegram getFile returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var fileInfo struct {
		Result struct {
			FileID   string `json:"file_id"`
			FileSize int64  `json:"file_size"`
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &fileInfo); err != nil {
		return nil, "", fmt.Errorf("Telegram getFile decode: %w", err)
	}
	if fileInfo.Result.FilePath == "" {
		return nil, "", errors.New("Telegram getFile returned no path")
	}

	// Step 2: download the actual bytes.
	dlURL := fmt.Sprintf("%s/file/bot%s/%s", c.apiBase, c.botToken, fileInfo.Result.FilePath)
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return nil, "", err
	}
	dlResp, err := c.http.Do(dlReq)
	if err != nil {
		return nil, "", fmt.Errorf("Telegram download: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode < 200 || dlResp.StatusCode >= 300 {
		dlBody, _ := io.ReadAll(io.LimitReader(dlResp.Body, 1<<20))
		return nil, "", fmt.Errorf("Telegram download returned %s: %s", dlResp.Status, strings.TrimSpace(string(dlBody)))
	}
	data, err := io.ReadAll(io.LimitReader(dlResp.Body, 16<<20)) // 16 MiB cap
	if err != nil {
		return nil, "", fmt.Errorf("Telegram download read: %w", err)
	}
	// Guess mime from file_path extension.
	mimeType := "application/octet-stream"
	if strings.HasSuffix(fileInfo.Result.FilePath, ".ogg") {
		mimeType = "audio/ogg"
	} else if strings.HasSuffix(fileInfo.Result.FilePath, ".jpg") || strings.HasSuffix(fileInfo.Result.FilePath, ".jpeg") {
		mimeType = "image/jpeg"
	} else if strings.HasSuffix(fileInfo.Result.FilePath, ".png") {
		mimeType = "image/png"
	} else if strings.HasSuffix(fileInfo.Result.FilePath, ".mp4") {
		mimeType = "video/mp4"
	} else if strings.HasSuffix(fileInfo.Result.FilePath, ".pdf") {
		mimeType = "application/pdf"
	}
	return data, mimeType, nil
}
