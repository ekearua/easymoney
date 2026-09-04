package interswitch

// TestSandboxCheckoutPageSmoke posts Xego's real Web Checkout redirect form
// (the exact fields interswitch_checkout.html submits) to the configured
// Interswitch base URL and asserts the gateway renders a payment page instead
// of the failure modes seen in the wild:
//
//   - an empty 200 with content-length 0 (the "Continue to Interswitch loads
//     a blank page" defect), or
//   - an error page embedding responseCode Z4 (merchant code / pay item not
//     provisioned on that host, e.g. the replace_me placeholders from
//     .env.example) or Z5 (duplicate transaction reference).
//
// A healthy, provisioned merchant returns a rendered page whose embedded
// IpgApp state carries no responseCode at all. The test skips unless the
// smoke gate is set so a plain `go test ./...` never touches the network:
//
//	INTERSWITCH_SANDBOX_SMOKE=1 \
//	INTERSWITCH_MERCHANT_CODE=MX... INTERSWITCH_PAY_ITEM_ID=... \
//	go test ./internal/providers/interswitch/ -run TestSandboxCheckoutPageSmoke -v
//
// Interswitch's documented demo sandbox merchant (MX6072 / 9405967) is used
// when no credentials are configured, so the harness itself can be verified.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSandboxRequerySmoke drives the server-side requery (gettransaction.json)
// against the sandbox with the configured INTERSWITCH_CLIENT_ID/SECRET and
// asserts the InterswitchAuth signature is accepted. It requeries a reference
// that cannot exist, so a healthy integration gets Interswitch's
// transaction-not-found JSON back — never an auth/401 rejection. The
// requery is the call that actually confirms a payment server-side, so a
// broken signature here means every checkout would render yet never confirm.
//
//	INTERSWITCH_SANDBOX_SMOKE=1 \
//	INTERSWITCH_CLIENT_ID=... INTERSWITCH_CLIENT_SECRET=... \
//	INTERSWITCH_MERCHANT_CODE=MX... \
//	go test ./internal/providers/interswitch/ -run TestSandboxRequerySmoke -v
func TestSandboxRequerySmoke(t *testing.T) {
	if os.Getenv("INTERSWITCH_SANDBOX_SMOKE") != "1" {
		t.Skip("set INTERSWITCH_SANDBOX_SMOKE=1 to requery the Interswitch sandbox")
	}
	clientID := strings.TrimSpace(os.Getenv("INTERSWITCH_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("INTERSWITCH_CLIENT_SECRET"))
	merchantCode := strings.TrimSpace(os.Getenv("INTERSWITCH_MERCHANT_CODE"))
	if clientID == "" || clientSecret == "" || merchantCode == "" {
		t.Skip("set INTERSWITCH_CLIENT_ID, INTERSWITCH_CLIENT_SECRET, and INTERSWITCH_MERCHANT_CODE to requery the sandbox")
	}
	mode := strings.ToUpper(strings.TrimSpace(os.Getenv("INTERSWITCH_CHECKOUT_MODE")))
	if mode == "" {
		mode = "TEST"
	}
	if mode != "TEST" {
		t.Skipf("smoke is for the sandbox; INTERSWITCH_CHECKOUT_MODE=%s refuses to run", mode)
	}
	baseURL := strings.TrimRight(os.Getenv("INTERSWITCH_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "https://sandbox.interswitchng.com"
	}

	client := New(Options{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		MerchantCode: merchantCode,
		BaseURL:      baseURL,
		Mode:         mode,
	})
	// This reference is generated fresh and never initialized, so Interswitch
	// must answer transaction-not-found rather than a duplicate/approval.
	reference := fmt.Sprintf("wpd_smoke_missing_%d", time.Now().UnixNano())
	verification, err := client.Verify(context.Background(), reference, 50_000)
	if err != nil {
		t.Fatalf("requery rejected: %v (check INTERSWITCH_CLIENT_ID/SECRET and the InterswitchAuth signature)", err)
	}
	// A missing transaction returns a non-empty JSON verdict; only a wrong
	// signature or a dead credential pair surfaces as an error above.
	if verification.Status == "" {
		t.Fatalf("requery returned an empty verdict for reference %s", reference)
	}
	t.Logf("PASS: requery accepted the InterswitchAuth signature (ref %s… → status %q)", shortRef(reference), verification.Status)
}

func TestSandboxCheckoutPageSmoke(t *testing.T) {
	if os.Getenv("INTERSWITCH_SANDBOX_SMOKE") != "1" {
		t.Skip("set INTERSWITCH_SANDBOX_SMOKE=1 to post the checkout form to the Interswitch sandbox")
	}
	merchantCode := strings.TrimSpace(os.Getenv("INTERSWITCH_MERCHANT_CODE"))
	payItemID := strings.TrimSpace(os.Getenv("INTERSWITCH_PAY_ITEM_ID"))
	if merchantCode == "" || payItemID == "" {
		// Interswitch's documented sandbox demo merchant, used in its Web
		// Checkout guide. Safe to post to in TEST mode.
		merchantCode, payItemID = "MX6072", "9405967"
		t.Logf("no INTERSWITCH_MERCHANT_CODE/PAY_ITEM_ID set; using Interswitch's demo sandbox merchant %s/%s", merchantCode, payItemID)
	}
	mode := strings.ToUpper(strings.TrimSpace(os.Getenv("INTERSWITCH_CHECKOUT_MODE")))
	if mode == "" {
		mode = "TEST"
	}
	if mode != "TEST" {
		t.Skipf("smoke is for the sandbox; INTERSWITCH_CHECKOUT_MODE=%s refuses to run", mode)
	}
	baseURL := strings.TrimRight(os.Getenv("INTERSWITCH_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "https://sandbox.interswitchng.com"
	}

	client := New(Options{
		MerchantCode: merchantCode,
		PayItemID:    payItemID,
		BaseURL:      baseURL,
		Mode:         mode,
	})
	// A unique reference per run so a stale row can never trip Z5.
	reference := fmt.Sprintf("wpd_smoke_%d", time.Now().UnixNano())
	redirect := "https://example.invalid/payments/return"
	page := client.NewPayPage(reference, "smoke@example.com", 50_000, redirect)
	if page.Action != baseURL+payPath {
		t.Fatalf("form action %q, want %q", page.Action, baseURL+payPath)
	}

	// Mirror interswitch_checkout.html field-for-field: merchant_code,
	// pay_item_id, txn_ref, amount, currency, cust_email, site_redirect_url,
	// mode. The template is the source of truth for the wire names.
	form := url.Values{}
	form.Set("merchant_code", page.MerchantCode)
	form.Set("pay_item_id", page.PayItemID)
	form.Set("txn_ref", page.TxnRef)
	form.Set("amount", fmt.Sprintf("%d", page.Amount))
	form.Set("currency", page.Currency)
	form.Set("cust_email", page.CustomerEmail)
	form.Set("site_redirect_url", page.SiteRedirectURL)
	form.Set("mode", page.Mode)

	t.Logf("POST %s (merchant %s, pay item %s, amount %d kobo, ref %s…)", page.Action, merchantCode, payItemID, page.Amount, shortRef(reference))
	httpClient := &http.Client{Timeout: 20 * time.Second}
	resp, err := httpClient.PostForm(page.Action, form)
	if err != nil {
		t.Fatalf("post checkout form: %v", err)
	}
	defer resp.Body.Close()

	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if readErr != nil {
			break
		}
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gateway returned %s", resp.Status)
	}
	// The reported defect: an empty 200 page (content-length 0).
	if len(strings.TrimSpace(string(body))) == 0 {
		t.Fatalf("gateway returned an EMPTY 200 page (content-length 0): the checkout form was rejected before rendering — check that INTERSWITCH_MERCHANT_CODE/INTERSWITCH_PAY_ITEM_ID are provisioned for Web Checkout on this host and that the amount is within the pay item's configured range")
	}

	// A rendered payment page embeds its state as window.IpgApp JSON with no
	// responseCode; a rejected page carries an error responseCode (Z4 =
	// merchant/pay item unknown, Z5 = duplicate reference).
	pageState := extractIpgApp(string(body))
	if code := responseCodeIn(pageState); code != "" {
		desc := responseDescriptionIn(pageState)
		switch code {
		case "Z4":
			t.Fatalf("gateway rejected the form: %s (%s) — merchant code / pay item %s/%s are not provisioned on %s (check for the replace_me placeholders from .env.example, or a pay item not enabled for Web Checkout)", code, desc, merchantCode, payItemID, page.Action)
		case "Z5":
			t.Fatalf("gateway rejected the form: %s (%s) — duplicate transaction reference; retry the smoke", code, desc)
		default:
			t.Fatalf("gateway rejected the form: responseCode %s (%s)", code, desc)
		}
	}
	if !strings.Contains(pageState, "IpgApp") && !strings.Contains(pageState, "checkoutAmount") {
		t.Fatalf("gateway returned a page without a rendered checkout state (%d bytes); expected a Web Checkout page", len(body))
	}
	t.Logf("PASS: gateway rendered the checkout page (%d bytes, ref %s…)", len(body), shortRef(reference))
}

var (
	ipgAppRe       = regexp.MustCompile(`IpgApp\s*=\s*JSON\.parse\('(.*?)'\)`)
	responseCodeRe = regexp.MustCompile(`"responseCode":"([^"]*)"`)
	descRe         = regexp.MustCompile(`"responseDescription":"([^"]*)"`)
)

func extractIpgApp(html string) string {
	if m := ipgAppRe.FindStringSubmatch(html); len(m) == 2 {
		return m[1]
	}
	return html
}

func responseCodeIn(state string) string {
	if m := responseCodeRe.FindStringSubmatch(state); len(m) == 2 {
		return m[1]
	}
	return ""
}

func responseDescriptionIn(state string) string {
	if m := descRe.FindStringSubmatch(state); len(m) == 2 {
		return m[1]
	}
	return ""
}

func shortRef(ref string) string {
	if len(ref) <= 16 {
		return ref
	}
	return ref[len(ref)-16:]
}
