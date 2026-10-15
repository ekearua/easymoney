# Xego — WhatsApp Integration Recommendations (Detailed Propositions)

**Scope:** WhatsApp Cloud API wire fidelity, multi-tenant business support,
individual-user UX, and the conversation FSM. Each proposition below is a
standalone change: it states the problem, the exact current code it touches,
the proposed design (interfaces, structs, SQL, pseudocode), migration steps,
acceptance criteria, and effort/risk.

All file references are to the current tree (commit `bd82c34`).

---

## 0. Current-state assessment (what is already solid)

Do not regress these. They are the parts worth keeping and extending:

| Concern | Where | Why it is good |
|---|---|---|
| Raw-body HMAC signature validation | `internal/providers/whatsapp/client.go:51` (`ValidateSignature`) | Constant-time compare over the unmodified body; the standard Meta pattern. |
| Webhook dedup + durability | `internal/store/store.go:5070` (`RecordWebhook`), `app.go:709` | Event key is a digest of the raw body; dedup before ack. |
| Inbound queue + bounded retry | `internal/store/store.go:4612` (`EnqueueInboundMessage`), `app.go:555` (`processInboundMessages`) | `ON CONFLICT DO NOTHING` on `provider_message_id`; lease with `FOR UPDATE SKIP LOCKED`; retry with `available_at`. |
| Transactional outbox | `internal/store/store.go:5270` (`RetryOutbox`), `app.go:598` (`deliverOutbox`) | Failed sends retry with exponential backoff and die after 5 attempts. |
| 24h-window awareness (single number) | `internal/service/payments.go:325` (`resultOutbox`) | Text inside the service window, approved template outside it. |
| Provider isolation | `internal/ports/ports.go:82` (`Messenger`) | Telegram + WhatsApp behind one interface; the FSM never sees wire formats. |
| Encryption at rest / redaction | `internal/crypto`, `internal/redact` | Chat payloads sealed; provider errors redacted before persistence. |

Everything in this document builds on these rather than replacing them.

---

## 1. Typed Meta error classification and retry policy

### Problem

`Client.send` (`internal/providers/whatsapp/client.go:278`) treats any non-2xx
response as an opaque failure. `RetryOutbox` (`internal/store/store.go:5270`)
then applies one exponential backoff to *every* failure. Meta distinguishes
permanent rejections (wrong template, undeliverable number, 24h window expired)
from transient ones (rate limit, server error). Today a permanently-bad message
is retried five times over ~64 minutes before it fails, and a rate-limited
message gets the same backoff as a fatal one.

### Proposed design

1. **Parse the Graph API error payload.** The `messages` endpoint returns:
   ```json
   {
     "error": {
       "code": 131030,
       "error_subcode": 2490010,
       "message": "Rate limit hit",
       "fbtrace_id": "..."
     }
   }
   ```
   Add a typed error in `internal/providers/whatsapp/client.go`:

   ```go
   type Error struct {
       Code      int    `json:"code"`
       Subcode   int    `json:"error_subcode,omitempty"`
       Message   string `json:"message"`
       Retryable bool   `json:"-"`
       Permanent bool   `json:"-"`
   }

   func (e *Error) Error() string { return fmt.Sprintf("whatsapp code %d: %s", e.Code, e.Message) }
   ```

2. **Classify by code** in `send` when `Status != 2xx`:

   ```go
   // transient → Retryable
   {130429, "rate limit"}, {131030, "rate limit"},
   {131056, "temporarily unavailable"},
   {130472, "re-engagement window transient"}, {131042, "service temporary"},
   // permanent → Permanent
   {131026, "undeliverable number"}, {131047, "re-engagement required"},
   {131053, "undeliverable"}, {132000, "media processing error"},
   {133016, "template rejected"}, {470, "template/message outside window"},
   ```

   Keep the map in the `whatsapp` package so classification lives next to the
   wire format.

