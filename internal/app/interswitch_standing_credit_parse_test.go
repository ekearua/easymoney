package app

import "testing"

func TestParseInterswitchStandingCredit_DocumentedNestedEvent(t *testing.T) {
	body := []byte(`{"event":"TRANSACTION.COMPLETED","uuid":"evt-1","data":{"amount":250000,"merchantReference":"credit-1","retrievalReferenceNumber":"0123456789"}}`)

	credit, ok, err := parseInterswitchStandingCredit(body, "evt-1")
	if err != nil {
		t.Fatalf("parse standing credit: %v", err)
	}
	if !ok {
		t.Fatal("documented nested event should be recognized")
	}
	if credit.AccountNumber != "0123456789" {
		t.Errorf("account number = %q, want %q", credit.AccountNumber, "0123456789")
	}
	if credit.Reference != "credit-1" {
		t.Errorf("reference = %q, want %q", credit.Reference, "credit-1")
	}
	if credit.AmountKobo != 250000 {
		t.Errorf("amount = %d, want %d", credit.AmountKobo, 250000)
	}
}

func TestParseInterswitchStandingCredit_LegacyFlatEvent(t *testing.T) {
	body := []byte(`{"resolverReference":"0123456789","reference":"credit-2","transactionAmount":100000}`)

	credit, ok, err := parseInterswitchStandingCredit(body, "event-2")
	if err != nil {
		t.Fatalf("parse standing credit: %v", err)
	}
	if !ok {
		t.Fatal("legacy flat event should be recognized")
	}
	if credit.AccountNumber != "0123456789" || credit.Reference != "credit-2" || credit.AmountKobo != 100000 {
		t.Fatalf("unexpected parsed credit: %+v", credit)
	}
}

func TestParseInterswitchStandingCredit_UsesEventReferenceFallback(t *testing.T) {
	body := []byte(`{"event":"TRANSACTION.COMPLETED","data":{"amount":5000,"retrievalReferenceNumber":"0123456789"}}`)

	credit, ok, err := parseInterswitchStandingCredit(body, "event-3")
	if err != nil {
		t.Fatalf("parse standing credit: %v", err)
	}
	if !ok {
		t.Fatal("event with a fallback reference should be recognized")
	}
	if credit.Reference != "event-3" {
		t.Errorf("reference = %q, want %q", credit.Reference, "event-3")
	}
}

func TestParseInterswitchStandingCredit_RejectsMissingAccountOrReference(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing account",
			body: `{"reference":"credit-4","data":{"amount":5000}}`,
		},
		{
			name: "missing reference",
			body: `{"data":{"amount":5000,"retrievalReferenceNumber":"0123456789"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, ok, err := parseInterswitchStandingCredit([]byte(test.body), "")
			if err != nil {
				t.Fatalf("parse standing credit: %v", err)
			}
			if ok {
				t.Fatal("event without account or reference should not be recognized")
			}
		})
	}
}

func TestParseInterswitchStandingCredit_RejectsMalformedJSON(t *testing.T) {
	if _, _, err := parseInterswitchStandingCredit([]byte(`{"data":`), "event-5"); err == nil {
		t.Fatal("malformed event should return an error")
	}
}
