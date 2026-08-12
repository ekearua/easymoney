package app

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
)

// TestServerHardening locks the C19 ISO 8.6 HTTP server posture so a future
// change cannot silently relax the timeout/size bounds that protect against
// slowloris, header floods, and unbounded connections.
func TestServerHardening(t *testing.T) {
	a := &App{cfg: config.Config{HTTPAddr: ":0"}, logger: slog.Default()}
	server := a.newHTTPServer(http.NewServeMux())

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"ReadHeaderTimeout", server.ReadHeaderTimeout, 5 * time.Second},
		{"ReadTimeout", server.ReadTimeout, 15 * time.Second},
		{"WriteTimeout", server.WriteTimeout, 45 * time.Second},
		{"IdleTimeout", server.IdleTimeout, 60 * time.Second},
		{"MaxHeaderBytes", server.MaxHeaderBytes, 1 << 20},
	}
	for _, check := range checks {
		check := check
		t.Run(check.name, func(t *testing.T) {
			if check.got != check.want {
				t.Fatalf("%s = %v, want %v", check.name, check.got, check.want)
			}
		})
	}
	if server.Addr != ":0" {
		t.Fatalf("Addr = %q, want %q", server.Addr, ":0")
	}
	if server.Handler == nil {
		t.Fatal("Handler must be wired")
	}
}

// TestVTPassWebhookSecretUsesHeaderOnly locks the C28 posture: the VTPass
// webhook secret is validated from the X-VTPass-Webhook-Secret header and never
// from a query parameter, so callback URLs stay secret-free and URLs are safe
// to log.
func TestVTPassWebhookSecretUsesHeaderOnly(t *testing.T) {
	const configured = "s3cr3t"

	if !vtpassWebhookSecretValid(configured, configured) {
		t.Fatal("matching header secret must be accepted")
	}
	if vtpassWebhookSecretValid(configured, "wrong") {
		t.Fatal("wrong header secret must be rejected")
	}
	if !vtpassWebhookSecretValid("", "anything") {
		t.Fatal("empty configured secret must fall back to permissive (dev default)")
	}
	if vtpassWebhookSecretValid(configured, "") {
		t.Fatal("missing header must be rejected when a secret is configured")
	}
}