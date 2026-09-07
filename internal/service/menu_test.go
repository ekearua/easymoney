package service

import (
	"testing"

	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/providers/ai"
)

func TestMenuRowsStayWithinWhatsAppLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rows []ports.InteractiveRow
	}{
		{name: "main menu", rows: mainMenuRows()},
		{name: "merchant services", rows: merchantServicesRows()},
		{name: "thrift menu", rows: thriftMenuRows()},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if len(test.rows) > 10 {
				t.Fatalf("%s has %d rows, WhatsApp allows at most 10", test.name, len(test.rows))
			}
		})
	}
}

func TestNestedMenuRowsExposeExpectedActions(t *testing.T) {
	t.Parallel()
	assertRowsContain(t, mainMenuRows(), "menu_merchant_services", "menu_thrift_services", "menu_pay_individual", "menu_fund_wallet", "menu_history", "menu_help")
	assertRowsContain(t, merchantServicesRows(), "menu_register_merchant", "menu_generate_invoice", "menu_merchant_dashboard", "menu_main")
	assertRowsContain(t, thriftMenuRows(), "menu_become_individual", "menu_create_thrift", "menu_join_thrift", "menu_thrift_dashboard", "menu_main")
}

func assertRowsContain(t *testing.T, rows []ports.InteractiveRow, ids ...string) {
	t.Helper()
	present := map[string]bool{}
	for _, row := range rows {
		present[row.ID] = true
	}
	for _, id := range ids {
		if !present[id] {
			t.Fatalf("expected row %q in %#v", id, rows)
		}
	}
}

// TestMenuRowIDsCoveredByDispatch guards the returning-user double-menu bug.
// The AI classifier (simulated provider) matches every menu_* row id as intent
// "menu" because it does substring matching, so the classifier must be gated
// to free text only and every real row id must be matched by an explicit case
// in handleMenu. A row id leaking to handleMenu's default branch re-sends the
// menu and produces the duplicate "Open menu" a user sees after their session
// has gone idle.
func TestMenuRowIDsCoveredByDispatch(t *testing.T) {
	t.Parallel()
	var all []ports.InteractiveRow
	all = append(all, mainMenuRows()...)
	all = append(all, merchantServicesRows()...)
	all = append(all, thriftMenuRows()...)

	seen := map[string]bool{}
	for _, row := range all {
		seen[row.ID] = true
	}
	if len(seen) == 0 {
		t.Fatal("no menu rows collected")
	}

	// Every dispatched row id must be one handleMenu switches on. The
	// compact if-chain collates all explicit alternates per case so a row id
	// missing from every case fails loudly instead of silently re-sending
	// the menu.
	for id := range seen {
		if !menuRowCovers(id) {
			t.Errorf("row id %q is missing from handleMenu dispatch; it would fall to default and re-send the menu", id)
		}
	}
}

// TestInteractiveSelectionsWouldBeMisclassifiedByAI regresses the root cause
// of the double menu: the simulated AI classifier matches every menu_* row id
// as a concrete intent (substring match). The interactive gate
// (message.Interactive == "") is what stops these reaching the classifier, so
// this documents that removing it would re-introduce the double menu and
// misrouting of row taps.
func TestInteractiveSelectionsWouldBeMisclassifiedByAI(t *testing.T) {
	t.Parallel()
	sim := ai.NewSimulated()
	var all []ports.InteractiveRow
	all = append(all, mainMenuRows()...)
	all = append(all, merchantServicesRows()...)
	all = append(all, thriftMenuRows()...)
	for _, row := range all {
		intent, err := sim.ClassifyIntent(t.Context(), row.ID, nil)
		if err != nil {
			t.Fatalf("classify %q: %v", row.ID, err)
		}
		if intent.Intent == "none" {
			t.Fatalf("row id %q unexpectedly has no AI intent", row.ID)
		}
	}
}