3. **Surface classification to the outbox.** Extend the outbox retry path so it
   can distinguish:
   - `Retryable` → keep the existing exponential backoff.
   - `Permanent` → fail immediately (no further attempts), write the error to
     `message_outbox.last_error`, and emit an alert row (reuse `audit_logs` or a
     new `delivery_failures` table) so an operator sees it.
   - Timeout/network error → Retryable (as today).

   Concretely: add an interface
   ```go
   // in internal/ports
   type SendClassifier interface {
       Classify(err error) (retryable, permanent bool)
   }
   ```
   and have `deliverOutbox` (`internal/app/app.go:598`) consult it before
   calling `RetryOutbox`/a new `FailOutbox`.

4. **Rate-limit backoff.** For `130429/131030`, read `error_subcode` +
   `error_details` where available and back off a multiple of the documented
   window (e.g., `available_at = now() + 1m` per call, capped) instead of the
   generic `1<<n` schedule.

### Migration / rollout

Pure additive: no schema change required if `last_error` is reused. If adding
`delivery_failures`, it is a new table (see §9 migration pattern).

### Acceptance criteria

- Unit test: `send` returns a typed `*Error` for a 400 with body
  `{"error":{"code":131026,...}}`; `Classify` reports permanent.
- Unit test: `RetryOutbox`-equivalent honors "permanent → fail now".
- Integration: outbox row for an undeliverable number is marked `failed` on
  first attempt with the code in `last_error`; a rate-limit failure retries with
  a longer delay.

### Effort / risk

~1 day. Low risk — no wire changes, only error handling. Highest observable win
for reliability.

---

## 2. Strict E.164 validation and canonicalization

### Problem

`recipientForCloudAPI` (`internal/providers/whatsapp/client.go:311`) strips
`+ - ( )` and spaces but never validates. Malformed numbers are sent to Meta and
fail with a non-obvious error. Meanwhile `normalizePhone`
(`internal/service/conversation.go:4311`) is a separate, looser normalizer used
at identity lookup, so two code paths can disagree about what a number is.

### Proposed design

1. **One canonical form.** Introduce a shared helper (recommend a small package
   `internal/number` or a method on the existing normalizer) that returns
   either a canonical E.164 string (`+2347061975340`) or an error:

   ```go
   func CanonicalE164(raw string) (string, error)
   ```

   Rules: strip ASCII whitespace/dashes/dots/parens; keep a leading `+`;
   require 8–15 digits after the country code; reject `00…` prefixes unless
   mapped; reject internal letters.

2. **Validate at ingress.** In `receiveWhatsAppWebhook` (`internal/app/app.go:703`)
   and `EnqueueInboundMessage`'s caller path, normalize `message.From` once. If a
   number fails, still record the webhook but log + reject the message row
   (do not crash the whole webhook batch).

3. **Validate at egress.** `recipientForCloudAPI` becomes:
   ```go
   func recipientForCloudAPI(to string) (string, error)
   ```
   returning a strict 9–15 digit string; `send` refuses with a typed
   `Error{Permanent: true}` before any HTTP call when the number is invalid.
   This removes the "send-then-fail-at-Meta" class of errors entirely.

### Acceptance criteria

- `CanonicalE164("+234 706 197 5340") == "+2347061975340"`.
- `CanonicalE164("hello")` errors; the WhatsApp client refuses to send to it
  without an HTTP call.
- No outbound call uses a number that did not pass canonicalization.

### Effort / risk

~0.5 day. Low risk. Do this before §7 multi-tenant routing because numbers are
the join key.

---

## 3. Interactive payload constraint enforcement + channel capabilities

### Problem

`SendInteractive` (`internal/providers/whatsapp/client.go:152`) builds lists and
buttons without enforcing Meta's hard limits. Row titles/descriptions are
truncated at the service layer (`conversation.go:4133`), but these are enforced
by Meta:

| Constraint | WhatsApp Cloud API limit |
|---|---|
| Reply buttons per message | 3 |
| Button title | 20 chars |
| Button `id` | 256 bytes |
| List `button` label | 20 chars |
| List sections | 1–10 |
| Rows per section | 1–30 |
| Row title | 24 chars |
| Row description | 72 chars |
| Row `id` | 256 bytes |

The `Messenger` port (`internal/ports/ports.go:82`) forces Telegram and WhatsApp
through one `ports.InteractiveMessage`; the FSM has no way to know WhatsApp
rejects a 4-button payload that Telegram accepts.

