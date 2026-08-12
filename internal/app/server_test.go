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