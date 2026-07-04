// Package ports declares external service boundaries used by the application.
package ports

import (
	"context"
	"encoding/json"
	"time"
)

// InitializePayment contains provider-neutral checkout initialization data.
type InitializePayment struct {
	Reference   string
	Email       string
	AmountKobo  int64
	Currency    string
	CallbackURL string
	Metadata    map[string]string
}

// Checkout is returned by a gateway after it creates a hosted checkout.
type Checkout struct {
	Reference string
	URL       string
}

// Verification is the normalized result of a server-side gateway verification.
type Verification struct {
	Reference  string
	Status     string
	AmountKobo int64
	Currency   string
	Domain     string
	Channel    string
	Metadata   map[string]string
	PaidAt     *time.Time
	Message    string
}

// GatewayWebhook is a validated and normalized payment provider event.
type GatewayWebhook struct {
	Event     string
	Reference string
	Raw       json.RawMessage
}

// PaymentGateway isolates the domain from a specific payment provider.
type PaymentGateway interface {
	Initialize(context.Context, InitializePayment) (Checkout, error)
	Verify(context.Context, string) (Verification, error)
	ValidateWebhook(body []byte, signature string) (GatewayWebhook, error)
}

// InteractiveMessage describes a message with reply buttons or a list.
type InteractiveMessage struct {
	To          string
	Body        string
	ButtonLabel string
	Sections    []InteractiveSection
	Buttons     []InteractiveButton
}

// InteractiveSection groups options in a WhatsApp list.
type InteractiveSection struct {
	Title string
	Rows  []InteractiveRow
}

// InteractiveRow is one selectable WhatsApp list item.
type InteractiveRow struct {
	ID          string
	Title       string
	Description string
}

// InteractiveButton is one WhatsApp reply button.
type InteractiveButton struct {
	ID    string
	Title string
}

// Messenger isolates conversation behavior from WhatsApp's wire format.
type Messenger interface {
	SendText(context.Context, string, string) error
	SendInteractive(context.Context, InteractiveMessage) error
	SendCheckout(context.Context, string, string, string) error
	SendTemplate(context.Context, string, string, []string) error
}

// DataFulfilmentRequest contains the data-bundle order details sent to a data provider.
type DataFulfilmentRequest struct {
	OrderID          string
	RequestCode      string
	NetworkCode      string
	PlanCode         string
	ProviderSKU      string
	BeneficiaryPhone string
	AmountKobo       int64
}

// DataFulfilmentResult is the provider-neutral result of a data-bundle request.
type DataFulfilmentResult struct {
	ProviderReference string
	Status            string
	Message           string
}

// DataProvider isolates Xego data sales from a specific VTU or network API.
type DataProvider interface {
	FulfilData(context.Context, DataFulfilmentRequest) (DataFulfilmentResult, error)
	CheckDataStatus(context.Context, string) (DataFulfilmentResult, error)
}