### Proposed design

1. **Validate in the WhatsApp client before HTTP.** Add a `validate` pass in
   `SendInteractive`: reject >3 buttons; truncate button titles to 20 runes,
   `id`s to 256 bytes; truncate list `button` label to 20 runes; enforce
   sections ≤10, rows ≤30/section. Return a typed permanent error on hard
   violations instead of sending.

2. **Add a capabilities port.** Extend `ports.Messenger` with:

   ```go
   type Capabilities struct {
       ButtonsPerMessage   int
       ButtonTitleRunes    int
       ListSectionsMax     int
       RowsPerSectionMax   int
       SupportsTemplates   bool
       SupportsMedia       bool
       SupportsInteractive bool
       FreeformWindow      time.Duration // length of customer service window
   }
   func Capabilities() Capabilities
   ```

   Telegram implements its own limits (inline keyboards are effectively
   unbounded); WhatsApp returns the table above. The FSM stays wire-agnostic but
   can now branch on capability (e.g., fall back to a list when buttons >3).

3. **Chunk long pickers by capability.** `sendMerchantPicker`, `sendDataPlanPicker`,
   etc. currently build one list per page. Use `Capabilities.RowsPerSectionMax`
   to decide chunking rather than the fixed `pickerPageSize`.

### Acceptance criteria

- A unit test feeds `SendInteractive` 4 buttons → typed permanent error, no HTTP
  request.
- A 25-char button title is truncated to 20 runes before the request.
- Telegram tests prove existing inline-keyboard behavior is unchanged.

### Effort / risk

~1 day. Low risk, additive. Pairs naturally with §1 (typed errors).

---

## 4. Richer inbound parsing: statuses, errors, context, metadata

### Problem

`ParseInbound` (`internal/providers/whatsapp/client.go:69`) only extracts text
and interactive selections. It silently ignores:

- `messages[].context` — the message a customer is replying to. Businesses and
  individuals expect "reply to the bot's last message" to work.
- `messages[].errors` / `statuses` — delivery outcomes (sent/delivered/read,
  failed). A business needs these to know a critical update never reached the
  customer.
- `entry[].id` (WABA id) and `value.metadata.phone_number_id` — which business
  number the customer actually messaged. **This is the key that multi-tenant
  routing needs and it is dropped today.**
- `contacts[].profile.name` — free display name Meta sometimes provides.
- Media messages (`image`, `document`) — a customer sending a payment-screenshot
  or a merchant sending an invoice PDF. These are common business flows.

### Proposed design

1. **Enrich `InboundMessage`** in `internal/providers/whatsapp`:

   ```go
   type InboundMessage struct {
       ID           string
       From         string          // customer wa_id
       ToPhoneID    string          // value.metadata.phone_number_id
       WABAID       string          // entry[].id
       Text         string
       Interactive  string
       ContextID    string          // messages[].context.id
       ReplyToID    string          // messages[].context.id
       ErrorCode    int             // messages[].errors[].code
       ErrorMessage string
       Status       string          // from statuses[]: sent|delivered|read|failed
       ProfileName  string
       MediaType    string          // image|document|video|audio|sticker
       MediaID      string
       Timestamp    time.Time
   }
   ```

2. **Handle statuses separately.** Delivery receipts are not conversation turns.
   `receiveWhatsAppWebhook` (`internal/app/app.go:703`) should route a payload
   whose only content is `statuses` to a `RecordDeliveryStatus` store call
   (table `delivery_status` keyed by provider message id + status + timestamp),
   and **not** enqueue an inbound FSM message. This keeps the conversation state
   machine from being fed non-conversation traffic.

3. **Persist `metadata.phone_number_id` + `entry[].id`.** Add columns to
   `inbound_messages` (`to_phone_id`, `waba_id`) — see §7 for the routing design
   that consumes them.

4. **Reply-by-context.** The FSM (`conversation.go:95` `Handle`) can treat a
   message with `ContextID` matching the last bot message id as a retry of the
   current prompt. This requires storing the outbound message id, which the
   outbox currently discards (see §6 for capturing `messages[].id` from the send
   response).

### Migration / rollout

Additive columns via a new migration; statuses handler is new code.

