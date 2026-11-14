package interswitch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"whatsapp-payment-demo/internal/ports"
)

// Quickteller Service v5 Airtime/Data Recharge (VTU) uses the Transactions
// endpoint on the Quickteller v5 host (see client transferBaseURL). The
// recharge request is keyed by requestReference and identified by paymentCode.
const vtuTransactionsPath = "/Transactions"

type vtuRechargeRequest struct {
	TerminalID       string `json:"TerminalId"`
	PaymentCode      string `json:"paymentCode"`
	CustomerID       string `json:"customerId"`
	CustomerMobile   string `json:"customerMobile"`
	CustomerEmail    string `json:"customerEmail,omitempty"`
	Amount           string `json:"amount"`
	RequestReference string `json:"requestReference"`
}

// vtuRechargeResponse captures the fields returned by POST /Transactions for a
// recharge request. Field names vary slightly across environments, so we accept
// common casings.
type vtuRechargeResponse struct {
	TransactionRef       string `json:"TransactionRef"`
	TransactionReference string `json:"transactionRef"`
	ApprovedAmount       string `json:"ApprovedAmount"`
	ResponseCode         string `json:"ResponseCode"`
	ResponseDescription  string `json:"ResponseDescription"`
	ResponseCodeGrouping string `json:"ResponseCodeGrouping"`
}

// vtuQueryResponse captures the fields returned by GET /Transactions?requestRef=.
type vtuQueryResponse struct {
	Amount                  string `json:"amount"`
	Customer                string `json:"customer"`
	CustomerMobile          string `json:"customerMobile"`
	RequestReference        string `json:"requestReference"`
	RequestRef              string `json:"requestRef"`
	Status                  string `json:"status"`
	TransactionRef          string `json:"transactionRef"`
	TransactionReference    string `json:"TransactionReference"`
	TransactionResponseCode string `json:"transactionResponseCode"`
	ResponseCode            string `json:"ResponseCode"`
	ResponseDescription     string `json:"ResponseDescription"`
	ResponseCodeGrouping    string `json:"ResponseCodeGrouping"`
}

// FulfilData purchases airtime or a data bundle through Quickteller Service v5.
// request.ProviderSKU is the Quickteller paymentCode; request.RequestCode (or
// ProviderReference) is used to derive the unique requestReference.
func (c *Client) FulfilData(ctx context.Context, request ports.DataFulfilmentRequest) (ports.DataFulfilmentResult, error) {
	paymentCode := strings.TrimSpace(request.ProviderSKU)
	if paymentCode == "" {
		return ports.DataFulfilmentResult{}, errors.New("interswitch VTU: paymentCode (plan ProviderSKU) is required")
	}
	phone := vtuCustomerPhone(request.BeneficiaryPhone)
	if phone == "" {
		return ports.DataFulfilmentResult{}, errors.New("interswitch VTU: beneficiary phone is required")
	}
	requestReference := vtuRequestReference(firstNonEmptyVTU(strings.TrimSpace(request.ProviderReference), strings.TrimSpace(request.RequestCode), strings.TrimSpace(request.OrderID)))
	if requestReference == "" {
		return ports.DataFulfilmentResult{}, errors.New("interswitch VTU: request reference is required")
	}
	amount := strconv.FormatInt(request.AmountKobo, 10)
	if request.AmountKobo <= 0 {
		return ports.DataFulfilmentResult{}, errors.New("interswitch VTU: amount must be greater than zero")
	}
	terminalID := strings.TrimSpace(c.terminalID())
	if terminalID == "" {
		return ports.DataFulfilmentResult{}, errors.New("interswitch VTU: terminal ID is required")
	}

	var resp vtuRechargeResponse
	if err := c.doAuthorizedOn(ctx, c.vtuBaseURL(), http.MethodPost, vtuTransactionsPath, vtuRechargeRequest{
		TerminalID:       terminalID,
		PaymentCode:      paymentCode,
		CustomerID:       phone,
		CustomerMobile:   phone,
		Amount:           amount,
		RequestReference: requestReference,
	}, &resp); err != nil {
		return ports.DataFulfilmentResult{}, err
	}

	providerRef := requestReference
	txnRef := firstNonEmptyVTU(strings.TrimSpace(resp.TransactionRef), strings.TrimSpace(resp.TransactionReference))
	status, message := normalizeVTURechargeResult(resp)
	return ports.DataFulfilmentResult{
		ProviderReference: providerRef,
		Status:            status,
		Message:           messageWithRef(message, txnRef),
	}, nil
}

