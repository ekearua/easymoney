package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestOpenAI spins up an httptest server and returns an adapter pointed at
// it plus the server so handlers can assert on the recorded request.
func newTestOpenAI(t *testing.T, handler http.HandlerFunc) (*OpenAI, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewOpenAI("test-key", server.URL, "gpt-test", "whisper-test", 2*time.Second), server
}

func TestClassifyIntentJSONMode(t *testing.T) {
	var gotBody map[string]any
	var gotHeader string
	provider, _ := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("OpenAI-Response-Format")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"intent\":\"pay\",\"entities\":{\"amount\":\"5000\"},\"confidence\":0.95}"}}]}`)
	})

	result, err := provider.ClassifyIntent(context.Background(), "send 5000 to jumia", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Intent != "pay" || result.Confidence != 0.95 || result.Entities["amount"] != "5000" {
		t.Fatalf("unexpected intent result: %+v", result)
	}
	// JSON mode must be requested in the request BODY (OpenAI's
	// response_format), not via the old non-standard header.
	if gotHeader != "" {
		t.Fatalf("OpenAI-Response-Format header must not be sent, got %q", gotHeader)
	}
	format, ok := gotBody["response_format"].(map[string]any)
	if !ok || format["type"] != "json_object" {
		t.Fatalf("request body must carry response_format json_object, got %v", gotBody["response_format"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) < 2 {
		t.Fatalf("expected system + user messages, got %v", gotBody["messages"])
	}
}

func TestAnswerPlainMode(t *testing.T) {
	var gotBody map[string]any
	provider, _ := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"choices":[{"message":{"content":"Xego lets you send money and buy data."}}]}`)
	})

	answer, err := provider.Answer(context.Background(), "what can you do?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "Xego") {
		t.Fatalf("unexpected answer: %q", answer)
	}
	// Non-JSON calls must not request response_format.
	if _, ok := gotBody["response_format"]; ok {
		t.Fatal("plain chat must not send response_format")
	}
}

func TestChatErrorRedacted(t *testing.T) {
	provider, _ := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		// Provider body carrying PII: a card PAN and a secret key. It must
		// never survive into the returned error.
		io.WriteString(w, `{"error":"sk_test_0123456789abcdef rate limited, card 5234123452341234 expired"}`)
	})

	_, err := provider.Answer(context.Background(), "hi", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, "5234123452341234") {
		t.Fatalf("card PAN leaked into error: %s", msg)
	}
	if strings.Contains(msg, "sk_test_0123456789abcdef") || strings.Contains(msg, "sk_test_") {
		t.Fatalf("secret key leaked into error: %s", msg)
	}
}

func TestTranscribeErrorRedacted(t *testing.T) {
	provider, _ := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"invalid audio, contact +2348012345678 or admin@xego.test"}`)
	})

	_, err := provider.Transcribe(context.Background(), []byte("x"), "audio/mpeg", "en")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, "2348012345678") {
		t.Fatalf("phone leaked into error: %s", msg)
	}
	if strings.Contains(msg, "admin@xego.test") {
		t.Fatalf("email leaked into error: %s", msg)
	}
}

func TestOpenAIEmptyChoices(t *testing.T) {
	provider, _ := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	})
	answer, err := provider.Answer(context.Background(), "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "" {
		t.Fatalf("expected empty answer for no choices, got %q", answer)
	}
}

func TestLastPromptTokensReportsUsage(t *testing.T) {
	provider, _ := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"08039999900"}}],"usage":{"prompt_tokens":150,"completion_tokens":12}}`) // nosec: test fixture
	})
	if got := provider.LastPromptTokens(); got != 0 {
		t.Fatalf("tokens before any call = %d, want 0", got)
	}
	if _, err := provider.ReadImage(context.Background(), []byte("img"), "image/png", "extract"); err != nil {
		t.Fatal(err)
	}
	if got := provider.LastPromptTokens(); got != 162 {
		t.Fatalf("tokens after vision call = %d, want 162 (150 prompt + 12 completion)", got)
	}
}

func TestTranscribeReportsUsageWhenPresent(t *testing.T) {
	provider, _ := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		// Upstream honored a JSON response_format: text plus usage block.
		io.WriteString(w, `{"text":"hello from whisper","usage":{"prompt_tokens":40,"completion_tokens":5}}`) // nosec: test fixture
	})
	text, err := provider.Transcribe(context.Background(), []byte("audio"), "audio/ogg", "en")
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello from whisper" {
		t.Fatalf("transcription = %q, want the parsed text", text)
	}
	if got := provider.LastPromptTokens(); got != 45 {
		t.Fatalf("tokens after whisper call = %d, want 45 (parsed usage)", got)
	}

	// Plain-text bodies stay the default behavior and report no usage.
	plain, _ := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `just words`) // nosec: test fixture
	})
	text, err = plain.Transcribe(context.Background(), []byte("audio"), "audio/ogg", "en")
	if err != nil {
		t.Fatal(err)
	}
	if text != "just words" {
		t.Fatalf("plain transcription = %q, want raw text", text)
	}
	if got := plain.LastPromptTokens(); got != 0 {
		t.Fatalf("tokens after plain whisper call = %d, want 0 (no usage reported)", got)
	}
}