### Acceptance criteria

- A webhook containing only `statuses` updates `delivery_status` and enqueues
  nothing; the same webhook re-delivered is deduped.
- A media message enqueues a row with `media_type`/`media_id` populated; the FSM
  can respond with "we can't read images yet — please type the reference" rather
  than silently doing nothing.
- `phone_number_id` is stored on every inbound row.

### Effort / risk

~1.5 days. Low risk. §4.3 is a hard prerequisite for §7.

---

## 5. Channel-scoped conversation sessions (correctness bug)

### Problem

`LoadSession`/`SaveSession` (`internal/store/store.go:4574`, `4594`) key
sessions only on `user_id`. A user who starts a payment on WhatsApp and then
messages the Telegram bot mid-flow resumes the *same* state machine with a
different channel and recipient. Consequences: an interactive WhatsApp prompt is
answered on Telegram (or vice versa), the wrong messenger is used for the reply,
and the "switch flow" safety check (`isInterruptibleState`, `conversation.go:137`)
is applied across channels.

### Proposed design

1. **Scope by `(user_id, channel)`.** Change the `conversation_sessions` primary
   key from `user_id` to `(user_id, channel)`. Add a composite key migration:

   ```sql
   ALTER TABLE conversation_sessions DROP CONSTRAINT conversation_sessions_pkey;
   ALTER TABLE conversation_sessions ADD COLUMN channel TEXT NOT NULL DEFAULT 'whatsapp';
   ALTER TABLE conversation_sessions ADD PRIMARY KEY (user_id, channel);
   ```

2. **API change.** `LoadSession(ctx, userID, channel)` and
   `SaveSession(ctx, session)` where `session` carries `Channel`. The FSM is
   already threaded with `channel` everywhere — the store calls are the only
   change surface.

3. **Cross-channel resume (optional but recommended).** When a session is found
   mid-flow on a *different* channel than the incoming message, either:
   - **(a) isolate** — each channel gets a fresh menu state; or
   - **(b) migrate** — move the session to the current channel and tell the user
     "Continuing your pending payment on this chat."
   Recommend (b) for single-person identity (both channels map to the same
   verified user), implemented as an explicit `MigrateSession` store call so the
   behavior is intentional and auditable.

### Acceptance criteria

- Start a payment on WhatsApp → message Telegram → session does not leak the
  pending payment into Telegram unless migrate mode is on; with migrate mode,
  the FSM continues on Telegram and all replies go to Telegram.
- Existing tests for session expiry still pass with the new key.

### Effort / risk

~1 day. This is the one real behavioral bug; medium risk because it touches the
session table. Do it early, before multi-tenant routing makes identity joins
harder.

---

## 6. Outbound message receipts and correlation

### Problem

`Client.send` (`internal/providers/whatsapp/client.go:278`) discards the
successful response body, which contains `messages[].id`. The outbox row never
learns its Meta message id, so §4's reply-by-context and delivery tracking have
no link from "outbox row" → "customer's reply context".

### Proposed design

1. **Return the message id.** `send` decodes the 200 body:
   ```json
   {"messages":[{"id":"wamid.HBgN...","message_status":"accepted"}]}
   ```
   and returns `(messageID string, err error)`. `SendText`, `SendInteractive`,
   `SendCheckout`, `SendTemplate`, `SendImage` gain a message-id return; the
   `Messenger` port is updated accordingly (this is a breaking port change —
   update the Telegram adapter to return a synthetic id or `""`).

2. **Persist it.** `CompleteOutbox` (`internal/store/store.go:5264`) also writes
   `provider_message_id` (new column on `message_outbox`).

3. **Use it.** The FSM stores the last outbound message id in the session data;
   §4's reply-by-context matches `messages[].context.id` against it. When a
   `statuses` webhook marks that id `failed`, raise an operator alert.

### Acceptance criteria

- Every successful send stores the Meta `wamid`.
- A `failed` status for a stored id creates an alert row.
- Telegram adapter still satisfies the port (returns `""`).

### Effort / risk

~1 day (port change ripples into tests). Medium risk only because the
`Messenger` interface changes — schedule it together with §3's capabilities
port so the interface changes once.

