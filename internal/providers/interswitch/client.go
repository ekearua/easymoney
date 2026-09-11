// Package interswitch implements the provider-neutral payment gateway port
// for Interswitch Web Checkout (formerly Webpay).
//
// Web Checkout uses a hosted payment page reached by posting an HTML form to
// the Interswitch gateway; the authoritative transaction outcome is confirmed
// with a server-side requery to the gettransaction.json endpoint, signed with
// the legacy InterswitchAuth scheme.
package interswitch

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// NGN is the ISO 4217 numeric code for the Nigerian Naira, which Interswitch
// Web Checkout expects in place of an alphabetic currency code.
const NGN = "566"

// Gateway endpoints on the Interswitch host.
const (
	payPath            = "/collections/w/pay"
	getTransactionPath = "/collections/api/v1/gettransaction.json"
)

// Hosts that render the hosted Web Checkout page. The legacy sandbox host
// (sandbox.interswitchng.com) still answers the form POST with an HTML shell
// but serves its JavaScript assets empty to browsers, so the payment page
// never mounts there; the newwebpay hosts are the documented, working form
// targets. The requery/API host (Options.BaseURL) is separate and must stay
// on the host that answers signed gettransaction.json calls.
const (
	testCheckoutHost = "https://newwebpay-sandbox.interswitchng.com"
	liveCheckoutHost = "https://newwebpay.interswitchng.com"
)

// OAuth token (Passport) hosts. The legacy sandbox API host refuses Basic
// credentials on /passport/oauth/token with 401 Bad credentials, so sandbox
// direct-API calls (virtual accounts, transfers, refunds, VTU) must mint
// tokens on the dedicated passport-sandbox host instead. LIVE has no
// separate passport host, and any non-sandbox BaseURL (proxies, test
// servers) keeps deriving the token URL from its own host.
const (
	testTokenHost   = "https://passport-sandbox.interswitchng.com"
	sandboxAPIHost  = "https://sandbox.interswitchng.com"
)

// Client integrates with Interswitch Web Checkout hosted checkout, server
// requery verification, and redirect-notification handling. It also carries
// an OAuth TokenManager so direct API endpoints (virtual accounts, transfer,
// refunds, VTU) can use a Bearer token.
type Client struct {
	clientID        string
	clientSecret    string
	webhookSecret   string
	merchantCode    string
	payItemID       string
	baseURL         string
	checkoutBaseURL string
	mode            string
	configuredTerminalID string
	sourceAccount   string
	token           *TokenManager
	http            *http.Client
}

// Options configures an Interswitch Web Checkout client.
type Options struct {
	ClientID        string
	ClientSecret    string
	WebhookSecret   string
	MerchantCode    string
	PayItemID       string
	BaseURL         string // API/requery host (gettransaction.json)
	CheckoutBaseURL string // optional; host that renders the payment page
	Mode            string // "TEST" or "LIVE"
	TokenURL        string // optional; defaults to BaseURL + /passport/oauth/token
	TerminalID      string // optional; terminal id used when the token response omits one
	SourceAccount   string // optional; funding account for NIP transfers
}

// New creates an Interswitch Web Checkout client with strict request timeouts.
// mode defaults to "TEST".
func New(o Options) *Client {
	mode := strings.ToUpper(strings.TrimSpace(o.Mode))
	if mode == "" {
		mode = "TEST"
	}
	checkoutBase := strings.TrimRight(o.CheckoutBaseURL, "/")
	if checkoutBase == "" {
		// The documented per-mode host that renders the Web Checkout page.
		switch mode {
		case "LIVE":
			checkoutBase = liveCheckoutHost
		default:
			checkoutBase = testCheckoutHost
		}
	}
	baseURL := strings.TrimRight(o.BaseURL, "/")
	tokenURL := strings.TrimRight(o.TokenURL, "/")
	if tokenURL == "" {
		// The sandbox API host cannot mint tokens; redirect to passport-sandbox.
		// An explicit INTERSWITCH_TOKEN_URL still wins for exotic setups.
		if baseURL == sandboxAPIHost {
			tokenURL = testTokenHost + "/passport/oauth/token"
		} else {
			tokenURL = baseURL + "/passport/oauth/token"
		}
	}
	c := &Client{
		clientID:        o.ClientID,
		clientSecret:    o.ClientSecret,
		webhookSecret:   o.WebhookSecret,
		merchantCode:    o.MerchantCode,
		payItemID:       o.PayItemID,
		baseURL:         baseURL,
		checkoutBaseURL: checkoutBase,
		mode:            mode,
		configuredTerminalID: strings.TrimSpace(o.TerminalID),
		sourceAccount:   strings.TrimSpace(o.SourceAccount),
		http:            &http.Client{Timeout: 15 * time.Second},
	}
	c.token = NewTokenManager(o.ClientID, o.ClientSecret, tokenURL, c.http)
	return c
}

