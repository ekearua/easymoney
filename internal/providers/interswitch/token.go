package interswitch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenResponse is the JSON shape returned by the Interswitch OAuth endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	MerchantCode string `json:"merchant_code"`
	TerminalID   string `json:"terminalId"`
	Env          string `json:"env"`
}

// TokenManager acquires and caches a Bearer token for the Interswitch direct
// API endpoints. Tokens are refreshed before expiry (5-minute safety margin).
// The manager is safe for concurrent use.
type TokenManager struct {
	clientID     string
	clientSecret string
	tokenURL     string
	http         *http.Client

	mu         sync.Mutex
	token      string
	terminalID string
	expiresAt  time.Time
}

// NewTokenManager creates a TokenManager that acquires tokens from the given
// OAuth endpoint using client_id and client_secret. http may be nil.
func NewTokenManager(clientID, clientSecret, tokenURL string, httpClient *http.Client) *TokenManager {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &TokenManager{
		clientID:     clientID,
		clientSecret: clientSecret,
		tokenURL:     tokenURL,
		http:         httpClient,
	}
}

// AccessToken returns a valid Bearer token, refreshing if needed. It returns
// an error if credentials are missing or the token endpoint is unreachable.
func (tm *TokenManager) AccessToken(ctx context.Context) (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.token != "" && time.Now().Before(tm.expiresAt) {
		return tm.token, nil
	}
	return tm.refresh(ctx)
}

// TerminalID returns the terminal ID obtained from the last successful token
// response. Returns "" if no token has been fetched yet.
func (tm *TokenManager) TerminalID() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.terminalID
}

// Invalidate discards the cached token so the next AccessToken call refreshes.
func (tm *TokenManager) Invalidate() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.token = ""
	tm.expiresAt = time.Time{}
}

// refresh acquires a fresh access token from the Interswitch OAuth endpoint.
// Must be called with tm.mu held.
func (tm *TokenManager) refresh(ctx context.Context) (string, error) {
	if tm.clientID == "" || tm.clientSecret == "" {
		return "", errors.New("interswitch: client ID and secret are required for OAuth token")
	}
	credentials := base64Encode(tm.clientID + ":" + tm.clientSecret)
	body := url.Values{}
	body.Set("grant_type", "client_credentials")
	body.Set("scope", "profile")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tm.tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return "", fmt.Errorf("interswitch oauth: %w", err)
	}
	req.Header.Set("Authorization", "Basic "+credentials)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := tm.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("interswitch oauth: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("interswitch oauth read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("interswitch oauth returned %s: %s", resp.Status, truncate(string(raw)))
	}
	var tok tokenResponse
	if err := json.Unmarshal(raw, &tok); err != nil {
		return "", fmt.Errorf("interswitch oauth decode: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("interswitch oauth: empty access token in response")
	}
	tm.token = tok.AccessToken
	tm.terminalID = tok.TerminalID
	// Refresh 5 minutes before actual expiry.
	tm.expiresAt = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - 5*time.Minute)
	return tm.token, nil
}

func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