---

## 7. Multi-tenant business WhatsApp accounts (the core proposition)

### Problem

Today exactly one WhatsApp number (one phone id / one access token / one
template locale) serves the entire product (`internal/app/app.go:83-89`). The
DB already has a first-class `merchants` concept, but the WhatsApp layer has no
notion of a *business account*: which number a customer messaged, which
merchant's token to send with, or which business's templates to use. This is the
largest gap between the product intent (businesses + individuals) and the
integration.

### Proposed design

1. **New table `whatsapp_business_accounts`:**

   ```sql
   CREATE TABLE whatsapp_business_accounts (
     id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
     merchant_id     UUID REFERENCES merchants(id) ON DELETE CASCADE,
     phone_number_id TEXT NOT NULL,
     waba_id         TEXT NOT NULL,
     display_phone   TEXT NOT NULL,            -- canonical E.164 of the business number
     access_token    TEXT NOT NULL,            -- AES-256-GCM sealed (internal/crypto)
     template_locale TEXT NOT NULL DEFAULT 'en',
     status          TEXT NOT NULL DEFAULT 'active',   -- active|suspended|disconnected
     created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
     updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
   );
   CREATE UNIQUE INDEX ON whatsapp_business_accounts (phone_number_id);
   CREATE UNIQUE INDEX ON whatsapp_business_accounts (merchant_id);
   ```

   Store `access_token` sealed (reuse `internal/crypto`); never log it. `merchant_id`
   is unique so one merchant has exactly one bot number in v1 (multiple numbers
   per merchant is a v2 extension).

2. **Client registry.** Replace the single `whatsappClient` with a
   `whatsapp.Registry`:

   ```go
   type Registry struct { /* phoneNumberID → *Client */ }
   func (r *Registry) ForPhoneNumber(phoneNumberID string) *Client
   func (r *Registry) ForMerchant(merchantID uuid.UUID) *Client
   func (r *Registry) Add(acct BusinessAccount) error
   func (r *Registry) Refresh(ctx) error   // reload from DB on a timer
   ```

   `Client` stays stateless (app secret + per-account token/phone id); the app
   secret and verify token remain global config.

3. **Inbound routing.** `receiveWhatsAppWebhook` (`internal/app/app.go:703`)
   resolves `value.metadata.phone_number_id` → account → merchant. Store
   `merchant_id` on the `inbound_messages` row. The FSM then:
   - builds the merchant picker scoped to that business's catalog when the
     customer is *not yet* mapped to the merchant, or
   - routes straight into that merchant's payment/service flow when the customer
     has a prior relationship (`users` ↔ merchant link, reuse `recent_merchants`
     or add `user_merchant_links`).

4. **Outbound routing.** `ConversationService` gains a per-merchant messenger
   lookup: `messengerFor(ctx, channel, merchantID)` returns the business's
   client. All sends inside a merchant-branded conversation use that client, so
   the customer sees the business number, and the business's 24h window and
   templates apply (see §8).

5. **Signup flow.** Extend merchant registration (`handleMerchantRegistrationDescription`,
   `conversation.go:695`) with a "connect your WhatsApp number" step: operator
   enters phone id / WABA id / token via `/admin/merchants` (token pasted into a
   form, sealed at rest). Optionally add a Meta Graph API call to fetch and store
   `display_phone` and verify ownership before `active`.

### Migration / rollout

- Migration adds the table (indexed, FKs).
- Config keeps `WHATSAPP_*` as the *platform/default* account; merchants opt in
  with their own account. Existing behavior (global number) remains for
  unassigned traffic.

### Acceptance criteria

- Two merchants, two phone numbers, two tokens: inbound to #A lands in A's
  merchant flow and is answered from #A; inbound to #B lands in B's flow and is
  answered from #B.
- A customer messaging the platform number (no merchant account) still gets the
  current global menu.
- No access token appears in logs (redact + seal verified by test).

### Effort / risk

~4–6 days. Medium risk — this is the biggest structural change and depends on
§4.3 (phone_number_id capture) and §6 (message id correlation). It is the
proposition with the most business value.

---

## 8. Per-business 24-hour window and proactive messaging

### Problem

