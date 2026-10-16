package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// Chat-FSM aggregation readiness guard.
//
// The product rule: a chat flow must never force a customer through one
// message per form field. Form-shaped chains (profile details, KYB, KYC
// evidence) must accept everything in one message, so every state that
// collects free-text facts is either
//
//   - "aggregated": the handler parses a multi-field message (labeled lines,
//     comma-separated values, or a one-shot dump) and re-asks only for what
//     is missing, or
//   - exempt: the state legitimately handles a single value because it is a
//     verification code, an amount, a menu/button selection, a review or
//     confirm turn, or one atomic fact (a phone number, a date, a bank pick).
//
// The guard walks the main dispatch switch in conversation.go with go/ast and
// fails when a dispatched state is missing from both manifests below — i.e.
// someone added a new chat state that still collects one field per message —
// and conversely when a manifest label no longer exists in the switch, so the
// manifests cannot rot into fiction.

// aggregatedChatStates collect or delegate to multi-field parsing.
var aggregatedChatStates = map[string]string{
	// CBN Tier 1 onboarding: name+email pair, then details block. The DOB,
	// gender and address labels are legacy delegates to onboard_details.
	"onboard_name":    "name+email aggregation",
	"onboard_email":   "name+email aggregation",
	"onboard_details": "DOB+gender+address block",
	"onboard_dob":     "legacy delegate to onboard_details",
	"onboard_gender":  "legacy delegate to onboard_details",
	"onboard_address": "legacy delegate to onboard_details",

	// Individual KYC upgrade: details block, ID type+number, BVN+NIN, EDD
	// set. The single-field labels are legacy delegates kept so sessions
	// saved pre-aggregation complete.
	"individual_details":         "legal name+DOB+gender+address+occupation block",
	"individual_legal_name":      "legacy delegate to individual_details",
	"individual_dob":             "legacy delegate to individual_details",
	"individual_gender":          "legacy delegate to individual_details",
	"individual_address":         "legacy delegate to individual_details",
	"individual_occupation":      "legacy delegate to individual_details",
	"individual_id_details":      "ID type+number block",
	"individual_id_type":         "legacy delegate to individual_id_details",
	"individual_id_number":       "legacy delegate to individual_id_details",
	"individual_bvn_nin":         "BVN+NIN block",
	"individual_bvn":             "legacy delegate to individual_bvn_nin",
	"individual_nin":             "legacy delegate to individual_bvn_nin",
	"individual_edd_details":     "address proof+source of funds block",
	"individual_address_proof":   "legacy delegate to individual_edd_details",
	"individual_source_of_funds": "legacy delegate to individual_edd_details",

	// Merchant KYB: category+description block after verification. Single
	// labels are legacy delegates.
	"merchant_register_details":     "category+description block",
	"merchant_register_name":        "legacy delegate to merchant_register_details",
	"merchant_register_category":    "legacy delegate to merchant_register_details",
	"merchant_register_description": "legacy delegate to merchant_register_details",

	// Multi-field by design at entry: invoice items accept one-per-line or
	// comma concat, the invoice customer pair accepts phone+email in one
	// message, thrift creation accepts the full concat dump, event custom
	// fields arrive comma-separated, and onboard email-code is exempt
	// separately (it is a verification turn).
	"invoice_item_name":      "one item per line or Name, Qty, Price concat; bulk multi-line",
	"invoice_customer_phone": "phone+email concat accepted",
	"thrift_name":            "full thrift concat dump accepted",
	"collect_custom_fields":  "comma-separated custom-field values",
}

