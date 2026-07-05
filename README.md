# Xego

Xego is a Nigeria-focused merchant checkout experience that uses WhatsApp Cloud API and Telegram Bot API as customer interfaces, hosted card checkout, and a bank-transfer payment path. It does not store card data in chat.
It also supports a mobile data purchase flow for MTN, Airtel, Glo, and 9mobile using a simulated fulfilment provider.

## Architecture

```text
Customer WhatsApp / Telegram
       | signed webhooks
       v
Go service -------- PostgreSQL
   |                 |- payment state and audit events
   |                 |- durable webhook/inbound queues
   |                 `- transactional message outbox
   |
   |- initialize/verify -> card checkout provider API
   |- outbound messages -> WhatsApp Cloud API / Telegram Bot API
   `- read-only admin + tokenized receipts
```

Card payment success is written only after the backend calls provider verification and confirms the reference, amount, currency, channel, and available payment/merchant metadata. Bank-transfer success is written only after the customer taps **I have transferred** against generated transfer instructions.

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
4. Install Docker Engine and the Compose plugin on the VPS, or deploy the native systemd binary if using the existing non-Docker setup.
5. Copy the repository and create `.env` from `.env.example`.
6. Set `PUBLIC_HOST` to the sslip.io hostname and `BASE_URL` to its HTTPS URL.
7. Start or restart the service.

Caddy obtains and renews TLS automatically. If the reserved public IP or hostname changes, update `PUBLIC_HOST`, `BASE_URL`, Meta's callback URL, Telegram's webhook URL, Paystack's webhook URL, and the approved WhatsApp template link policy as applicable.

Back up PostgreSQL before upgrades. Keep the database port private; only Caddy exposes public ports.

## Provider setup

### Email confirmation

Onboarding can require a 6-digit email confirmation code before the user reaches the main menu.

For real email delivery, configure:

```env
EMAIL_CONFIRMATION_ENABLED=true
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USERNAME=replace-me
SMTP_PASSWORD=replace-me
SMTP_FROM=hello@example.com
```

For a local or investor-demo rehearsal before SMTP is ready, set `EMAIL_DEMO_CODE_IN_CHAT=true`. Do not use the chat fallback for production-like testing because it proves the flow, not email ownership.

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

### Telegram Bot API

- Create a bot with BotFather and set `TELEGRAM_ENABLED=true`.
- Store `TELEGRAM_BOT_TOKEN` and a random `TELEGRAM_WEBHOOK_SECRET`.
- Webhook URL: `https://<host>/webhooks/telegram`
- Register the webhook:

```bash
curl -X POST "https://api.telegram.org/bot$TELEGRAM_BOT_TOKEN/setWebhook" \
  -d "url=$BASE_URL/webhooks/telegram" \
  -d "secret_token=$TELEGRAM_WEBHOOK_SECRET" \
  -d "drop_pending_updates=true"
```

The service validates `X-Telegram-Bot-Api-Secret-Token` before accepting Telegram updates.

### Paystack

- Use only an `sk_test_...` secret.
- Webhook URL: `https://<host>/webhooks/paystack`
- Callback URL is supplied per transaction as `https://<host>/payments/return`.
- Enable card checkout for the test integration.

The callback never marks a transaction successful by itself. Both callback and webhook invoke server-side verification.

### SMS data request codes

- Enable with `SMS_ENABLED=true`.
- Webhook URL: `https://<host>/webhooks/sms`
- Send the shared secret in `X-SMS-Webhook-Secret` or `X-Xego-SMS-Secret`.
- JSON payload fields: `id` or `message_id`, `from` or `sender`, and `body` or `text`.
- Form-encoded payloads with the same field names are also accepted.

Supported inbound SMS commands:

```text
DATA MTN MTN1GB 08031234567
PLANS MTN
STATUS XG-DATA-8K2Q
DATA HELP
```

The SMS MVP returns the reply text from the webhook response. A live outbound SMS sender can be connected later without changing the order lifecycle.

### VTPass sandbox data fulfilment

The default data provider is the local simulator. To use VTPass sandbox:

