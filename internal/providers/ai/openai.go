// Package ai provides provider adapters for AI conversations, image-to-text
// (OCR), and speech-to-text (STT). The OpenAI-compatible adapter covers all
// three via chat completions, vision, and Whisper endpoints.
package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// OpenAI implements ports.ChatAI, ports.ImageReader, and ports.SpeechToText
// via an OpenAI-compatible API (OpenAI, Azure OpenAI, local proxies).
type OpenAI struct {
	apiKey     string
	baseURL    string
	chatModel  string
	whisperMod string
	httpClient *http.Client
}

// NewOpenAI creates the OpenAI-compatible adapter.
func NewOpenAI(apiKey, baseURL, chatModel, whisperModel string, timeout time.Duration) *OpenAI {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if chatModel == "" {
		chatModel = "gpt-4o"
	}
	if whisperModel == "" {
		whisperModel = "whisper-1"
	}
	return &OpenAI{
		apiKey:     apiKey,
		baseURL:    baseURL,
		chatModel:  chatModel,
		whisperMod: whisperModel,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// --- ChatAI implementation ---

// knownIntents enumerates the FSM intents the classifier can return.
var knownIntents = []string{
	"pay", "buy_data", "invoice", "thrift", "become_individual", "verify_id",
	"status", "help", "menu", "cancel", "skip", "ai", "none",
}

func (o *OpenAI) ClassifyIntent(ctx context.Context, userMessage string, contextLines []string) (ports.IntentResult, error) {
	systemPrompt := `You are an intent classifier for a WhatsApp payment assistant called Xego.
Given the user message and conversation context, classify it into exactly one intent and extract any entities.

Known intents:
- pay: user wants to send money or make a payment (extract: amount, recipient/merchant_name)
- buy_data: user wants to buy mobile data (extract: network, plan_size)
- invoice: user wants to create or pay an invoice
- thrift: user wants to create, join, or manage a thrift group (extract: thrift_name)
- become_individual: user wants to become an individual/thrift member
- verify_id: user is providing NIN or BVN (extract: id_type, id_number)
- status: user wants to check payment/order status
- help: user needs help or has a question
- menu: user wants to see the menu
- cancel: user wants to cancel current action
- skip: user wants to skip a step
- ai: user wants to chat with the AI assistant (questions, general conversation)
- none: message does not match any intent

Respond with JSON only: {"intent":"...","entities":{...},"confidence":0.0-1.0}
Confidence must be 0.0 if no intent matches.`

	var msgs []chatMessage
	msgs = append(msgs, chatMessage{Role: "system", Content: systemPrompt})
	if len(contextLines) > 0 {
		contextStr := strings.Join(contextLines, "\n")
		msgs = append(msgs, chatMessage{Role: "system", Content: "Conversation context:\n" + contextStr})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: userMessage})

	resp, err := o.chatCompletion(ctx, msgs, true)
	if err != nil {
		return ports.IntentResult{}, fmt.Errorf("classify intent: %w", err)
	}
	if resp == "" {
		return ports.IntentResult{}, nil
	}

	var result struct {
		Intent     string            `json:"intent"`
		Entities   map[string]string `json:"entities"`
		Confidence float64           `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return ports.IntentResult{}, fmt.Errorf("parse intent JSON: %w", err)
	}
	return ports.IntentResult{
		Intent:     result.Intent,
		Entities:   result.Entities,
		Confidence: result.Confidence,
	}, nil
}

func (o *OpenAI) Answer(ctx context.Context, question string, contextLines []string) (string, error) {
	systemPrompt := `You are Xego, a helpful WhatsApp payment assistant for Nigerian users.
Answer questions about payments, data purchases, thrift groups, invoices, and account status.
Keep responses short (2-3 sentences max) and conversational.
Never ask for or mention card numbers, PINs, CVVs, or OTPs.
If unsure, suggest the user type "menu" to see available options.`

	var msgs []chatMessage
	msgs = append(msgs, chatMessage{Role: "system", Content: systemPrompt})
	if len(contextLines) > 0 {
		contextStr := strings.Join(contextLines, "\n")
		msgs = append(msgs, chatMessage{Role: "system", Content: "Conversation context:\n" + contextStr})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: question})

	return o.chatCompletion(ctx, msgs, false)
}

// --- ImageReader implementation ---

func (o *OpenAI) ReadImage(ctx context.Context, imageData []byte, mimeType string, prompt string) (string, error) {
	if prompt == "" {
		prompt = "Extract all readable text from this image. If it is a payment receipt or transfer confirmation, extract the reference number, amount, date, sender, and recipient. If it is a NIN or BVN slip, extract the ID number. Return only the extracted text, no commentary."
	}

	b64 := base64.StdEncoding.EncodeToString(imageData)
	content := []any{
		map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url":    "data:" + mimeType + ";base64," + b64,
				"detail": "high",
			},
		},
		map[string]any{
			"type": "text",
			"text": prompt,
		},
	}

	msgs := []chatMessage{{Role: "user", Content: content}}
	resp, err := o.chatCompletion(ctx, msgs, false)
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	return strings.TrimSpace(resp), nil
}

// --- SpeechToText implementation ---

func (o *OpenAI) Transcribe(ctx context.Context, audioData []byte, mimeType string, language string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", o.whisperMod)
	if language != "" {
		_ = writer.WriteField("language", language)
	}
	_ = writer.WriteField("response_format", "text")

	ext := "ogg"
	switch {
	case strings.Contains(mimeType, "mp3"):
		ext = "mp3"
	case strings.Contains(mimeType, "wav"):
		ext = "wav"
	case strings.Contains(mimeType, "webm"):
		ext = "webm"
	case strings.Contains(mimeType, "mp4"):
		ext = "mp4"
	case strings.Contains(mimeType, "mpeg"):
		ext = "mp3"
	}
	part, err := writer.CreateFormFile("file", "audio."+ext)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audioData); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("whisper read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("whisper returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return strings.TrimSpace(string(respBody)), nil
}

// --- shared chat completion ---

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []any for multimodal
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (o *OpenAI) chatCompletion(ctx context.Context, msgs []chatMessage, jsonMode bool) (string, error) {
	body := chatRequest{Model: o.chatModel, Messages: msgs}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if jsonMode {
		req.Header.Set("OpenAI-Response-Format", `{"type":"json_object"}`)
	}

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("chat request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("chat read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("chat returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("chat decode: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", nil
	}
	return chatResp.Choices[0].Message.Content, nil
}

// ProviderName returns the adapter identifier for logging.
func (o *OpenAI) ProviderName() string {
	return "openai"
}