// PayPage carries the data needed to render the Interswitch Web Checkout
// redirect form on a platform-hosted page.
type PayPage struct {
	Action        string
	MerchantCode  string
	PayItemID     string
	TxnRef        string
	Amount        int64
	Currency      string
	CustomerEmail string
	SiteRedirectURL string
	Mode          string
}

// PayPageURL returns the platform-hosted page that renders and submits the
// Interswitch Web Checkout form for the given transaction reference. callbackURL
// is the platform's own callback route, used to derive the platform origin.
func (c *Client) PayPageURL(callbackURL, reference string) string {
	origin := originFromCallback(callbackURL)
	return origin + "/checkout/interswitch/" + url.PathEscape(reference)
}

// NewPayPage builds the fields for the Interswitch Web Checkout redirect form
// for a concrete payment. siteRedirectURL is where the customer returns. The
// form action targets the checkout host (the host that renders the payment
// page), distinct from the requery/API host in Options.BaseURL.
func (c *Client) NewPayPage(reference, email string, amountKobo int64, siteRedirectURL string) PayPage {
	return PayPage{
		Action:          c.checkoutBaseURL + payPath,
		MerchantCode:    c.merchantCode,
		PayItemID:       c.payItemID,
		TxnRef:          reference,
		Amount:          amountKobo,
		Currency:        NGN,
		CustomerEmail:   email,
		SiteRedirectURL: siteRedirectURL,
		Mode:            c.mode,
	}
}

// Initialize prepares a hosted Interswitch Web Checkout for the payment. The
// Web Checkout page is reached by posting a form to the gateway, so the
// returned Checkout.URL points at the platform's own form-rendering page,
// which submits to Interswitch in the browser.
func (c *Client) Initialize(ctx context.Context, input ports.InitializePayment) (ports.Checkout, error) {
	if c.merchantCode == "" || c.payItemID == "" {
		return ports.Checkout{}, errors.New("interswitch: merchant code and pay item id are required")
	}
	return ports.Checkout{
		Reference: input.Reference,
		URL:       c.PayPageURL(input.CallbackURL, input.Reference),
	}, nil
}

// RequeryResponse is the normalized payload returned by the Interswitch
// gettransaction requery endpoint.
type requeryResponse struct {
	Amount                    int64  `json:"Amount"`
	CardNumber                string `json:"CardNumber"`
	MerchantReference         string `json:"MerchantReference"`
	PaymentReference          string `json:"PaymentReference"`
	RetrievalReferenceNumber  string `json:"RetrievalReferenceNumber"`
	TransactionDate           string `json:"TransactionDate"`
	ResponseCode              string `json:"ResponseCode"`
	ResponseDescription       string `json:"ResponseDescription"`
	AccountNumber             string `json:"AccountNumber"`
}

