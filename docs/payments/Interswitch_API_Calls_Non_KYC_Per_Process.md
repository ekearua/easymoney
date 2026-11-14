# Interswitch API Calls (Non-KYC/KYB) — Per Process

## Scope and notes
- **In scope:** Interswitch/Quickteller API calls made by this codebase for payments, collections, wallets, transfers/payouts, refunds, VTU/data, and related verification/reconciliation flows.
- **Excluded:** KYC/KYB calls (e.g. identity verification, screening/sanctions/PEP, CAC/corporate registry, FIRS/tax ID and other verification/adapters in `internal/providers/interswitch/kyc.go`).
- **Source of truth:** Current implementation in `internal/providers/interswitch/*` and their call sites in `internal/app/*` and `internal/service/*` (including recent uncommitted alignment changes). Where behavior depends on environment/config, the configured/default host is noted.
- **Security:** This document does **not** include secrets. Wallet/customer PINs and sensitive credentials are sent only in secure provider requests as required and must never be logged, stored, or exposed in chat/UI.

## Cross-cutting: OAuth access token acquisition
Used by all direct API calls that authenticate with Bearer tokens (virtual accounts, standing VAs, merchant/customer wallets, transfers/name enquiry/transaction query, refunds, VTU). Calls retry once on `401` with a refreshed token.

| Item | Details |
|---|---|
| **Process/trigger** | On-demand when `TokenManager.AccessToken` is needed; cached until near expiry (refreshed ~5 minutes before expiry). |
| **Endpoint** | `POST {tokenBaseURL}/passport/oauth/token` |
| **Host/base URL** | Derived by client config: sandbox API host uses `https://passport-sandbox.interswitchng.com`; otherwise defaults to `{baseURL}/passport/oauth/token` unless `TokenURL` overridden. |
| **Auth** | HTTP Basic (`Authorization: Basic base64(client_id:client_secret)`), `Content-Type: application/x-www-form-urlencoded`. |
| **Key request** | Form: `grant_type=client_credentials`, `scope=profile`. |
| **Key response** | `access_token`, `token_type`, `expires_in`, `merchant_code`, `terminalId`, `env`. Terminal ID from token (or configured fallback) is used by some authorized calls. |
| **Code** | `internal/providers/interswitch/token.go` (`TokenManager`, `AccessToken`, `refresh`), wired in `client.go` (`New`, `doAuthorized*`, `terminalID`). |

## 1. Card checkout (Hosted Fields / Web Checkout) and payment verification
Covers card payment initialization, customer-facing checkout rendering, post-return/webhook confirmation, and authoritative requery.

### 1.1 Initialize checkout (platform-side; no direct Interswitch API call)
| Item | Details |
|---|---|
| **Process/trigger** | Start card checkout/payment session. |
| **Call made** | No outbound Interswitch HTTP API call at initialization. Returns a platform-hosted checkout URL (`/checkout/interswitch/{reference}`) that renders the form/Hosted Fields page. |
| **Hosted Fields SDK (browser-side)** | Page config loads Interswitch Hosted Fields SDK script (default per mode: TEST `https://hostedifelds.qa.interswitchng.com/sdk.js`, LIVE `https://hostedfields.interswitchng.com/sdk.js`, overridable). SDK `paymentParameters` include merchant/pay item/reference/amount/currency/redirect and `fields` for `cardNumber`, `expirationDate`, `cvv`, `pin`, `otp`. Card data stays in Interswitch-hosted iframes. |
| **Legacy Web Checkout (browser-side)** | Redirect form posts to `{checkoutBaseURL}/collections/w/pay` with `merchant_code`, `pay_item_id`, `txn_ref`, `amount`, `currency` (NGN numeric `566`), `cust_id/email`, `site_redirect_url`, `mode`. |
| **Hosts/config** | Checkout host defaults: TEST `https://newwebpay-sandbox.interswitchng.com`, LIVE `https://newwebpay.interswitchng.com` (overridable). |
| **Code** | `client.go` (`Initialize`, `PayPageURL`, `NewPayPage`, `payPath`), `page.go` (`HostedFieldsSDKURL`, `NewHostedFieldsPage`), `internal/app/checkout.go`, `internal/service/payments.go`. |