`resultOutbox` (`internal/service/payments.go:325`) decides template-vs-text
using one global `payment.LastInboundAt > 23h`. With multi-tenant accounts the
window is per business number. And the app has *no* path for proactive business
messages (payment reminders, invoice-due nudges, thrift-cycle notifications)
because every message today is a reply inside an inbound-driven FSM.

### Proposed design

1. **Track the window per account.** Add to `whatsapp_business_accounts` (or a
   derived view):
   ```sql
   last_inbound_at TIMESTAMPTZ,   -- updated on every inbound for that phone_number_id
   ```
   A `WindowCheck(ctx, phoneNumberID, at) (open bool)` helper returns
   `open = now() - last_inbound_at < 24h`. Update it atomically in the webhook
   path (or in `EnqueueInboundMessage` for the `whatsapp` channel).

2. **Single outbound gate.** Add one function used by every sender path:

   ```go
   // returns (useTemplate, blocked, error)
   func (s *ConversationService) outboundStrategy(ctx, channel, account, msg) (Template, bool, error)
   ```

   Logic: if `window open` → interactive/text; if `window closed` and a matching
   approved template exists → template; if `window closed` and no template →
   blocked (typed permanent error, operator alert). `resultOutbox` is refactored
   onto this gate instead of its inline `LastInboundAt` check.

3. **Template catalog per business.** `template_name`/`locale` move from global
   config onto the business account (see §9). `SendTemplate`
   (`internal/providers/whatsapp/client.go:189`) already accepts name +
   parameters — it gains account selection via the registry.

4. **Proactive message queue.** New table `outbound_campaigns` (business, user,
   template, parameters, status, run_at) and a worker tick (reuse the 2s worker
   loop in `runWorkers`, `internal/app/app.go:509`) that:
   - picks due rows,
   - evaluates `outboundStrategy`,
   - writes to the existing `message_outbox` for delivery (reuse retry/backoff).

### Acceptance criteria

- Customer messages business #A; 25h later a campaign row is due → sent as the
  approved template from #A, never as free-form text.
- Customer messages #A within the window → campaign sends as interactive/text.
- A business with no approved template and a closed window → campaign fails with
  `blocked`, alert raised, nothing delivered.

### Effort / risk

~3 days. Medium risk (new worker + window semantics). Substantial product value:
this unlocks reminders/notifications, the classic WhatsApp business use case.

---

## 9. Business profile and template lifecycle

### Problem

- The WhatsApp Business Profile (about, address, email, websites, description)
  is owned by Meta per phone number. The app has no code to read or sync it, so
  merchant web settings (`/merchant/settings`, `merchantUpdateSettings`) could
  drift from what customers actually see.
- Templates are created and approved in Meta's dashboard per WABA. The app has
  no visibility into approval state, yet §8's proactive sending depends on
  "approved template exists".

### Proposed design

1. **Profile sync.** `whatsapp.Client.GetBusinessProfile(ctx)` →
   `GET /{phone_id}?fields=verified_name,display_phone,profile_picture_url,about,address,description,email,websites,vertical`
   cached in `whatsapp_business_accounts` (or a child table). Expose read-only in
   the merchant settings page with "last synced at"; a `Refresh` button and a
   daily worker re-sync. The web UI never writes these fields (Meta owns them).

2. **Template status.** Subscribe the app to the `message_template_status_update`
   webhook field in addition to `messages`. Add `webhooks/whatsapp/templates`
   handling that records `template_name, status, language, category, WABA id`
   into `whatsapp_templates`:

   ```sql
   CREATE TABLE whatsapp_templates (
     id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
     waba_id         TEXT NOT NULL,
     name            TEXT NOT NULL,
     language        TEXT NOT NULL,
     category        TEXT NOT NULL,
     status          TEXT NOT NULL,      -- APPROVED|PENDING|REJECTED|PAUSED|DISABLED
     created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
     updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
     UNIQUE (waba_id, name, language)
   );
   ```

   `outboundStrategy` (§8) queries this table for APPROVED templates only.
   `/admin/merchants/{id}` and `/merchant/settings` show template status.

### Acceptance criteria