// CheckDataStatus requeries a VTU transaction by its requestReference.
func (c *Client) CheckDataStatus(ctx context.Context, providerReference string) (ports.DataFulfilmentResult, error) {
	requestReference := strings.TrimSpace(providerReference)
	if requestReference == "" {
		return ports.DataFulfilmentResult{Status: "pending", Message: "missing Interswitch VTU request reference"}, nil
	}
	query := url.Values{}
	query.Set("requestRef", requestReference)
	var resp vtuQueryResponse
	if err := c.doAuthorizedOn(ctx, c.vtuBaseURL(), http.MethodGet, vtuTransactionsPath+"?"+query.Encode(), nil, &resp); err != nil {
		return ports.DataFulfilmentResult{}, err
	}
	ref := firstNonEmptyVTU(strings.TrimSpace(resp.RequestReference), strings.TrimSpace(resp.RequestRef), requestReference)
	txnRef := firstNonEmptyVTU(strings.TrimSpace(resp.TransactionRef), strings.TrimSpace(resp.TransactionReference))
	status, message := normalizeVTUQueryResult(resp)
	return ports.DataFulfilmentResult{
		ProviderReference: ref,
		Status:            status,
		Message:           messageWithRef(message, txnRef),
	}, nil
}

func (c *Client) vtuBaseURL() string {
	if base := strings.TrimRight(strings.TrimSpace(c.transferBaseURL), "/"); base != "" {
		return base
	}
	if strings.EqualFold(c.mode, "LIVE") {
		return liveTransferHost
	}
	return testTransferHost
}

func normalizeVTURechargeResult(resp vtuRechargeResponse) (status, message string) {
	code := strings.TrimSpace(resp.ResponseCode)
	group := strings.ToUpper(strings.TrimSpace(resp.ResponseCodeGrouping))
	desc := strings.TrimSpace(resp.ResponseDescription)
	msg := firstNonEmptyVTU(desc, group, code)
	switch {
	case code == "90000" || group == "SUCCESSFUL":
		if isPendingDescription(desc) {
			return "pending", msg
		}
		return "fulfilled", msg
	case group == "PENDING":
		return "pending", msg
	case code != "":
		// Non-success response codes indicate the request was not accepted/completed.
		return "failed", msg
	default:
		return "pending", firstNonEmptyVTU(msg, "awaiting recharge status")
	}
}

func normalizeVTUQueryResult(resp vtuQueryResponse) (status, message string) {
	s := strings.ToLower(strings.TrimSpace(resp.Status))
	code := strings.TrimSpace(firstNonEmptyVTU(resp.TransactionResponseCode, resp.ResponseCode))
	group := strings.ToUpper(strings.TrimSpace(resp.ResponseCodeGrouping))
	desc := strings.TrimSpace(resp.ResponseDescription)
	msg := firstNonEmptyVTU(desc, strings.TrimSpace(resp.Status), group, code)
	switch s {
	case "completed":
		return "fulfilled", msg
	case "pending":
		return "pending", msg
	case "failed", "reversed":
		return "failed", msg
	}
	switch {
	case code == "90000" && group == "SUCCESSFUL":
		return "fulfilled", msg
	case group == "PENDING":
		return "pending", msg
	case code != "" && code != "90000":
		return "failed", msg
	default:
		return "pending", firstNonEmptyVTU(msg, "awaiting recharge status")
	}
}

func isPendingDescription(desc string) bool {
	d := strings.ToLower(strings.TrimSpace(desc))
	return strings.Contains(d, "pending") || strings.Contains(d, "processing") || strings.Contains(d, "progress")
}

func messageWithRef(message, txnRef string) string {
	message = strings.TrimSpace(message)
	txnRef = strings.TrimSpace(txnRef)
	if txnRef == "" {
		return message
	}
	if message == "" {
		return "TransactionRef: " + txnRef
	}
	if strings.Contains(message, txnRef) {
		return message
	}
	return message + " (TransactionRef: " + txnRef + ")"
}

var vtuNonAlnum = regexp.MustCompile(`[^A-Za-z0-9]+`)

// vtuRequestReference returns a unique request reference that complies with the
// v5 Transactions API constraint (max 20 characters).
func vtuRequestReference(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	clean := strings.ToUpper(vtuNonAlnum.ReplaceAllString(seed, ""))
	if clean == "" {
		clean = strings.ToUpper(vtuNonAlnum.ReplaceAllString(seed, ""))
	}
	if len(clean) <= 20 && clean != "" {
		return clean
	}
	sum := sha256.Sum256([]byte(seed))
	// Deterministic compact reference (20 chars) derived from the seed.
	return strings.ToUpper(hex.EncodeToString(sum[:10]))
}

// vtuCustomerPhone normalizes a beneficiary phone number to the international
// format shown in the v5 recharge examples (e.g. 234805673157).
func vtuCustomerPhone(phone string) string {
	phone = strings.TrimSpace(phone)
	phone = strings.TrimPrefix(phone, "+")
	// Keep digits only.
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	switch {
	case strings.HasPrefix(digits, "234") && len(digits) >= 13:
		return digits
	case strings.HasPrefix(digits, "0") && len(digits) == 11:
		return "234" + digits[1:]
	case len(digits) == 10:
		return "234" + digits
	default:
		return digits
	}
}

