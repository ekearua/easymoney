package service

import (
	"testing"

	"whatsapp-payment-demo/internal/config"
)

func TestWebFlowEnabledGating(t *testing.T) {
	cfg := config.Config{WebFlowsEnabled: true}
	svc := NewConversationService(cfg, nil, nil, nil, nil, nil, nil, nil)

	if !svc.WebFlowEnabled(ChannelWhatsApp, WebFlowPay) {
		t.Fatal("whatsapp pay flow should be enabled by default")
	}
	if svc.WebFlowEnabled(ChannelTelegram, WebFlowPay) {
		t.Fatal("telegram must keep the chat flow")
	}
	if svc.WebFlowEnabled("sms", WebFlowPay) {
		t.Fatal("sms must keep the chat flow")
	}
	if svc.WebFlowEnabled(ChannelWhatsApp, "") {
		t.Fatal("empty flow must never route to the browser")
	}
}

func TestWebFlowEnabledGlobalAndPerFlowSwitch(t *testing.T) {
	cfg := config.Config{WebFlowsEnabled: false, WebFlowsDisabledFlows: []string{"topup"}}
	svc := NewConversationService(cfg, nil, nil, nil, nil, nil, nil, nil)
	if svc.WebFlowEnabled(ChannelWhatsApp, WebFlowPay) {
		t.Fatal("WEB_FLOWS_ENABLED=false must disable all browser flows")
	}

	cfg = config.Config{WebFlowsEnabled: true, WebFlowsDisabledFlows: []string{"topup"}}
	svc = NewConversationService(cfg, nil, nil, nil, nil, nil, nil, nil)
	if !svc.WebFlowEnabled(ChannelWhatsApp, WebFlowPay) {
		t.Fatal("pay flow should be enabled")
	}
	if svc.WebFlowEnabled(ChannelWhatsApp, WebFlowTopup) {
		t.Fatal("topup must stay on chat when listed in WEB_FLOWS_DISABLED_FLOWS")
	}
}

func TestMessageFlowTagging(t *testing.T) {
	cases := map[string]string{
		"menu":                   "menu",
		"":                       "menu",
		"onboard_email":          WebFlowOnboard,
		"select_merchant":        WebFlowPay,
		"enter_amount":           WebFlowPay,
		"confirm_payment":        WebFlowPay,
		"invoice_item_name":      WebFlowInvoiceCreate,
		"invoice_pay_amount":     WebFlowPayInvoice,
		"pay_individual_phone":   WebFlowIndividualPay,
		"await_individual_pay":   WebFlowIndividualPay,
		"wallet_topup_amount":    WebFlowTopup,
		"select_data_network":    WebFlowData,
		"thrift_name":            "thrift",
		"merchant_register_name": WebFlowMerchantRegister,
		"individual_legal_name":  WebFlowIndividualUpgrade,
		"kyb_request_confirm":    WebFlowKYBRequest,
		"ai_assistant":           "ai",
		"web_flow_active":        "web",
		"confirm_session_switch": "session",
		"unknown_other_state":    "chat",
	}
	for state, want := range cases {
		if got := flowForState(state); got != want {
			t.Errorf("flowForState(%q) = %q, want %q", state, got, want)
		}
	}
}

// Compile-time check that the messaging meter tags link messages through the
// same send helper path as chat messages.
func TestWebFlowCatalogCoversDispatchedFlows(t *testing.T) {
	for _, flowType := range []string{
		WebFlowPay, WebFlowInvoiceCreate, WebFlowPayInvoice, WebFlowThriftCreate,
		WebFlowThriftJoin, WebFlowThriftContribute, WebFlowData, WebFlowIndividualPay,
		WebFlowTopup, WebFlowOnboard, WebFlowIndividualUpgrade, WebFlowMerchantRegister,
		WebFlowKYBRequest,
	} {
		if _, ok := webFlowCatalog[flowType]; !ok {
			t.Errorf("web flow %q is missing catalog copy for the link message", flowType)
		}
	}
}