### 1.2 Authoritative transaction requery / verification
| Item | Details |
|---|---|
| **Process/trigger** | Confirm payment outcome on customer return/callback, inbound gateway webhook signal, reconciliation, and other verification paths before treating payment as successful. |
| **Endpoint** | `GET {baseURL}/collections/api/v1/gettransaction.json?merchantcode={merchantCode}&transactionreference={reference}&amount={amountKobo}` |
| **Host/base URL** | API/requery host `baseURL` (configured `INTERSWITCH_BASE_URL`). |
| **Auth/headers** | Legacy **InterswitchAuth** signature (not Bearer): `Authorization: InterswitchAuth base64(clientID)`, `Timestamp`, `Nonce`, `Signature` (HMAC-SHA1 over `METHOD&urlencoded(endpoint)&timestamp&nonce&clientID&secret`, base64), `SignatureMethod: SHA1`, `Content-Type: application/json`. |
| **Key request params** | `merchantcode`, `transactionreference` (reference/txnref), `amount` in minor units (required by requery). |
| **Key response fields** | `Amount`, `MerchantReference`, `PaymentReference`, `RetrievalReferenceNumber`, `TransactionDate`, `ResponseCode`, `ResponseDescription`, `CardNumber`, `AccountNumber` (as returned). |
| **Outcome mapping** | Normalized to provider-neutral status/message (`success`/`pending`/`failed`/`abandoned`) via `interswitchStatus` (e.g. `00/10/11` success, pending/intermediate codes pending, cancellations abandoned, known declines/other terminal failures failed). Returned as `ports.Verification` with metadata including response code/description. |
| **Code** | `client.go` (`Verify`, `newRequeryRequest`, `applyInterswitchAuthHeaders`, `interswitchSignature`, `interswitchStatus`, `getTransactionPath`), used by `internal/service/payments.go` (`VerifyAndApply`), `internal/app/checkout.go`, `internal/app/webhooks.go`, reconciliation/app flows. |

### 1.3 Inbound Interswitch webhook validation (receiver endpoint)
| Item | Details |
|---|---|
| **Process/trigger** | Receive gateway notification; treated as a signal to requery/confirm (not sole authority). |
| **Endpoint** | Inbound to Xego: webhook handler (e.g. `POST /webhooks/interswitch` per deployment config). No outbound Interswitch API call for validation itself. |
| **Auth/validation** | `X-Interswitch-Signature` header verified as HMAC-SHA512 over raw request body using configured webhook secret (mandatory). Parses event payload (`event`, `uuid`, `timestamp`, `data.merchantReference`/etc.) to identify reference, then triggers authoritative requery (1.2). |
| **Code** | `client.go` (`ValidateWebhook`), `internal/app/webhooks.go`. |

### 1.4 Gateway/host health check (operational)
| Item | Details |
|---|---|
| **Process/trigger** | Health/reachability check when invoked. |
| **Endpoint** | `GET {baseURL}/` |
| **Auth** | None (plain GET). |
| **Key response/outcome** | Transport-level reachability; response body not interpreted beyond successful request/close. |
| **Code** | `client.go` (`Health`). |

## 2. Bank transfer collection — Dynamic Virtual Account (per-payment)
| Item | Details |
|---|---|
| **Process/trigger** | Generate a one-time virtual account for a specific payment/collection so customer can pay by bank transfer within a validity window. Confirmation follows provider notification/requery flows. |
| **Endpoint** | `POST {baseURL}/paymentgateway/api/v1/virtualaccounts/transaction` |
| **Host/base URL** | API host `baseURL`. |
| **Auth/headers** | OAuth Bearer token (`Authorization: Bearer <token>`), `Content-Type: application/json`, `TerminalId` header when available. |
| **Key request** | `merchantCode`, `payableCode` (pay item), `currencyCode` (`566`/NGN), `amount` (string, minor units), `accountName`, `transactionReference`. |
| **Key response** | `accountNumber`, `accountName`, `bankName`, `amount`, `transactionReference`, `responseCode`, `validityPeriodMins`. Returned as transfer instruction (account details, reference, expiry/validity). |
| **Code** | `internal/providers/interswitch/dva.go` (`Transfer`, `virtualAccountPath`), via `doAuthorized` in `oauth.go`; used by `internal/service/payments.go` and app transfer flows. |

