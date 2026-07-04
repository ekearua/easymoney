package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

const ProviderSimulatedData = "simulated_data"

// DataService coordinates catalog lookup, payment linking, and provider fulfilment.
type DataService struct {
	store    *store.Store
	payments *PaymentService
	provider ports.DataProvider
}

// NewDataService creates the mobile-data order coordinator.
func NewDataService(repository *store.Store, payments *PaymentService, provider ports.DataProvider) *DataService {
	return &DataService{store: repository, payments: payments, provider: provider}
}

// CreateOrder stores a data order in draft state for a validated plan and phone number.
func (s *DataService) CreateOrder(ctx context.Context, user store.User, channel, recipient, planCode, beneficiaryPhone string) (store.DataOrderView, error) {
	plan, err := s.store.DataPlanByCode(ctx, planCode)
	if err != nil {
		return store.DataOrderView{}, err
	}
	phone, err := domain.NormalizeNigerianPhone(beneficiaryPhone)
	if err != nil {
		return store.DataOrderView{}, err
	}
	for i := 0; i < 5; i++ {
		code, err := domain.NewDataRequestCode()
		if err != nil {
			return store.DataOrderView{}, err
		}
		orderChannel := strings.TrimSpace(channel)
		if orderChannel == "" {
			orderChannel = ChannelWhatsApp
		}
		order, err := s.store.CreateDataOrder(ctx, user.ID, orderChannel, recipient, phone, plan, code)
		if err == nil {
			return order, nil
		}
		if !strings.Contains(strings.ToLower(err.Error()), "request_code") {
			return store.DataOrderView{}, err
		}
	}
	return store.DataOrderView{}, errors.New("could not allocate a data request code")
}

// CreatePaymentForOrder attaches a card or bank-transfer payment to a data order.
func (s *DataService) CreatePaymentForOrder(ctx context.Context, user store.User, order store.DataOrderView, provider, channel, recipient string) (store.PaymentView, store.DataOrderView, error) {
	merchant, err := s.store.XegoDataMerchant(ctx)
	if err != nil {
		return store.PaymentView{}, store.DataOrderView{}, err
	}
	payment, err := s.payments.CreateDraftForProvider(ctx, user, merchant, order.AmountKobo, provider, channel, recipient)
	if err != nil {
		return store.PaymentView{}, store.DataOrderView{}, err
	}
	order, err = s.store.AttachDataOrderPayment(ctx, order.ID, payment.ID)
	if err != nil {
		return store.PaymentView{}, store.DataOrderView{}, err
	}
	return payment, order, nil
}

// ProcessFulfilments fulfils settled data orders exactly once in bounded batches.
func (s *DataService) ProcessFulfilments(ctx context.Context, limit int) error {
	orders, err := s.store.ClaimFulfillableDataOrders(ctx, limit)
	if err != nil {
		return err
	}
	for _, order := range orders {
		result, err := s.provider.FulfilData(ctx, ports.DataFulfilmentRequest{
			OrderID:          order.ID.String(),
			RequestCode:      order.RequestCode,
			NetworkCode:      order.NetworkCode,
			PlanCode:         order.PlanCode,
			ProviderSKU:      order.ProviderSKU,
			BeneficiaryPhone: order.BeneficiaryPhone,
			AmountKobo:       order.AmountKobo,
		})
		status := domain.DataOrderFulfilled
		message := result.Message
		if err != nil {
			status = domain.DataOrderFailed
			message = err.Error()
		} else if !strings.EqualFold(result.Status, "fulfilled") && !strings.EqualFold(result.Status, "success") {
			if strings.EqualFold(result.Status, "pending") || strings.EqualFold(result.Status, "processing") || strings.EqualFold(result.Status, "queued") {
				if err := s.store.DeferDataOrderFulfilment(ctx, order.ID, result.ProviderReference, result.Message); err != nil {
					return err
				}
				continue
			}
			status = domain.DataOrderFailed
			if message == "" {
				message = "provider did not fulfil the data order"
			}
		}
		if err := s.store.CompleteDataOrderFulfilment(ctx, order.ID, status, result.ProviderReference, message, s.resultOutbox(order, status, message)); err != nil {
			return err
		}
	}
	return nil
}

