# Xego Platform — Manual Testing Schedule

> **Generated:** 2026-09-16  
> **Companion spreadsheet:** `MANUAL_TEST_SCHEDULE.xlsx`  
> **Ordering:** Progressive tiered flow — each phase exhausts services at the current KYC tier before the next phase upgrades the customer.

---

## 1. Test Environment Setup & Prerequisites

The tester should stand up a **demo/staging** environment with the simulated rails enabled. Read `.env.example` for the full surface.

### 1.1 Provisioning commands

```powershell
go run ./cmd/demo hash-password      # generate ADMIN_PASSWORD_HASH (bcrypt)
go run ./cmd/demo random-data-key    # DATA_ENCRYPTION_KEY (64 hex)
go run ./cmd/demo random-totp-key    # TOTP_ENCRYPTION_KEY (64 hex)
go run ./cmd/demo migrate            # apply the 68 embedded migrations
go run ./cmd/demo seed               # baseline merchants/system fixtures
go run ./cmd/demo server             # run the HTTP server on :8080
```

Docker alternative: `docker compose up -d --build` (migrate one-shot -> app -> Caddy TLS).

### 1.2 Required environment (fresh setup)

| Variable | Demo value | Purpose |
|---|---|---|
| `APP_ENV` | `development` | Environment mode |
| `DATABASE_URL` | local Postgres DSN | System of record |
| `ALLOW_SIMULATED_RAILS_IN_PROD` | `true` (staging only) | Allows simulated payout/refund/data/identity rails |
| `INTERSWITCH_CHECKOUT_MODE` | `TEST` | Sandbox web checkout |
| `BANK_TRANSFER_MODE` | `simulate` | Bank-transfer rail |
| `PAYOUT_PROVIDER` / `REFUND_PROVIDER` | `simulated` | Payout/refund rails |
| `DATA_PROVIDER` | `simulated` (or `vtpass` sandbox) | Data fulfilment |
| `IDENTITY_PROVIDER` / `SCREENING_PROVIDER` | `simulated` | KYC identity/screening |
| `TOTP_ENABLED` | `true` | 2FA for admin/merchant login |
| `DATA_ENCRYPTION_KEY` / `TOTP_ENCRYPTION_KEY` | from `random-*` commands | At-rest encryption |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD_HASH` | from `hash-password` | Primary admin bootstrap |

Optional channels: `TELEGRAM_ENABLED=true` (+token), `INSTAGRAM_ENABLED=true`, `TIKTOK_ENABLED=true`, `SMS_ENABLED=true`, `REDIS_URL`, `EVENT_BUS` (`memory`/`kafka`).

### 1.3 Tools the tester needs

- A WhatsApp test account (or sandbox) + the app's configured WhatsApp number.
- Telegram bot + test account (Telegram channel).
- Instagram/TikTok business accounts (optional channels).
- Interswitch **sandbox** test card for hosted checkout.
- `curl`/Postman for the Partner API (HMAC-SHA256 signing).
- `ngrok` or a public tunnel if testing webhooks against a local server.
- Browser (admin/merchant consoles), Postgres client for ledger/integrity checks.

### 1.4 Partner API request shape (quick reference)

```text
POST /api/v1/payments
X-Xego-Key: xeg_sk_test_...
X-Xego-Timestamp: <unix seconds>
X-Xego-Signature: <lowercase hex HMAC-SHA256(key, METHOD\nPATH\nTIMESTAMP\nBODY)>

{"reference":"order-123","amount":{"value":50000,"currency":"NGN"},
 "customer":{"phone":"+2348012345678","email":"buyer@example.com"}}
