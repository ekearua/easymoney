package service

// Expiry coverage for reports/Payment_Pipeline_Test_Plan.xlsx scenarios B2
// (unconfirmed simulated bank transfer) and C3 (card checkout abandoned
// before the hosted page). Both walk the real Reconcile worker: Unresolved
// gateway payments are requeried first, then expireStale terminates drafts
// and awaiting-confirmation payments older than the session TTL. A terminal
// non-success transition releases the money-in allowance reservation taken at
// draft creation and books no ledger rows — money-in is only ever recorded on
// success.
//
// Determinism: the harness configures SessionTTL to a negative duration, so
// Reconcile's cutoff (now - SessionTTL) lies in the future and freshly
// created payments are immediately expirable. No sleeping, no backdating.
//
// Run with: TEST_DATABASE_URL=postgres://... go test ./internal/service/ -run 'TestCardCheckoutExpiry|TestBankTransferExpiry' -v

import (
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
	whatsappkyc "whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/store"
)

// newExpiryHarness is the pipeline harness with an immediately-expiring
// session TTL, so Reconcile expires fresh payments deterministically.
func newExpiryHarness(t *testing.T) *pipelineHarness {
	t.Helper()
	h := newPipelineHarness(t)
	h.payments.cfg.SessionTTL = -time.Minute
	return h
}

// allowanceInUse reports whether the payer currently holds a money-in
// allowance reservation (any amount) for the recent window.
func (h *pipelineHarness) allowanceInUse(t *testing.T, payer store.User) int64 {
	t.Helper()
	used, err := h.repository.AllowanceUsage(h.t.Context(), store.AccountIndividual, payer.ID, whatsappkyc.DirIn, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return used
}

// C3: a card checkout abandoned before the hosted page is completed must
// expire through Reconcile, release the money-in allowance, and book nothing.
func TestCardCheckoutExpiry_C3(t *testing.T) {
	h := newExpiryHarness(t)
	payer := h.newPayer(t, "+2348012340777")
	merchant := h.merchant(t)

	payment, err := h.payments.CreateCollectionDraft(h.t.Context(), payer, merchant, 5_000,
		ProviderInterswitch, ChannelWhatsApp, payer.WhatsAppNumber)
	if err != nil {
		t.Fatal(err)
	}
	if payment.Status != "awaiting_confirmation" {
		t.Fatalf("draft status = %s, want awaiting_confirmation", payment.Status)
	}
	if used := h.allowanceInUse(t, payer); used != payment.AmountKobo {
		t.Fatalf("allowance in use = %d, want %d (draft must reserve money-in)", used, payment.AmountKobo)
	}

	if err := h.payments.Reconcile(h.t.Context()); err != nil {
		t.Fatal(err)
	}

	expired, err := h.repository.PaymentByID(h.t.Context(), payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != "expired" {
		t.Fatalf("payment status = %s, want expired after reconcile", expired.Status)
	}
	if used := h.allowanceInUse(t, payer); used != 0 {
		t.Fatalf("allowance in use = %d, want 0 (expiry must release the reservation)", used)
	}
	if entries := h.ledgerForPayment(t, expired); len(entries) != 0 {
		t.Fatalf("expiry booked %d ledger rows, want 0", len(entries))
	}

	// Idempotence: a second reconcile pass must not change anything.
	if err := h.payments.Reconcile(h.t.Context()); err != nil {
		t.Fatal(err)
	}
	again, err := h.repository.PaymentByID(h.t.Context(), payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != "expired" {
		t.Fatalf("payment status = %s after second reconcile, want expired", again.Status)
	}
}

// B2: a simulated bank transfer whose customer never sends CONFIRM must
// expire through Reconcile with the instruction on record, the allowance
// released, and no ledger postings.
func TestBankTransferExpiry_B2(t *testing.T) {
	h := newExpiryHarness(t)
	payer := h.newPayer(t, "+2348012340888")
	merchant := h.merchant(t)

	payment, err := h.payments.CreateDraftForProvider(h.t.Context(), payer, merchant, 5_000,
		ProviderBankTransfer, ChannelWhatsApp, payer.WhatsAppNumber)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := h.repository.ListActiveBankTransferAccounts(h.t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) == 0 {
		t.Fatal("no seeded bank transfer accounts")
	}
	if _, _, err := h.payments.InitializeBankTransferSimulation(h.t.Context(), payment, accounts[0]); err != nil {
		t.Fatal(err)
	}
	if used := h.allowanceInUse(t, payer); used != payment.AmountKobo {
		t.Fatalf("allowance in use = %d, want %d", used, payment.AmountKobo)
	}

	// The customer never sends CONFIRM; the reconcile worker ends the attempt.
	if err := h.payments.Reconcile(h.t.Context()); err != nil {
		t.Fatal(err)
	}

	expired, err := h.repository.PaymentByID(h.t.Context(), payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != "expired" {
		t.Fatalf("payment status = %s, want expired after reconcile", expired.Status)
	}
	if used := h.allowanceInUse(t, payer); used != 0 {
		t.Fatalf("allowance in use = %d, want 0 (expiry must release the reservation)", used)
	}
	if entries := h.ledgerForPayment(t, expired); len(entries) != 0 {
		t.Fatalf("expiry booked %d ledger rows, want 0", len(entries))
	}
}

// compile-time assertion that the harness config stays wired to the service
// config type used by SessionTTL.
var _ = config.Config{SessionTTL: time.Minute}