## 3. Standing virtual account provisioning and linked-credit confirmation

### 3.1 Reserve/provision standing virtual account
| Item | Details |
|---|---|
| **Process/trigger** | Provision/reuse a long-lived virtual account for a wallet holder/customer to receive bank-transfer credits. |
| **Endpoint** | `POST {baseURL}/paymentgateway/api/v1/standva/transaction` |
| **Host/base URL** | API host `baseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request** | `merchantCode`, `payableCode`, `currencyCode` (`566`), `accountName`, `transactionReference`. |
| **Key response** | `accountNumber`, `accountName`, `bankName`, `transactionReference`, `responseCode`. |
| **Code** | `internal/providers/interswitch/standva.go` (`ReserveStandingAccount`, `standingVirtualAccountPath`). |

### 3.2 Confirm linked VA credit via wallet transaction lookup (provider-wallet mode)
| Item | Details |
|---|---|
| **Process/trigger** | When standing credits require provider verification, confirm a bank-transfer credit to the linked Interswitch wallet before mirroring into local ledger; unresolved/not-found remains pending for reconciliation. |
| **Endpoint** | `GET {walletBaseURL}/api/v1/transaction/?merchantCode={merchantCode}&reference={reference}` |
| **Host/base URL** | Merchant Wallet API `walletBaseURL` (default `https://merchant-wallet.k8.isw.la/merchant-wallet`, overridable). |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request params** | `merchantCode`, `reference` (credit/transaction reference). |
| **Key response** | Array of transaction records; matched by reference/payment reference. Fields used include `reference`/`paymentReference`, `responseCode`, `transactionDirection`, `amount`, `currencyCode`, `merchantWalletId`, `reversed`. Interpreted as confirmed credit only when provider indicates successful, non-reversed CREDIT matching wallet/reference/amount/currency (per app logic). |
| **Code** | `internal/providers/interswitch/wallets.go` (`CustomerWalletTransaction`, `MerchantWalletTransactionByReference`, `merchantWalletTransactionPath`), used by `internal/app/interswitch_wallet_credits.go` and reconciliation flows. |

## 4. Merchant/customer wallet provisioning and account/wallet reads

### 4.1 Create merchant/customer wallet
| Item | Details |
|---|---|
| **Process/trigger** | Provision an Interswitch wallet (and linked virtual account details) for a customer as part of wallet enablement/provisioning. Outcome may be treated conservatively on ambiguous provider errors. |
| **Endpoint** | `POST {walletBaseURL}/api/v1/wallet` |
| **Host/base URL** | `walletBaseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request** | `name`, `merchantCode`, `status` (default `ACTIVE`), `mobileNo`, `provider`, `firstName`, `pin` (4 digits, sent to provider only), `email`. |
| **Key response** | Wallet object including `id`, `provider`, `currencyCode`, `walletId`, `merchantCode`, `status`, `walletCreationStatus`, `virtualAccount` (`accountNumber/name/bankName/bankCode/accountPayableCode`, etc.). |
| **Code** | `internal/providers/interswitch/wallets.go` (`CreateMerchantWallet`), called from app wallet provisioning flows (e.g. `internal/app/interswitch_wallets.go`). |

### 4.2 Get wallet details
| Item | Details |
|---|---|
| **Process/trigger** | Fetch current provider wallet identity/status (no PIN). |
| **Endpoint** | `GET {walletBaseURL}/api/v1/wallet/details/{merchantCode}?walletId={walletId}` |
| **Host/base URL** | `walletBaseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request params** | Path: `merchantCode`; query: `walletId`. |
| **Key response** | Wallet details/status/linked virtual account fields. |
| **Code** | `internal/providers/interswitch/wallets.go` (`MerchantWalletDetails`). |