```

Webhook deliveries carry `X-Xego-Event` (payment.succeeded/failed) and `X-Xego-Signature` = HMAC-SHA256(secret, body).

## 2. Demo Data Reference

Runs `go run ./cmd/demo seed` to create:

| Item | Value |
|---|---|
| Merchant: Lagos Lunchbox | Food - weekday meals, lunch packs, catering |
| Merchant: Kora Books | Books - books, stationery, reading accessories |
| Merchant: BrightFix NG | Services - home repair, maintenance, installation |
| System merchant | xego-individual-pay (Xego Individual Pay) |
| System merchant | xego-wallet-topup (Xego Wallet Top-up) |
| Demo admin user | +2348000000001 'Demo Merchant Admin' (owner of seeded merchants) |
| KYC tier limits | CBN ladder L0-L4 / B0-B3 with single/daily/monthly ceilings |

Demo invoice recipient numbers (allow-list): `+2347061975340`, `+2348033072780`.

## 3. Test Execution Guide

- Record **Result** and **Notes** in the Excel Execution Log sheet or below each case.
- Run phases in order (1 -> 15). Each phase lists its prerequisites.
- Use the Interswitch sandbox and simulated rails for card/transfer scenarios.
- P0 = critical path (block release if failing); P1 = important; P2 = edge/nice-to-have.
- The Excel supports multiple test cycles — filter by Cycle to track regression.

## 4. Test Cases by Phase

### Phase 1: System Setup & Health (5 cases)

- **Tier/Actor:** System
- **Prerequisites:** None
- **On completing this phase you unlock:** A working environment where customers can onboard and merchants can register.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-01-001 | Health | System | Web | Liveness endpoint responds | P0 |
| TC-01-002 | Health | System | Web | Readiness endpoint verifies database | P0 |
| TC-01-003 | Migrations | System | CLI | All database migrations apply cleanly (and idempotently) | P0 |
| TC-01-004 | Config / Boot | System | CLI | Server starts with a valid seeded environment | P0 |
| TC-01-005 | Seed Data | System | CLI | Baseline seed data is created | P1 |

<details>
<summary>Step-by-step details for Phase 1</summary>

#### TC-01-001 - Liveness endpoint responds

- **Module:** Health
- **Actor:** System   **Channel:** Web   **Priority:** P0
- **Preconditions:** Server running (go run ./cmd/demo server, or docker compose up -d).

**Steps**

1. Call GET https://<host>/health/live
2. Observe HTTP status and body.

**Expected Result:** HTTP 200 with an ok/healthy body. Liveness returns immediately and does not depend on the database.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-01-002 - Readiness endpoint verifies database

- **Module:** Health
- **Actor:** System   **Channel:** Web   **Priority:** P0
- **Preconditions:** Server running with DATABASE_URL configured.

**Steps**

1. Call GET https://<host>/health/ready
2. Record status while Postgres is up.
3. Stop PostgreSQL, call again.

**Expected Result:** HTTP 200 while the database is reachable; HTTP 503 (or error) once Postgres is down. Readiness reflects real DB connectivity.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-01-003 - All database migrations apply cleanly (and idempotently)

- **Module:** Migrations
- **Actor:** System   **Channel:** CLI   **Priority:** P0
- **Preconditions:** Fresh PostgreSQL instance; DATABASE_URL configured.

**Steps**

1. Run `go run ./cmd/demo migrate`.
2. Capture migration output.
3. Run `go run ./cmd/demo migrate` again.

**Expected Result:** All ~68 embedded migrations apply in filename order with no errors. A second run is a no-op (no duplicate application).

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-01-004 - Server starts with a valid seeded environment

- **Module:** Config / Boot
- **Actor:** System   **Channel:** CLI   **Priority:** P0
- **Preconditions:** .env configured with simulated rails and INTERSWITCH_CHECKOUT_MODE=TEST.

**Steps**

1. Load .env.
2. Run `go run ./cmd/demo migrate` then `go run ./cmd/demo seed`.
3. Run `go run ./cmd/demo server`.
4. Watch startup logs.

**Expected Result:** Server binds on :8080, background workers start (message drain, webhook delivery, reconciliation, payment expiry, transaction monitoring, retention), admin account is re-synced from ADMIN_EMAIL/ADMIN_PASSWORD_HASH, no fatal startup errors.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-01-005 - Baseline seed data is created

- **Module:** Seed Data
- **Actor:** System   **Channel:** CLI   **Priority:** P1
- **Preconditions:** Migrated database; `go run ./cmd/demo seed` run.

**Steps**

1. Run `go run ./cmd/demo seed`.
2. Check the merchant list in chat or via SQL.
3. Confirm system merchants exist.

**Expected Result:** Seeded merchants exist: Lagos Lunchbox (Food), Kora Books (Books), BrightFix NG (Services), plus system merchants xego-individual-pay and xego-wallet-topup. Demo user +2348000000001 owns the seeded merchants.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 2: Customer Onboarding (All Channels) (10 cases)

- **Tier/Actor:** L0
- **Prerequisites:** Phase 1
- **On completing this phase you unlock:** Basic card and bank-transfer payments with no KYC required.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-02-001 | Onboarding | Individual | WhatsApp | WhatsApp onboarding — display name entry | P0 |
| TC-02-002 | Onboarding | Individual | WhatsApp | WhatsApp onboarding — email entry + validation | P0 |
| TC-02-003 | Onboarding | Individual | WhatsApp | WhatsApp number confirmation | P0 |
| TC-02-004 | Onboarding | Individual | Telegram | Telegram onboarding via /start | P0 |
| TC-02-005 | Onboarding | Individual | Instagram | Instagram Messenger onboarding with quick replies | P1 |
| TC-02-006 | Onboarding | Individual | TikTok | TikTok DM onboarding (numbered text menu fallback) | P2 |
| TC-02-007 | Onboarding | Individual | WhatsApp | Re-onboarding / existing user resumes menu | P1 |
| TC-02-008 | Menu | Individual | WhatsApp | Main menu navigation | P0 |
| TC-02-009 | Menu | Individual | WhatsApp | Interactive button / list reply handling | P1 |
| TC-02-010 | Onboarding | Individual | SMS | SMS channel only accepts data commands | P2 |
| TC-02-011 | Onboarding | Individual | Telegram | Approved individual on a fresh channel gets intro + menu (no re-onboarding) | P1 |
| TC-02-012 | Onboarding | Individual | Telegram | Unapproved user on a fresh channel still gets account confirmation | P1 |

<details>
<summary>Step-by-step details for Phase 2</summary>

#### TC-02-001 - WhatsApp onboarding — display name entry

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** WhatsApp webhook URL and Meta callback configured; first-time number; server running.

**Steps**

1. From a fresh WhatsApp number, send the configured WhatsApp number a first message (e.g. 'Hi').
2. Observe the reply.

**Expected Result:** Bot greets and asks for the customer's display name. The conversation enters the onboarding state.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-002 - WhatsApp onboarding — email entry + validation

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarding started.

**Steps**

1. Reply with a name (e.g. 'Ada Obi').
2. Observe email prompt.
3. Reply 'not-an-email' first, then a valid email.

**Expected Result:** Acknowledges the name, asks for email. Invalid email is rejected with a retry prompt; a valid email is accepted and the bot asks to confirm the WhatsApp number.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-003 - WhatsApp number confirmation

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarding in progress at the number-confirmation step.

**Steps**

1. Confirm the WhatsApp number when asked.
2. Observe confirmation message.
3. Verify the user record in /admin/users.

**Expected Result:** Number confirms, user reaches the main menu. Receipts/invoices are usable. number_confirmed_at is set (PII masked in admin view).

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-004 - Telegram onboarding via /start

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** Telegram   **Priority:** P0
- **Preconditions:** TELEGRAM_ENABLED=true; bot token + webhook configured.

**Steps**

1. Open the Telegram bot and send /start.
2. Complete name, email, and confirmation prompts.

**Expected Result:** Same onboarding flow runs; completion lands on the main menu; Telegram identity is confirmed natively.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-005 - Instagram Messenger onboarding with quick replies

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** Instagram   **Priority:** P1
- **Preconditions:** INSTAGRAM_ENABLED=true; Meta app/page + webhook verified.

**Steps**

1. Message the linked Instagram business account from a fresh user.
2. Complete name/email flow using quick-reply buttons.

**Expected Result:** Onboarding proceeds over Instagram DMs with quick-reply options; completion reaches the main menu; webhook signature is verified server-side.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-006 - TikTok DM onboarding (numbered text menu fallback)

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** TikTok   **Priority:** P2
- **Preconditions:** TIKTOK_ENABLED=true; TikTok app/secret/access token configured.

**Steps**

1. DM the TikTok business account.
2. Confirm interactive primitives degrade to numbered text menus / plain links.
3. Choose options by typing numbers.

**Expected Result:** Onboarding works with plain numbered text menus and links (no interactive buttons); chat FSM only, no web flows on TikTok.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-007 - Re-onboarding / existing user resumes menu

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** A user that already completed onboarding.

**Steps**

1. Send 'Hi' or the menu keyword from an already-onboarded number.
2. Observe reply.

**Expected Result:** Existing user jumps straight to the main menu; onboarding is not repeated for confirmed numbers.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-008 - Main menu navigation

- **Module:** Menu
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarded customer.

**Steps**

1. Send the main menu keyword.
2. Confirm all options are present.
3. Select each option and confirm navigation.

**Expected Result:** Menu lists payment, invoice, thrift, data, wallet, individual-pay, and register-merchant options as applicable; each opens the correct sub-flow.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-009 - Interactive button / list reply handling

- **Module:** Menu
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** Onboarded customer; WhatsApp interactive messages available.

**Steps**

1. When presented with interactive buttons or a list, tap an option instead of typing.
2. Observe the flow.

**Expected Result:** Button/list selections are parsed like typed intents and the flow advances without further prompting.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-010 - SMS channel only accepts data commands

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** SMS   **Priority:** P2
- **Preconditions:** SMS_ENABLED=true; shared secret configured.

**Steps**

1. POST an onboarding-like message to /webhooks/sms with the shared secret.
2. Observe response.

**Expected Result:** SMS only supports data-order commands (DATA/PLANS/STATUS); onboarding or payment text is answered with a help/unsupported reply.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-011 - Approved individual on a fresh channel gets intro + menu (no re-onboarding)

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** Telegram   **Priority:** P1
- **Preconditions:** An approved individual (account level `individual`, KYC ≥ L2) already onboarded on WhatsApp; global identity fully cleared; Telegram not yet stamped (`telegram_confirmed_at` NULL).

**Steps**

1. From the approved user's Telegram account, send any plain text (e.g. "hi").
2. Observe the reply.
3. Send a second message and observe.

**Expected Result:** First message returns a one-time non-blocking intro ("It's the same you — your Telegram is now linked...") followed by the main menu. No "Confirm this account" onboarding gate appears. The intro fires exactly once: the second message goes straight to normal menu handling. The user's `verification_level`, `account_level`, and KYC tier are unchanged.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-02-012 - Unapproved user on a fresh channel still gets account confirmation

- **Module:** Onboarding
- **Actor:** Individual   **Channel:** Telegram   **Priority:** P1
- **Preconditions:** A confirmed WhatsApp user who has NOT reached the approved global tier (account level still `customer`, or KYC < L2); Telegram not yet stamped.

**Steps**

1. From the unapproved user's Telegram account, send any plain text (e.g. "hi").
2. Observe the reply.

**Expected Result:** Per-channel onboarding still runs exactly as before: the user is asked to confirm the Telegram account ("Confirm this account?"). The approved-user intro is not shown.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 3: L0 — Card Checkout (10 cases)

- **Tier/Actor:** L0
- **Prerequisites:** Phase 2
- **On completing this phase you unlock:** Bank transfer as an alternative payment method.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-03-001 | Payment | Individual | WhatsApp | Make payment — merchant selection + search | P0 |
| TC-03-002 | Payment | Individual | WhatsApp | Make payment — valid amount accepted | P0 |
| TC-03-003 | Payment | Individual | WhatsApp | Make payment — amount validation | P1 |
| TC-03-004 | Card Checkout | Individual | Web | Card checkout — open secure checkout | P0 |
| TC-03-005 | Card Checkout | Individual | Web | Interswitch hosted-fields page renders | P0 |
| TC-03-006 | Card Checkout | Individual | Web | Card payment — success end-to-end (requery-driven) | P0 |
| TC-03-007 | Card Checkout | Individual | Web | Card payment — failure / cancellation handling | P1 |
| TC-03-008 | Card Checkout | Individual | Web | /payments/return does not confirm alone | P1 |
| TC-03-009 | Receipt | Individual | Web | Payment receipt page + QR code | P1 |
| TC-03-010 | Idempotency | System | Webhook | Webhook replay does not duplicate payment | P0 |

<details>
<summary>Step-by-step details for Phase 3</summary>

#### TC-03-001 - Make payment — merchant selection + search

- **Module:** Payment
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarded customer; at least one active seeded merchant.

**Steps**

1. Choose Make payment from the menu.
2. Observe merchant list.
3. Select 'Lagos Lunchbox'.
4. Repeat with a typed search term (name or category).

**Expected Result:** Merchant list renders with recently selected merchants first; selection moves to amount entry; typed search filters by name/category.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-03-002 - Make payment — valid amount accepted

- **Module:** Payment
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Merchant selected.

**Steps**

1. Enter amount 5000 (NGN 5,000).
2. Observe the payment summary prompt.

**Expected Result:** Amount between NGN 100 and NGN 100,000 is accepted. Summary shows merchant and amount in naira.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-03-003 - Make payment — amount validation

- **Module:** Payment
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** Merchant selected.

**Steps**

1. Enter 50 (below NGN 100).
2. Enter 200000 (above NGN 100,000).
3. Enter non-numeric text.

**Expected Result:** Out-of-range amounts are rejected with a clear message and a re-entry prompt; non-numeric input handled gracefully without crashing the session.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-03-004 - Card checkout — open secure checkout

- **Module:** Card Checkout
- **Actor:** Individual   **Channel:** Web   **Priority:** P0
- **Preconditions:** Payment draft created and amount confirmed.

**Steps**

1. Choose 'Card checkout'.
2. Confirm the payment summary.
3. Open the secure checkout link.

**Expected Result:** Summary confirms merchant, amount, and fee. Secure checkout loads at /checkout/{token}. Card data is never collected in chat.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-03-005 - Interswitch hosted-fields page renders

- **Module:** Card Checkout
- **Actor:** Individual   **Channel:** Web   **Priority:** P0
- **Preconditions:** INTERSWITCH_CHECKOUT_MODE=TEST with valid sandbox credentials.

**Steps**

1. Open the hosted checkout.
2. Verify the Interswitch hosted page renders.

**Expected Result:** Interswitch (sandbox) hosted-fields page renders with a valid pay item and no Z4/Z5 rejection; card fields do not touch the Xego server.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-03-006 - Card payment — success end-to-end (requery-driven)

- **Module:** Card Checkout
- **Actor:** Individual   **Channel:** Web   **Priority:** P0
- **Preconditions:** Interswitch sandbox test card; TEST gateway.

**Steps**

1. Complete card entry on the Interswitch sandbox.
2. Return via /payments/return.
3. Wait for the webhook + server-side requery.
4. Check chat for the final result.

**Expected Result:** Payment reaches 'succeeded' only after the authoritative server-side requery confirms reference/amount/currency/channel. Chat reports success; receipt URL shows the same status; double-entry ledger rows are posted.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-03-007 - Card payment — failure / cancellation handling

- **Module:** Card Checkout
- **Actor:** Individual   **Channel:** Web   **Priority:** P1
- **Preconditions:** Payment draft created.

**Steps**

1. Open secure checkout.
2. Cancel, or use a failing test card.
3. Observe the return page and chat.

**Expected Result:** Payment ends in a failed/abandoned state rather than a successful one. Reopening the flow creates a fresh draft; the abandoned attempt is detached from the user session.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-03-008 - /payments/return does not confirm alone

- **Module:** Card Checkout
- **Actor:** Individual   **Channel:** Web   **Priority:** P1
- **Preconditions:** A checkout attempt was made.

**Steps**

1. Complete or cancel a card checkout.
2. Note the behaviour of GET /payments/return.

**Expected Result:** The return page reflects the checkout outcome but never marks the payment successful by itself — confirmation requires the terminal webhook + requery.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-03-009 - Payment receipt page + QR code

- **Module:** Receipt
- **Actor:** Individual   **Channel:** Web   **Priority:** P1
- **Preconditions:** At least one succeeded payment.

**Steps**

1. Open the receipt URL sent to chat.
2. Verify status, provider, amount.
3. Load /receipts/{token}/scan-qr.png.

**Expected Result:** Receipt renders the matching status/provider/amount. The QR PNG downloads and a merchant scanner resolves it to the receipt. Receipt URL is an unguessable bearer token.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-03-010 - Webhook replay does not duplicate payment

- **Module:** Idempotency
- **Actor:** System   **Channel:** Webhook   **Priority:** P0
- **Preconditions:** A succeeded payment and the ability to replay a signed TRANSACTION.COMPLETED webhook.

**Steps**

1. Re-fire a signed completed webhook for the same reference.
2. Verify payment count and ledger rows.

**Expected Result:** Replay creates no second payment, no duplicate ledger posting, and no duplicate merchant notification. Confirmation is idempotent by reference.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 4: L0 — Bank Transfer (8 cases)

- **Tier/Actor:** L0
- **Prerequisites:** Phases 2-3
- **On completing this phase you unlock:** Mobile data purchase — also L0, no KYC needed.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-04-001 | Bank Transfer | Individual | WhatsApp | Bank transfer — fee display | P0 |
| TC-04-002 | Bank Transfer | Individual | Web | Bank transfer — secure instructions / transfer page | P0 |
| TC-04-003 | Bank Transfer | Individual | Web | Bank transfer — completion with server-side verification | P0 |
| TC-04-004 | Bank Transfer | Individual | Web | Bank transfer — simulated failure and retry | P1 |
| TC-04-005 | Bank Transfer | Individual | System | Expired transfer session closed by expiry worker | P2 |
| TC-04-006 | Bank Transfer | Individual | Web | Method pre-selection on a reopened flow | P1 |
| TC-04-007 | Bank Transfer | Individual | WhatsApp | Transfer confirmation message (two-message contract) | P1 |
| TC-04-008 | Chat Guard | Individual | WhatsApp | No card data accepted in chat | P0 |

<details>
<summary>Step-by-step details for Phase 4</summary>

#### TC-04-001 - Bank transfer — fee display

- **Module:** Bank Transfer
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Payment draft created.

**Steps**

1. Choose 'Bank transfer'.
2. Observe the summary before continuing.

**Expected Result:** Summary shows the bank-transfer fee (1.8% capped at NGN 2,500) alongside the amount; user reviews before continuing.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-04-002 - Bank transfer — secure instructions / transfer page

- **Module:** Bank Transfer
- **Actor:** Individual   **Channel:** Web   **Priority:** P0
- **Preconditions:** Bank transfer selected and confirmed.

**Steps**

1. Continue through the transfer flow.
2. Open /transfer/{reference} or continue to the Interswitch checkout.

**Expected Result:** A transfer/instructions surface conveys the transfer details and the Interswitch checkout path; no sensitive data is shown in chat.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-04-003 - Bank transfer — completion with server-side verification

- **Module:** Bank Transfer
- **Actor:** Individual   **Channel:** Web   **Priority:** P0
- **Preconditions:** BANK_TRANSFER_MODE=simulate (or sandbox Interswitch).

**Steps**

1. Complete the transfer on the checkout.
2. Trigger/simulate the verified webhook.
3. Confirm chat result and receipt.

**Expected Result:** Xego verifies the transfer result server-side before success. Chat reports success with matching receipt; payment resolves to 'succeeded'.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-04-004 - Bank transfer — simulated failure and retry

- **Module:** Bank Transfer
- **Actor:** Individual   **Channel:** Web   **Priority:** P1
- **Preconditions:** Ability to simulate a failing transfer or use a failing test scenario.

**Steps**

1. Cause the transfer to fail.
2. Observe payment state.
3. Retry the payment.

**Expected Result:** Payment fails cleanly (not stuck). Retry creates a fresh draft without double-booking; reconciliation reports no false success.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-04-005 - Expired transfer session closed by expiry worker

- **Module:** Bank Transfer
- **Actor:** Individual   **Channel:** System   **Priority:** P2
- **Preconditions:** SESSION_TTL configured; a draft created and left idle.

**Steps**

1. Create a payment draft.
2. Leave it beyond the session TTL.
3. Wait for the expiry worker.
4. Observe state.

**Expected Result:** Overdue sessions/payments transition to an expired/failed state; the customer is not charged and can recreate the flow.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-04-006 - Method pre-selection on a reopened flow

- **Module:** Bank Transfer
- **Actor:** Individual   **Channel:** Web   **Priority:** P1
- **Preconditions:** Previously cancelled a hosted-card checkout (non-succeeded).

**Steps**

1. Reopen the pay flow after the cancelled hosted-card attempt.
2. Note the pre-selected method.
3. Switch to bank transfer.

**Expected Result:** The review step pre-selects the previously chosen method; switching to bank transfer works without re-navigating the form.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-04-007 - Transfer confirmation message (two-message contract)

- **Module:** Bank Transfer
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** A bank-transfer payment succeeded.

**Steps**

1. After the transfer completes successfully, read the final chat message.

**Expected Result:** Chat reports the final result (success + receipt URL). ChannelCheckout payments send exactly two messages (flow link + confirmation) with no generic update.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-04-008 - No card data accepted in chat

- **Module:** Chat Guard
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarded customer.

**Steps**

1. Type a card number, CVV, PIN, or OTP into chat mid-flow.
2. Observe the response.

**Expected Result:** Chat guard blocks or blanks card/PIN/CVV/OTP-like input with a warning; no sensitive data is stored in chat payloads at rest.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 5: L0 — Mobile Data Purchase (8 cases)

- **Tier/Actor:** L0
- **Prerequisites:** Phase 2
- **On completing this phase you unlock:** Wallet top-up (wallet opens at L0 as pending) and the L0→L1 KYC upgrade that activates wallet payments.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-05-001 | Data | Individual | WhatsApp | Browse data plans with search and paging | P0 |
| TC-05-002 | Data | Individual | SMS | PLANS SMS command | P1 |
| TC-05-003 | Data | Individual | SMS | DATA SMS command | P1 |
| TC-05-004 | Data | Individual | SMS | STATUS + DATA HELP SMS commands | P2 |
| TC-05-005 | Data | Individual | WhatsApp | Data order payment completes | P0 |
| TC-05-006 | Data | Individual | WhatsApp | Beneficiary phone validation | P1 |
| TC-05-007 | Data | System | Webhook | VTPass webhook marks order fulfilled/failed | P1 |
| TC-05-008 | Data | System | Webhook | VTPass callback with bad secret rejected | P1 |

<details>
<summary>Step-by-step details for Phase 5</summary>

#### TC-05-001 - Browse data plans with search and paging

- **Module:** Data
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarded customer; data plans synced/available.

**Steps**

1. Choose 'Buy Data'.
2. Select a network (MTN/Airtel/Glo/9mobile).
3. Browse plan pages and/or type a search term like '1GB' or 'monthly'.

**Expected Result:** Plans render as paged lists; search filters by size/validity; selection moves to beneficiary entry.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-05-002 - PLANS SMS command

- **Module:** Data
- **Actor:** Individual   **Channel:** SMS   **Priority:** P1
- **Preconditions:** SMS_ENABLED=true; shared secret known.

**Steps**

1. POST a form/JSON payload to /webhooks/sms: body `PLANS MTN`.
2. Observe the response.

**Expected Result:** Reply lists MTN plans with paging/search guidance; correct shared secret required.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-05-003 - DATA SMS command

- **Module:** Data
- **Actor:** Individual   **Channel:** SMS   **Priority:** P1
- **Preconditions:** SMS_ENABLED=true.

**Steps**

1. POST body `DATA MTN MTN1GB 08031234567` to /webhooks/sms.

**Expected Result:** Reply contains a data request code (XG-DATA-...) and a checkout URL to pay for the order.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-05-004 - STATUS + DATA HELP SMS commands

- **Module:** Data
- **Actor:** Individual   **Channel:** SMS   **Priority:** P2
- **Preconditions:** An existing data order reference.

**Steps**

1. POST `STATUS XG-DATA-8K2Q`.
2. POST `DATA HELP`.

**Expected Result:** STATUS returns order state; DATA HELP returns command syntax help.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-05-005 - Data order payment completes

- **Module:** Data
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Beneficiary phone entered; order draft created.

**Steps**

1. Pay via card checkout or bank transfer.
2. Confirm the order state after success.

**Expected Result:** Data order transitions to fulfilled after payment success (simulated or VTPass). Receipt issued.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-05-006 - Beneficiary phone validation

- **Module:** Data
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** Data flow started.

**Steps**

1. Enter an invalid phone number.
2. Enter a valid 11-digit Nigerian number.

**Expected Result:** Invalid numbers are rejected; valid number accepted and used as beneficiary.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-05-007 - VTPass webhook marks order fulfilled/failed

- **Module:** Data
- **Actor:** System   **Channel:** Webhook   **Priority:** P1
- **Preconditions:** DATA_PROVIDER=vtpass (sandbox) and VTPASS_WEBHOOK_SECRET set.

**Steps**

1. Send a signed callback for a pending order.
2. Observe order state.

**Expected Result:** Secret validated (header only); callback flips the pending order to fulfilled/failed by provider reference; recorded in /admin/webhooks.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-05-008 - VTPass callback with bad secret rejected

- **Module:** Data
- **Actor:** System   **Channel:** Webhook   **Priority:** P1
- **Preconditions:** VTPASS_WEBHOOK_SECRET configured.

**Steps**

1. POST a VTPass callback with a wrong secret.
2. Observe response.

**Expected Result:** Callback rejected, not recorded as delivered.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 6: L0→L1 — Wallet & KYC Upgrade (9 cases)

- **Tier/Actor:** L0→L1
- **Prerequisites:** Phases 2-5
- **On completing this phase you unlock:** Invoice payments and the merchant invoicing workflow at L1.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-06-001 | Wallet | Individual | WhatsApp | Fund wallet flow opens | P0 |
| TC-06-002 | Wallet | Individual | Web | Wallet top-up via card checkout completes | P0 |
| TC-06-003 | Wallet | Individual | WhatsApp | Wallet balance check | P1 |
| TC-06-004 | Wallet | Individual | Web | Pay from wallet — L1 gate blocks L0 users | P0 |
| TC-06-005 | KYC | Individual | WhatsApp | KYC L0→L1 advancement (channel confirmed evidence) | P0 |
| TC-06-006 | Wallet | Individual | Web | Pay from wallet — L1 user succeeds | P0 |
| TC-06-007 | Wallet | Individual | Web | Insufficient wallet balance handling | P1 |
| TC-06-008 | Wallet | Admin | CLI | Wallet CLI commands (wallet-balance / wallet-withdraw) | P2 |
| TC-06-009 | Wallet | Admin | Console | Wallet ledger validation | P1 |

<details>
<summary>Step-by-step details for Phase 6</summary>

#### TC-06-001 - Fund wallet flow opens

- **Module:** Wallet
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarded customer at L0.

**Steps**

1. Choose 'Fund wallet' / 'Top up'.
2. Enter an amount.

**Expected Result:** A payment is minted against the xego-wallet-topup system merchant. Wallet opens as pending at L0.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-06-002 - Wallet top-up via card checkout completes

- **Module:** Wallet
- **Actor:** Individual   **Channel:** Web   **Priority:** P0
- **Preconditions:** Fund-wallet draft created.

**Steps**

1. Pay the top-up via card checkout on the sandbox.
2. After success, check wallet balance.

**Expected Result:** Top-up success credits the customer's wallet (replay-safe via journal ref). Balance increases. Wallet status remains 'pending' at L0.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-06-003 - Wallet balance check

- **Module:** Wallet
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** A customer with a wallet.

**Steps**

1. Ask the bot for wallet balance.

**Expected Result:** Current wallet balance is returned (derived from the ledger).

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-06-004 - Pay from wallet — L1 gate blocks L0 users

- **Module:** Wallet
- **Actor:** Individual   **Channel:** Web   **Priority:** P0
- **Preconditions:** Wallet at L0 / pending (before KYC upgrade).

**Steps**

1. Select 'Pay from wallet' at the review step.
2. Observe the inline warning.

**Expected Result:** Page warns that the wallet needs L1 verification before wallet payment; card/transfer methods still available. The system message: 'Wallet payments need an active wallet. Confirm your account to reach level L1 and activate your wallet.'

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-06-005 - KYC L0→L1 advancement (channel confirmed evidence)

- **Module:** KYC
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarded customer with confirmed WhatsApp number and email.

**Steps**

1. The customer has confirmed their WhatsApp number and provided email during onboarding.
2. Open /admin/kyc and verify the customer.
3. Advance the customer from L0 to L1 with EvChannelConfirmed evidence.

**Expected Result:** Customer advances to L1. Wallet transitions from 'pending' to 'active'. Allowance limits increase to L1 ceilings (single ₦5M, daily ₦5M, monthly ₦30M). Admin audit log records the advancement.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-06-006 - Pay from wallet — L1 user succeeds

- **Module:** Wallet
- **Actor:** Individual   **Channel:** Web   **Priority:** P0
- **Preconditions:** Customer at L1 with active wallet and sufficient balance.

**Steps**

1. Choose a merchant and amount.
2. Select 'Pay from wallet' at the review step.
3. Confirm.

**Expected Result:** Payment debits the wallet atomically with the payment transition; wallet and ledger balances both update; receipt issued in chat.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-06-007 - Insufficient wallet balance handling

- **Module:** Wallet
- **Actor:** Individual   **Channel:** Web   **Priority:** P1
- **Preconditions:** Wallet active but balance < payment amount.

**Steps**

1. Select 'Pay from wallet' for an amount above the balance.

**Expected Result:** Page shows current balance and that it is less than the amount; prevents wallet payment and suggests funding or another method.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-06-008 - Wallet CLI commands (wallet-balance / wallet-withdraw)

- **Module:** Wallet
- **Actor:** Admin   **Channel:** CLI   **Priority:** P2
- **Preconditions:** A wallet user exists with balance.

**Steps**

1. Run `go run ./cmd/demo wallet-balance <phone>`.
2. Run `go run ./cmd/demo wallet-withdraw <phone> <amount>`.

**Expected Result:** wallet-balance prints the ledger-derived balance; wallet-withdraw debits it and posts matching ledger entries.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-06-009 - Wallet ledger validation

- **Module:** Wallet
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Recent wallet top-ups/payments exist.

**Steps**

1. Open /admin/ledger and /admin/reconciliation.
2. Verify wallet debits/credits balance.

**Expected Result:** User wallet account entries balance against payment/refund journal references; reconciliation reports match.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 7: L1 — Invoices (12 cases)

- **Tier/Actor:** L1
- **Prerequisites:** Phase 6
- **On completing this phase you unlock:** Becoming an approved individual (KYC identity) and thrift group savings.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-07-001 | Invoices | Merchant | WhatsApp | Invoice requires merchant registration | P0 |
| TC-07-002 | Invoices | Merchant | WhatsApp | Invoice creation — line items and delivery fee | P0 |
| TC-07-003 | Invoices | Customer | Web | Invoice public page renders | P0 |
| TC-07-004 | Invoices | Customer | WhatsApp | Pay invoice via PAY command | P0 |
| TC-07-005 | Invoices | Customer | Web | Invoice full payment | P0 |
| TC-07-006 | Invoices | Customer | Web | Invoice partial payment | P1 |
| TC-07-007 | Invoices | Customer | Web | Invoice split payment (multiple payers) | P1 |
| TC-07-008 | Invoices | Merchant | API | Invoice via Partner API — line items + delivery fee | P1 |
| TC-07-009 | Invoices | Customer | Web | Invoice pay link (wa.me) opens correct URL | P1 |
| TC-07-010 | Invoices | Customer | Web | Invoice payment via card checkout | P1 |
| TC-07-011 | Invoices | Customer | Web | Invoice payment via bank transfer | P1 |
| TC-07-012 | Invoices | Merchant | Console | Merchant invoice list reflects payments | P2 |

<details>
<summary>Step-by-step details for Phase 7</summary>

#### TC-07-001 - Invoice requires merchant registration

- **Module:** Invoices
- **Actor:** Merchant   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Admin has approved a merchant registration (or use a seeded merchant owner).

**Steps**

1. As an approved merchant user, choose 'Generate invoice'.
2. As a non-merchant, attempt the same.

**Expected Result:** Only merchant-owning users can generate invoices; others are told to register/approve first.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-002 - Invoice creation — line items and delivery fee

- **Module:** Invoices
- **Actor:** Merchant   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Approved merchant user.

**Steps**

1. Add customer email (an allowed demo number: +2347061975340 or +2348033072780).
2. Add one or more line items with quantity and unit price.
3. Add a delivery fee.
4. Submit.

**Expected Result:** Invoice is created with status, amount total = sum(line items) + delivery fee. Reference format XG-INV-... returned.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-003 - Invoice public page renders

- **Module:** Invoices
- **Actor:** Customer   **Channel:** Web   **Priority:** P0
- **Preconditions:** An invoice reference exists.

**Steps**

1. Open GET /invoices/{reference}.
2. Verify line items, totals, amount_paid, and pay link.

**Expected Result:** Page shows all line items, delivery fee, total, current amount_paid, and the wa.me pay link.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-004 - Pay invoice via PAY command

- **Module:** Invoices
- **Actor:** Customer   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Invoice created; customer chats with the bot.

**Steps**

1. From the customer chat send `PAY XG-INV-...`.
2. Choose full or partial amount.
3. Choose a payment method.

**Expected Result:** The invoice resolves from the reference; payment methods (card checkout / bank transfer) proceed; amount_paid updates after success.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-005 - Invoice full payment

- **Module:** Invoices
- **Actor:** Customer   **Channel:** Web   **Priority:** P0
- **Preconditions:** Invoice exists with no payments.

**Steps**

1. Pay the invoice in full (card or transfer).
2. Check invoice state.

**Expected Result:** amount_paid == total; invoice status becomes paid/fulfilled. Each contribution gets its own receipt.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-006 - Invoice partial payment

- **Module:** Invoices
- **Actor:** Customer   **Channel:** Web   **Priority:** P1
- **Preconditions:** Invoice with total > NGN 100.

**Steps**

1. Pay an amount below the total.
2. Check invoice state.

**Expected Result:** Invoice remains partially paid; status reflects partial; amount_paid increases; the balance is still payable.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-007 - Invoice split payment (multiple payers)

- **Module:** Invoices
- **Actor:** Customer   **Channel:** Web   **Priority:** P1
- **Preconditions:** Invoice exists.

**Steps**

1. Pay part of the invoice from one customer.
2. Pay the remainder from another.
3. Confirm when total collected equals total.

**Expected Result:** Multiple contributions accumulate; invoice flips to paid only when total collected equals the invoice total.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-008 - Invoice via Partner API — line items + delivery fee

- **Module:** Invoices
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** Merchant API key minted; HMAC signing available.

**Steps**

1. POST /api/v1/invoices with items and an optional delivery_fee.
2. Observe response.

**Expected Result:** Invoice created idempotently; response includes status, amount, amount_paid, items, and wa.me pay_link.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-009 - Invoice pay link (wa.me) opens correct URL

- **Module:** Invoices
- **Actor:** Customer   **Channel:** Web   **Priority:** P1
- **Preconditions:** An invoice exists.

**Steps**

1. Tap the wa.me pay link from the invoice page or API response.

**Expected Result:** Link opens WhatsApp with a prefilled command that resolves to the invoice (PAY XG-INV-...) and continues the flow.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-010 - Invoice payment via card checkout

- **Module:** Invoices
- **Actor:** Customer   **Channel:** Web   **Priority:** P1
- **Preconditions:** Invoice with remaining balance.

**Steps**

1. Choose card checkout to pay the invoice.
2. Complete on Interswitch sandbox.

**Expected Result:** Invoice balance reduces; success reflected in chat/receipt; no overpayment accepted.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-011 - Invoice payment via bank transfer

- **Module:** Invoices
- **Actor:** Customer   **Channel:** Web   **Priority:** P1
- **Preconditions:** Invoice with remaining balance.

**Steps**

1. Choose bank transfer for the invoice.
2. Complete on the transfer rail.

**Expected Result:** Transfer completes with server-side verification; invoice amount_paid increases; chat reports success + receipt.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-07-012 - Merchant invoice list reflects payments

- **Module:** Invoices
- **Actor:** Merchant   **Channel:** Console   **Priority:** P2
- **Preconditions:** Merchant has invoices in various payment states.

**Steps**

1. Open /merchant/invoices.
2. Verify references, statuses, amounts, amount_paid.

**Expected Result:** Invoices list shows current status and amount_paid; states update after new contributions.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 8: L1+ — Thrift Group Savings (10 cases)

- **Tier/Actor:** L1+
- **Prerequisites:** Phase 6
- **On completing this phase you unlock:** KYC advancement to L2 and customer-to-customer individual pay.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-08-001 | Thrift | Individual | WhatsApp | Become individual — KYC identity for thrift | P0 |
| TC-08-002 | Thrift | Individual | WhatsApp | Create thrift group | P0 |
| TC-08-003 | Thrift | Individual | WhatsApp | Share thrift invite code | P1 |
| TC-08-004 | Thrift | Individual | WhatsApp | Join thrift group with JOIN command | P0 |
| TC-08-005 | Thrift | Individual | WhatsApp | Thrift member-count validation (2-12) | P1 |
| TC-08-006 | Thrift | Individual | WhatsApp | Activate thrift with payout rotation order | P0 |
| TC-08-007 | Thrift | Individual | WhatsApp | Contribute to thrift cycle | P0 |
| TC-08-008 | Thrift | Customer | Web | Thrift group public page | P2 |
| TC-08-009 | Thrift | Admin | Console | Admin marks thrift payout completed | P1 |
| TC-08-010 | Thrift | Admin | Console | Thrift cycles continue to completion | P2 |

<details>
<summary>Step-by-step details for Phase 8</summary>

#### TC-08-001 - Become individual — KYC identity for thrift

- **Module:** Thrift
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarded customer at L1.

**Steps**

1. Choose 'Become individual'.
2. Complete the email OTP.
3. Enter legal name, DOB, address, occupation.
4. Observe KYC result.

**Expected Result:** KYC marked approved_simulated; the customer can now create/join thrift groups. Admin can review the case in /admin/kyc.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-08-002 - Create thrift group

- **Module:** Thrift
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** KYC approved (Become individual completed).

**Steps**

1. Choose 'Create thrift'.
2. Enter group name, fixed contribution amount, weekly/monthly frequency, and 2-12 target members.

**Expected Result:** Group is created with an invite code (XG-THRIFT-...). Creator can share the invite code.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-08-003 - Share thrift invite code

- **Module:** Thrift
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** Thrift group created.

**Steps**

1. Share the generated invite code with prospective members.

**Expected Result:** Invite code is in the expected format and joinable by other KYC-approved users.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-08-004 - Join thrift group with JOIN command

- **Module:** Thrift
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** A thrift group exists; joining member is KYC approved.

**Steps**

1. Send `JOIN XG-THRIFT-...`.
2. Confirm the join.

**Expected Result:** Member joins; creator sees updated member count.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-08-005 - Thrift member-count validation (2-12)

- **Module:** Thrift
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** Thrift group exists.

**Steps**

1. Attempt to create with 0/1 members (reject) and >12 members (reject).
2. Attempt to join a full group.

**Expected Result:** Out-of-range target member counts are rejected; a full group does not accept additional joins.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-08-006 - Activate thrift with payout rotation order

- **Module:** Thrift
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** All target members joined.

**Steps**

1. Creator sends `ACTIVATE XG-THRIFT-...`.
2. Enter the payout rotation order.

**Expected Result:** Group activates; first payout cycle begins with the configured rotation order.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-08-007 - Contribute to thrift cycle

- **Module:** Thrift
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Activated group; current payer known.

**Steps**

1. Send `CONTRIBUTE XG-THRIFT-...`.
2. Choose card checkout or bank transfer.
3. Complete payment.

**Expected Result:** Contribution posts; current cycle balance updates; next payout becomes eligible on full cycle collection.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-08-008 - Thrift group public page

- **Module:** Thrift
- **Actor:** Customer   **Channel:** Web   **Priority:** P2
- **Preconditions:** An activated thrift group exists.

**Steps**

1. Open GET /thrift/{name}.

**Expected Result:** Group page renders name, contribution amount, frequency, member list, and next payout info without exposing sensitive PII.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-08-009 - Admin marks thrift payout completed

- **Module:** Thrift
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Cycle fully contributed; simulated payout pending in /admin/thrift.

**Steps**

1. Sign in to /admin.
2. Open /admin/thrift.
3. Mark the pending simulated payout completed.

**Expected Result:** Payout marked completed; group advances to the next cycle until every member has received one payout.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-08-010 - Thrift cycles continue to completion

- **Module:** Thrift
- **Actor:** Admin   **Channel:** Console   **Priority:** P2
- **Preconditions:** Group with 2+ members; several cycles processed.

**Steps**

1. Continue marking payouts complete across cycles.
2. Observe rotation.

**Expected Result:** Each member receives a payout exactly once per rotation before the group completes/winds down.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 9: L2 — Individual Pay (C2C) (6 cases)

- **Tier/Actor:** L2
- **Prerequisites:** Phase 6
- **On completing this phase you unlock:** Merchant registration, admin approval, and the merchant console.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-09-001 | KYC | Admin | Console | KYC L1→L2 advancement (identity on file) | P0 |
| TC-09-002 | Individual Pay | Individual | WhatsApp | Individual pay flow starts from menu | P0 |
| TC-09-003 | Individual Pay | Individual | WhatsApp | Recipient phone + validation | P1 |
| TC-09-004 | Individual Pay | Individual | WhatsApp | Amount + narration entry and review | P0 |
| TC-09-005 | Individual Pay | Individual | Web | Confirm payment, settle, and credit recipient wallet | P1 |
| TC-09-006 | Individual Pay | Individual | Web | Insufficient funds / failed payout handling | P2 |

<details>
<summary>Step-by-step details for Phase 9</summary>

#### TC-09-001 - KYC L1→L2 advancement (identity on file)

- **Module:** KYC
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** Customer at L1 with identity submitted during 'Become individual' flow.

**Steps**

1. Open /admin/kyc.
2. Advance the customer from L1 to L2 with EvIdentityOnFile evidence and clear screening.

**Expected Result:** Customer advances to L2. Allowance limits increase to L2 ceilings (single ₦20M, daily ₦20M, monthly ₦50M). Audit log records the advancement.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-09-002 - Individual pay flow starts from menu

- **Module:** Individual Pay
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Customer at L2 (after TC-09-001).

**Steps**

1. Choose 'Individual pay' / send-money from the menu.
2. Observe the 8-step guided flow begins.

**Expected Result:** Flow starts: recipient phone, amount, narration, review, payment method, confirm.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-09-003 - Recipient phone + validation

- **Module:** Individual Pay
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** Individual pay flow started.

**Steps**

1. Enter an invalid phone, then a valid E.164 Nigerian number.

**Expected Result:** Invalid phone rejected; valid number accepted and carried forward.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-09-004 - Amount + narration entry and review

- **Module:** Individual Pay
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Recipient entered.

**Steps**

1. Enter amount.
2. Enter a narration.
3. Review the transfer summary.

**Expected Result:** Summary shows amount, recipient, narration, and the NIP flat NGN 100 payee fee; confirm step reached.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-09-005 - Confirm payment, settle, and credit recipient wallet

- **Module:** Individual Pay
- **Actor:** Individual   **Channel:** Web   **Priority:** P1
- **Preconditions:** Review confirmed.

**Steps**

1. Complete the payment (card/transfer).
2. Observe fulfilment after success.

**Expected Result:** Payment succeeds against the xego-individual-pay system merchant; the recipient (if L1+) is credited via the payout rail; sender receives confirmation.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-09-006 - Insufficient funds / failed payout handling

- **Module:** Individual Pay
- **Actor:** Individual   **Channel:** Web   **Priority:** P2
- **Preconditions:** Transaction fails or insufficient balance at settlement.

**Steps**

1. Trigger a failing payout scenario.
2. Observe state.

**Expected Result:** Payout fails cleanly (retryable) or payment reflects the failure; no phantom credits; admin/settlement views show the failure.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 10: Merchant Onboarding & Console (12 cases)

- **Tier/Actor:** Merchant
- **Prerequisites:** Phases 2, 14 (approval)
- **On completing this phase you unlock:** Merchant services, events, and QR scanner setup.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-10-001 | Merchant Reg | Individual | WhatsApp | Register merchant — email OTP + business details | P0 |
| TC-10-002 | Merchant Reg | Admin | Console | Admin approves merchant registration | P0 |
| TC-10-003 | Auth | Merchant | Console | Merchant set password flow | P1 |
| TC-10-004 | Auth | Merchant | Console | Merchant TOTP enrollment (QR display) | P0 |
| TC-10-005 | Auth | Merchant | Console | Merchant login with TOTP | P0 |
| TC-10-006 | Settings | Merchant | Console | Merchant profile update | P2 |
| TC-10-007 | Settings | Merchant | Console | Settings update | P1 |
| TC-10-008 | API Keys | Merchant | Console | Create API key — one-time plaintext view | P0 |
| TC-10-009 | API Keys | Merchant | Console | Revoke API key | P1 |
| TC-10-010 | Webhooks | Merchant | Console | Webhook configuration (signing secret mint) | P0 |
| TC-10-011 | Scanner | Merchant | Console | Merchant scanner page renders | P1 |
| TC-10-012 | Scanner | Merchant | Console | Scan entry and processing | P1 |

<details>
<summary>Step-by-step details for Phase 10</summary>

#### TC-10-001 - Register merchant — email OTP + business details

- **Module:** Merchant Reg
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P0
- **Preconditions:** Onboarded customer.

**Steps**

1. Choose 'Register merchant' from the menu.
2. Enter the 6-digit email OTP.
3. Enter business name, category, description.
4. Confirm the request.

**Expected Result:** Request created with status awaiting_approval; appears in /admin/merchants. User becomes the merchant owner upon admin approval.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-002 - Admin approves merchant registration

- **Module:** Merchant Reg
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** A pending merchant registration.

**Steps**

1. Open /admin/merchants.
2. POST /admin/merchant-registrations/{id}/approve.

**Expected Result:** Registration becomes a payable merchant; requesting user linked as owner; invoice features unlock. Audit log entry written.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-003 - Merchant set password flow

- **Module:** Auth
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** Merchant without a password (invited/reset).

**Steps**

1. Open /merchant/set-password.
2. Set a new password and confirm.

**Expected Result:** Password saved (bcrypt); subsequent logins use it. Weak passwords rejected per policy.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-004 - Merchant TOTP enrollment (QR display)

- **Module:** Auth
- **Actor:** Merchant   **Channel:** Console   **Priority:** P0
- **Preconditions:** TOTP_ENABLED=true; merchant account with password.

**Steps**

1. Log in with password.
2. Observe QR/enrollment on first successful login.

**Expected Result:** A QR code / secret is shown once for the authenticator app; subsequent logins require the 6-digit code.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-005 - Merchant login with TOTP

- **Module:** Auth
- **Actor:** Merchant   **Channel:** Console   **Priority:** P0
- **Preconditions:** TOTP enrolled.

**Steps**

1. Open /merchant/login.
2. Enter credentials.
3. Enter the 6-digit authenticator code.

**Expected Result:** Login succeeds only after password + TOTP. TOTP secret stored encrypted.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-006 - Merchant profile update

- **Module:** Settings
- **Actor:** Merchant   **Channel:** Console   **Priority:** P2
- **Preconditions:** Logged-in merchant.

**Steps**

1. Open /merchant/profile.
2. Update profile details.
3. Save.

**Expected Result:** Profile updates persist; masked PII rules apply where applicable.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-007 - Settings update

- **Module:** Settings
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** Logged-in merchant.

**Steps**

1. Open /merchant/settings.
2. Update configurations.
3. Save.

**Expected Result:** Settings persist; relevant UI reflects the change.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-008 - Create API key — one-time plaintext view

- **Module:** API Keys
- **Actor:** Merchant   **Channel:** Console   **Priority:** P0
- **Preconditions:** Logged-in merchant.

**Steps**

1. Open /merchant/settings -> Partner API keys.
2. Create a new key.

**Expected Result:** The plaintext key (xeg_sk_test_... / xeg_sk_live_...) is shown exactly once; only the SHA-256 hash is stored. Key usable immediately.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-009 - Revoke API key

- **Module:** API Keys
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** An API key exists.

**Steps**

1. Revoke the key.
2. Attempt an API call with it.

**Expected Result:** Revoked key is rejected (unauthorized) on subsequent Partner API calls.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-010 - Webhook configuration (signing secret mint)

- **Module:** Webhooks
- **Actor:** Merchant   **Channel:** Console   **Priority:** P0
- **Preconditions:** Logged-in merchant.

**Steps**

1. Open /merchant/settings -> Webhook notifications.
2. Set a callback URL and mint a signing secret.

**Expected Result:** Secret shown once and sealed at rest; saved URL receives signed payment.succeeded/failed deliveries.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-011 - Merchant scanner page renders

- **Module:** Scanner
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** Logged-in merchant with a scanning service configured.

**Steps**

1. Open /merchant/scanner.

**Expected Result:** QR scanner page loads, pointing at /merchant/scan processing with the configured service/reader.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-10-012 - Scan entry and processing

- **Module:** Scanner
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** A seeded receipt/QR token exists.

**Steps**

1. Open /merchant/scan.
2. Submit the scanned token (or POST /api/readers/scan from a reader).

**Expected Result:** The scan resolves to the payment/receipt; the merchant sees the expected payment details.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 11: Merchant Services & Events (10 cases)

- **Tier/Actor:** Merchant
- **Prerequisites:** Phase 10
- **On completing this phase you unlock:** Server-to-server Partner API integration (HMAC-signed).

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-11-001 | Services | Merchant | Console | Services list renders | P1 |
| TC-11-002 | Services | Merchant | Console | Create a service | P1 |
| TC-11-003 | Services | Merchant | Console | Edit a service | P2 |
| TC-11-004 | Services | Merchant | Console | Toggle service enabled/disabled | P1 |
| TC-11-005 | Services | Merchant | Console | Service payment list | P2 |
| TC-11-006 | Events | Merchant | Console | Events list, create, and edit | P1 |
| TC-11-007 | Events | Merchant | Console | Event tickets and toggle | P2 |
| TC-11-008 | Invoices | Merchant | Console | Merchant invoice list | P1 |
| TC-11-009 | Scanner Config | Admin | Console | Scanning services configuration | P1 |
| TC-11-010 | AcceptedNumbers | Admin | Console | Accepted demo numbers management | P2 |

<details>
<summary>Step-by-step details for Phase 11</summary>

#### TC-11-001 - Services list renders

- **Module:** Services
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** Merchant with services.

**Steps**

1. Open /merchant/services.

**Expected Result:** All merchant services listed with status (enabled/disabled) and payment counts.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-11-002 - Create a service

- **Module:** Services
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** Logged-in merchant.

**Steps**

1. Open /merchant/services/new.
2. Fill name, description, category.
3. Submit.

**Expected Result:** Service created and listed; it becomes selectable as configured.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-11-003 - Edit a service

- **Module:** Services
- **Actor:** Merchant   **Channel:** Console   **Priority:** P2
- **Preconditions:** Existing service.

**Steps**

1. Open /merchant/services/{id}/edit.
2. Modify fields.
3. Submit.

**Expected Result:** Changes persist and reflect immediately in the service list.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-11-004 - Toggle service enabled/disabled

- **Module:** Services
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** Existing service.

**Steps**

1. Toggle the service off.
2. Attempt an order/payment against it.
3. Toggle back on.

**Expected Result:** Disabled service no longer accepts new payments; re-enabling restores it.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-11-005 - Service payment list

- **Module:** Services
- **Actor:** Merchant   **Channel:** Console   **Priority:** P2
- **Preconditions:** Service with payments.

**Steps**

1. Open /merchant/services/{id}/payments.

**Expected Result:** All payments for the service shown with status, amount, and receipt links.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-11-006 - Events list, create, and edit

- **Module:** Events
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** Logged-in merchant.

**Steps**

1. Open /merchant/events.
2. Create an event (name, venue, date, tickets).
3. Edit it.

**Expected Result:** Events CRUD works; created event renders with tickets.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-11-007 - Event tickets and toggle

- **Module:** Events
- **Actor:** Merchant   **Channel:** Console   **Priority:** P2
- **Preconditions:** Event with tickets.

**Steps**

1. Open /merchant/events/{id}/tickets.
2. Toggle the event.

**Expected Result:** Tickets list with availability; disabled event stops ticket purchases.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-11-008 - Merchant invoice list

- **Module:** Invoices
- **Actor:** Merchant   **Channel:** Console   **Priority:** P1
- **Preconditions:** Merchant with invoices.

**Steps**

1. Open /merchant/invoices.

**Expected Result:** Invoices listed with reference, status, amount, amount_paid.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-11-009 - Scanning services configuration

- **Module:** Scanner Config
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Admin role.

**Steps**

1. Open /admin/scanning.
2. Create a scanning service and a reader.
3. Set a phone whitelist.

**Expected Result:** Services/readers CRUD works; whitelist applies to scans.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-11-010 - Accepted demo numbers management

- **Module:** AcceptedNumbers
- **Actor:** Admin   **Channel:** Console   **Priority:** P2
- **Preconditions:** Admin role.

**Steps**

1. Open /admin/accepted-numbers.
2. Update the allow-list for demo invoices.

**Expected Result:** Allow-list persists; only listed numbers can receive demo invoices.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 12: Partner API Integration (22 cases)

- **Tier/Actor:** API
- **Prerequisites:** Phase 10
- **On completing this phase you unlock:** Settlements, refunds, and dispute management.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-12-001 | Auth | Merchant | API | Missing auth headers rejected | P0 |
| TC-12-002 | Auth | Merchant | API | Invalid/unknown API key rejected | P0 |
| TC-12-003 | Auth | Merchant | API | Stale timestamp rejected | P1 |
| TC-12-004 | Auth | Merchant | API | Incorrect signature rejected | P0 |
| TC-12-005 | Payments | Merchant | API | Create payment — valid request | P0 |
| TC-12-006 | Payments | Merchant | API | Create payment — idempotent replay on reference | P0 |
| TC-12-007 | Payments | Merchant | API | Create payment — validation errors | P1 |
| TC-12-008 | Payments | Merchant | API | Get payment status | P0 |
| TC-12-009 | Payments | Merchant | API | Re-verify a payment | P1 |
| TC-12-010 | Invoices | Merchant | API | Create invoice via API | P1 |
| TC-12-011 | Invoices | Merchant | API | Get invoice status (amount_paid) | P1 |
| TC-12-012 | Checkouts | Merchant | API | Create request-money link (checkout) | P1 |
| TC-12-013 | Checkouts | Merchant | API | Checkout resolves when payer pays | P1 |
| TC-12-014 | Balance | Merchant | API | Get merchant balance | P0 |
| TC-12-015 | Settlements | Merchant | API | Cut settlement batch | P0 |
| TC-12-016 | Settlements | Merchant | API | Get settlement batch status | P1 |
| TC-12-017 | Payouts | Merchant | API | Request payout for open batch | P0 |
| TC-12-018 | Payouts | Merchant | API | Payout failure with declining bank code 011 | P1 |
| TC-12-019 | Settlements | Merchant | API | Register and list settlement accounts | P1 |
| TC-12-020 | Refunds | Merchant | API | Refund a succeeded payment | P0 |
| TC-12-021 | Disputes | Merchant | API | List and view disputes | P2 |
| TC-12-022 | RateLimit | Merchant | API | Per-key API rate limiting | P2 |

<details>
<summary>Step-by-step details for Phase 12</summary>

#### TC-12-001 - Missing auth headers rejected

- **Module:** Auth
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** Merchant API key.

**Steps**

1. POST /api/v1/payments with no X-Xego-Key / timestamp / signature.

**Expected Result:** 401 unauthorized with a structured error envelope; no payment created.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-002 - Invalid/unknown API key rejected

- **Module:** Auth
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** A valid and an invalid key.

**Steps**

1. Sign a request with an unknown/revoked key.

**Expected Result:** 401 unauthorized; the request is not processed.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-003 - Stale timestamp rejected

- **Module:** Auth
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** Merchant API key.

**Steps**

1. Compute the signature with a timestamp older than 5 minutes.

**Expected Result:** Request rejected (timestamp outside allowed window).

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-004 - Incorrect signature rejected

- **Module:** Auth
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** Merchant API key.

**Steps**

1. Send a request with a tampered signature (wrong key or body).

**Expected Result:** 401 unauthorized; HMAC key doubles as the signing secret, so tampering is rejected.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-005 - Create payment — valid request

- **Module:** Payments
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** Valid API key; amount in range.

**Steps**

1. POST /api/v1/payments with reference, amount (kobo), currency NGN, customer phone/email.

**Expected Result:** Payment created with status awaiting_confirmation/initialized; http 201; reference reserved for idempotency.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-006 - Create payment — idempotent replay on reference

- **Module:** Payments
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** An existing payment reference.

**Steps**

1. Replay the same POST /api/v1/payments with the same reference and a different body/amount.

**Expected Result:** Replay returns the existing payment (no duplicate), regardless of body differences.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-007 - Create payment — validation errors

- **Module:** Payments
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** Valid API key.

**Steps**

1. Try out-of-range amounts, missing reference, invalid phone format.

**Expected Result:** 400-ish errors with codes like amount_out_of_range / invalid_request; nothing persisted.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-008 - Get payment status

- **Module:** Payments
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** A created payment.

**Steps**

1. GET /api/v1/payments/{reference}.

**Expected Result:** Current lifecycle status returned (awaiting_confirmation/initialized/succeeded/failed...).

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-009 - Re-verify a payment

- **Module:** Payments
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** A payment that may have resolved.

**Steps**

1. POST /api/v1/payments/{reference}/verify.

**Expected Result:** Server re-checks against the gateway and returns the authoritative status.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-010 - Create invoice via API

- **Module:** Invoices
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** Valid API key.

**Steps**

1. POST /api/v1/invoices with line items, optional reference/delivery_fee/due_at.

**Expected Result:** Invoice created idempotently; response includes status, amount, amount_paid, items, wa.me pay_link.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-011 - Get invoice status (amount_paid)

- **Module:** Invoices
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** Invoice with some payments.

**Steps**

1. GET /api/v1/invoices/{reference}.

**Expected Result:** Returned state includes amount_paid updated with contributions.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-012 - Create request-money link (checkout)

- **Module:** Checkouts
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** Valid API key.

**Steps**

1. POST /api/v1/checkouts with amount, optional note/redirect_url/expires_at.

**Expected Result:** Checkout created with status open and checkout_url (/link/{token}); expired past dates rejected.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-013 - Checkout resolves when payer pays

- **Module:** Checkouts
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** An open checkout link.

**Steps**

1. Pay the link as a customer.
2. GET /api/v1/checkouts/{reference}.

**Expected Result:** Checkout state reflects payment; on terminal success checkout becomes paid with payment_id/payment_status.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-014 - Get merchant balance

- **Module:** Balance
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** Merchant with collected payments.

**Steps**

1. GET /api/v1/balance.

**Expected Result:** available_balance and ledger breakdown per account (net/debits/credits in kobo) returned.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-015 - Cut settlement batch

- **Module:** Settlements
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** Merchant has succeeded, un-batched payments.

**Steps**

1. POST /api/v1/settlements with batch_no.

**Expected Result:** Batch created with total (kobo), line_count, status open, fee and payout_amount objects; idempotent on batch_no.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-016 - Get settlement batch status

- **Module:** Settlements
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** A settlement batch exists.

**Steps**

1. GET /api/v1/settlements/{batch_no}.

**Expected Result:** Batch state (open/scheduled/processed/failed), totals, fee, and included payments returned.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-017 - Request payout for open batch

- **Module:** Payouts
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** An open batch; default active settlement account.

**Steps**

1. POST /api/v1/settlements/{batch_no}/payout.

**Expected Result:** Payout queued then dispatched by worker -> completed/failed; GET /api/v1/payouts/{reference} reflects it.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-018 - Payout failure with declining bank code 011

- **Module:** Payouts
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** Simulated rail; settlement account with bank_code 011.

**Steps**

1. Request a payout to bank code 011.
2. Observe the outcome.

**Expected Result:** Payout fails deterministically (SIM rail declines 011); row is retryable; reversal can reopen the batch.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-019 - Register and list settlement accounts

- **Module:** Settlements
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** Valid API key.

**Steps**

1. POST /api/v1/settlement-accounts (bank_code, account_number, account_name).
2. GET /api/v1/settlement-accounts.

**Expected Result:** Account registered as pending and becomes default if first; list shows status and is_default.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-020 - Refund a succeeded payment

- **Module:** Refunds
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** A succeeded payment NOT in a scheduled/processed batch.

**Steps**

1. POST /api/v1/payments/{reference}/refund (optional reason).

**Expected Result:** Full refund: pending -> completed (simulated rail), payment transitions to refunded, ledger reversal posted, payment.refunded emitted.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-021 - List and view disputes

- **Module:** Disputes
- **Actor:** Merchant   **Channel:** API   **Priority:** P2
- **Preconditions:** Merchant disputes exist (admin-created).

**Steps**

1. GET /api/v1/disputes.
2. GET /api/v1/disputes/{id}.

**Expected Result:** Disputes listed with status; detail returned; no unauthorized access to other merchants' disputes.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-12-022 - Per-key API rate limiting

- **Module:** RateLimit
- **Actor:** Merchant   **Channel:** API   **Priority:** P2
- **Preconditions:** RATE_LIMIT_API_KEYS_PER_MINUTE set to a low value for the test.

**Steps**

1. Fire requests beyond the limit in one minute.

**Expected Result:** Requests beyond the limit are rejected with a rate_limited envelope; counter resets next window.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 13: Settlements, Refunds & Disputes (10 cases)

- **Tier/Actor:** Both
- **Prerequisites:** Phases 10-12
- **On completing this phase you unlock:** Admin console operations, RBAC role verification, and KYC/KYB review queues.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-13-001 | Settlements | Merchant | API | Cut a settlement batch and verify fee | P0 |
| TC-13-002 | Settlements | Admin | Console | Admin settlement overview | P1 |
| TC-13-003 | Settlements | Admin | Console | Approve and disable settlement accounts | P1 |
| TC-13-004 | Payouts | Admin | Console | Retry a failed payout | P1 |
| TC-13-005 | Payouts | Admin | Console | Reverse a payout and reopen batch | P1 |
| TC-13-006 | Refunds | Admin | Console | Refund approval workflow | P0 |
| TC-13-007 | Refunds | Admin | Console | Fail a pending refund | P1 |
| TC-13-008 | Refunds | Merchant | API | Refund blocked if payment is in a scheduled/processed batch | P1 |
| TC-13-009 | Disputes | Admin | Console | Dispute resolution (won/lost) | P1 |
| TC-13-010 | Ledger | Admin | Console | Ledger postings for settlement/refund/dispute verified | P0 |

<details>
<summary>Step-by-step details for Phase 13</summary>

#### TC-13-001 - Cut a settlement batch and verify fee

- **Module:** Settlements
- **Actor:** Merchant   **Channel:** API   **Priority:** P0
- **Preconditions:** Merchant with succeeded, un-batched payments.

**Steps**

1. POST /api/v1/settlements.
2. Record total, fee (SETTLEMENT_FEE_BPS), payout_amount.

**Expected Result:** Batch open; fee == total * bps (default 2.5%); payout_amount == total - fee; ledger moved merchant_payable -> settlement_payable.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-13-002 - Admin settlement overview

- **Module:** Settlements
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Batches/accounts/payouts exist.

**Steps**

1. Open /admin/settlements.

**Expected Result:** Batches, settlement accounts, and payouts all listed with statuses.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-13-003 - Approve and disable settlement accounts

- **Module:** Settlements
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Pending settlement accounts exist.

**Steps**

1. Approve an account.
2. Disable another.

**Expected Result:** Approved account becomes active; disabled accounts cannot receive payouts; default account respected.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-13-004 - Retry a failed payout

- **Module:** Payouts
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** A failed payout (bank 011).

**Steps**

1. POST /admin/settlements/payouts/{id}/retry.

**Expected Result:** Payout re-dispatches with the same deterministic outcome; no duplicated external payouts.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-13-005 - Reverse a payout and reopen batch

- **Module:** Payouts
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** A failed/processing payout.

**Steps**

1. POST /admin/settlements/payouts/{id}/reverse.

**Expected Result:** Payout marked reversed; batch reopened; funds released back to merchant_payable; a fresh payout can be requested.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-13-006 - Refund approval workflow

- **Module:** Refunds
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** A pending refund.

**Steps**

1. Open /admin/refunds.
2. Approve the refund.

**Expected Result:** Refund completes via rail; payment refunded; ledger reversal posted.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-13-007 - Fail a pending refund

- **Module:** Refunds
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** A pending refund.

**Steps**

1. POST /admin/refunds/{id}/fail.

**Expected Result:** Refund fails; payment remains refund-eligible or state documented.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-13-008 - Refund blocked if payment is in a scheduled/processed batch

- **Module:** Refunds
- **Actor:** Merchant   **Channel:** API   **Priority:** P1
- **Preconditions:** A succeeded payment included in a scheduled/processed settlement batch.

**Steps**

1. Attempt POST /api/v1/payments/{reference}/refund.

**Expected Result:** Refund rejected with instructions to reverse the payout first.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-13-009 - Dispute resolution (won/lost)

- **Module:** Disputes
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Open disputes exist.

**Steps**

1. Open /admin/disputes.
2. Resolve one as won, one as lost.

**Expected Result:** Disputes transition out of open; merchant API list reflects final status.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-13-010 - Ledger postings for settlement/refund/dispute verified

- **Module:** Ledger
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** Recent settlement/refund activity.

**Steps**

1. Open /admin/ledger.
2. Trace entries for a batch, a payout, a refund, a dispute.

**Expected Result:** Balanced debit/credit pairs exist for each; hash chain intact; merchant balance reflects postings.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 14: Admin Console & RBAC (20 cases)

- **Tier/Actor:** Admin
- **Prerequisites:** Phases 1-9 (data to manage)
- **On completing this phase you unlock:** Cross-cutting security: audit log, reconciliation, SIEM, encryption, and compliance.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-14-001 | Auth | Admin | Console | Admin login with TOTP | P0 |
| TC-14-002 | RBAC | Admin | Console | Readonly role — view only, no actions | P0 |
| TC-14-003 | RBAC | Admin | Console | Compliance role — dashboards + merchant approval | P0 |
| TC-14-004 | RBAC | Admin | Console | Support role — password reset only | P1 |
| TC-14-005 | RBAC | Admin | Console | Admin role — full access verified | P1 |
| TC-14-006 | Dashboard | Admin | Console | Metrics dashboard renders | P0 |
| TC-14-007 | Users | Admin | Console | User list with masked PII | P0 |
| TC-14-008 | Merchants | Admin | Console | Merchant list and registrations | P0 |
| TC-14-009 | Payments | Admin | Console | Payment list | P0 |
| TC-14-010 | KYC | Admin | Console | KYC review queue — approve/reject | P1 |
| TC-14-011 | KYC | Admin | Console | Risk scoring band display | P1 |
| TC-14-012 | Monitoring | Admin | Console | Transaction monitoring alert resolution | P1 |
| TC-14-013 | KYB | Admin | Console | KYB profile advancement | P1 |
| TC-14-014 | KYB | Admin | Console | KYB advance-request approve/clear | P2 |
| TC-14-015 | Allowances | Admin | Console | Tier limit update | P2 |
| TC-14-016 | Thrift | Admin | Console | Thrift groups management | P1 |
| TC-14-017 | Data | Admin | Console | Data orders list | P2 |
| TC-14-018 | Messaging | Admin | Console | Messaging cost meter | P2 |
| TC-14-019 | Operators | Admin | Console | Operator management (create/role/enable/password) | P1 |
| TC-14-020 | Auth | Admin | Console | Admin TOTP disable | P2 |

<details>
<summary>Step-by-step details for Phase 14</summary>

#### TC-14-001 - Admin login with TOTP

- **Module:** Auth
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** TOTP_ENABLED=true; ADMIN_EMAIL/ADMIN_PASSWORD_HASH set.

**Steps**

1. Open /admin/login.
2. Log in with credentials.
3. Provide the 6-digit authenticator code.

**Expected Result:** Two-step login succeeds (password first, then TOTP); first success shows QR enrollment.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-002 - Readonly role — view only, no actions

- **Module:** RBAC
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** A readonly admin account.

**Steps**

1. Log in as readonly.
2. Attempt to approve a merchant, reset a password, or reach admin-only routes.

**Expected Result:** Readonly can view dashboards but all mutating actions are rejected (403) and admin-only routes are inaccessible.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-003 - Compliance role — dashboards + merchant approval

- **Module:** RBAC
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** A compliance admin account.

**Steps**

1. Log in as compliance.
2. Approve a pending merchant registration.
3. Attempt admin-only actions (operator mgmt, payment terms).

**Expected Result:** Compliance can approve merchants and view dashboards/reports; admin-only actions are blocked.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-004 - Support role — password reset only

- **Module:** RBAC
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** A support admin account.

**Steps**

1. Log in as support.
2. Reset a merchant password.
3. Attempt compliance/admin actions.

**Expected Result:** Support can reset merchant passwords and view dashboards; other privileged actions are blocked.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-005 - Admin role — full access verified

- **Module:** RBAC
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Primary admin account.

**Steps**

1. As admin, exercise payment terms, thrift payouts, scanning config, webhooks, operator management.

**Expected Result:** All admin-only actions permitted without 403.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-006 - Metrics dashboard renders

- **Module:** Dashboard
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** Logged-in admin.

**Steps**

1. Open /admin (redirects to /admin/metrics).

**Expected Result:** Dashboard shows counts/figures for users, merchants, payments, revenue.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-007 - User list with masked PII

- **Module:** Users
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** Users exist.

**Steps**

1. Open /admin/users.

**Expected Result:** Users listed with PII masked (partial phone/email). Full values not exposed in the list.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-008 - Merchant list and registrations

- **Module:** Merchants
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** Merchants and pending registrations exist.

**Steps**

1. Open /admin/merchants.

**Expected Result:** Approved merchants and awaiting_approval registrations listed distinctly.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-009 - Payment list

- **Module:** Payments
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** Payments exist.

**Steps**

1. Open /admin/payments.

**Expected Result:** Payments listed with reference, status, amount, merchant, timestamps.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-010 - KYC review queue — approve/reject

- **Module:** KYC
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Pending KYC cases.

**Steps**

1. Open /admin/kyc review queue.
2. POST /admin/kyc/cases/{id}/review approve and reject.

**Expected Result:** Decision persists; affected user's KYC tier/status updates accordingly.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-011 - Risk scoring band display

- **Module:** KYC
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Users with computed risk scores.

**Steps**

1. Open /admin/kyc and view risk scores.

**Expected Result:** Score (0-100) and band (low/medium/high) displayed per user.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-012 - Transaction monitoring alert resolution

- **Module:** Monitoring
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** An alert exists (velocity/structuring/round amounts).

**Steps**

1. Open /admin/monitoring/alerts.
2. Resolve an alert.

**Expected Result:** Alert resolves and is recorded; monitor cycle continues.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-013 - KYB profile advancement

- **Module:** KYB
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Registered merchants with B0 profiles.

**Steps**

1. Open /admin/kyb.
2. Advance a merchant to B1.

**Expected Result:** Business tier advances; business wallet/settlement limits reflect the new tier.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-014 - KYB advance-request approve/clear

- **Module:** KYB
- **Actor:** Admin   **Channel:** Console   **Priority:** P2
- **Preconditions:** A merchant KYB advance request exists.

**Steps**

1. Approve the advance request (or clear it).

**Expected Result:** Tier updates on approval; cleared requests leave tier unchanged.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-015 - Tier limit update

- **Module:** Allowances
- **Actor:** Admin   **Channel:** Console   **Priority:** P2
- **Preconditions:** Admin/compliance role.

**Steps**

1. POST /admin/tier-limits/{id} to update single/daily/monthly ceilings.

**Expected Result:** New limits applied immediately to that tier/direction for in/out flows.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-016 - Thrift groups management

- **Module:** Thrift
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Thrift groups exist.

**Steps**

1. Open /admin/thrift.

**Expected Result:** Groups, members, contributions, and pending simulated payouts visible.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-017 - Data orders list

- **Module:** Data
- **Actor:** Admin   **Channel:** Console   **Priority:** P2
- **Preconditions:** Data orders exist.

**Steps**

1. Open /admin/data-orders.

**Expected Result:** Orders listed with network, plan, beneficiary, status.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-018 - Messaging cost meter

- **Module:** Messaging
- **Actor:** Admin   **Channel:** Console   **Priority:** P2
- **Preconditions:** Outbound messages recorded.

**Steps**

1. Open /admin/messaging.

**Expected Result:** Outbound message counts and cost estimates rendered per channel.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-019 - Operator management (create/role/enable/password)

- **Module:** Operators
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Admin role.

**Steps**

1. Create an admin at /admin/admins.
2. Change its role.
3. Disable/enable.
4. Reset its password.

**Expected Result:** All operations succeed for admin role; role changes take effect on next session; audit logged.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-14-020 - Admin TOTP disable

- **Module:** Auth
- **Actor:** Admin   **Channel:** Console   **Priority:** P2
- **Preconditions:** Admin with TOTP enabled.

**Steps**

1. POST /admin/totp/disable.
2. Confirm the next login skips TOTP.

**Expected Result:** TOTP disabled for the account and recorded in the audit log.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

### Phase 15: Security, Audit & Compliance (19 cases)

- **Tier/Actor:** Admin/System
- **Prerequisites:** Phase 14 (audit trail populated)
- **On completing this phase you unlock:** End of test schedule — all services have been exercised.

| ID | Module | Actor | Channel | Title | Priority |
|---|---|---|---|---|---|
| TC-15-001 | Audit | Admin | Console | Audit log integrity (hash chain) | P0 |
| TC-15-002 | Ledger | Admin | Console | Ledger hash-chain verification | P0 |
| TC-15-003 | Reconciliation | Admin | Console | Reconciliation console + manual run | P0 |
| TC-15-004 | SIEM | Admin | Console | SIEM view + CSV/JSON export | P1 |
| TC-15-005 | Analytics | Admin | Console | Analytics dashboard + export | P1 |
| TC-15-006 | Reports | Admin | Console | STR/CTR/PEP reports | P1 |
| TC-15-007 | DSR | Admin | Console | Data subject requests (DSR) lifecycle | P1 |
| TC-15-008 | Archive | Admin | Console | Archive ledger view | P2 |
| TC-15-009 | LegalHolds | Admin | Console | Legal holds add/remove | P1 |
| TC-15-010 | Retention | Admin | CLI | Retention purge (archive-then-delete) | P2 |
| TC-15-011 | ChatGuard | Admin | Console | Chat guard incidents | P2 |
| TC-15-012 | Webhooks | Admin | Console | Webhook delivery log + dead-letter replay | P1 |
| TC-15-013 | Screening | Admin | CLI | KYC rescreen | P2 |
| TC-15-014 | Identity | Individual | WhatsApp | NIN/BVN identity verification (simulated) | P1 |
| TC-15-015 | Encryption | Admin | Console | Encryption at rest verification | P0 |
| TC-15-016 | RateLimit | System | Web | Public page rate limiting | P1 |
| TC-15-017 | RateLimit | System | Webhook | Webhook rate limiting | P1 |
| TC-15-018 | Logging | Admin | System | PII/error redaction in logs | P1 |
| TC-15-019 | Headers | System | Web | Correlation ID propagation and security headers | P2 |

<details>
<summary>Step-by-step details for Phase 15</summary>

#### TC-15-001 - Audit log integrity (hash chain)

- **Module:** Audit
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** Privileged actions have been performed (phases 6-14).

**Steps**

1. Open /admin/audit.
2. Inspect chain status and entries for a performed action.

**Expected Result:** Audit rows list actor/IP/action/resource; integrity check reports no broken links. Append-only enforced.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-002 - Ledger hash-chain verification

- **Module:** Ledger
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** Ledger entries exist from payments/settlements.

**Steps**

1. Open /admin/ledger.
2. Trigger chain verification.

**Expected Result:** Chain verified intact; no broken links in the append-only ledger.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-003 - Reconciliation console + manual run

- **Module:** Reconciliation
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** Payments exist across states.

**Steps**

1. Open /admin/reconciliation.
2. POST /admin/reconciliation/run.

**Expected Result:** Run completes; mismatches surface as exception rows; a clean run shows zero unexplained rows.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-004 - SIEM view + CSV/JSON export

- **Module:** SIEM
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Domain events and audit rows exist.

**Steps**

1. Open /admin/siem.
2. Filter by topic/source/merchant/time.
3. Export CSV and JSON.

**Expected Result:** Unified event view renders; exports produce well-formed CSV/JSON with the selected filters.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-005 - Analytics dashboard + export

- **Module:** Analytics
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Transaction history exists.

**Steps**

1. Open /admin/analytics.
2. Set a date range.
3. Export CSV per section.

**Expected Result:** Daily revenue, merchant settlement summaries, volume, payout history render; CSV exports respect the range.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-006 - STR/CTR/PEP reports

- **Module:** Reports
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Transaction data exists.

**Steps**

1. Open /admin/reports.
2. Download each kind.

**Expected Result:** CSV files generate with expected columns; compliance/admin roles only.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-007 - Data subject requests (DSR) lifecycle

- **Module:** DSR
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Compliance/admin role.

**Steps**

1. Create a DSR.
2. Export subject data.
3. Resolve the request.

**Expected Result:** DSR created, data export available, resolution recorded. Erasure archives then removes PII.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-008 - Archive ledger view

- **Module:** Archive
- **Actor:** Admin   **Channel:** Console   **Priority:** P2
- **Preconditions:** Retention archive has records.

**Steps**

1. Open /admin/archive.

**Expected Result:** Archived ledger entries rendered; hash-chain and append-only invariants displayed.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-009 - Legal holds add/remove

- **Module:** LegalHolds
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Admin role.

**Steps**

1. Add a legal hold.
2. Run the retention purge.
3. Remove the hold.

**Expected Result:** Held subjects are frozen and skipped by retention purge until the hold is removed.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-010 - Retention purge (archive-then-delete)

- **Module:** Retention
- **Actor:** Admin   **Channel:** CLI   **Priority:** P2
- **Preconditions:** Retention policy configured.

**Steps**

1. Run `go run ./cmd/demo retain`.

**Expected Result:** Records past retention archived then deleted in bounded batches; legal-hold subjects skipped.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-011 - Chat guard incidents

- **Module:** ChatGuard
- **Actor:** Admin   **Channel:** Console   **Priority:** P2
- **Preconditions:** A chat-guard incident was triggered (Phase 4, TC-04-008).

**Steps**

1. Open /admin/chat-guard.

**Expected Result:** Blocked card/PIN/OTP incidents listed with actor and redacted payload.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-012 - Webhook delivery log + dead-letter replay

- **Module:** Webhooks
- **Actor:** Admin   **Channel:** Console   **Priority:** P1
- **Preconditions:** Webhook deliveries and a dead-lettered entry exist.

**Steps**

1. Open /admin/webhooks.
2. Open /admin/dead-letter.
3. POST /admin/dead-letter/{id}/replay.

**Expected Result:** Deliveries listed with status; replay re-queues without duplicating payments.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-013 - KYC rescreen

- **Module:** Screening
- **Actor:** Admin   **Channel:** CLI   **Priority:** P2
- **Preconditions:** SCREENING_PROVIDER=simulated.

**Steps**

1. Run `go run ./cmd/demo rescreen`.

**Expected Result:** Profiles due for rescreen re-scored; results updated; screen schedule advanced.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-014 - NIN/BVN identity verification (simulated)

- **Module:** Identity
- **Actor:** Individual   **Channel:** WhatsApp   **Priority:** P1
- **Preconditions:** KYC L2+ flow requiring ID evidence.

**Steps**

1. Submit NIN/BVN in the KYC flow.

**Expected Result:** Simulated verifier returns a deterministic result; verified ID attached to profile.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-015 - Encryption at rest verification

- **Module:** Encryption
- **Actor:** Admin   **Channel:** Console   **Priority:** P0
- **Preconditions:** DATA_ENCRYPTION_KEY and TOTP_ENCRYPTION_KEY configured.

**Steps**

1. Inspect stored chat payloads and TOTP secrets in the database.
2. Confirm ciphertext, not plaintext.

**Expected Result:** AES-256-GCM ciphertext stored for chat payloads/CSRF tokens and TOTP secrets; keys never in DB.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-016 - Public page rate limiting

- **Module:** RateLimit
- **Actor:** System   **Channel:** Web   **Priority:** P1
- **Preconditions:** RATE_LIMIT_PUBLIC_PER_MINUTE at default (60).

**Steps**

1. Hammer a public page > 60 times/min from one IP.

**Expected Result:** Requests beyond the limit are throttled (429); legitimate traffic unaffected after the window.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-017 - Webhook rate limiting

- **Module:** RateLimit
- **Actor:** System   **Channel:** Webhook   **Priority:** P1
- **Preconditions:** RATE_LIMIT_WEBHOOKS_PER_MINUTE at default (120).

**Steps**

1. Fire > 120 webhook POSTs in a minute to /webhooks/whatsapp.

**Expected Result:** Excess calls throttled; no backlog beyond the inbound queue; no payment corruption.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-018 - PII/error redaction in logs

- **Module:** Logging
- **Actor:** Admin   **Channel:** System   **Priority:** P1
- **Preconditions:** Structured JSON logging enabled.

**Steps**

1. Trigger an error containing card/PIN-like data.
2. Inspect log output.

**Expected Result:** Logs redact sensitive fields and error internals; correlation ID present.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

#### TC-15-019 - Correlation ID propagation and security headers

- **Module:** Headers
- **Actor:** System   **Channel:** Web   **Priority:** P2
- **Preconditions:** Server running.

**Steps**

1. Send a request with X-Request-ID.
2. Inspect response headers.

**Expected Result:** X-Request-ID echoed/propagated through logs; security headers (CSP, X-Frame-Options, etc.) present.

- **Result (Pass/Fail/Blocked):** ________________    **Notes:** ________________

</details>

## 5. Summary

| Phase | Title | Tier | Cases |
|---|---|---|---|
| 1 | System Setup & Health | System | 5 |
| 2 | Customer Onboarding (All Channels) | L0 | 10 |
| 3 | L0 — Card Checkout | L0 | 10 |
| 4 | L0 — Bank Transfer | L0 | 8 |
| 5 | L0 — Mobile Data Purchase | L0 | 8 |
| 6 | L0→L1 — Wallet & KYC Upgrade | L0→L1 | 9 |
| 7 | L1 — Invoices | L1 | 12 |
| 8 | L1+ — Thrift Group Savings | L1+ | 10 |
| 9 | L2 — Individual Pay (C2C) | L2 | 6 |
| 10 | Merchant Onboarding & Console | Merchant | 12 |
| 11 | Merchant Services & Events | Merchant | 10 |
| 12 | Partner API Integration | API | 22 |
| 13 | Settlements, Refunds & Disputes | Both | 10 |
| 14 | Admin Console & RBAC | Admin | 20 |
| 15 | Security, Audit & Compliance | Admin/System | 19 |
| **Total** | | | **171** |

Priority distribution:
- P0 (critical path): 69  
- P1 (important): 76  
- P2 (edge/nice-to-have): 26  

---

> This schedule is a **manual** vehicle. Automated coverage lives in `go test ./...` (unit/integration) and `test/payment-flows/` (Playwright).