func firstNonEmptyVTU(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// --- Discovery helpers (Quickteller Service v5) ---

// VTUCategory represents a service category (e.g. Mobile/Recharge).
type VTUCategory struct {
	ID          int    `json:"Id"`
	Name        string `json:"Name"`
	Description string `json:"Description"`
}

// VTUBiller represents a telco/biller within a category.
type VTUBiller struct {
	ID             int    `json:"Id"`
	Name           string `json:"Name"`
	ShortName      string `json:"ShortName"`
	Narration      string `json:"Narration"`
	CustomerField1 string `json:"CustomerField1"`
	CurrencyCode   string `json:"CurrencyCode"`
	CurrencySymbol string `json:"CurrencySymbol"`
	AmountType     int    `json:"AmountType"`
	CategoryID     int    `json:"CategoryId"`
	CategoryName   string `json:"CategoryName"`
	NetworkID      string `json:"NetworkId"`
	ProductCode    string `json:"ProductCode"`
	Type           string `json:"Type"`
}

// VTUPaymentItem represents a payment option/bundle with the PaymentCode used
// for recharge requests.
type VTUPaymentItem struct {
	ID              string `json:"Id"`
	Name            string `json:"Name"`
	BillerName      string `json:"BillerName"`
	ConsumerIDField string `json:"ConsumerIdField"`
	Code            string `json:"Code"`
	BillerType      string `json:"BillerType"`
	ItemFee         string `json:"ItemFee"`
	Amount          string `json:"Amount"`
	BillerID        string `json:"BillerId"`
	BillerCategory  string `json:"BillerCategoryId"`
	CurrencyCode    string `json:"CurrencyCode"`
	CurrencySymbol  string `json:"CurrencySymbol"`
	IsAmountFixed   bool   `json:"IsAmountFixed"`
	PaymentCode     string `json:"PaymentCode"`
	AmountType      int    `json:"AmountType"`
	PaydirectItem   string `json:"PaydirectItemCode"`
}

type vtuCategoriesResponse struct {
	BillerCategories []VTUCategory `json:"BillerCategories"`
	ResponseCode     string        `json:"ResponseCode"`
	ResponseGrouping string        `json:"ResponseCodeGrouping"`
}

type vtuServicesResponse struct {
	BillerList struct {
		Count    int `json:"Count"`
		Category []struct {
			ID          int         `json:"Id"`
			Name        string      `json:"Name"`
			Description string      `json:"Description"`
			Billers     []VTUBiller `json:"Billers"`
		} `json:"Category"`
	} `json:"BillerList"`
	ResponseCode     string `json:"ResponseCode"`
	ResponseGrouping string `json:"ResponseCodeGrouping"`
}

type vtuOptionsResponse struct {
	PaymentItems     []VTUPaymentItem `json:"PaymentItems"`
	ResponseCode     string           `json:"ResponseCode"`
	ResponseGrouping string           `json:"ResponseCodeGrouping"`
}

// GetVTUCategories lists service categories (Step 1). Airtime/data recharge is
// typically under the "Mobile/Recharge" category (commonly id 4).
func (c *Client) GetVTUCategories(ctx context.Context) ([]VTUCategory, error) {
	var resp vtuCategoriesResponse
	if err := c.doAuthorizedOn(ctx, c.vtuBaseURL(), http.MethodGet, "/services/categories", nil, &resp); err != nil {
		return nil, err
	}
	return resp.BillerCategories, nil
}

// GetVTUBillers lists billers/telcos within a category (Step 2), e.g. categoryId=4.
func (c *Client) GetVTUBillers(ctx context.Context, categoryID int) ([]VTUBiller, error) {
	path := "/services?categoryId=" + strconv.Itoa(categoryID)
	var resp vtuServicesResponse
	if err := c.doAuthorizedOn(ctx, c.vtuBaseURL(), http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	for _, cat := range resp.BillerList.Category {
		if cat.ID == categoryID || categoryID == 0 {
			return cat.Billers, nil
		}
	}
	// Fallback: return billers from the first matching/category block if any.
	if len(resp.BillerList.Category) == 1 {
		return resp.BillerList.Category[0].Billers, nil
	}
	return nil, nil
}

// GetVTUPaymentItems lists payment items/options for a biller service (Step 3),
// including the PaymentCode required for recharge requests.
func (c *Client) GetVTUPaymentItems(ctx context.Context, serviceID int) ([]VTUPaymentItem, error) {
	path := "/services/options?serviceid=" + strconv.Itoa(serviceID)
	var resp vtuOptionsResponse
	if err := c.doAuthorizedOn(ctx, c.vtuBaseURL(), http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.PaymentItems, nil
}
