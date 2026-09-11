// OAuth-authorized JSON helpers used by the Interswitch direct API endpoints
// (virtual accounts, fund transfers, refunds, VTU). These ride on the Bearer
// token obtained from the OAuth endpoint and retry once when the token has been
// revoked server-side.
package interswitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var errUnauthorized = errors.New("interswitch API rejected the access token")

// doAuthorized performs one JSON request against a baseURL-relative API path
// using the OAuth Bearer token. A 401 retries once with a freshly refreshed
// token before failing.
func (c *Client) doAuthorized(ctx context.Context, method, path string, payload, target any) error {
	token, err := c.token.AccessToken(ctx)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		raw, err := c.doWithToken(ctx, method, path, payload, token)
		if err == nil {
			if len(raw) > 0 && target != nil {
				if err := json.Unmarshal(raw, target); err != nil {
					return fmt.Errorf("decode interswitch %s response: %w", path, err)
				}
			}
			return nil
		}
		if errors.Is(err, errUnauthorized) && attempt == 0 {
			c.token.Invalidate()
			token, err = c.token.AccessToken(ctx)
			if err != nil {
				return err
			}
			continue
		}
		return err
	}
}

func (c *Client) doWithToken(ctx context.Context, method, path string, payload any, token string) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if terminalID := c.terminalID(); terminalID != "" {
		req.Header.Set("TerminalId", terminalID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("interswitch %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("interswitch %s returned %s: %s", path, resp.Status, truncate(string(raw)))
	}
	return raw, nil
}

// terminalID returns the terminal id captured from the last OAuth token
// response, falling back to the configured terminal id.
func (c *Client) terminalID() string {
	if c.token != nil {
		if id := c.token.TerminalID(); id != "" {
			return id
		}
	}
	return c.configuredTerminalID
}