package tiktok

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

func signBody(secret string, body []byte, at time.Time) string {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "." + string(body)))
	return "t=" + timestamp + ",s=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidateSignature(t *testing.T) {
	t.Parallel()
	client := New("secret", "token", "")
	body := []byte(`{"event":"{}"}`)
	now := time.Unix(1_700_000_000, 0)
	sig := signBody("secret", body, now)
	if err := client.ValidateSignatureAt(body, sig, now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := client.ValidateSignatureAt(body, "t=1700000000,s=deadbeef", now); err == nil {
		t.Fatal("wrong signature should be rejected")
	}
	if err := client.ValidateSignatureAt(body, "garbage", now); err == nil {
		t.Fatal("malformed header should be rejected")
	}
	stale := signBody("secret", body, now.Add(-10*time.Minute))
	if err := client.ValidateSignatureAt(body, stale, now); err == nil {
		t.Fatal("stale timestamp should be rejected")
	}
}

func TestParseInboundTextAndImage(t *testing.T) {
	t.Parallel()
	event := `{"event_id":"evt-1","event_type":"message.received","timestamp":1700000000,` +
		`"data":{"conversation_id":"conv-1","sender":{"open_id":"open-1","union_id":"union-1"},` +
		`"content":{"text":"make payment"}}}`
	messages, err := ParseInbound([]byte(`{"event":` + quoteJSON(event) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].OpenID != "open-1" || messages[0].UnionID != "union-1" || messages[0].ConversationID != "conv-1" || messages[0].Text != "make payment" || messages[0].EventID != "evt-1" {
		t.Fatalf("unexpected text parse: %#v", messages)
	}
	image := `{"event_id":"evt-2","timestamp":1700000001,"data":{"sender":{"open_id":"open-1"},"content":{"media":[{"url":"https://cdn.example/1.jpg","mime_type":"image/jpeg","caption":"receipt"}]}}}`
	direct, err := ParseInbound([]byte(image))
	if err != nil {
		t.Fatal(err)
	}
	if len(direct) != 1 || direct[0].MediaType != "image" || direct[0].MediaURL != "https://cdn.example/1.jpg" || direct[0].Caption != "receipt" {
		t.Fatalf("unexpected image parse: %#v", direct)
	}
}

func quoteJSON(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestParseInboundIgnoresNonMessageEvents(t *testing.T) {
	t.Parallel()
	empty, err := ParseInbound([]byte(`{"event":""}`))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty event should parse to nothing: %#v err=%v", empty, err)
	}
	noSender, err := ParseInbound([]byte(`{"event":"{\"event_id\":\"evt-3\",\"data\":{\"content\":{\"text\":\"hi\"}}}"}`))
	if err != nil || len(noSender) != 0 {
		t.Fatalf("event without sender should parse to nothing: %#v err=%v", noSender, err)
	}
}

func TestSendInteractiveDegradesToNumberedMenu(t *testing.T) {
	t.Parallel()
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ConversationID string `json:"conversation_id"`
			Content        struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		bodies = append(bodies, payload.Content.Text)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := New("secret", "token", server.URL)
	if err := client.SendInteractive(context.Background(), ports.InteractiveMessage{
		To:   "conv-1",
		Body: "Choose an option",
		Sections: []ports.InteractiveSection{{
			Title: "Menu",
			Rows: []ports.InteractiveRow{
				{ID: "menu_pay", Title: "Make payment", Description: "Pay a merchant"},
				{ID: "menu_topup", Title: "Top up wallet"},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatalf("expected 1 send, got %d", len(bodies))
	}
	if !strings.Contains(bodies[0], "1. Make payment — Pay a merchant") || !strings.Contains(bodies[0], "2. Top up wallet") {
		t.Fatalf("numbered menu missing: %q", bodies[0])
	}
}

func TestSendLinkDegradesToTextLink(t *testing.T) {
	t.Parallel()
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		bodies = append(bodies, payload.Content.Text)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := New("secret", "token", server.URL)
	if err := client.SendLink(context.Background(), "conv-1", "Pay a merchant securely", "https://xego.test/w/abc", "Open payments"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], "https://xego.test/w/abc") || !strings.Contains(bodies[0], "Open payments") {
		t.Fatalf("text link missing: %q", bodies)
	}
}

func TestSendTextRejectsUnconfiguredClient(t *testing.T) {
	t.Parallel()
	client := New("secret", "", "")
	if err := client.SendText(context.Background(), "conv-1", "hi"); err == nil {
		t.Fatal("unconfigured client should error")
	}
}