// Verify confirms the status of a transaction using the Interswitch
// gettransaction requery endpoint, authenticated with the legacy
// InterswitchAuth signature. amountKobo is the amount the merchant expected
// to collect and is echoed to the gateway as the amount query parameter:
// Interswitch's gettransaction.json rejects requeries without it (response
// code 20031, "Amount not supplied or amount not in minor format"), so the
// expected amount is mandatory for a requery to return the transaction.
func (c *Client) Verify(ctx context.Context, reference string, amountKobo int64) (ports.Verification, error) {
	if c.merchantCode == "" {
		return ports.Verification{}, errors.New("interswitch: merchant code is required for requery")
	}
	req, err := c.newRequeryRequest(ctx, reference, amountKobo)
	if err != nil {
		return ports.Verification{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ports.Verification{}, fmt.Errorf("interswitch requery: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ports.Verification{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ports.Verification{}, fmt.Errorf("interswitch requery returned %s: %s", resp.Status, truncate(string(raw)))
	}
	var data requeryResponse
	if err := json.Unmarshal(raw, &data); err != nil {
		return ports.Verification{}, fmt.Errorf("decode interswitch requery: %w", err)
	}
	if data.ResponseCode == "" && data.MerchantReference == "" && data.Amount == 0 {
		return ports.Verification{}, fmt.Errorf("interswitch requery empty response: %s", truncate(string(raw)))
	}
	return ports.Verification{
		Reference:  reference,
		Status:     interswitchStatus(data.ResponseCode),
		AmountKobo: data.Amount,
		Currency:   "NGN",
		Domain:     strings.ToLower(c.mode),
		Channel:    "card",
		Metadata:   map[string]string{},
		Message:    data.ResponseDescription,
	}, nil
}

func (c *Client) newRequeryRequest(ctx context.Context, reference string, amountKobo int64) (*http.Request, error) {
	query := url.Values{}
	query.Set("merchantcode", c.merchantCode)
	query.Set("transactionreference", reference)
	query.Set("amount", strconv.FormatInt(amountKobo, 10))
	endpoint := getTransactionPath + "?" + query.Encode()
	now := time.Now().Unix()
	nonce, _ := randomNonce()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+endpoint, nil)
	if err != nil {
		return nil, err
	}
	applyInterswitchAuthHeaders(req, c.clientID, c.clientSecret, http.MethodGet, endpoint, now, nonce)
	return req, nil
}

// ValidateWebhook authenticates and parses an Interswitch outbound webhook
// notification sent to the configured webhook URL. Interswitch signs the raw
// JSON body with HMAC-SHA512 using the dedicated webhook secret generated in
// the Quickteller Business dashboard and sends the hex-encoded digest in the
// X-Interswitch-Signature header. The signature is mandatory: an unsigned or
// mismatched message is not from Interswitch and is rejected. As with the
// redirect notification, the webhook is a signal to confirm the outcome; the
// authoritative transaction status must still be obtained from a server-side
// requery (Verify) before delivering value.
func (c *Client) ValidateWebhook(body []byte, signature string) (ports.GatewayWebhook, error) {
	if signature == "" {
		return ports.GatewayWebhook{}, errors.New("missing Interswitch signature header")
	}
	if c.webhookSecret == "" {
		return ports.GatewayWebhook{}, errors.New("Interswitch webhook secret is not configured")
	}
	mac := hmac.New(sha512.New, []byte(c.webhookSecret))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(signature))), []byte(expected)) {
		return ports.GatewayWebhook{}, errors.New("invalid Interswitch signature")
	}

	var payload struct {
		Event     string `json:"event"`
		UUID      string `json:"uuid"`
		Timestamp int64  `json:"timestamp"`
		Data      struct {
			MerchantReference string `json:"merchantReference"`
			ResponseCode      string `json:"responseCode"`
			ResponseDescr     string `json:"responseDescription"`
			Amount            int64  `json:"amount"`
			Currency          string `json:"currencyCode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ports.GatewayWebhook{}, fmt.Errorf("decode Interswitch webhook payload: %w", err)
	}
	event := strings.TrimSpace(payload.Event)
	ref := strings.TrimSpace(payload.Data.MerchantReference)
	if ref == "" {
		ref = strings.TrimSpace(payload.UUID)
	}
	return ports.GatewayWebhook{
		Event:     event,
		Reference: ref,
		Raw:       json.RawMessage(body),
	}, nil
}

// Health checks whether the Interswitch gateway is reachable.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("interswitch health: %w", err)
	}
	resp.Body.Close()
	return nil
}

// interswitchStatus normalizes an Interswitch response code to the
// provider-neutral status set that mapGatewayStatus understands. Code 00 means
// approved; a few known decline codes map to failed; everything unresolved maps
// to pending so it is re-requeried rather than incorrectly marked failed.
func interswitchStatus(code string) string {
	switch strings.TrimSpace(code) {
	case "00":
		return "success"
	case "01", "02", "03", "04", "05", "51", "55", "91", "93":
		return "failed"
	default:
		return "pending"
	}
}

func applyInterswitchAuthHeaders(req *http.Request, clientID, clientSecret, method, endpoint string, now int64, nonce string) {
	auth := "InterswitchAuth " + base64.StdEncoding.EncodeToString([]byte(clientID))
	signature := interswitchSignature(clientID, clientSecret, method, endpoint, now, nonce)
	req.Header.Set("Authorization", auth)
	req.Header.Set("Timestamp", strconv.FormatInt(now, 10))
	req.Header.Set("Nonce", nonce)
	req.Header.Set("Signature", signature)
	req.Header.Set("SignatureMethod", "SHA1")
	req.Header.Set("Content-Type", "application/json")
}

// interswitchSignature computes the legacy InterswitchAuth request signature.
// Order: METHOD & urlencoded(endpoint) & timestamp & nonce & clientID & secret.
func interswitchSignature(clientID, clientSecret, method, endpoint string, now int64, nonce string) string {
	canonical := method + "&" + url.QueryEscape(endpoint) + "&" + strconv.FormatInt(now, 10) + "&" + nonce + "&" + clientID + "&" + clientSecret
	mac := hmac.New(sha1.New, []byte(clientSecret))
	_, _ = mac.Write([]byte(canonical))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func randomNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func originFromCallback(callbackURL string) string {
	callbackURL = strings.TrimRight(callbackURL, "/")
	suffix := "/payments/return"
	if strings.HasSuffix(callbackURL, suffix) {
		return strings.TrimSuffix(callbackURL, suffix)
	}
	return callbackURL
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
