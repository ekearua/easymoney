# Xego

Xego is a Nigeria-focused merchant checkout demo that uses WhatsApp Cloud API as the customer interface, Paystack hosted checkout in test mode, and a simulated bank-to-bank transfer rail. It does not hold money, issue balances, settle merchants, or receive card data.

## Architecture

```text
Customer WhatsApp
       │ signed webhook
       ▼
Go service ─────── PostgreSQL
   │                   ├─ payment state and audit events
   │                   ├─ durable webhook/inbound queues
   │                   └─ transactional message outbox
   │
   ├─ initialize/verify ── Paystack test API
   ├─ outbound messages ─ WhatsApp Cloud API
   └─ read-only admin + tokenized receipts
```

Card payment success is written only after the backend calls Paystack verification and confirms the reference, amount, currency, test domain, card channel, and available payment/merchant metadata. Bank-transfer success is demo-only and is written only after the customer taps **I have transferred** against generated transfer instructions.

## Local development

Requirements:

- Go 1.26.x
- PostgreSQL 15 or newer

Copy `.env.example` to `.env`, provide a local `DATABASE_URL`, then load the variables into your shell. Generate the admin bcrypt hash interactively:

```powershell
go run ./cmd/demo hash-password
```

In the Docker Compose `.env` file, keep the bcrypt hash inside single quotes so its `$` characters remain literal.

Run the service:

```powershell
go run ./cmd/demo migrate
go run ./cmd/demo seed
go run ./cmd/demo server
```

Useful commands:

```powershell
go test ./...
go vet ./...
go run ./cmd/demo reconcile
go run ./cmd/demo retain
```

## Oracle Free Tier deployment

1. Reserve the VM's public IPv4 address.
2. Create the hostname by replacing dots with hyphens: `203.0.113.10` becomes `203-0-113-10.sslip.io`.
3. In the Oracle VCN security list and the VM firewall, allow inbound TCP 80/443 and UDP 443.
4. Install Docker Engine and the Compose plugin on the VPS.
5. Copy the repository and create `.env` from `.env.example`.
6. Set `PUBLIC_HOST` to the sslip.io hostname and `BASE_URL` to its HTTPS URL.
7. Start the stack:

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f app caddy
```

Caddy obtains and renews TLS automatically. If the reserved public IP or hostname changes, update `PUBLIC_HOST`, `BASE_URL`, Meta's callback URL, Paystack's webhook URL, and the approved WhatsApp template link policy as applicable.

Back up the `postgres_data` Docker volume before upgrades. Keep the database port private; only Caddy exposes public ports.

## Provider setup

### WhatsApp Cloud API

- Callback URL: `https://<host>/webhooks/whatsapp`
- Verify token: the exact `WHATSAPP_VERIFY_TOKEN`
- Subscribe the app to the `messages` field.
- Configure the permanent/system-user access token and phone-number ID.
- Set `WHATSAPP_GRAPH_VERSION` explicitly to a currently supported version from the Meta app dashboard; production has no silent version default.
- Create an English utility template named by `WHATSAPP_STATUS_TEMPLATE` with four body parameters:
  1. status
  2. amount
  3. merchant
  4. receipt URL

The service validates `X-Hub-Signature-256` against the unmodified request body.

### Paystack

- Use only an `sk_test_...` secret.
- Webhook URL: `https://<host>/webhooks/paystack`
- Callback URL is supplied per transaction as `https://<host>/payments/return`.
- Enable card checkout for the test integration.

The callback never marks a transaction successful by itself. Both callback and webhook invoke server-side verification.

## Manual acceptance script

1. Message the configured WhatsApp number.
2. Enter a name and email.
3. Choose **Make payment**, a fictional merchant, and an amount from ₦100 to ₦100,000.
4. Choose **Card checkout** or **Bank transfer**.
5. For card checkout, confirm the test-mode summary, open Paystack checkout, and use an official Paystack test card.
6. For bank transfer, choose a Nigerian collection bank, review the demo account details, then tap **I have transferred**. Do not send real money.
7. Confirm that WhatsApp reports the final result and the receipt URL displays the same status and provider.
8. Sign into `/admin/login` and inspect metrics, payments, masked users, merchants, and webhook processing.
9. Repeat the Paystack webhook and confirm the payment and notification are not duplicated.

## Security and retention

- Card number, CVV, PIN, OTP, balances, and reusable authorization data are never stored.
- Webhook bodies are read once for signature verification; only normalized Paystack event/reference data and normalized WhatsApp message fields are queued.
- Admin sessions store only token hashes and use secure, HTTP-only, same-site cookies in production.
- Personally identifiable demo records are removed after 90 days by the daily retention worker or `retain` command.
- Receipt URLs are unguessable bearer capabilities. Do not publish them.

## Production limitations

This is an investor demo, not a licensed payment processor. It has no KYC, AML monitoring, merchant onboarding, disputes, refunds, payouts, ledger, settlement, reconciliation reports, or live-money authorization.
