package service

import (
	"testing"

	"whatsapp-payment-demo/internal/ports"
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
	assertRowsContain(t, mainMenuRows(), "menu_merchant_services", "menu_thrift_services", "menu_status", "menu_history", "menu_help")
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