// exemptChatStates are single-value turns where one field per message is
// correct. The reason tags keep the manifest auditable.
var exemptChatStates = map[string]string{
	// Verification codes and OTP-class turns.
	"onboard_email_code":           "verification code",
	"merchant_register_email_code": "verification code",
	"individual_email_code":        "verification code",
	"link_code":                    "verification code",

	// Amounts: a single numeric value.
	"enter_amount":              "amount",
	"enter_service_quantity":    "amount",
	"invoice_pay_amount":        "amount",
	"invoice_item_quantity":     "amount",
	"invoice_item_unit_price":   "amount",
	"wallet_topup_amount":       "amount",
	"thrift_amount":             "amount",
	"thrift_edit_amount_value":  "amount",
	"thrift_edit_members_value": "amount",
	"pay_individual_amount":     "amount",

	// Selections, menus, reviews and confirms: buttons or keyword turns.
	"select_merchant":            "selection",
	"select_service_or_amount":   "selection",
	"select_event_tier":          "selection",
	"select_payment_method":      "selection",
	"select_data_network":        "selection",
	"select_data_plan":           "selection",
	"select_data_payment_method": "selection",
	"confirm_payment":            "confirm",
	"confirm_data_order":         "confirm",
	"confirm_service_purchase":   "confirm",
	"invoice_review":             "confirm", "invoice_add_item": "selection",
	"invoice_edit_item":           "selection",
	"invoice_remove_item":         "selection",
	"invoice_select_merchant":     "selection",
	"invoice_delivery_fee":        "amount",
	"thrift_concat_review":        "confirm",
	"thrift_pay_method":           "selection",
	"thrift_join_confirm":         "confirm",
	"thrift_activate_order":       "confirm",
	"kyb_request_select_merchant": "selection",
	"kyb_request_details":         "aggregated",
	"kyb_request_confirm":         "confirm",
	"invoice_pay_method":          "selection",
	"pay_individual_method":       "selection",
	"wallet_topup_method":         "selection",
	"ai_assistant":                "AI-routed free text",
	"web_flow_active":             "browser owns the flow",
	"confirm_session_switch":      "confirm",

	// One atomic fact per turn.
	"enter_data_phone":            "phone",
	"invoice_customer_email":      "email",
	"invoice_due_date":            "date",
	"pay_individual_phone":        "phone",
	"pay_individual_bank_code":    "bank pick",
	"pay_individual_bank_pick":    "bank pick",
	"pay_individual_account":      "account number",
	"pay_individual_confirm":      "confirm",
	"individual_liveness":         "liveness media capture turn",
	"thrift_join_code":            "join code",
	"thrift_frequency":            "frequency token",
	"thrift_target":               "target amount",
	"thrift_edit_field":           "field picker",
	"thrift_edit_frequency_value": "frequency token",
	"invoice_edit_field":          "field picker",
	"invoice_edit_value":          "single picked-field value",
	"link_phone":                  "phone",

	// Cash-pickup remittance: single-value turns mirroring individual pay.
	"send_agent_name":    "recipient name",
	"send_agent_phone":   "phone",
	"send_agent_amount":  "amount",
	"send_agent_region":  "selection",
	"send_agent_confirm": "confirm",

	// Legacy delegates into aggregated handlers (classification kept here so
	// the switch case lists stay discoverable).
	"onboard_confirm_account": "confirm",
	"merchant_register_email": "email",
	"individual_email":        "email",
}

// dispatchedSessionStates extracts every state label dispatched by the
// `switch session.State` in conversation.go.
func dispatchedSessionStates(t *testing.T) map[string]bool {
	t.Helper()
	const file = "conversation.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	states := map[string]bool{}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		sel, ok := sw.Tag.(*ast.SelectorExpr)
		if !ok || sel.X.(*ast.Ident) == nil || sel.X.(*ast.Ident).Name != "session" || sel.Sel.Name != "State" {
			return true
		}
		found = true
		for _, c := range sw.Body.List {
			cc, ok := c.(*ast.CaseClause)
			if !ok || cc.List == nil {
				continue // default clause
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				state, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				states[state] = true
			}
		}
		return true
	})
	if !found {
		t.Fatalf("no `switch session.State` found in %s", file)
	}
	if len(states) == 0 {
		t.Fatalf("dispatch switch in %s has no case labels", file)
	}
	return states
}

// TestChatFSMAggregationReadiness fails when a dispatched chat state is
// neither aggregated-capable nor explicitly exempt, and when the manifests
// drift from the dispatch switch.
func TestChatFSMAggregationReadiness(t *testing.T) {
	t.Parallel()
	states := dispatchedSessionStates(t)

	for state := range states {
		_, aggregated := aggregatedChatStates[state]
		_, exempt := exemptChatStates[state]
		if !aggregated && !exempt {
			t.Errorf("chat state %s is dispatched but classified in neither manifest; "+
				"if it collects free-text facts it must parse a multi-field message "+
				"(add to aggregatedChatStates with its aggregation), otherwise add it "+
				"to exemptChatStates with a reason tag", state)
		}
	}
	for state := range aggregatedChatStates {
		if !states[state] {
			t.Errorf("aggregatedChatStates lists %q but the dispatch switch no longer has it; remove the stale label", state)
		}
	}
	for state := range exemptChatStates {
		if !states[state] {
			t.Errorf("exemptChatStates lists %q but the dispatch switch no longer has it; remove the stale label", state)
		}
	}
}
