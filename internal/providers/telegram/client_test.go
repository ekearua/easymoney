package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestValidateSecret(t *testing.T) {
	t.Parallel()
	client := New("token", "https://api.telegram.org", "secret")
	if err := client.ValidateSecret("secret"); err != nil {
		t.Fatalf("valid secret rejected: %v", err)
	}
	if err := client.ValidateSecret("wrong"); err == nil {
		t.Fatal("wrong secret should be rejected")
	}
}

func TestParseInboundMessageAndCallback(t *testing.T) {
	t.Parallel()
	messages, err := ParseInbound([]byte(`{"update_id":101,"message":{"message_id":9,"text":"/start","chat":{"id":12345},"from":{"id":67890,"username":"ada"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ChatID != "12345" || messages[0].UserID != "67890" || messages[0].Text != "/start" || messages[0].Username != "ada" {
		t.Fatalf("unexpected message parse: %#v", messages)
	}
	callbacks, err := ParseInbound([]byte(`{"update_id":102,"callback_query":{"id":"cb-1","data":"menu_pay","message":{"message_id":10,"chat":{"id":12345}},"from":{"id":67890,"username":"ada"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(callbacks) != 1 || callbacks[0].Interactive != "menu_pay" || callbacks[0].CallbackQueryID != "cb-1" {
		t.Fatalf("unexpected callback parse: %#v", callbacks)
	}
}

func TestSendInteractiveUsesInlineKeyboard(t *testing.T) {
	t.Parallel()
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendMessage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := New("token", server.URL, "secret")
	err := client.SendInteractive(context.Background(), ports.InteractiveMessage{
		To:   "12345",
		Body: "Choose an option",
		Buttons: []ports.InteractiveButton{
			{ID: "menu_pay", Title: "Make payment"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if requestBody["chat_id"] != "12345" {
		t.Fatalf("unexpected chat_id: %#v", requestBody["chat_id"])
	}
	if _, ok := requestBody["reply_markup"].(map[string]any)["inline_keyboard"]; !ok {
		t.Fatalf("inline keyboard missing: %#v", requestBody)
	}
}

func TestDownloadFile(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bottoken/getFile" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"doc-1","file_path":"voice_1.ogg"}}`))
			return
		}
		if r.URL.Path == "/file/bottoken/voice_1.ogg" {
			_, _ = w.Write([]byte("fake-ogg-bytes"))
			return
		}
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
	}))
	defer server.Close()

	client := New("token", server.URL, "secret")
	data, mime, err := client.DownloadFile(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}
	if string(data) != "fake-ogg-bytes" {
		t.Fatalf("unexpected bytes: %q", data)
	}
	if mime != "audio/ogg" {
		t.Fatalf("unexpected mime: %q", mime)
	}
}

func TestDownloadFileErrors(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	client := New("token", server.URL, "secret")
	if _, _, err := client.DownloadFile(context.Background(), "doc-1"); err == nil {
		t.Fatal("missing file should error")
	}
	if _, _, err := New("", server.URL, "secret").DownloadFile(context.Background(), "doc-1"); err == nil {
		t.Fatal("unconfigured client should error")
	}
}