### 4.3 Get wallet balance (provider-authoritative balance path)
| Item | Details |
|---|---|
| **Process/trigger** | Retrieve available/ledger balance from provider when needed for wallet operations/gating/display paths that rely on provider balance. |
| **Endpoint** | `GET {walletBaseURL}/api/v1/wallet/balance/{merchantCode}?walletId={walletId}` |
| **Host/base URL** | `walletBaseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request params** | Path: `merchantCode`; query: `walletId`. |
| **Key response** | `responseCode`, `responseMessage`, `availableBalance`, `ledgerBalance`, `successful`; adapted to provider-neutral balance (available/ledger in minor units, currency). |
| **Code** | `internal/providers/interswitch/wallets.go` (`MerchantWalletBalance`, `CustomerWalletBalance`). |

### 4.4 Lookup wallet transaction by reference (reconciliation/read)
| Item | Details |
|---|---|
| **Process/trigger** | Reconcile ambiguous wallet debits/reversals or verify credits by stable reference without repeating money movement. |
| **Endpoint** | `GET {walletBaseURL}/api/v1/transaction/?merchantCode={merchantCode}&reference={reference}` |
| **Host/base URL** | `walletBaseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request params** | `merchantCode`, `reference`. |
| **Key response** | Matching transaction record(s): reference/payment reference, response/status indicators, direction, amount, currency, wallet record id, reversed flag. |
| **Code** | `internal/providers/interswitch/wallets.go` (`MerchantWalletTransactionByReference`, `CustomerWalletTransaction`), used by `internal/app/interswitch_wallet_operations.go`, credits/reconciliation paths. |

## 5. Wallet-funded money movement (provider wallet debit and reversal)

### 5.1 Debit customer wallet (PIN-authorized)
| Item | Details |
|---|---|
| **Process/trigger** | Execute provider wallet debit for wallet-funded portions/flows when `walletPortion > 0` (secure PIN flow), using a stable idempotency/reference. Ambiguous outcomes must be reconciled by lookup, not blindly retried. |
| **Endpoint** | `POST {walletBaseURL}/api/v1/transaction/transact` |
| **Host/base URL** | `walletBaseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request** | `walletId`, `amount` (string, minor units), `pin` (4 digits, never persisted/logged), `reference`, `transactionCode`, `narration`. |
| **Key response** | `responseCode`, `responseMessage`/`responseDescription`, `reference`/`transactionReference`, `status`. On provider/network uncertainty, implementation treats outcome as unknown and requires reconciliation by reference. |
| **Code** | `internal/providers/interswitch/wallets.go` (`TransactMerchantWallet`, `DebitCustomerWallet`), used by payment/wallet operation flows (e.g. `internal/service/payments.go`, app wallet operation handling). |

### 5.2 Reverse wallet transaction
| Item | Details |
|---|---|
| **Process/trigger** | Compensating reversal of a completed/confirmed wallet debit using a distinct reversal reference (e.g. refund/cash-out failure handling paths where applicable and confirmed). |
| **Endpoint** | `POST {walletBaseURL}/api/v1/transaction/reverse` |
| **Host/base URL** | `walletBaseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request** | `walletId`, `reference` (original), `reversalReference` (new, auditable). |
| **Key response** | Success/failure indicated by provider response/status (e.g. confirmed `responseCode 00` and `REVERSED` status in neutral adapter path). Ambiguous outcomes treated as unknown pending confirmation. |
| **Code** | `internal/providers/interswitch/wallets.go` (`ReverseMerchantWalletTransaction`, `ReverseCustomerWallet`), used by app/service compensation/reconciliation logic as implemented. |

## 6. Transfers / payouts (Quickteller Send Money v5) and bank rails

### 6.1 Get funds-transfer bank codes (bank directory)
| Item | Details |
|---|---|
| **Process/trigger** | Retrieve bank directory for transfer/name-enquiry/payout destination selection. |
| **Endpoint** | `GET {baseURL}/api/v2/quickteller/configuration/fundstransferbanks` |
| **Host/base URL** | API host `baseURL` (legacy endpoint per implementation). |
| **Auth/headers** | Legacy **InterswitchAuth** signature headers (`Authorization`, `Timestamp`, `Nonce`, `Signature`, `SignatureMethod: SHA1`), `accept: application/json`, `TerminalID` when available. |
| **Key response** | `{ "banks": [ { "id", "cbnCode", "bankName", "bankCode" }, ... ] }`. |
| **Code** | `internal/providers/interswitch/payout.go` (`GetBankCodes`, `bankCodesPath`). |