// ApplyProviderResult resolves a pending data order from a provider webhook or requery result.
func (s *DataService) ApplyProviderResult(ctx context.Context, providerReference, providerStatus, message string) (store.DataOrderView, bool, error) {
	order, err := s.store.DataOrderByProviderReference(ctx, providerReference)
	if err != nil {
		return store.DataOrderView{}, false, err
	}
	target := mapProviderDataStatus(providerStatus)
	if target == "" {
		return order, false, nil
	}
	if err := s.store.CompleteDataOrderFulfilment(ctx, order.ID, target, providerReference, message, s.resultOutbox(order, target, message)); err != nil {
		return order, false, err
	}
	updated, err := s.store.DataOrderByID(ctx, order.ID)
	return updated, true, err
}

func mapProviderDataStatus(status string) domain.DataOrderStatus {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "delivered", "successful", "transaction successful", "success", "fulfilled":
		return domain.DataOrderFulfilled
	case "failed", "reversed", "cancelled":
		return domain.DataOrderFailed
	default:
		return ""
	}
}

func (s *DataService) resultOutbox(order store.DataOrderView, status domain.DataOrderStatus, message string) store.OutboxSpec {
	body := fmt.Sprintf("Xego data order update\n\nStatus: %s\nNetwork: %s\nPlan: %s\nPhone: %s\nRequest code: %s",
		strings.ToUpper(string(status)), order.NetworkName, order.PlanName, order.BeneficiaryPhone, order.RequestCode)
	if status == domain.DataOrderFulfilled {
		body += "\n\nYour data order has been fulfilled."
	} else if message != "" {
		body += "\n\n" + message
	}
	payload, _ := json.Marshal(map[string]any{"body": body})
	return store.OutboxSpec{UserID: order.UserID, Channel: order.Channel, Recipient: order.Recipient, Kind: "text", Payload: payload}
}

// SMSCommand is a normalized inbound SMS instruction.
type SMSCommand struct {
	Kind        string
	NetworkCode string
	PlanCode    string
	Phone       string
	RequestCode string
}

// ParseSMSCommand parses the customer-facing SMS keyword syntax.
func ParseSMSCommand(body string) (SMSCommand, error) {
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(body)))
	if len(fields) == 0 {
		return SMSCommand{}, errors.New("empty command")
	}
	switch fields[0] {
	case "DATA":
		if len(fields) == 2 && fields[1] == "HELP" {
			return SMSCommand{Kind: "help"}, nil
		}
		if len(fields) != 4 {
			return SMSCommand{}, errors.New("invalid DATA command")
		}
		return SMSCommand{Kind: "data", NetworkCode: fields[1], PlanCode: fields[2], Phone: fields[3]}, nil
	case "PLANS":
		if len(fields) != 2 {
			return SMSCommand{}, errors.New("invalid PLANS command")
		}
		return SMSCommand{Kind: "plans", NetworkCode: fields[1]}, nil
	case "STATUS":
		if len(fields) != 2 {
			return SMSCommand{}, errors.New("invalid STATUS command")
		}
		return SMSCommand{Kind: "status", RequestCode: fields[1]}, nil
	default:
		return SMSCommand{}, errors.New("unknown command")
	}
}

// HandleSMS processes one inbound SMS command and returns the reply body.
func (s *DataService) HandleSMS(ctx context.Context, messageID, sender, body string) (string, error) {
	normalizedSender, err := domain.NormalizeNigerianPhone(sender)
	if err != nil {
		normalizedSender = normalizePhone(sender)
	}
	fresh, existing, err := s.store.ReserveSMSRequest(ctx, messageID, normalizedSender, body)
	if err != nil {
		return "", err
	}
	if !fresh {
		if strings.TrimSpace(existing.ResponseBody) != "" {
			return existing.ResponseBody, nil
		}
		return smsHelp(), nil
	}
	command, parseErr := ParseSMSCommand(body)
	if parseErr != nil {
		reply := smsHelp()
		_ = s.store.CompleteSMSRequest(ctx, messageID, "", "", "failed", reply, parseErr.Error())
		return reply, nil
	}
	reply, requestCode, err := s.handleParsedSMS(ctx, normalizedSender, command)
	status := "processed"
	errorMessage := ""
	if err != nil {
		status = "failed"
		errorMessage = err.Error()
		reply = replyOrHelp(reply)
	}
	if err := s.store.CompleteSMSRequest(ctx, messageID, command.Kind, requestCode, status, reply, errorMessage); err != nil {
		return "", err
	}
	return reply, nil
}