```env
DATA_PROVIDER=vtpass
VTPASS_BASE_URL=https://sandbox.vtpass.com/api
VTPASS_API_KEY=replace-with-vtpass-api-key
VTPASS_PUBLIC_KEY=replace-with-vtpass-public-key
VTPASS_SECRET_KEY=replace-with-vtpass-secret-key
VTPASS_TIMEOUT=45s
```

In the VTPass sandbox dashboard, enable API access and whitelist the data products. To import all currently available MTN, Airtel, Glo, and 9mobile bundles into Xego, run:

```bash
go run ./cmd/demo sync-vtpass-data-plans
```

On the server, use a root shell to read the protected env file safely:

```bash
sudo bash -c 'set -a; source /etc/whatsapp-payment.env; set +a; /opt/whatsapp-payment/whatsapp-payment-demo sync-vtpass-data-plans'
```

The sync stores each VTPass `variation_code` as `data_plans.provider_sku`, and generates an SMS-safe Xego plan code from it. You can inspect imported bundles with:

```sql
SELECT n.code AS network, p.code, p.display_name, p.provider_sku, p.price_kobo
FROM data_plans p
JOIN data_networks n ON n.id = p.network_id
ORDER BY n.sort_order, p.sort_order;
```

The VTPass adapter maps Xego networks to `mtn-data`, `airtel-data`, `glo-data`, and `etisalat-data`, sends the plan `provider_sku` as `variation_code`, and uses the Xego request code to create a VTPass `request_id`. If VTPass times out before returning headers, Xego keeps the order pending/retryable and stores the attempted `request_id` so the next retry does not create a second VTPass transaction.

Large VTPass catalogs are shown in chat as paged lists. Customers can select visible plans, tap next/previous page, or type a search term such as `1GB`, `2GB`, `weekly`, or `monthly`.

Optional VTPass callback URL:

```text
https://<host>/webhooks/vtpass?secret=<VTPASS_WEBHOOK_SECRET>
```

If `VTPASS_WEBHOOK_SECRET` is set, Xego also accepts the same value in `X-VTPass-Webhook-Secret`. VTPass callbacks are recorded in `/admin/webhooks` and can mark pending data orders fulfilled or failed by provider reference/request id.

## Manual acceptance script

1. Message the configured WhatsApp number or Telegram bot. Use `/start` on Telegram.
2. Enter a name and email.
3. Enter the 6-digit email confirmation code. Configure SMTP for real email delivery, or set `EMAIL_DEMO_CODE_IN_CHAT=true` while testing the demo flow.
4. Confirm the WhatsApp number or Telegram account.
5. Choose **Make payment**, select a merchant, and enter an amount from ₦100 to ₦100,000. If the merchant list is long, type a merchant name/category to search or use the page controls. Recently selected merchants appear first.
6. Choose **Card checkout** or **Bank transfer**.
7. For card checkout, confirm the payment summary, open secure checkout, and complete the provider flow.
8. For bank transfer, choose a collection bank from the bank list. You can browse pages or type a bank name to search. Review the account details, enter the reference in your bank app narration/remark/reference field, then tap **I have transferred**.
9. Confirm that the chat reports the final result and the receipt URL displays the same status and provider.
10. Sign into `/admin/login` and inspect metrics, payments, masked users, merchants, and webhook processing.
11. Repeat the Paystack webhook and confirm the payment and notification are not duplicated.
12. Choose **Buy Data**, select a network and plan, enter a beneficiary phone number, pay, and confirm the data order becomes fulfilled after payment success.
13. Post an SMS command to `/webhooks/sms` and confirm the response contains a request code and checkout URL.

## Security and retention

- Card number, CVV, PIN, OTP, balances, and reusable authorization data are never stored.
- Webhook bodies are read once for signature verification; only normalized provider event/reference data and normalized chat message fields are queued.
- Admin sessions store only token hashes and use secure, HTTP-only, same-site cookies in production.
- Personally identifiable records are removed after 90 days by the daily retention worker or `retain` command.
- Receipt URLs are unguessable bearer capabilities. Do not publish them.

## Production limitations

This build is not a licensed payment processor. Before live-money use, add KYC, AML monitoring, merchant onboarding, disputes, refunds, payouts, ledger, settlement, reconciliation reports, and live-money authorization controls.
