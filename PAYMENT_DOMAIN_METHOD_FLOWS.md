# Payment domain method flows

This is an implementation-backed map of the payment domain. It covers the
exported helpers in `internal/domain/payment.go` and the public operational
methods on `internal/service/payments.go` that create, settle, or maintain a
payment. Amounts are integer kobo.

The request referred to `ENGINEERING-SERVICEs.md`; that file is not in this
checkout. The authoritative sources used here are the Go implementation,
tests, store layer, and `XEGO_PAYMENT_DIAGRAMS.md`.

## Payment lifecycle

```mermaid
stateDiagram-v2
    [*] --> draft
    draft --> awaiting_confirmation: create draft
    awaiting_confirmation --> initialized: gateway checkout
    awaiting_confirmation --> pending: simulated bank transfer
    awaiting_confirmation --> succeeded: wallet confirmation
    initialized --> pending: gateway reports unresolved
    initialized --> succeeded: verified success
    initialized --> failed: verified decline
    pending --> succeeded: verified / simulated confirmation
    pending --> failed: verified decline
    draft --> expired
    awaiting_confirmation --> expired
    initialized --> expired
    pending --> expired
    succeeded --> refunded
```

## Domain helpers (`internal/domain/payment.go`)

### `CanTransition`

```mermaid
flowchart LR
    A[Current status and requested status] --> B{Same status?}
    B -- yes --> OK[allow]
    B -- no --> C{Edge in validTransitions?}
    C -- yes --> OK
    C -- no --> NO[reject]
```

### `ParseNGNAmount`

```mermaid
flowchart LR
    A[Human NGN text] --> B[Remove commas, currency symbols and spaces]
    B --> C{Valid non-negative naira.decimals?}
    C -- no --> E[return validation error]
    C -- yes --> D[Convert naira + up to 2 decimals to kobo]
    D --> F{Within minKobo and maxKobo?}
    F -- no --> E
    F -- yes --> G[return kobo]
```

### `FormatNGN`

```mermaid
flowchart LR
    A[Kobo integer] --> B[Split into naira and remainder]
    B --> C[Group naira thousands]
    C --> D{Remainder is zero?}
    D -- yes --> E[Return ₦N]
    D -- no --> F[Return ₦N.xx]
```

### `NewProviderReference`

```mermaid
flowchart LR
    A[Generate UUID] --> B[Remove hyphens] --> C[Prefix wpd_] --> D[Opaque gateway reference]
```

### `NewReceiptToken` and `NewCheckoutToken`

```mermaid
flowchart LR
    A[Caller requests capability token] --> B[Read 32 cryptographically random bytes]
    B --> C{Random source succeeded?}
    C -- no --> E[return error]
    C -- yes --> D[Base64 Raw URL encode] --> F[Return opaque bearer token]
```

### `CanonicalE164Phone`

```mermaid
flowchart LR
    A[Raw phone input] --> B[Keep digits only]
    B --> C{Digits empty?}
    C -- yes --> D[Return empty]
    C -- no --> E{Starts with local 0?}
    E -- yes --> F[Replace 0 with 234]
    E -- no --> G[Keep digits]
    F --> H[Prefix +]
    G --> H
    H --> I[Canonical phone]
```

## Payment creation and checkout

### `CreateDraft`, `CreateDraftForProvider`, and `CreateCollectionDraft`

`CreateDraft` is the convenience card/WhatsApp collection path. The other two
delegate to the same core; collection drafts add the Xego fee, while
provider-specific drafts charge the supplied amount exactly.

```mermaid
flowchart TD
    A[CreateDraft / CreateCollectionDraft / CreateDraftForProvider] --> B{Collection draft?}
    B -- yes --> C[Calculate collection fee; total = base + fee]
    B -- no --> D[Use supplied amount]
    C --> E[createDraftCore]
    D --> E
    E --> F{Provider auto?}
    F -- yes --> G[ProviderRouter.PickProvider]
    F -- no --> H[Validate provider]
    G --> H
    H --> I[Generate payment ID, provider ref, receipt and checkout tokens]
    I --> J{Non-wallet rail?}
    J -- yes --> K[Ensure KYC profile and reserve money-in allowance]
    J -- no --> L[Skip money-in reservation]
    K --> M[Store.CreatePayment: draft]
    L --> M
    M --> N[TransitionPayment: awaiting_confirmation]
    N --> O[Return PaymentView]
```

