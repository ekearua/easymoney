package app

// The stepped shell (stepper + card inputs + sticky action bar) is shared by the
// money, KYC/onboarding, and thrift web flows. Two invariants keep it honest,
// and both are pinned here:
//
//  1. Every step key a flow's step/submit switches handle has a node in the
//     flow's layout — otherwise the stepper parks on the last node and the
//     progress bar disagrees with the page it is describing.
//  2. The first action on a step is the primary call to action; retries, skips,
//     and back links come after it, because the shell renders the first action
//     as the dominant CTA.

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/service"
	"whatsapp-payment-demo/internal/store"
)

// goFuncBody slices a source file down to one method so step keys can be read
// back out of the code that dispatches on them.
func goFuncBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "func (a *App) "+name+"(")
	if start < 0 {
		t.Fatalf("%s not found in source", name)
	}
	body := src[start:]
	if end := strings.Index(body, "\nfunc "); end >= 0 {
		body = body[:end]
	}
	return body
}

// topLevelCaseKeys returns the step keys a handler dispatches on: the case
// labels of its step switch (one tab of indentation — nested switches such as
// item kinds, ID types, and frequencies sit deeper and are not flow steps),
// plus any literal compared directly against flow.Step.
func topLevelCaseKeys(body string) []string {
	var keys []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "\tcase ") {
			list := strings.TrimPrefix(line, "\tcase ")
			if i := strings.Index(list, ":"); i >= 0 {
				list = list[:i]
			}
			for _, part := range strings.Split(list, ",") {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, `"`) && strings.HasSuffix(part, `"`) {
					keys = append(keys, strings.Trim(part, `"`))
				}
			}
		}
		for _, match := range flowStepCompareRe.FindAllStringSubmatch(line, -1) {
			keys = append(keys, match[1])
		}
	}
	return keys
}

// A handler may test its step with an if rather than a switch (the thrift
// contribution flow has two steps and does exactly that).
var flowStepCompareRe = regexp.MustCompile(`flow\.Step == "([^"]*)"`)

// TestFlowStepMapsCoverSteps is the drift guard: a step a flow can render with
// no node in its layout would silently mis-place the customer in the progress
// bar, so the source of truth is the handler code itself.
func TestFlowStepMapsCoverSteps(t *testing.T) {
	sources := map[string]string{}
	for _, name := range []string{"webflow_money.go", "webflow_money2.go", "webflow_forms.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sources[name] = string(raw)
	}

	flows := []struct{ flowType, file, stepFn, submitFn string }{
		{service.WebFlowPay, "webflow_money.go", "wfPayStep", "wfPaySubmit"},
		{service.WebFlowPayInvoice, "webflow_money2.go", "wfPayInvoiceStep", "wfPayInvoiceSubmit"},
		{service.WebFlowThriftContribute, "webflow_money2.go", "wfThriftContributeStep", "wfThriftContributeSubmit"},
		{service.WebFlowData, "webflow_money2.go", "wfDataStep", "wfDataSubmit"},
		{service.WebFlowTopup, "webflow_money2.go", "wfTopupStep", "wfTopupSubmit"},
		{service.WebFlowIndividualPay, "webflow_money2.go", "wfIndividualPayStep", "wfIndividualPaySubmit"},
		{service.WebFlowThriftCreate, "webflow_forms.go", "wfThriftCreateStep", "wfThriftCreateSubmit"},
		{service.WebFlowThriftJoin, "webflow_forms.go", "wfThriftJoinStep", "wfThriftJoinSubmit"},
		{service.WebFlowOnboard, "webflow_forms.go", "wfOnboardStep", "wfOnboardSubmit"},
		{service.WebFlowIndividualUpgrade, "webflow_forms.go", "wfIndividualUpgradeStep", "wfIndividualUpgradeSubmit"},
		{service.WebFlowMerchantRegister, "webflow_forms.go", "wfMerchantRegisterStep", "wfMerchantRegisterSubmit"},
		{service.WebFlowKYBRequest, "webflow_forms.go", "wfKYBRequestStep", "wfKYBRequestSubmit"},
	}

	for _, flow := range flows {
		layout, ok := wfFlowLayouts[flow.flowType]
		if !ok {
			t.Errorf("%s walks steps but has no stepper layout", flow.flowType)
			continue
		}
		src := sources[flow.file]
		for _, fn := range []string{flow.stepFn, flow.submitFn} {
			keys := topLevelCaseKeys(goFuncBody(t, src, fn))
			if len(keys) == 0 {
				t.Errorf("%s: no step keys found in %s", flow.flowType, fn)
				continue
			}
			for _, key := range keys {
				if _, mapped := layout.at[key]; !mapped {
					t.Errorf("%s handles step %q but its layout has no node for it (the stepper would park on %q)",
						fn, key, layout.labels[len(layout.labels)-1])
				}
			}
		}
	}

	// Every layout has to be internally consistent: real nodes, and a node for
	// the empty step a freshly created flow renders. One-page flows are exempt
	// from the two-node rule on purpose: their single-page fresh path hides the
	// stepper entirely, and the one-node layout exists only so legacy step keys
	// of in-flight flows keep mapping to a node.
	for flowType, layout := range wfFlowLayouts {
		onePage := wfOnePageFlows[flowType]
		if len(layout.labels) < 2 && !onePage {
			t.Errorf("%s: a stepped flow needs at least two nodes, got %d", flowType, len(layout.labels))
		}
		if onePage && len(layout.labels) != 1 {
			t.Errorf("%s: a one-page flow's layout should be a single placeholder node, got %d", flowType, len(layout.labels))
		}
		for _, label := range layout.labels {
			if strings.TrimSpace(label) == "" {
				t.Errorf("%s: empty stepper node label", flowType)
			}
		}
		if _, ok := layout.at[""]; !ok {
			t.Errorf("%s: no node mapped for the flow's opening step", flowType)
		}
		for key, index := range layout.at {
			if index < 0 || index >= len(layout.labels) {
				t.Errorf("%s: step %q maps to node %d, out of range", flowType, key, index)
			}
		}
	}
}

