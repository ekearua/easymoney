package interswitch

import (
	"encoding/json"
	"net/url"
	"time"
)

// HostedFieldsPage carries the data the payment page needs to configure the
// Interswitch Hosted Fields SDK (secured inline iframe fields). Card data only
// ever lives inside the Interswitch-hosted iframes, so no PCI DSS scope is
// introduced on the platform side.
type HostedFieldsPage struct {
	SDKURL               string
	MerchantCode         string
	PayableCode          string
	Amount               int64
	CurrencyCode         string
	DateOfPayment        string
	TransactionReference string
	MerchantCustomerId   string
	MerchantCustomerName string
	RedirectURL          string
	Mode                 string
	SDKOrigin            string
	// ConfigJSON is the SDK create() configuration rendered as JSON for the
	// page's CSP-safe data island (hosted_fields.js parses it).
	ConfigJSON string
}

// HostedFieldsSDKURL is the URL of the Interswitch Hosted Fields SDK loaded
// by the card page. An explicit Options.HostedFieldsSDKURL (the
// INTERSWITCH_HOSTED_FIELDS_SDK_URL env var) always wins. Otherwise the URL
// follows the documented per-mode hosts: test uses the QA SDK URL and live uses
// the production SDK URL.
func (c *Client) HostedFieldsSDKURL() string {
	if c.hostedFieldsSDKURL != "" {
		return c.hostedFieldsSDKURL
	}
	if strings.EqualFold(c.mode, "LIVE") {
		return "https://hostedfields.interswitchng.com/sdk.js"
	}
	return "https://hostedifelds.qa.interswitchng.com/sdk.js"
} // NewHostedFieldsPage builds the configuration for one credit/debit card
// checkout rendered through the Hosted Fields SDK. redirectURL is where the
// SDK sends the browser after the payment attempt.
func (c *Client) NewHostedFieldsPage(reference, email string, amountKobo int64, redirectURL string) HostedFieldsPage {
	customerName := email
	if customerName == "" {
		customerName = "Xego customer"
	}
	sdkURL := c.HostedFieldsSDKURL()
	// isw-hosted-fields create() contract: paymentParameters carries the
	// transaction, cardinal.containerSelector is REQUIRED (3DS challenge
	// content is injected there), and fields keys must be exactly
	// cardNumber/expirationDate/cvv/pin/otp, each pointing at an existing node.
	config := map[string]any{
		"paymentParameters": map[string]any{
			"amount":               amountKobo, // amount in minor
			"currencyCode":         NGN,
			"merchantCode":         c.merchantCode,
			"payableCode":          c.payItemID,
			"transactionReference": reference,
			"merchantCustomerId":   reference,
			"merchantCustomerName": customerName,
			"dateOfPayment":        time.Now().Format("2006-01-02T15:04:05"),
			"redirectURL":          redirectURL + "?reference=" + reference,
		},
		"cardinal": map[string]any{
			"containerSelector": ".cardinal-container",
			"activeClass":       "show",
		},
		"fields": map[string]any{
			"cardNumber":     hostedField("#cardNumber-container", "****  ****  ****  ****"),
			"expirationDate": hostedField("#expirationDate-container", "MM / YY"),
			"cvv":            hostedField("#cvv-container", "***"),
			"pin":            hostedField("#pin-container", "* * * *"),
			"otp":            hostedField("#otp-container", "* * * * * *"),
		},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		raw = []byte("{}")
	}
	return HostedFieldsPage{
		SDKURL:               sdkURL,
		MerchantCode:         c.merchantCode,
		PayableCode:          c.payItemID,
		Amount:               amountKobo,
		CurrencyCode:         NGN,
		DateOfPayment:        time.Now().Format("2006-01-02T15:04:05"),
		TransactionReference: reference,
		MerchantCustomerId:   reference,
		MerchantCustomerName: customerName,
		RedirectURL:          redirectURL + "?reference=" + reference,
		Mode:                 c.mode,
		SDKOrigin:            hostedFieldsOrigin(sdkURL),
		ConfigJSON:           string(raw),
	}
}

// hostedFieldStyles styles the secure input inside its iframe. The SDK copies
// these keys straight onto the input's style object, so they must be valid CSS
// property names.
var hostedFieldStyles = map[string]any{
	"box-sizing": "border-box",
	"width":      "100%",
	"height":     "100%",
	"border":     "none",
	"outline":    "none",
	"padding":    "0 8px",
	"font-size":  "16px",
	"color":      "#1a1a1a",
	"background": "#ffffff",
}

// hostedField builds one entry of the SDK's fields configuration. The styles
// map is REQUIRED: the frame's field builder calls Object.keys(styles) and
// silently rejects the field (leaving the container empty and the page
// unusable) when it is missing — an empty map is accepted, a null is not.
func hostedField(selector, placeholder string) map[string]any {
	return map[string]any{
		"selector":    selector,
		"placeholder": placeholder,
		"styles":      hostedFieldStyles,
	}
}

// hostedFieldsOrigin extracts the origin of a script URL for CSP framing rules.
func hostedFieldsOrigin(sdkURL string) string {
	parsed, err := url.Parse(sdkURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}
