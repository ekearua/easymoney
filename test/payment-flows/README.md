# Payment-flow Playwright smoke tests

These tests prove the in-browser payment UX that the Go unit/integration tests can't
drive: the pay-review page rendering the wallet-state preview, pre-selecting a previously
chosen method after a reopened flow, and confirming the "two messages" contract for web-flow
payments (link + confirmation, with the generic "Xego payment update" outbox message
suppressed for ChannelCheckout payments).

## Prerequisites

- Node 18+ and npm.
- A running Xego app server (the `make run` / `go run ./cmd/...` binary) pointed at a real
  or demo database with at least one verified user that has an active wallet with balance, and
  at least one merchant with an active service.
- Playwright installed in this directory:

```bash
cd test/payment-flows
npm install
npx playwright install chromium
```

## Config

Copy `config.example.json` to `config.json` and fill in the values for your environment.
The file is `.gitignore`d so secrets never land in the repo.

## Run

```bash
cd test/payment-flows
npx playwright test
```

Run a single spec:

```bash
npx playwright test -g "pre-selects the previously chosen method"
```

Run with the UI visible (helpful when developing):

```bash
npx playwright test --ui
```

## What each spec asserts

- `pay-review-render.spec.ts` — the pay review page renders the merchant/item/amount review
  rows, the payment-method radio group with card + bank-transfer + wallet options, and the
  "Pay" / "Back" actions.
- `wallet-preview.spec.ts` — when the customer picks "Pay from wallet" on the review step and
  the wallet is not yet active (L0 / pending), the page shows an inline warning explaining that
  the wallet needs L1 verification before it can be used here. When the wallet is active but
  short of funds, the page shows the current balance and that it is less than the amount. When
  the wallet is active and sufficient, no preview warning is shown.
- `method-preselection.spec.ts` — after a hosted-card checkout is cancelled (the customer returns
  to the app on the `/checkout/...` return path with a non-succeeded payment), the pay flow is
  reopened at its review step and the previously chosen method is pre-selected in the radio group
  so the customer can switch method without re-navigating the form. The abandoned payment attempt
  is detached (its `payment_id` is cleared), so retrying creates a fresh draft.
- `two-message-contract.spec.ts` — a completed web-flow payment sends exactly two outbound messages
  (the flow link + the flow confirmation). The generic "Xego payment update" outbox status message
  is not sent for ChannelCheckout payments. This spec is informational when run against a real
  WhatsApp-connected server (it reads the app's message-log admin endpoint or the outbound message
  reporter); against the demo server without WhatsApp it skips.

## Notes

- These tests are *smoke* tests: they assert the rendered UX, not the full ledger. The ledger and
  payment correctness are covered by the Go integration suite (`go test ./internal/...` with
  `TEST_DATABASE_URL` set).
- The "cancel on hosted checkout" path depends on the Interswitch sandbox (or a mocked gateway).
  In the demo/no-secret configuration the hosted checkout page may be a stub; the spec still
  exercises the app-side reopen path by posting the return callback with a non-succeeded reference.
- Keep these tests focussed on UX regressions. Don't expand them into general ledger or gateway
  integration tests — that's the Go suite's job.
