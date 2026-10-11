# Restricted-purpose program purses

A **program** is a sponsor merchant's restricted money scheme. A sponsor (a
ministry, an NGO, an employer) funds it; enrolled individuals — beneficiaries —
each hold a **purse**; and that purse can only be spent at the merchants on the
program's allow-list. A Ministry of Agriculture program paying farmers whose
funds will only buy farm inputs at approved input dealers is the canonical case.

## The one design decision everything else follows from

**A purse is a real double-entry ledger account (`2302_program_purse:<id>`), not
a tag or a flag on the wallet.**

That single choice buys the four properties that matter, and it buys them by
construction rather than by checks somebody has to remember to write:

| Property | Why it holds |
|---|---|
| Restricted money cannot be withdrawn to a bank | `WithdrawFromUserWallet` posts from `2301_user_wallet:<user>` only. A purse is a different account, so there is nothing to reach. |
| Restricted money cannot be sent person-to-person | The transfer rail runs through the same payment path, and the internal transfer merchant can never be on an allow-list (it is seeded inactive; `AddProgramMerchant` filters on `active`). |
| A purse balance can never drift from the journal | `ProgramPurse.BalanceKobo` is summed from `ledger_entries`, never stored. |
| Expiry and refunds are ordinary movements | Both are balanced postings (purse → sponsor wallet, purse restored by reversal), so the book always proves where the money went. |

## Data model (migration 100)

- `merchants.merchant_type` — `standard | sponsor | program_acceptor`.
  **Classification only.** The allow-list authorises a spend; a mistyped type can
  never widen access.
- `merchant_programs` — code, name, sponsor, beneficiary-facing label, default
  purse validity in days (0 = no expiry), status.
- `merchant_program_members` — the allow-list. `blocked` is a distinct state
  from removal so a refusal never destroys the record of a prior approval.
- `program_enrollments` — the link from a program to an individual account.
  Enrollment is the precondition for a purse.
- `program_purses` — one per (program, beneficiary), `UNIQUE(program_id, user_id)`.
- `payments.purse_id` — which purse funded a payment (nil = ordinary wallet or
  gateway rails).
- `bulk_batches.program_id` — a bulk batch that disburses into purses rather
  than ordinary wallets. Nil keeps the previous behaviour exactly.

No foreign keys to `users`/`merchants`, mirroring `wallet_accounts` (058):
retention and DSR purge delete those rows, and a purse must never block a purge.

## Where the rules are enforced

There is exactly **one** place that can authorise a purse payment:
`store.PurseSpendAllowedTx`, called from `transitionPayment` **inside the same
transaction that moves the money**. It re-checks that the purse is active and
unexpired, the program is active, and the payee merchant is approved and not
removed.

`ApprovedPurseForPayee` is the draft-time *selection* helper. It exists so the
right account is chosen and the customer can be told early — it is not the
authority, and a merchant de-listed between draft and confirmation still cannot
be paid.

## Money flows

```
sponsor business wallet  --disbursement-->  purse          (CreditProgramPurseTx)
purse                    --purchase------>  customer float  (transitionPayment)
customer float           --split--------->  merchant payable (existing split hook)
purse                    --expiry-------->  sponsor wallet  (ReturnExpiredPurse)
purse                    --refund-------->  restored by the payment reversal
```

Refunds needed **no new code**: `paymentTouchesWalletTx` now also matches purse
accounts, so the existing refund paths (both of them) skip the extra wallet
credit and the reversal restores the purse. Money refunded from an approved
merchant stays restricted.

## Compliance posture

- Disbursement to a beneficiary is a payment **to** them, so normal tier
  allowances and transaction monitoring apply to that leg.
- A purse spend runs through `ConfirmWalletPayment`, so an active compliance
  freeze (`ReserveAllowance`) blocks it like any other money-out.
- The disbursement leg reuses the existing bulk rail's Tier-2 eligibility gate.
- Every allow-list and enrollment mutation is audited with the acting admin.

## Surfaces

- `/admin/programs` — create programs, manage the allow-list (approve / block /
  remove), enroll beneficiaries by phone, activate/deactivate, and read the
  roll-up (disbursed, spent, returned, held) plus every purse with its balance
  and expiry.
- The 6-hourly `purse_expiry` worker returns unspent balances to sponsors.
- **Chat (wired).** Tapping *Pay from wallet* on a merchant purchase now funds
  from an eligible purse when one covers the merchant, and from the ordinary
  wallet otherwise (`walletOrPurseDraft`). Both are instant in-platform
  transfers, so the rail the customer experiences is unchanged; only which
  balance is drawn down differs, and the review message names the programme, its
  balance and its expiry *before* anything is confirmed. A purse purchase adds
  **no Faya collection fee** — programme money buys the merchant's amount
  exactly. Confirmation failures get specific copy: a merchant de-listed
  mid-flight and an expired purse each explain that nothing was taken.

## Known gaps / follow-ups

1. **Other flows do not yet consider purses.** Merchant collections reached
   through the payment-method menu are wired. Invoice, thrift, data and
   individual-pay drafts are not — only invoice payments are merchant purchases,
   so that is the one worth extending next. Web flows (browser) are also not
   wired.
2. **No beneficiary listing.** There is no "my programme funds" command yet, so
   a beneficiary only discovers a purse when they go to pay. The data is ready:
   `ProgramPursesService.PurseSummariesForUser` returns each purse with the
   merchants it may be spent at.
3. **Sponsor read-only dashboard** is not built; sponsors currently have no
   self-service view (the admin console is the only surface).
4. **No split funding.** If a purse cannot cover the whole charge, the purchase
   falls back to the ordinary rail rather than drawing part of the purse.
5. **A purse spend still consumes the individual's money-out tier allowance.**
   Intentional and conservative, but worth confirming against the tier policy.
6. **Fee policy for purse purchases** is now explicit: no collection fee is
   charged on restricted funding (the purse pays the merchant's amount exactly),
   and the chat review says so. Revisit if programme purchases should attract
   one.
7. **Manual test schedule + README** are not updated for this feature.

## Testing

- `internal/store/store_programs_integration_test.go` covers the invariants: enrollment
  required, replay-safe funding, withdrawal isolation, allow-list selection and
  immediate revocation, system merchants never approvable, expiry refusal, and
  single-shot repatriation with the sponsor made whole.
- Not yet covered: the bulk program disbursement path
  (`PostBulkProgramPaymentItem`) end to end, and the admin handlers.