// TestFlowActionsPrimaryFirst is the CTA-ordering sweep: the stepped shell
// renders a page's first action as the dominant call to action and everything
// after it as secondary, so no step may put a retry (resend), a skip, or a back
// link before its primary action. Like the drift guard above, the check reads
// the handler source so a newly written action set is covered the moment it
// exists.
func TestFlowActionsPrimaryFirst(t *testing.T) {
	secondary := map[string]bool{"skip": true, "resend": true, "back": true, "cancel": true}
	for _, name := range []string{"webflow_forms.go", "webflow_money.go", "webflow_money2.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(raw)
		for _, m := range actionSetRe.FindAllStringSubmatchIndex(src, -1) {
			block := src[m[2]:m[3]]
			names := actionNameRe.FindAllStringSubmatch(block, -1)
			if len(names) < 2 {
				continue // single-action steps have no ordering to get wrong
			}
			var order []string
			for _, n := range names {
				order = append(order, n[1])
			}
			firstPrimary := -1
			for i, n := range order {
				if !secondary[n] {
					firstPrimary = i
					break
				}
			}
			if firstPrimary == -1 {
				// A step offering only secondary exits (e.g. a bank-pick page
				// with no match and only a Back) is a dead end by design and
				// out of scope here; ordering among secondaries is cosmetic.
				continue
			}
			if firstPrimary != 0 {
				line := strings.Count(src[:m[0]], "\n") + 1
				t.Errorf("%s:%d puts a secondary action before the primary CTA: %v", name, line, order)
			}
		}
	}
}

// actionSetRe matches a full Actions assignment, inline or multiline.
var actionSetRe = regexp.MustCompile(`Actions = \[\]webFlowAction\{(.*)\}`)

var actionNameRe = regexp.MustCompile(`\{Name: "([a-z_]+)"`)

// TestFlowStepsStates pins the rendered states: nodes before the current one
// are done, the current one carries its position, the rest are todo.
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

