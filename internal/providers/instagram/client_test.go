package instagram

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/ports"
)

func TestValidateSignature(t *testing.T) {
	t.Parallel()
	client := New("secret", "token", "ig-1", "v23.0")
	body := []byte(`{"object":"instagram"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if err := client.ValidateSignature(body, sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := client.ValidateSignature(body, "sha256=deadbeef"); err == nil {
		t.Fatal("wrong signature should be rejected")
	}
	if err := client.ValidateSignature(body, ""); err == nil {
		t.Fatal("missing signature should be rejected")
	}
	empty := New("", "token", "ig-1", "v23.0")
	if err := empty.ValidateSignature(body, sig); err == nil {
		t.Fatal("unconfigured secret should reject")
	}
}

func TestParseInboundTextQuickReplyAndPostback(t *testing.T) {
	t.Parallel()
	text, err := ParseInbound([]byte(`{"object":"instagram","entry":[{"id":"ig-1","time":1700000000,"messaging":[{"sender":{"id":"igsid-1"},"recipient":{"id":"ig-1"},"timestamp":1700000001,"message":{"mid":"m.1","text":"make payment"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(text) != 1 || text[0].IGSID != "igsid-1" || text[0].Text != "make payment" || text[0].ID != "m.1" {
		t.Fatalf("unexpected text parse: %#v", text)
	}
	quick, err := ParseInbound([]byte(`{"object":"instagram","entry":[{"id":"ig-1","messaging":[{"sender":{"id":"igsid-1"},"timestamp":1700000002,"message":{"mid":"m.2","text":"Pay","quick_reply":{"payload":"menu_pay"}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(quick) != 1 || quick[0].Interactive != "menu_pay" {
		t.Fatalf("unexpected quick-reply parse: %#v", quick)
	}
	postback, err := ParseInbound([]byte(`{"object":"instagram","entry":[{"id":"ig-1","messaging":[{"sender":{"id":"igsid-1"},"timestamp":1700000003,"postback":{"payload":"confirm_account"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(postback) != 1 || postback[0].Interactive != "confirm_account" {
		t.Fatalf("unexpected postback parse: %#v", postback)
	}
}

func TestParseInboundMediaAttachment(t *testing.T) {
	t.Parallel()
	messages, err := ParseInbound([]byte(`{"object":"instagram","entry":[{"id":"ig-1","messaging":[{"sender":{"id":"igsid-1"},"timestamp":1700000004,"message":{"mid":"m.3","attachments":[{"type":"image","payload":{"url":"https://cdn.example/1.jpg"}}]}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].MediaType != "image" || messages[0].MediaURL != "https://cdn.example/1.jpg" {
		t.Fatalf("unexpected media parse: %#v", messages)
	}
	voice, err := ParseInbound([]byte(`{"object":"instagram","entry":[{"id":"ig-1","messaging":[{"sender":{"id":"igsid-1"},"timestamp":1700000005,"message":{"mid":"m.4","attachments":[{"type":"audio","payload":{"url":"https://cdn.example/1.ogg"}}]}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(voice) != 1 || voice[0].MediaType != "audio" {
		t.Fatalf("unexpected audio parse: %#v", voice)
	}
}

func TestParseInboundIgnoresOtherObjectsAndEmptyPayloads(t *testing.T) {
	t.Parallel()
	other, err := ParseInbound([]byte(`{"object":"page","entry":[]}`))
	if err != nil || len(other) != 0 {
		t.Fatalf("non-instagram object should parse to nothing: %#v err=%v", other, err)
	}
	empty, err := ParseInbound([]byte(`{"object":"instagram","entry":[]}`))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty entry should parse to nothing: %#v err=%v", empty, err)
	}
	if _, err := ParseInbound([]byte(`not-json`)); err == nil {
		t.Fatal("malformed body should error")
	}
}

func TestSendInteractiveUsesQuickReplies(t *testing.T) {
	t.Parallel()
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v23.0/me/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"recipient_id":"igsid-1","message_id":"m.9"}`))
	}))
	defer server.Close()

	client := New("secret", "token", "ig-1", "v23.0")
	client.http = server.Client()
	// Point the client at the test server by overriding the base URL through
	// graphVersion-independent means: SendInteractive builds its own URL, so
	// use an httptest-backed transport rewrite instead.
	client.http = &http.Client{Transport: rewriteHostTransport{target: server.URL, base: client.http.Transport}}
	if err := client.SendInteractive(context.Background(), ports.InteractiveMessage{
		To:   "igsid-1",
		Body: "Choose an option",
		Buttons: []ports.InteractiveButton{
			{ID: "switch_yes", Title: "Switch"},
			{ID: "switch_no", Title: "Continue current"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	message := requestBody["message"].(map[string]any)
	replies := message["quick_replies"].([]any)
	if len(replies) != 2 {
		t.Fatalf("expected 2 quick replies, got %d", len(replies))
	}
	first := replies[0].(map[string]any)
	if first["payload"] != "switch_yes" || first["title"] != "Switch" {
		t.Fatalf("unexpected quick reply: %#v", first)
	}
}

func TestSendLinkUsesGenericTemplate(t *testing.T) {
	t.Parallel()
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"recipient_id":"igsid-1","message_id":"m.10"}`))
	}))
	defer server.Close()

	client := New("secret", "token", "ig-1", "v23.0")
	client.http = &http.Client{Transport: rewriteHostTransport{target: server.URL}}
	if err := client.SendLink(context.Background(), "igsid-1", "Pay a merchant securely", "https://xego.test/w/abc", "Open payments"); err != nil {
		t.Fatal(err)
	}
	message := requestBody["message"].(map[string]any)
	attachment := message["attachment"].(map[string]any)
	if attachment["type"] != "template" {
		t.Fatalf("expected template attachment, got %#v", attachment)
	}
	payload := attachment["payload"].(map[string]any)
	elements := payload["elements"].([]any)
	element := elements[0].(map[string]any)
	buttons := element["buttons"].([]any)
	button := buttons[0].(map[string]any)
	if button["url"] != "https://xego.test/w/abc" || button["title"] != "Open payments" {
		t.Fatalf("unexpected link button: %#v", button)
	}
}

func TestSendTextRejectsUnconfiguredClient(t *testing.T) {
	t.Parallel()
	client := New("secret", "", "", "v23.0")
	if err := client.SendText(context.Background(), "igsid-1", "hi"); err == nil {
		t.Fatal("unconfigured client should error")
	}
}

func TestTruncation(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 100)
	if got := truncateQuickReply(long); len(got) != 20 {
		t.Fatalf("quick reply title should cap at 20 chars, got %d", len(got))
	}
	if got := truncateGenericTitle(long); len(got) != 80 {
		t.Fatalf("generic title should cap at 80 chars, got %d", len(got))
	}
}

// rewriteHostTransport redirects requests to the test server so the client's
// hard-coded graph.facebook.com URL can be exercised in tests.
type rewriteHostTransport struct {
	target string
	base   http.RoundTripper
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(strings.TrimPrefix(t.target, "https://"), "http://")
	transport := t.base
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(req)
}