### `CreateWalletTopupDraft`

```mermaid
flowchart LR
    A[Top-up request] --> B[Load wallet-topup system merchant]
    B --> C[createDraftCore with wallet_topup metadata]
    C --> D[Await customer confirmation and gateway settlement]
    D --> E[Post-success wallet_topup hook credits payer wallet]
```

### `CreateCheckout` and `ResolveCheckout`

```mermaid
flowchart TD
    A[Merchant creates request-money link] --> B{Authenticated merchant is payee?}
    B -- no --> X[Reject]
    B -- yes --> C[Store.CreateCheckout]
    C --> D[Customer opens link with phone]
    D --> E[CanonicalE164Phone]
    E --> F{Valid E.164?}
    F -- no --> X
    F -- yes --> G{Active linked payment exists?}
    G -- yes --> H[Return existing attempt]
    G -- no --> I[Load payee; get or create payer]
    I --> J[CreateCollectionDraft using checkout amount]
    J --> K[Link checkout to payment]
    K --> L[Return awaiting-confirmation payment]
```

### `HostedCheckoutURL` and `InitializeCheckout`

```mermaid
flowchart LR
    A[Awaiting-confirmation payment] --> B[HostedCheckoutURL builds /checkout/{token}]
    B --> C[Customer explicitly confirms]
    C --> D{Gateway configured for provider?}
    D -- no --> X[Return error]
    D -- yes --> E[Gateway.Initialize with reference, amount, callback and metadata]
    E --> F{Returned reference matches?}
    F -- no --> X
    F -- yes --> G[Store.SetCheckout URL]
    G --> H[Payment is initialized]
```

## Rail-specific confirmation

### `ConfirmWalletPayment`

```mermaid
flowchart TD
    A[Wallet payment awaiting confirmation] --> B{Provider is wallet and state valid?}
    B -- no --> X[Return error]
    B -- already succeeded --> R[Idempotent no-op]
    B -- yes --> C[Ensure KYC profile]
    C --> D[Reserve payer money-out allowance]
    D --> E[TransitionPaymentWithOutbox: succeeded]
    E --> F{Transition failed?}
    F -- yes --> G[Release allowance and return error]
    F -- no --> H[Atomically debit payer wallet and post success]
    H --> I[Run post-success hooks]
```

### `InitializeBankTransferSimulation` and `ConfirmBankTransferSimulation`

```mermaid
flowchart LR
    A[Bank-transfer payment awaiting confirmation] --> B{Correct provider and state?}
    B -- no --> X[Return error]
    B -- yes --> C[Store.InitializeBankTransferSimulation]
    C --> D[Save chosen collection bank and instruction]
    D --> E[State becomes pending]
    E --> F[Customer WhatsApp confirmation]
    F --> G[Store.ConfirmBankTransferSimulation]
    G --> H[Transition with success outbox]
    H --> I{State changed?}
    I -- yes --> J[Run post-success hooks]
    I -- no --> K[Idempotent return]
```

### `CreateIndividualPayDraft` and individual-pay settlement

```mermaid
flowchart TD
    A[Payer gives recipient and amount] --> B[Load individual-pay system merchant]
    B --> C[createDraftCore with individual_pay metadata]
    C --> D[Gateway / transfer rail settles sender payment]
    D --> E[Success hook: applyIndividualPaySettlement]
    E --> F[Decode recipient, NIP and fee metadata]
    F --> G{Recipient gets > 0?}
    G -- no --> H[No wallet credit]
    G -- yes --> I[Get/create recipient and payout destination]
    I --> J[Ensure recipient KYC profile]
    J --> K[Record idempotent split settlement]
    K --> L[Credit recipient wallet]
```

## Authoritative gateway settlement

### `VerifyAndApply`