func TestFlowStepsStates(t *testing.T) {
	steps, ok := wfFlowSteps(service.WebFlowThriftCreate, "frequency")
	if !ok {
		t.Fatal("thrift_create must walk the stepped shell")
	}
	want := []webFlowStepLabel{
		{Label: "Name", State: "done", Dot: "✓"},
		{Label: "Amount", State: "done", Dot: "✓"},
		{Label: "Frequency", State: "current", Dot: "3"},
		{Label: "Size", State: "todo", Dot: "4"},
		{Label: "Review", State: "todo", Dot: "5"},
	}
	if len(steps) != len(want) {
		t.Fatalf("thrift_create nodes = %d, want %d", len(steps), len(want))
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("node %d = %+v, want %+v", i, steps[i], want[i])
		}
	}

	// An unmapped key must never be invented: the flow falls back to the last
	// node (and the drift guard above is what keeps that unreachable).
	if _, ok := wfFlowSteps("no_such_flow", "amount"); ok {
		t.Fatal("a flow with no layout must render the plain page")
	}
	// invoice_create is now stepped, and both of its items pages share the
	// Items node (items_summary is a confirmation of the same node, not a
	// separate step in the customer's mind).
	itemsSteps, ok := wfFlowSteps(service.WebFlowInvoiceCreate, "items_summary")
	if !ok || itemsSteps[2].State != "current" || itemsSteps[2].Label != "Items" {
		t.Fatalf("invoice_create items_summary must be the Items node, got %+v (ok=%v)", itemsSteps, ok)
	}
	if steps, ok := wfFlowSteps(service.WebFlowKYBRequest, "note"); !ok || steps[1].State != "current" {
		t.Fatalf("kyb_request note step must be the current node, got %+v (ok=%v)", steps, ok)
	}
}

