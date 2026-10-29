# Plan: Add Facebook Messenger Channel

## 1. Goal
Add Facebook Messenger as a customer channel, mirroring the existing Instagram
adapter. Meta app submission scope: one channel, no new payment rails.

## 2. Verified API Facts (Meta docs, updated Sep-Oct 2026)
- Messenger and Instagram Messaging share one Send API: `POST /<PAGE_ID>/messages`,
  recipient ID, Page access token. Instagram uses IGSID; Messenger uses PSID.
- Outbound messages require the customer to message you first (24h standard
  window; 7 days only for Click-to-Messenger / Click-to-Direct ad first messages).
- As of 2026-04-27, `CONFIRMED_EVENT_UPDATE`, `ACCOUNT_UPDATE`,
  `POST_PURCHASE_UPDATE` message tags return error 100.
- Instagram DMs already work in this codebase (`internal/providers/instagram`).

## 3. Scope Decisions
1. Facebook Messenger: lighter-weight integration (same Send API as Instagram).
2. Target: live against real Meta Graph APIs.
3. Instagram tagging: dropped — Meta has no `tags` webhook field; `mentions` is
   bundled inside `comments` and requires Advanced Access; no payment mechanism
   exists for tags.
4. Instagram DM: already implemented — no new work.
5. Threads: excluded — no DM inbound webhook or send API. Threads webhooks
   (`replies`, `mentions`, `publish`) cover public posts/replies, not conversations.
6. Scope lock: WhatsApp, Telegram, Instagram, TikTok, SMS, Facebook Messenger.
   No new payment rails, fees, or settlement features.

## 4. Implementation Tasks

### 4.1 New adapter: `internal/providers/facebookmessenger/client.go`
Mirror `internal/providers/instagram/client.go`. Differences:
- Identity is the Page-scoped ID (PSID), not IGSID.
- `New(appSecret, accessToken, pageID, graphVersion)`.
- `ValidateSignature(body, signature)` — HMAC-SHA256 over raw body with app secret,
  `sha256=` prefix (same as Instagram).
- `ParseInbound(body)` — Messenger object; sender.id is the PSID.
- Send methods: `SendText`, `SendInteractive` (quick replies / generic template
  buttons), `SendLink` (generic template `web_url` button), `SendCheckout`,
  `SendTemplate` (degrade to text), `SendImage` (degrade to text — no binary
  upload), `SendLocationRequest` (unsupported — return error, callers fall back
  to browser link).
- `DownloadFile(ctx, attachmentID, mediaURL)` — Graph `/media-id` resolve then
  download, 16 MiB cap.

### 4.2 Migration: `internal/store/migrations/104_facebook_messenger_channels.sql`
Follow `064_instagram_tiktok_channels.sql` exactly. Add to `users`:
```sql
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS facebook_psid text UNIQUE,
    ADD COLUMN IF NOT EXISTS facebook_username text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS facebook_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS facebook_confirmed_at timestamptz;
```
Note: migration ordering is enforced by the store's runner; verify the existing
ordering convention before adding (the last is `103_pos_agent_network.sql`).

### 4.3 Store: `internal/store/store_users.go` (or wherever channel identity lives)
Add `FacebookMessengerPSID`, `FacebookMessengerUsername`,
`FacebookMessengerVerifiedAt`, `FacebookMessengerConfirmedAt` fields to the
`User` struct, plus lookups `FindUserByFacebookMessengerPSID` and the
`MergeUsers` channel-handle branch (`ChannelFacebookMessenger`).

### 4.4 Service: `internal/service/conversation.go`
Add `ChannelFacebookMessenger = "facebookmessenger"` constant. Add the channel
to `normalizeChannel`, `onboardingCompleteForChannel`, `channelHandleForUser`,
`channelDisplayName`, and the cross-channel carry-over logic so an approved
customer gets the one-time intro + menu instead of re-onboarding.

### 4.5 Config: `internal/config/config.go`
Add fields:
```go
FacebookMessengerEnabled      bool
FacebookMessengerAppSecret    string
FacebookMessengerAccessToken  string
FacebookMessengerPageID       string
FacebookMessengerVerifyToken  string
FacebookMessengerGraphVersion string
```
Wire them from env in the loader (same pattern as `Instagram*`).

### 4.6 App wiring: `internal/app/app.go`
- In `New`: construct `facebookClient` when `cfg.FacebookMessengerEnabled`,
  add to `messengers[service.ChannelFacebookMessenger]`.
- Store `facebookmessengerClient` on the `App` struct.
- In `routes()`: add
  `router.Get("/webhooks/facebookmessenger", a.verifyFacebookMessengerWebhook)`
  and `router.With(webhookLimit).Post("/webhooks/facebookmessenger", a.receiveFacebookMessengerWebhook)`.
- In `Download`: add a `service.ChannelFacebookMessenger` case.
- Add the `receive*`/`verify*` handler methods mirroring the Instagram ones.

### 4.7 Docs: `README.md` and `.env.example`
Add a "Facebook Messenger" section mirroring "Instagram Messaging" and the new
env vars.

## 5. Validation
- `go build ./...`
- `go vet ./...`
- `go test ./...`
- Confirm the migration runner accepts `104_*` (run `go run ./cmd/demo migrate`
  against a scratch DB).

## 6. Out of Scope (do not add)
- Instagram tagging (no API surface).
- Threads (no DM webhook/send API; separate publishing use case if wanted later).
- New payment rails, fees, or settlement features.