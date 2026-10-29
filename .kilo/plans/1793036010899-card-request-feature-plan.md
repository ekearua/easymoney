# Card Request Feature Plan

## Goal
Add ability for a customer to request a physical card tied to their wallet account, and view balances on their cards, using Interswitch Card 360.

## Scope (in)
- Request physical card issuance for a customer's wallet account
- View balance on issued cards
- Interswitch Card 360 integration (passport OAuth auth)

## Scope (out)
- Card personalization / data prep (HSM keys)
- Card PIN change, block/unblock
- 3DS challenge flows

## Affected boundaries
- `internal/ports/ports.go` — new `CardIssuer`/`CardBalance` interfaces
- `internal/providers/interswitch/card360.go` — new adapter
- `internal/config/config.go` — Card 360 credentials (passport username/password, base URL)
- `internal/store` — card records table + migration
- `internal/app` — handlers for request + balance view
- `web/templates` — card request/balance UI

## Data flow
1. Customer requests card → app calls Card 360 client registration + issuance
2. Card record stored with PAN (masked), expiry, status, linked wallet
3. Balance inquiry calls Card 360 balance endpoint with permission

## Failure modes
- Passport auth failure → surface error, no partial state
- Insufficient permissions (view_balance) → 403, prompt admin config
- HSM/key config mismatch → fail fast at startup

## Validation
- Unit tests against httptest fakes for Card 360 client
- Integration test for request + balance round trip

## Open questions
- Exact Card 360 endpoint paths and request shapes (need dev docs beyond overview)
- Whether card issuance requires client registration first (yes per overview)
- Permissions model for balance view (passport auth_client_details)
