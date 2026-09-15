package app

// The pay flow's render path can advance past a step the customer never sees
// (a merchant with an empty catalog has nothing to choose on the item step).
// Such a skip is a real flow transition: if it only mutates the in-memory flow,
// the page renders the amount step while the stored flow row still says "item",
// so the stepper lags a step behind the page and the amount form's submit is
// dispatched as an item submission that dead-ends on a page with no fields.
// simulateAutoSkippedItemStep drives that exact sequence through the real HTTP
// routes and asserts the page, the stepper, and the stored step all agree.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// stepperCurrentLabel returns the label of the stepper's current node, which is
// what the customer reads as "you are here".
func stepperCurrentLabel(body string) string {
	match := stepperCurrentRe.FindStringSubmatch(body)
	if match == nil {
		return ""
	}
	return match[1]
}

var stepperCurrentRe = regexp.MustCompile(`<li class="current"><span class="dot">[^<]*</span><span class="lbl">([^<]*)</span></li>`)

func simulateAutoSkippedItemStep(t *testing.T, ctx context.Context, run *simRun, convo *service.ConversationService, repository *store.Store, messenger *simMessenger, cfg config.Config, payer store.User) {
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

	// A seeded merchant without a catalog: the item step offers only
	// "Custom amount", which the render path skips.
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

	// 1. merchant: confirming it lands on the auto-skipped item step.
	status, _, loc := run.post("/w/"+token, url.Values{"merchant_slug": {slug}})
	if status != http.StatusSeeOther {
		t.Fatalf("merchant step: status=%d", status)
	}
	status, body, _ := run.get(loc)
	if status != http.StatusOK {
		t.Fatalf("auto-skipped step page: status=%d", status)
	}
	flow, err := repository.WebFlowByToken(ctx, token)
	if err != nil {
		t.Fatalf("load flow: %v", err)
	}
	if flow.Step != "amount" {
		t.Errorf("auto-skipped flow step = %q, want %q — the following submit is dispatched against this step", flow.Step, "amount")
	}
	if got := stepperCurrentLabel(body); got != "Amount" {
		t.Errorf("stepper current = %q, want %q (page: %s)", got, "Amount", run.page(body))
	} else {
		fmt.Printf("  ✅ auto-skipped item: page=Amount stepper=%s stored_step=%s\n", got, flow.Step)
	}

	// 2. amount: must advance to review, not dead-end on the stale step.
	status, body, loc = run.post("/w/"+token, url.Values{"amount_kobo": {"2500"}})
	if status != http.StatusSeeOther {
		t.Fatalf("amount step after auto-skip: status=%d loc=%s page=%s", status, loc, run.page(body))
	}
	status, body, _ = run.get(loc)
	if status != http.StatusOK {
		t.Fatalf("review page: status=%d", status)
	}
	if got := stepperCurrentLabel(body); got != "Review" {
		t.Errorf("stepper current after amount = %q, want %q (page: %s)", got, "Review", run.page(body))
	}
	fmt.Printf("  ✅ amount → review: stepper=%s\n", stepperCurrentLabel(body))

	// Leave nothing behind: close the flow so a later run starts clean.
	if _, _, err := repository.CompleteWebFlow(ctx, token); err != nil {
		t.Fatalf("close flow: %v", err)
	}
}