### 6.2 Name enquiry (destination account validation)
| Item | Details |
|---|---|
| **Process/trigger** | Validate destination account and resolve account holder name before transfer/payout dispatch. |
| **Endpoint** | `POST {transferBaseURL}/Transactions/DoAccountNameInquiry` |
| **Host/base URL** | Quickteller v5/transfer host `transferBaseURL` (TEST default `https://qa.interswitchng.com/quicktellerservice/api/v5`, LIVE default `https://interswitchng.com/quicktellerservice/api/v5`, overridable). |
| **Auth/headers** | OAuth Bearer token, `Content-Type: application/json`, `accept: application/json`, `TerminalID`, plus headers `accountid` (account number) and `bankcode` (bank code). Body empty. |
| **Key request** | Account number and bank code passed in headers as above. |
| **Key response** | `AccountName`, `ResponseCode`, `ResponseCodeGrouping`. Non-success/empty results treated as enquiry failure. |
| **Code** | `internal/providers/interswitch/payout.go` (`NameEnquiry`, `nameEnquiryPath`), used by wallet cash-out/bulk/ops flows (e.g. `internal/service/wallet_payouts.go`, `internal/service/bulk_approval.go`). |

### 6.3 Initiate transfer (payout / funds transfer)
| Item | Details |
|---|---|
| **Process/trigger** | Dispatch NIP/funds transfer payout using a stable transfer reference as idempotency key. |
| **Endpoint** | `POST {transferBaseURL}/transactions/TransferFunds` |
| **Host/base URL** | `transferBaseURL` (Quickteller v5). |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request** | `transferCode` (idempotency/reference), `mac` (SHA-512 over canonical initiation/termination amount+currency+method+country fields), `termination` (amount, accountReceivable accountNumber/accountType, entityCode/bank, currency `566`, paymentMethod `AC`, country `NG`), `sender` (phone/email/lastname/othernames), `initiatingEntityCode` (default `PBL`), `initiation` (amount, currency `566`, paymentMethod `CA`, channel `7`), `beneficiary` (lastname/othernames). |
| **Key response** | `ResponseCode`, `ResponseCodeGrouping`, `TransactionReference`, `TransferCode`, `TransactionDate`, etc. `90000` treated as succeeded (with external ref), `70120` as pending/ambiguous (`ErrPayoutPending`), other codes as failed/declined per mapping. |
| **Code** | `internal/providers/interswitch/payout.go` (`Payout`, `sendMoneyMAC`, `transferPath`), used by payout/settlement/cash-out services. |

### 6.4 Query transfer/transaction status and reconcile payout
| Item | Details |
|---|---|
| **Process/trigger** | Query transfer outcome by request/transfer reference; used for reconciliation and status checks (including payout reconciliation). |
| **Endpoint** | `GET {transferBaseURL}/Transactions?requestRef={requestRef}` |
| **Host/base URL** | `transferBaseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request params** | `requestRef` (transfer/request reference). |
| **Key response** | `requestReference`, `status`, `transactionRef`, `transactionResponseCode`, `amount`, `currencyCode` (normalized in client). Interpreted into succeeded/failed/pending/unknown reconciliation states (e.g. completed/success/`90000` vs failed/declined/rejected/reversed/cancelled vs pending/unknown). |
| **Code** | `internal/providers/interswitch/payout.go` (`QueryTransaction`, `ReconcilePayout`, `normalizeTransferStatus`, `queryPath`), used by payout reconciliation/service flows. |

## 7. Refunds (Payment Gateway refund APIs)

### 7.1 Create refund
| Item | Details |
|---|---|
| **Process/trigger** | Submit a full/partial refund for a parent payment (after approval/orchestration as applicable in service layer). |
| **Endpoint** | `POST {baseURL}/paymentgateway/api/v1/refunds` |
| **Host/base URL** | API host `baseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request** | `amount` (kobo/minor units), `refundReference` (generated, e.g. `XGREF-*`), `refundType` (`FULL`/`PARTIAL`), `parentPaymentId`. |
| **Key response** | `responseCode`, `responseDescription`, `refundReference`. Success/accepted interpreted from response/reference and known success codes; otherwise failure result with message. |
| **Code** | `internal/providers/interswitch/refund.go` (`Refund`, `refundPath`), used by `internal/service/refunds.go`. |

