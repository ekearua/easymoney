package service

import (
	"testing"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func TestSessionSwitchable(t *testing.T) {
	t.Parallel()
	active := []string{
		"select_merchant", "enter_amount", "select_payment_method", "confirm_payment",
		"await_bank_transfer", "web_flow_active", "ai_assistant",
		"merchant_register_email", "individual_legal_name", "thrift_name",
		"invoice_item_name", "pay_individual_phone", "wallet_topup_amount",
		"select_data_network", "kyb_request_confirm", "onboard_name",
	}
	for _, state := range active {
		if !sessionSwitchable(state) {
			t.Errorf("sessionSwitchable(%q) = false, want true (active flow)", state)
		}
	}
	idle := []string{"", "menu", "confirm_session_switch"}
	for _, state := range idle {
		if sessionSwitchable(state) {
			t.Errorf("sessionSwitchable(%q) = true, want false (idle)", state)
		}
	}
}

func TestServiceSwitchIntentRecognizesEveryMenuRow(t *testing.T) {
	t.Parallel()
	var all []ports.InteractiveRow
	all = append(all, mainMenuRows()...)
	all = append(all, menuRowsFor(store.User{})...)
	all = append(all, merchantServicesRows()...)
	all = append(all, thriftMenuRows()...)
	for _, row := range all {
		if typ, _ := serviceSwitchIntent(row.ID); typ == "" {
			t.Errorf("menu row %q is not recognized as a service-switch input; tapping it mid-session would be swallowed", row.ID)
		}
	}
}

func TestServiceSwitchIntentCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{input: "pay", want: "pay"},
		{input: "Make Payment", want: "pay"},
		{input: "buy data", want: "data"},
		{input: "fund wallet", want: "topup"},
		{input: "send money", want: "individual_pay"},
		{input: "register merchant", want: "merchant_register"},
		{input: "create invoice", want: "invoice_create"},
		{input: "join thrift", want: "thrift_join"},
		{input: "CONTRIBUTE Office Pool", want: "thrift_contribution"},
		{input: "JOIN Savings Circle", want: "thrift_join"},
		{input: "ACTIVATE Office Pool", want: "thrift_activate"},
		{input: "PAY XG-INV-12345", want: "invoice_payment"},
		{input: "pay_invoice:INV-456", want: "invoice_payment"},
		{input: "my limits", want: "limits"},
		{input: "ask xego", want: "ai"},
		{input: "complete profile", want: "profile"},
		{input: "back", want: "menu"},
		{input: "menu_main", want: "menu"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.input, func(t *testing.T) {
			got, _ := serviceSwitchIntent(test.input)
			if got != test.want {
				t.Fatalf("serviceSwitchIntent(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestServiceSwitchIntentIgnoresProseAndCommands(t *testing.T) {
	t.Parallel()
	notSwitches := []string{
		"hello", "5000", "what's my balance", "confirm", "continue",
		"cancel", "menu", "start", "skip", "NIN 12345678901",
	}
	for _, input := range notSwitches {
		if typ, _ := serviceSwitchIntent(input); typ != "" {
			t.Errorf("serviceSwitchIntent(%q) = %q, want empty (not a new service request)", input, typ)
		}
	}
}

func TestFlowStateLabelCoversDispatchedStates(t *testing.T) {
	t.Parallel()
	states := []string{
		"select_merchant", "pay_individual_phone", "merchant_register_email",
		"thrift_name", "invoice_item_name", "invoice_pay_amount", "select_data_network",
		"confirm_data_order", "kyb_request_confirm", "onboard_name", "onboard_email",
		"web_flow_active", "ai_assistant", "individual_legal_name",
	}
	for _, state := range states {
		if label := flowStateLabel(state); label == "" || label == "in a payment flow" {
			t.Errorf("flowStateLabel(%q) = %q, want a specific readable label", state, label)
		}
	}
}