- After a template is approved in Meta, the dashboard shows `APPROVED` within
  the webhook latency; proactive sends only ever use `APPROVED` templates.
- Merchant settings page shows the Meta-owned profile fields read-only with a
  sync timestamp.
- Rejected template → alert raised, no campaign uses it.

### Effort / risk

~2 days. Low-medium. Independent of §7 (can land on the single-number setup
first) but most valuable after it.

---

## 10. Business handoff / team inbox

### Problem

`conversation_sessions` and `inbound_messages` model a single automated
customer FSM. A real business needs humans in the loop: assign a thread to an
agent, pause the bot, reply from the agent without corrupting the FSM, and
re-enable the bot.

### Proposed design

1. **Thread ownership.** Add to `inbound_messages` and the session:
   `assigned_agent_id UUID NULL` and `bot_active BOOL NOT NULL DEFAULT true`.
2. **Pause/resume.** When `bot_active=false`, `Handle` skips the FSM switch and
   stores the message in a `conversation_log`; agent replies go through a new
   `POST /merchant/conversations/{id}/reply` that sends via the business client
   (using `outboundStrategy`) and records the agent as author.
3. **Agent reply ≠ customer FSM.** Agent text sends use `SendText` directly, not
   the FSM menu; the outbox `author` column (`agent`/`bot`/`system`) keeps
   attribution.
4. **UI.** `/merchant/conversations` list (customer, last message, status,
   assigned agent) + detail view with a message thread and reply box. This can
   ship as a thin page over `inbound_messages` + `message_outbox`.

### Acceptance criteria

- An agent can pause the bot, reply, and resume without the customer's FSM state
  breaking.
- Replies are attributed to the agent in the thread UI and audit log.
- Customer's next message resumes the FSM at the same state it was paused.

### Effort / risk

~3–4 days. Medium. This is the natural product for "businesses"; pair it with
§7 so it works per merchant number.

---

## 11. Persona-aware customer menus

### Problem

Every user — including individuals who only buy data or pay merchants — sees the
full merchant/thrift menu (`mainMenuRows`, `merchantServicesRows`,
`internal/service/conversation.go:3200-3230`). Merchant tools leak to plain
customers and the menu is cluttered.

### Proposed design

1. **Persona from DB state.** Derive persona in `Handle` once:
   ```go
   type persona int
   const (personaIndividual persona = iota; personaMerchant; personaCustomer)
   func (s *ConversationService) personaFor(ctx, user) persona
   ```
   Rules: `user.AccountLevel == "merchant"` → merchant; KYC tier ≥ L2
   (`internal/kyc`) or thrift member → individual; else customer.
2. **Menu builders take persona.** `mainMenuRows(persona)` drops:
   - merchant rows (register/invoice/dashboard) for non-merchants,
   - "become individual" for existing individuals,
   - thrift rows for customers until they choose "become individual" (keep one
     entry point).
3. **Keyword parity.** Keep text keywords (`merchant registration`, etc.)
   working but gate them to the persona; a customer typing a merchant-only
   keyword gets a short "not available for your account type" reply with the
   path to upgrade.

### Acceptance criteria

- A fresh customer menu has ≤4 rows (pay, buy data, thrift, help) vs 7 today.
- A merchant menu omits "become individual".
- Existing manual-acceptance-script flows still work (keywords accepted).

### Effort / risk

~1 day. Low. Pure presentation logic in the FSM.

---

## 12. Free-form FSM refactor (supporting change)

### Problem

`internal/service/conversation.go` is 4,495 lines with a ~60-case string
`switch` in `Handle` (`conversation.go:163-292`) and every handler signature
`(ctx, channel, recipient, user, session, input)` — six positional args threaded
through ~100 functions. Adding business-aware routing (§7, §8) on top of this is
where it will get expensive.

### Proposed design

1. **Typed session context.** Introduce
   ```go
   type SessionContext struct {
       Channel   string
       Recipient string
       User      store.User
       Session   store.Session
       Input     string
       MerchantID *uuid.UUID   // resolved by §7 routing
   }
   ```
   and rewrite handlers as `func (s *ConversationService) handleAmount(c SessionContext) error`. Mechanical, compiler-checked.