```mermaid
flowchart TD
    A[Return route, webhook, or reconciler reference] --> B[Store.PaymentByReference]
    B --> C{Gateway configured?}
    C -- no --> X[Return error]
    C -- yes --> D[Gateway.Verify with exact expected amount]
    D --> E[validateVerification]
    E --> F{Reference, amount, currency, domain and metadata valid?}
    F -- no --> X
    F -- yes --> G[mapGatewayStatus]
    G --> H{Known status?}
    H -- no --> I[No transition]
    H -- pending --> J[TransitionPayment: pending]
    H -- terminal --> K[TransitionPaymentWithOutbox]
    K --> L{Succeeded and changed?}
    L -- yes --> M[applyPaymentSuccessHooks]
    L -- no --> N[Return updated payment]
    M --> N
```

`ValidateWebhook` in the Interswitch adapter validates the HMAC-SHA512
signature, but a webhook remains only a signal. `VerifyAndApply` performs the
signed server-side requery before value is delivered.

## Post-success processing

### `applyPaymentSuccessHooks`, `runPaymentHook`, and `ApplyPendingPaymentHooks`

```mermaid
flowchart TD
    A[Successful, changed payment] --> B{Payment purpose?}
    B -- wallet top-up --> C[Only wallet_topup hook]
    B -- individual payment --> D[Only individual_pay hook]
    B -- ordinary collection --> E[Invoice, thrift, service, event, splits, receipt scan hooks]
    C --> F[EnsurePaymentHooks]
    D --> F
    E --> F
    F --> G[runPaymentHook]
    G --> H{Hook succeeded?}
    H -- yes --> I[MarkPaymentHookDone]
    H -- no --> J[Mark failed with exponential retry time]
    J --> K[Worker: ApplyPendingPaymentHooks]
    K --> G
    K --> L{Eight failures?}
    L -- yes --> M[Final failure; manual review]
```

### `applyCollectionSplits` and `collectionSplitAmounts`

```mermaid
flowchart LR
    A[Succeeded ordinary collection] --> B{Linked to invoice, thrift, or data?}
    B -- yes --> C[Skip: its purpose hook owns settlement]
    B -- no --> D{Metadata has base + fee and totals match?}
    D -- yes --> E[Merchant receivable = base; Xego fee = metadata fee]
    D -- no --> F[Recalculate fee; deduct it from paid amount]
    E --> G[Store.ApplyPaymentSplits]
    F --> G
    G --> H[Credit merchant payable and Xego payable]
```

### `createReceiptScanToken` and `NotifyMerchantPayment`

```mermaid
flowchart TD
    A[Successful service or event purchase] --> B[Find purchased quantity]
    B --> C[Ensure receipt scan token]
    C --> D{New token?}
    D -- no --> E[Idempotent no-op]
    D -- yes --> F[Build scan and receipt URLs]
    F --> G[Generate QR image]
    G --> H{QR encoding / image enqueue succeeds?}
    H -- yes --> I[Enqueue image to customer]
    H -- no --> J[Fallback to text message]
    K[Phase 3 notification consumer] --> L[Find invoice and merchant preferences]
    L --> M{Payment notifications enabled and owner contact exists?}
    M -- yes --> N[Enqueue merchant payment notification]
    M -- no --> O[No notification]
```

## Operational methods

### `Reconcile` and expiration

```mermaid
flowchart TD
    A[Scheduled reconciler] --> B[Load unresolved payments older than 30 seconds]
    B --> C[For each: VerifyAndApply with reconciliation source]
    C --> D[Load attempts older than SessionTTL]
    D --> E[Transition each to expired with result outbox]
    E --> F[Continue after individual failures; log them]
```

### `ProviderList` and `IsGatewaySuccessEvent`

```mermaid
flowchart LR
    A[ProviderList] --> B[Read registered gateway map keys] --> C[Return provider names]
    D[IsGatewaySuccessEvent] --> E{Provider is Interswitch and event is TRANSACTION.COMPLETED?}
    E -- yes --> F[true: trigger authoritative verification]
    E -- no --> G[false]
```

## Invariants to retain when changing the domain

- Only `VerifyAndApply`, wallet confirmation, and the explicit simulated bank
  confirmation can move a payment to `succeeded`.
- Gateway redirects and webhooks never establish success on their own;
  server-side verification does.
- Payment transitions and customer result notifications are persisted together
  through the outbox path for terminal outcomes.
- Allowance reservations are reference-keyed and must be released when a
  non-wallet attempt becomes terminal without succeeding.
- Hooks are post-commit, idempotent, and retried; a hook failure must not undo
  a successful payment transition.
