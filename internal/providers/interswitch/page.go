package interswitch

import (
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
	MerchantCustomerID   string
	MerchantCustomerName string
	RedirectURL          string
	Mode                 string
	SDKOrigin            string
}

// HostedFieldsSDKURL is the per-mode URL of the Interswitch Hosted Fields SDK.
func (c *Client) HostedFieldsSDKURL() string {
	if c.mode == "LIVE" {
		return "https://hostedfields.interswitchng.com/sdk.js"
	}
	return "https://hostedifelds.qa.interswitchng.com/sdk.js"
}

// NewHostedFieldsPage builds the configuration for one credit/debit card
// checkout rendered through the Hosted Fields SDK. redirectURL is where the
// SDK sends the browser after the payment attempt.
func (c *Client) NewHostedFieldsPage(reference, email string, amountKobo int64, redirectURL string) HostedFieldsPage {
	customerName := email
	if customerName == "" {
		customerName = "Xego customer"
	}
	return HostedFieldsPage{
		SDKURL:               c.HostedFieldsSDKURL(),
		MerchantCode:         c.merchantCode,
		PayableCode:          c.payItemID,
		Amount:               amountKobo,
		CurrencyCode:         NGN,
		DateOfPayment:        time.Now().Format("2006-01-02 15:04:05"),
		TransactionReference: reference,
		MerchantCustomerID:   reference,
		MerchantCustomerName: customerName,
		RedirectURL:          redirectURL + "?reference=" + reference,
		Mode:                 c.mode,
		SDKOrigin:            hostedFieldsOrigin(c.HostedFieldsSDKURL()),
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