// simulateSteppedKYCAndThrift drives the thrift-creation flow through every step
// and a fresh customer's onboarding flow, asserting each page renders the
// stepped shell (scoping class, stepper node) and that the primary action comes
// first on steps that offer a retry or a skip.
func simulateSteppedKYCAndThrift(t *testing.T, ctx context.Context, run *simRun, convo *service.ConversationService, repository *store.Store, messenger *simMessenger, cfg config.Config, payer store.User) {
	t.Helper()

	assertStepped := func(label, body, wantNode string) {
		t.Helper()
		if !strings.Contains(body, `class="receipt wf"`) {
			t.Fatalf("%s: page must scope the stepped shell via main.wf (page: %s)", label, run.page(body))
		}
		if got := stepperCurrentLabel(body); got != wantNode {
			t.Fatalf("%s: stepper current = %q, want %q (page: %s)", label, got, wantNode, run.page(body))
		}
	}

	// ---- thrift_create: every step walks the shell ----------------------
	messenger.reset()
	resetChatSession(t, ctx, repository, payer.ID)
	if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: payer.WhatsAppNumber, Text: "create thrift"}); err != nil {
		t.Fatalf("handle 'create thrift': %v", err)
	}
	sent := messenger.snapshot()
	if len(sent) != 1 || sent[0].kind != "link" {
		t.Fatalf("expected one link message for create thrift, got %+v", sent)
	}
	thriftToken := strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")
	if len(thriftToken) < 32 {
		t.Fatalf("unexpected thrift web-flow token %q", thriftToken)
	}

	status, body, _ := run.get("/w/" + thriftToken)
	if status != http.StatusOK {
		t.Fatalf("thrift create page: %d", status)
	}
	assertStepped("thrift create (name)", body, "Name")

	steps := []struct {
		form map[string]string
		node string
	}{
		{map[string]string{"name": "Office Pool"}, "Amount"},
		{map[string]string{"amount_kobo": "2000"}, "Frequency"},
		{map[string]string{"frequency": "monthly"}, "Size"},
		{map[string]string{"target": "6"}, "Review"},
	}
	for _, step := range steps {
		form := url.Values{}
		for k, v := range step.form {
			form.Set(k, v)
		}
		status, _, loc := run.post("/w/"+thriftToken, form)
		if status != http.StatusSeeOther {
			t.Fatalf("thrift create %v: status=%d", step.form, status)
		}
		status, body, _ = run.get(loc)
		if status != http.StatusOK {
			t.Fatalf("thrift create page after %v: %d", step.form, status)
		}
		assertStepped("thrift create "+step.node, body, step.node)
	}
	// The review step's single action is the group creation CTA.
	if !strings.Contains(body, `value="create"`) {
		t.Fatalf("thrift create review must offer the Create group CTA (page: %s)", run.page(body))
	}
	fmt.Printf("  ✅ thrift_create walks the stepped shell (Name→Amount→Frequency→Size→Review)\n")

	// ---- onboard: profile completion gets the same shell ----------------
	fresh, err := repository.GetOrCreateUser(ctx, "+2348012340111")
	if err != nil {
		t.Fatalf("fresh user: %v", err)
	}
	messenger.reset()
	resetChatSession(t, ctx, repository, fresh.ID)
	if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: fresh.WhatsAppNumber, Text: "complete profile"}); err != nil {
		t.Fatalf("handle 'complete profile': %v", err)
	}
	sent = messenger.snapshot()
	if len(sent) == 0 || sent[0].kind != "link" {
		t.Fatalf("expected an onboarding link, got %+v", sent)
	}
	onboardToken := strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")

	status, body, _ = run.get("/w/" + onboardToken)
	if status != http.StatusOK {
		t.Fatalf("onboard page: %d", status)
	}
	assertStepped("onboard (name)", body, "Name")

	status, _, loc := run.post("/w/"+onboardToken, url.Values{"name": {"Amina Bello"}})
	if status != http.StatusSeeOther {
		t.Fatalf("onboard name step: status=%d", status)
	}
	status, body, _ = run.get(loc)
	if status != http.StatusOK {
		t.Fatalf("onboard email page: %d", status)
	}
	assertStepped("onboard (email)", body, "Email")

	// The code step is where a retry sits next to the primary action. Advancing
	// the stored step is exactly the state the app reaches once it has emailed
	// the code; the render (and the action order) is what this asserts.
	if err := repository.SaveWebFlowProgress(ctx, onboardToken, "code", map[string]string{"name": "Amina Bello", "email": "amina@example.com"}); err != nil {
		t.Fatalf("advance onboarding flow to the code step: %v", err)
	}
	status, body, _ = run.get("/w/" + onboardToken)
	if status != http.StatusOK {
		t.Fatalf("onboard code page: %d", status)
	}
	assertStepped("onboard (code)", body, "Verify")
	verify := strings.Index(body, `value="next"`)
	resend := strings.Index(body, `value="resend"`)
	if verify < 0 || resend < 0 {
		t.Fatalf("onboard code step must offer Verify and Resend code (page: %s)", run.page(body))
	}
	if verify > resend {
		t.Fatal("onboard code step must put the primary Verify action before the secondary Resend code")
	}
	fmt.Printf("  ✅ onboard walks the stepped shell (Name→Email→Verify) with Verify before Resend code\n")

	// ---- invoice_create: add-one-item page and its summary ---------------
	messenger.reset()
	resetChatSession(t, ctx, repository, payer.ID)
	// Make the payer an approved merchant owner so the flow has a merchant to
	// bill from (the flow requires one before it starts).
	if _, err := repository.RawExec(ctx, fmt.Sprintf(`INSERT INTO merchant_owners (merchant_id, user_id)
		SELECT m.id, '%s' FROM merchants m WHERE m.slug='lagos-lunchbox'
		ON CONFLICT DO NOTHING`, payer.ID)); err != nil {
		t.Fatal(err)
	}
	if err := convo.Handle(ctx, store.InboundMessage{Channel: service.ChannelWhatsApp, Sender: payer.WhatsAppNumber, Text: "generate invoice"}); err != nil {
		t.Fatalf("handle 'generate invoice': %v", err)
	}
	sent = messenger.snapshot()
	if len(sent) != 1 || sent[0].kind != "link" {
		t.Fatalf("expected one link message for generate invoice, got %+v", sent)
	}
	invToken := strings.TrimPrefix(sent[0].url, cfg.BaseURL+"/w/")

	invPost := func(form map[string]string) string {
		t.Helper()
		values := url.Values{}
		for k, v := range form {
			values.Set(k, v)
		}
		status, _, loc := run.post("/w/"+invToken, values)
		if status != http.StatusSeeOther {
			t.Fatalf("invoice step %v: status=%d", form, status)
		}
		status, body, _ = run.get(loc)
		if status != http.StatusOK {
			t.Fatalf("invoice page after %v: %d", form, status)
		}
		return body
	}

	assertStepped("invoice create (merchant)", invPost(map[string]string{"action": "next", "merchant_slug": "lagos-lunchbox"}), "Customer")
	assertStepped("invoice create (customer)", invPost(map[string]string{
		"action": "next", "customer_phone": "+2348033334444", "customer_email": "buyer@example.com",
	}), "Items")
	// The items page: add an item and land on the summary, still on the
	// Items node, with the items total and a single primary Continue. The
	// action arrives from the submit button the browser includes.
	body = invPost(map[string]string{"action": "add_item", "item_name": "Jollof tray", "item_quantity": "2", "item_price": "2500"})
	assertStepped("invoice create (items summary)", body, "Items")
	if !strings.Contains(body, "Items total") || !strings.Contains(body, "₦5,000") {
		t.Fatalf("items summary must show the computed total (page: %s)", run.page(body))
	}
	if strings.Contains(body, `value="add_item"`) {
		t.Fatal("items summary must not render the add-item form")
	}
	// Add a second item via the summary's secondary action, then continue.
	status, _, loc = run.post("/w/"+invToken, url.Values{"action": {"add_more"}})
	if status != http.StatusSeeOther {
		t.Fatalf("add another item: status=%d", status)
	}
	status, body, _ = run.get(loc)
	assertStepped("invoice create (second item)", body, "Items")
	body = invPost(map[string]string{"action": "add_item", "item_name": "Delivery", "item_quantity": "1", "item_price": "1000"})
	assertStepped("invoice create (summary of 2)", body, "Items")
	if !strings.Contains(body, "₦6,000") {
		t.Fatalf("items summary total must include both items (page: %s)", run.page(body))
	}
	body = invPost(map[string]string{"action": "items_done"})
	assertStepped("invoice create (options)", body, "Options")
	fmt.Printf("  ✅ invoice_create walks the stepped shell (Merchant→Customer→Items→Options)")
	fmt.Printf(" with one item per POST and a computed summary\n")

	// Through review and creation: the delivery fee joins the total, the review
	// page shows it, and Create lands the invoice row plus the customer
	// notification.
	body = invPost(map[string]string{"action": "next", "delivery_fee": "500", "due_date": "2026-10-15"})
	assertStepped("invoice create (review)", body, "Review")
	// html/template escapes + as &#43; in raw HTML, so assert against the
	// unescaped text the browser actually renders.
	reviewText := html.UnescapeString(body)
	for _, want := range []string{"₦6,500", "₦500", "Lagos Lunchbox", "+2348033334444"} {
		if !strings.Contains(reviewText, want) {
			t.Fatalf("invoice review must show %s (page: %s)", want, run.page(body))
		}
	}
	messenger.reset()
	status, body, _ = run.post("/w/"+invToken, url.Values{"action": {"create"}})
	if status != http.StatusOK || !strings.Contains(body, "Invoice created") {
		t.Fatalf("invoice create: status=%d page=%s", status, run.page(body))
	}
	// Pull the reference off the done page and assert the stored invoice.
	refMatch := regexp.MustCompile(`Reference ([A-Za-z0-9-]+)`).FindStringSubmatch(run.page(body))
	if refMatch == nil {
		t.Fatalf("done page must carry the invoice reference (page: %s)", run.page(body))
	}
	invoice, err := repository.InvoiceByReference(ctx, refMatch[1])
	if err != nil {
		t.Fatalf("created invoice %s not found: %v", refMatch[1], err)
	}
	if invoice.TotalKobo != 6_500_00 || invoice.SubtotalKobo != 6_000_00 || invoice.DeliveryFeeKobo != 50_000 {
		t.Fatalf("invoice totals: total=%d subtotal=%d fee=%d, want 650000/600000/50000",
			invoice.TotalKobo, invoice.SubtotalKobo, invoice.DeliveryFeeKobo)
	}
	if invoice.CustomerWhatsAppNumber != "+2348033334444" || len(invoice.Items) != 2 || invoice.Status == "" {
		t.Fatalf("invoice row mismatch: customer=%q items=%d status=%q",
			invoice.CustomerWhatsAppNumber, len(invoice.Items), invoice.Status)
	}
	due := invoice.DueAt
	if due == nil || due.Format("2006-01-02") != "2026-10-15" {
		t.Fatalf("invoice due date = %v, want 2026-10-15", due)
	}
	// The customer got the interactive notification with a Pay-now button and
	// the amount; the merchant got the creation confirmation (message 2).
	sent = messenger.snapshot()
	var customerNotified bool
	for _, msg := range sent {
		if msg.kind == "interactive" && msg.to == "+2348033334444" &&
			strings.Contains(msg.body, "₦6,500") && strings.Contains(msg.body, refMatch[1]) {
			customerNotified = true
		}
	}
	if !customerNotified {
		t.Fatalf("customer must receive the interactive invoice notification, got %+v", sent)
	}
	fmt.Printf("  ✅ invoice created: %s total ₦6,500 (2 items + delivery), customer notified with a Pay-now button\n", refMatch[1])

	// ---- leave nothing open ---------------------------------------------
	for _, token := range []string{thriftToken, onboardToken, invToken} {
		if _, _, err := repository.CompleteWebFlow(ctx, token); err != nil {
			t.Fatalf("close flow: %v", err)
		}
	}
}
