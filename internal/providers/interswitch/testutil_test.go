package interswitch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newAPIServer wraps a test handler with the OAuth token endpoint the client
// hits before every authorized API call.
func newAPIServer(t *testing.T, api http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/passport/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tokenResponse{
				AccessToken: "test-access-token",
				TokenType:   "Bearer",
				ExpiresIn:   43200,
				TerminalID:  "TD-TEST",
			})
			return
		}
		api(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}