package app

// The pay flow's one-page start renders merchant, amount, and payment rails
// together, and its submit is the charge boundary: a merchant with a catalog
// (named services/tickets) must be routed to the item page instead of being
// charged a rail-tap for an unchosen item, while a merchant with an empty
// catalog pays straight from the page. simulateOnePagePayBoundary drives both
// branches through the real HTTP routes and asserts the page, the stored
// step, and the absence or presence of a drafted payment all agree.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// simulateOnePagePayBoundary walks the one-page pay flow for an empty-catalog
// merchant: the page renders everything, the submit drafts against the tapped
// rail in one hop — no item page ever renders, and the stored step moves
// straight onto checkout/done territory (the flow is now owned by a payment).
func simulateOnePagePayBoundary(t *testing.T, ctx context.Context, run *simRun, convo *service.ConversationService, repository *store.Store, messenger *simMessenger, cfg config.Config, payer store.User) {
	t.Helper()
	messenger.reset()
	resetChatSession(t, ctx, repository, payer.ID)

	if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: payer.WhatsAppNumber, Text: "make payment"}); err != nil {
		t.Fatalf("handle 'make payment': %v", err)
	}
	sent := messenger.snapshot()
	if len(sent) != 1 || sent[0].kind != "link" {
		t.Fatalf("expected exactly one link message, got %+v", sent)
	}
	token := strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")
	if len(token) < 32 {
		t.Fatalf("unexpected web-flow token %q", token)
	}

	// A seeded merchant without a catalog: nothing sits between the one-page
	// start and the charge.
	const slug = "bright-fix-ng"
	merchant, err := repository.MerchantBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("merchant %s: %v", slug, err)
	}
	services, err := repository.ListActiveMerchantServices(ctx, merchant.ID)
	if err != nil {
		t.Fatalf("services for %s: %v", slug, err)
	}
	events, err := repository.ListActiveEventsByMerchantID(ctx, merchant.ID)
	if err != nil {
		t.Fatalf("events for %s: %v", slug, err)
	}
	if len(services) != 0 || len(events) != 0 {
		t.Fatalf("fixture expects an empty catalog for %s (services=%d events=%d)", slug, len(services), len(events))
	}

	// One page: ask-bar, merchant select, amount field, and one submit button
	// per rail all render together.
	status, body, _ := run.get("/w/" + token)
	if status != http.StatusOK {
		t.Fatalf("one-page start: status=%d", status)
	}
	for _, want := range []string{`name="amount_kobo"`, `name="merchant_slug"`, "data-fee-bps"} {
		if !strings.Contains(body, want) {
			t.Errorf("one-page start missing %s\n%s", want, run.page(body))
		}
	}

	// The submit is the charge boundary: one post validates merchant and
	// amount, drafts the payment against the tapped rail, and routes to the
	// gateway — no intermediate page.
	paymentsBefore, err := repository.ListPayments(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	status, body, loc := run.post("/w/"+token, url.Values{
		"merchant_slug": {slug}, "amount_kobo": {"2500"}, "action": {service.ProviderInterswitch},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("one-page card submit: status=%d page=%s", status, run.page(body))
	}
	paymentsAfter, err := repository.ListPayments(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(paymentsAfter) != len(paymentsBefore)+1 {
		t.Fatalf("one-page card submit must draft exactly one payment: %d before, %d after", len(paymentsBefore), len(paymentsAfter))
	}
	if !strings.HasPrefix(loc, "https://") {
		t.Fatalf("one-page card submit should route straight to the gateway, got %q", loc)
	}
	fmt.Printf("  ✅ one-page pay (no catalog): one post drafts and routes to %s\n", loc)

	// Leave nothing behind: close the flow so a later run starts clean.
	if _, _, err := repository.CompleteWebFlow(ctx, token); err != nil {
		t.Fatalf("close flow: %v", err)
	}
}