func (s *DataService) handleParsedSMS(ctx context.Context, sender string, command SMSCommand) (string, string, error) {
	switch command.Kind {
	case "help":
		return smsHelp(), "", nil
	case "plans":
		plans, err := s.store.ListActiveDataPlans(ctx, command.NetworkCode)
		if err != nil {
			return "", "", err
		}
		if len(plans) == 0 {
			return "No active Xego data plans found for that network. Send DATA HELP for examples.", "", nil
		}
		lines := []string{"Xego " + strings.ToUpper(command.NetworkCode) + " plans:"}
		for _, plan := range plans {
			lines = append(lines, fmt.Sprintf("%s %s %s", plan.Code, plan.DisplayName, domain.FormatNGN(plan.PriceKobo)))
		}
		return strings.Join(lines, "\n"), "", nil
	case "status":
		order, err := s.store.DataOrderByRequestCode(ctx, command.RequestCode)
		if err != nil {
			return "We could not find that Xego request code. Check the code and try again.", command.RequestCode, nil
		}
		return fmt.Sprintf("Xego %s: %s %s for %s is %s.", order.RequestCode, order.NetworkName, order.PlanName, order.BeneficiaryPhone, strings.ToUpper(string(order.Status))), order.RequestCode, nil
	case "data":
		network, err := s.store.DataNetworkByCode(ctx, command.NetworkCode)
		if err != nil {
			return "That network is not available. Send PLANS MTN, PLANS AIRTEL, PLANS GLO, or PLANS 9MOBILE.", "", nil
		}
		plan, err := s.store.DataPlanByCode(ctx, command.PlanCode)
		if err != nil || !strings.EqualFold(plan.NetworkCode, network.Code) {
			return "That plan code is not available for " + network.Name + ". Send PLANS " + network.Code + " to see options.", "", nil
		}
		user, err := s.store.GetOrCreateUser(ctx, sender)
		if err != nil {
			return "", "", err
		}
		if user.Email == "" {
			// Paystack requires an email. SMS-only customers get a deterministic placeholder
			// that keeps card collection out of chat and can be replaced after onboarding.
			if err := s.store.UpdateUserEmail(ctx, user.ID, "sms+"+strings.TrimPrefix(user.WhatsAppNumber, "+")+"@xego.local"); err != nil {
				return "", "", err
			}
			user.Email = "sms+" + strings.TrimPrefix(user.WhatsAppNumber, "+") + "@xego.local"
		}
		order, err := s.CreateOrder(ctx, user, "sms", user.WhatsAppNumber, plan.Code, command.Phone)
		if err != nil {
			return "", "", err
		}
		payment, _, err := s.CreatePaymentForOrder(ctx, user, order, ProviderPaystack, "sms", user.WhatsAppNumber)
		if err != nil {
			return "", order.RequestCode, err
		}
		payment, err = s.payments.InitializeCheckout(ctx, payment)
		if err != nil {
			return "", order.RequestCode, err
		}
		return fmt.Sprintf("Xego request %s created: %s %s for %s. Pay %s here: %s. Check status with STATUS %s.",
			order.RequestCode, order.NetworkName, order.PlanName, order.BeneficiaryPhone, domain.FormatNGN(order.AmountKobo), payment.CheckoutURL, order.RequestCode), order.RequestCode, nil
	default:
		return smsHelp(), "", nil
	}
}

func smsHelp() string {
	return "Xego Data SMS format: DATA <NETWORK> <PLAN_CODE> <PHONE>. Example: DATA MTN MTN1GB 08031234567. Send PLANS MTN or STATUS XG-DATA-1234."
}

func replyOrHelp(reply string) string {
	if strings.TrimSpace(reply) != "" {
		return reply
	}
	return smsHelp()
}