### 7.2 Query refund by reference
| Item | Details |
|---|---|
| **Process/trigger** | Fetch refund status/details by refund reference (provider-confirmed state). Per implementation, HTTP 200 with empty/null/empty object/array body is treated as not found. |
| **Endpoint** | `GET {baseURL}/paymentgateway/api/v1/refunds?refundReference={refundReference}` |
| **Host/base URL** | API host `baseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key response** | Refund record fields such as `id`, `refundReference`, `parentPaymentId`, `parentTransactionReference`, `refundType`, `refundAmount`, `refundStatus`, `createdDate`, `processedDate`, `refundTransactionReference`, `refundTransactionResponseCode`, `reasonForRefund`, etc. |
| **Code** | `internal/providers/interswitch/refund.go` (`QueryRefund`), `oauth.go` (`doAuthorizedRaw`). |

### 7.3 List refunds
| Item | Details |
|---|---|
| **Process/trigger** | Retrieve paginated refunds for a merchant (reporting/reconciliation/admin views as wired). |
| **Endpoint** | `GET {baseURL}/paymentgateway/api/v1/refunds` with optional query params |
| **Host/base URL** | API host `baseURL`. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request params** | Optional `merchantCode`, `pageNum`, `pageSize`, `startDate`, `endDate`. |
| **Key response** | `{ "count": <n>, "content": [<refund records>...] }`. |
| **Code** | `internal/providers/interswitch/refund.go` (`ListRefunds`). |

## 8. VTU / data & airtime recharge (Quickteller Service v5)

VTU calls use the Quickteller/transfer host base (`vtuBaseURL()` resolves to `transferBaseURL` or mode default). Auth is OAuth Bearer via `doAuthorizedOn`.

### 8.1 Purchase/recharge (fulfil airtime/data)
| Item | Details |
|---|---|
| **Process/trigger** | Submit airtime/data purchase using a Quickteller payment code and stable request reference. |
| **Endpoint** | `POST {vtuBaseURL}/Transactions` |
| **Host/base URL** | Quickteller v5 host (same base as transfer in implementation). |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request** | `TerminalId`, `paymentCode` (plan/provider SKU), `customerId` (normalized phone), `customerMobile`, `customerEmail` (optional, as applicable), `amount` (minor units as string), `requestReference` (deterministic/normalized, max 20 chars in implementation). |
| **Key response** | `TransactionRef`/`transactionRef`, `ApprovedAmount`, `ResponseCode`, `ResponseDescription`, `ResponseCodeGrouping`. Mapped to fulfilment status/message (fulfilled/pending/failed) with transaction ref context. |
| **Code** | `internal/providers/interswitch/vtu.go` (`FulfilData`, `vtuTransactionsPath`), used by data fulfilment service flows. |

### 8.2 Query/reconcile VTU transaction status
| Item | Details |
|---|---|
| **Process/trigger** | Requery VTU request by request reference to determine fulfilment outcome. |
| **Endpoint** | `GET {vtuBaseURL}/Transactions?requestRef={requestReference}` |
| **Host/base URL** | Quickteller v5 host. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key request params** | `requestRef`. |
| **Key response** | `amount`, `customer`, `customerMobile`, `requestReference`/`requestRef`, `status`, `transactionRef`/`TransactionReference`, `transactionResponseCode`, `ResponseCode`, `ResponseDescription`, `ResponseCodeGrouping`. Mapped to fulfilled/pending/failed. |
| **Code** | `internal/providers/interswitch/vtu.go` (`CheckDataStatus`). |

### 8.3 Discovery: list categories
| Item | Details |
|---|---|
| **Process/trigger** | Discover VTU service categories (e.g. Mobile/Recharge). |
| **Endpoint** | `GET {vtuBaseURL}/services/categories` |
| **Host/base URL** | Quickteller v5 host. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key response** | `BillerCategories[]` (`Id`, `Name`, `Description`), response codes/grouping. |
| **Code** | `internal/providers/interswitch/vtu.go` (`GetVTUCategories`). |

### 8.4 Discovery: list billers/services in category
| Item | Details |
|---|---|
| **Process/trigger** | Discover billers/telcos for a category (e.g. categoryId 4). |
| **Endpoint** | `GET {vtuBaseURL}/services?categoryId={categoryID}` |
| **Host/base URL** | Quickteller v5 host. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key response** | `BillerList` with category/billers (IDs, names, network/product/currency/amount metadata as provided). |
| **Code** | `internal/providers/interswitch/vtu.go` (`GetVTUBillers`). |

### 8.5 Discovery: list payment items/options (payment codes)
| Item | Details |
|---|---|
| **Process/trigger** | Discover payment items/bundles for a biller service, including `PaymentCode` required for recharge. |
| **Endpoint** | `GET {vtuBaseURL}/services/options?serviceid={serviceID}` |
| **Host/base URL** | Quickteller v5 host. |
| **Auth/headers** | OAuth Bearer token, JSON, `TerminalId` when available. |
| **Key response** | `PaymentItems[]` including `PaymentCode`, amount/fixed flags, currency, biller/service identifiers, item metadata. |
| **Code** | `internal/providers/interswitch/vtu.go` (`GetVTUPaymentItems`). |

## Appendix A — Host/base URL summary
| Purpose | Config/field | Typical/default behavior (per implementation) |
|---|---|---|
| API/requery, payment gateway APIs (refunds, DVA/standing VA), legacy-signed endpoints on API host | `baseURL` (`INTERSWITCH_BASE_URL`) | Must point to correct environment API/requery host (e.g. sandbox API host in test setups). |
| Hosted checkout page (Web Checkout form action) | `checkoutBaseURL` / mode-derived | TEST: `https://newwebpay-sandbox.interswitchng.com`; LIVE: `https://newwebpay.interswitchng.com`. |
| Quickteller v5 (transfers, name enquiry, tx query, VTU) | `transferBaseURL` / mode-derived | TEST: `https://qa.interswitchng.com/quicktellerservice/api/v5`; LIVE: `https://interswitchng.com/quicktellerservice/api/v5`. |
| OAuth token | `tokenURL` / derived | Sandbox API host -> `https://passport-sandbox.interswitchng.com/passport/oauth/token`; else `{baseURL}/passport/oauth/token` unless overridden. |
| Merchant Wallet API | `walletBaseURL` | Default `https://merchant-wallet.k8.isw.la/merchant-wallet` unless overridden. |
| Hosted Fields SDK (browser) | `HostedFieldsSDKURL` / mode-derived | TEST: `https://hostedifelds.qa.interswitchng.com/sdk.js`; LIVE: `https://hostedfields.interswitchng.com/sdk.js`. |

