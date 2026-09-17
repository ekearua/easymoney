package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
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

func TestDownloadMedia(t *testing.T) {
	t.Parallel()
	client := New("app-secret", "token", "1001", "v23.0", "en")
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v23.0/media-1"):
			return textResponse(http.StatusOK, `{"url":"https://media.signed/media-1","mime_type":"image/jpeg"}`), nil
		case r.URL.Host == "media.signed":
			return responseWith(`image/jpeg`, []byte("fake-jpeg-bytes")), nil
		}
		return textResponse(http.StatusInternalServerError, `{"error":{"message":"boom"}}`), nil
	})}

	data, mime, err := client.DownloadMedia(context.Background(), "media-1")
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}
	if string(data) != "fake-jpeg-bytes" || mime != "image/jpeg" {
		t.Fatalf("unexpected download: mime=%q bytes=%q", mime, data)
	}
}

func TestDownloadMediaErrors(t *testing.T) {
	t.Parallel()
	client := New("app-secret", "token", "1001", "v23.0", "en")
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return textResponse(http.StatusNotFound, `{"error":{"message":"media not found"}}`), nil
	})}
	if _, _, err := client.DownloadMedia(context.Background(), "missing"); err == nil {
		t.Fatal("missing media should error")
	}
	if _, _, err := New("app-secret", "", "1001", "v23.0", "en").DownloadMedia(context.Background(), "media-1"); err == nil {
		t.Fatal("unconfigured client should error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func textResponse(status int, body string) *http.Response {
	r := responseWith("application/json", []byte(body))
	r.StatusCode = status
	r.Status = http.StatusText(status)
	return r
}

func responseWith(contentType string, body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     http.StatusText(http.StatusOK),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}