2. **Data-driven transitions.**
   ```go
   var handlers = map[string]func(*ConversationService, SessionContext) error{
       "select_merchant":        (*ConversationService).handleMerchant,
       "enter_amount":           (*ConversationService).handleAmount,
       // ... one line per state
   }
   ```
   `Handle` becomes a lookup + `notFound → handleMenu`. New states are additive
   and the switch no longer grows.

3. **Per-concern files.** Move handlers into `conversation_payments.go`,
   `conversation_thrift.go`, `conversation_invoice.go`, `conversation_onboarding.go`,
   `conversation_data.go`, `conversation_merchant.go`, `conversation_shared.go`.
   No behavior change; pure re-organization with the session context.

### Acceptance criteria

- `go test ./internal/service/...` green with zero behavior diffs.
- `Handle`'s transition logic fits on one screen.
- Grep shows no remaining 6-arg handler signature.

### Effort / risk

~3–4 days. Low behavioral risk (mechanical), but large diff. Do *after* §5
(channel scoping) and §7 (routing) so the new context type already carries what
those need.

---

## 13. WhatsApp Flows for structured collection (optional, later)

### Problem

Free-text parsing is used for invoice line items (`parseCommaSeparatedFields`,
`conversation.go:4167`), thrift creation, custom fields, and KYC forms. Parsing
errors produce back-and-forth repair messages and brittle UX.

### Proposed design

1. **Flow messages.** Add `SendFlow(ctx, to, flowID, screen, data)` to the
   WhatsApp client: `interactive.type = "flow"` with `nfm_reply` handling in
   `ParseInbound` returning the JSON payload as `InboundMessage.FlowData`.
2. **Adopt for the highest-friction forms first:** invoice line items and
   merchant registration. The FSM state receives structured JSON instead of
   free text; keep free-text as the fallback for Telegram (which has no flows).
3. **Port capability flag** (`SupportsFlows`) so the FSM picks the form
   rendering per channel.

### Acceptance criteria

- Invoice creation on WhatsApp uses one flow; a submitted payload creates the
  invoice without parse-repair loops.
- Telegram continues to use the text path.
- A customer on an older WhatsApp client gets a graceful text fallback.

### Effort / risk

~3 days. Medium risk (new interactive type + Meta dashboard flow configuration).
High polish value, low urgency.

---

## 14. Phased roadmap

| Phase | Propositions | Rationale | Effort |
|---|---|---|---|
| **P0 — correctness & wire fidelity** | §5 sessions, §2 E.164, §1 error classification, §3 interactive limits | Real bugs + reliability, all additive, no schema-heavy work | ~3.5 days |
| **P1 — visibility** | §4 inbound enrichment, §6 outbound message ids | Prerequisites for routing; gives delivery telemetry | ~2.5 days |
| **P2 — multi-tenant business** | §7 business accounts, §8 per-business window, §9 templates/profile | The core business proposition | ~6–8 days |
| **P3 — human-in-the-loop** | §10 team inbox, §11 persona menus | Business usability | ~4–5 days |
| **P4 — FSM hygiene** | §12 refactor (can start earlier in parallel), §13 flows | Keep the codebase scalable | ~4–7 days |

Ordering rule: nothing in a later phase depends on an earlier proposition except
where stated (P1 → P2, P0.§2 → P2).

## 15. Open questions for the operator

1. **Multi-tenant tokens.** Will merchants bring their own Meta WABA + number
   (bring-your-own), or does Xego operate a shared WABA with many numbers under
   one system-user token? BYO changes §7's token handling (per-account sealing
   stays, but phone-number provisioning differs).
2. **Merchant outreach model.** Do businesses primarily *receive* customer
   messages (respond to inbound) or *proactively* message customers (campaigns)?
   This decides how much of §8's campaign machinery to build now.
3. **Agent inbox priority.** Is a human handoff a launch requirement or a v2?
   If v2, defer §10 and ship §11 persona menus in its place.
4. **Multiple numbers per merchant.** v1 assumes one number per merchant; confirm
   that before the unique index in §7 is locked in.
5. **Flows.** Is the Meta dashboard a realistic operational dependency? If yes,
   schedule §13's Flow forms ahead of P4.