## Appendix B — Authentication schemes used
- **OAuth Bearer:** `Authorization: Bearer <access_token>` from `POST /passport/oauth/token` (client credentials). Used for direct JSON APIs on `baseURL`, `transferBaseURL`, and `walletBaseURL` via authorized helpers.
- **Legacy InterswitchAuth (HMAC-SHA1):** `Authorization: InterswitchAuth base64(clientID)`, `Timestamp`, `Nonce`, `Signature`, `SignatureMethod: SHA1`. Used for transaction requery (`gettransaction.json`) and bank-codes directory call per implementation.
- **Inbound webhook signature:** `X-Interswitch-Signature` = HMAC-SHA512(raw body, webhook secret), hex-encoded.

## Appendix C — Primary implementation files (non-KYC/KYB)
- `internal/providers/interswitch/client.go` — client/config, checkout init/page URLs, requery/verify, webhook validation, legacy auth/signing, health.
- `internal/providers/interswitch/page.go` — Hosted Fields page/SDK configuration.
- `internal/providers/interswitch/token.go`, `oauth.go` — OAuth token management and authorized HTTP helpers.
- `internal/providers/interswitch/dva.go` — dynamic virtual account (per-payment collection).
- `internal/providers/interswitch/standva.go` — standing virtual account reservation.
- `internal/providers/interswitch/wallets.go` — merchant/customer wallet create/details/balance/transaction lookup/debit/reverse.
- `internal/providers/interswitch/payout.go` — transfer funds, name enquiry, transfer query/reconcile, bank codes.
- `internal/providers/interswitch/refund.go` — refund create/query/list.
- `internal/providers/interswitch/vtu.go` — VTU recharge/query and discovery (categories/billers/payment items).
- Excluded: `internal/providers/interswitch/kyc.go` and any KYC/KYB-specific flows.
