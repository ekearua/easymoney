package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestValidateSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"object":"whatsapp_business_account"}`)
	mac := hmac.New(sha256.New, []byte("app-secret"))
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	client := New("app-secret", "", "", "v23.0", "en")
	if err := client.ValidateSignature(body, signature); err != nil {
		t.Fatal(err)
	}
	if err := client.ValidateSignature(body, "sha256=00"); err == nil {
		t.Fatal("invalid signature should fail")
	}
}

func TestParseInboundTextAndInteractive(t *testing.T) {
	t.Parallel()
	body := []byte(`{
	  "entry":[{"changes":[{"value":{"messages":[
	    {"id":"wamid.1","from":"2348012345678","timestamp":"1760000000","type":"text","text":{"body":" hello "}},
	    {"id":"wamid.2","from":"2348012345678","timestamp":"1760000001","type":"interactive","interactive":{"type":"list_reply","list_reply":{"id":"menu_pay","title":"Make payment"}}}
	  ]}}]}]
	}`)
	messages, err := ParseInbound(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages", len(messages))
	}
	if messages[0].Text != "hello" || messages[1].Interactive != "menu_pay" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
	if messages[0].Timestamp.Equal(time.Time{}) {
		t.Fatal("unix timestamp should be parsed")
	}
}
