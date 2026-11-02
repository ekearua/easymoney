# Merchant roles for bulk payments - implementation plan

Status: approved plan, not yet implemented. Bank payout is in scope for this work.

## 1. Goal

Bulk payments are gated per merchant by `merchants.bulk_enabled` and every
`/merchant/bulk/*` route sits behind `requireBulkPayments`. Today the merchant
console has a single access level: a session resolves to a `merchant_owners`
row, so whoever can sign in can do everything.

This plan adds two merchant roles inside a bulk-enabled merchant - records and
payments - assignable both by the platform admin and by the merchant owner, and
turns the location-aware batch flow into a filter-driven, approved process that
can pay either a Faya wallet or a bank account.

## 2. Decisions in force

| Question | Decision |
| --- | --- |
| Staff identity | Merchant sub-accounts with their own logins (separate from `merchant_owners`) |
| Who grants roles | Platform admin and merchant owner |
| Records role | Owns the recipient list exclusively: import, edit, remove, verify. The payments role has no interaction with those documents |
| Document storage | Text and metadata in Postgres (template fields plus source-file provenance). No binaries |
| Geography | Free text from `docs/user-scripts/Records Template.xlsx`: State, LGA, Ward, Polling Unit/Booth |
| Batch amounts | One uniform amount per batch |
| Approval | A single payments-role holder may build and release alone; the audit trail records who |
| Payability | A recipient must be verified by the records role |
| Location flow | Keep stage 1 then stage 2, toggleable per merchant by the admin |
| Payout target | Wallet by default; admin can switch the merchant to bank account. **Bank payout is in scope now** |
| Verification | Manual attestation by the records role |
| Locations | Owner and admin only; the records role assigns recipients to existing locations |
| Owner | Full access to everything, always |
| Existing rows | None; the feature is tested after this work, so no backfill |

## 3. Input data

`docs/user-scripts/Records Template.xlsx` defines the record shape - 14 columns,
170 numbered rows:

```
S/N | Name | Phone Number | BVN | NIN | VIN | Account Number | Bank Name |
Account Name | State | LGA | Ward | Polling Unit/Booth | Source File
```

`S/N` is row ordinal only. `Source File` is provenance for the upload that
created or last touched the row. Everything else is stored on the recipient.

## 4. Identity, roles, sessions

Owner stays the full-access principal; members are new rows, not
`merchant_owners` entries.

```sql
-- 108_merchant_members.sql
CREATE TABLE merchant_members (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
  email text NOT NULL UNIQUE,
  password_hash text NOT NULL DEFAULT '',
  role text NOT NULL CHECK (role IN ('records','payments')),
  status text NOT NULL DEFAULT 'invited' CHECK (status IN ('invited','active','disabled')),
  invited_by_member uuid, invited_by_owner boolean NOT NULL DEFAULT false,
  invited_at timestamptz NOT NULL DEFAULT now(),
  activated_at timestamptz, last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE merchant_sessions ADD COLUMN member_id uuid REFERENCES merchant_members(id) ON DELETE CASCADE;
ALTER TABLE merchant_sessions ADD COLUMN member_role text NOT NULL DEFAULT '';
```

- `ValidateMerchantSession` returns a role: `owner` when `member_id IS NULL`,
  otherwise the member's role. `requireMerchant` stores it in request context
  next to the existing merchant and CSRF keys.
- New `requireMerchantRole(roles...)`; the owner passes every check. Bulk routes
  keep `requireBulkPayments` outside the role gate, so revoking bulk access
  disables both roles immediately.
- MFA is currently per merchant (`MFAScopeMerchant` plus `merchant.ID`), which
  would share one second factor across staff. Members get a new scope
  `merchant_member` keyed by member id; the owner keeps the current scope.
- Login resolves `merchant_members` by email first, then falls back to the owner
  path through `MerchantByEmail`. Invites use the configured SMTP settings; when
  SMTP is unset the invite link is shown to the inviter instead. Disabling or
  revoking deletes that member's sessions.
- Member actions write to the existing hash-chained `audit_logs` with
  `actor_type='merchant'`, `actor_id` set to the member id, and `actor_email`.

## 5. Recipient records (the single list)

`center_users` is already `UNIQUE (merchant_id, phone_e164)` - one merchant-wide
list with one location per phone - so it becomes the records table rather than a
new table.

```sql
-- 109_recipient_records.sql  (on center_users)
ALTER TABLE center_users ALTER COLUMN center_id DROP NOT NULL;
-- bvn_sealed / nin_sealed / vin_sealed      sealed with the existing sealValue
-- bvn_index / nin_index / vin_index          peppered sha256 for duplicate detection
-- account_number, bank_name, account_name
-- state, lga, ward, polling_unit, source_file
-- verification_status ('unverified'|'verified'|'rejected'),
--   verified_by_member, verified_at, verification_note
-- last_paid_at, last_paid_amount_kobo
CREATE INDEX center_users_geo_idx ON center_users (merchant_id, state, lga, ward);
```

- Identity numbers are sealed at rest, masked for display through `maskPII`, and
  indexed by blind hash so duplicate BVN/NIN can be caught without decryptable
  lookups.
- The importer replaces `parseRecipientFile` with header-driven mapping (column
  order irrelevant, aliases for `Phone Number`/`phone`, `BVN`, `State`, and so
  on), per-row validation, and a summary of added, updated and skipped rows with
  reasons instead of today's silent skip.
- Actions: add, edit, remove, verify, reject with note. Records role and owner
  only.

## 6. Filters, individual history, locations

- `merchant_centers` CRUD moves behind owner and admin. The records role may
  assign a recipient to an existing location but not create or delete locations.
  Batch creation no longer requires a location when location verification is off.
- One shared filter query, used by both roles with different projections:

  ```
  state, lga, ward, polling_unit, verification, q (name or phone), page
  ```

  The payments role sees the same rows with `bvn`, `nin`, `vin`, `bank_name` and
  `account_name` omitted and the account number reduced to its last four digits:
  it must never read identity documents, yet it can still release a bank-mode
  batch from stored details.

- Per-recipient detail: payment count, total received, last amount and date,
  plus the item list (batch number, date, amount, status, snapped location).
  Served by one indexed aggregate over `bulk_batch_items`.

## 7. Batch lifecycle

```sql
-- 110_bulk_approval.sql
-- bulk_batches: center_id nullable; status gains draft/submitted/approved/rejected;
--   created_by_member, submitted_at, approved_by_member, approved_at,
--   rejected_reason, filter_snapshot jsonb, payout_mode text
-- bulk_batch_items: snapshot center_id, state, lga, ward, polling_unit at build time
ALTER TABLE merchants ADD COLUMN bulk_location_required boolean NOT NULL DEFAULT true;
ALTER TABLE merchants ADD COLUMN bulk_payout_mode text NOT NULL DEFAULT 'wallet'
  CHECK (bulk_payout_mode IN ('wallet','bank'));
```

Lifecycle:

```
draft -> submitted -> approved -> stage1 -> stage2 -> completed | partial | failed
                 \-> rejected
```

- "Process payment based on filtered results" means all matching rows, not the
  visible page. A preview endpoint returns count and total; submission snapshots
  the filter plus the resolved recipient ids, so later record edits cannot
  change a saved batch.
- Existing guards (non-empty target set, `MaxTargets`, wallet balance, KYB
  payout ceiling leading to `compliance_flag`) run at preview and submit, and
  again at approval.
- Approved batch with `bulk_location_required` on: enqueues today's stage 1 then
  stage 2, unchanged. With it off: one job pays the matched verified recipients
  directly, still debiting per successful item.
- Only `verification_status='verified'` recipients enter a batch. Unverified and
  rejected rows are listed but excluded, and the excluded count is shown at
  preview.

## 8. Payout modes (both in scope)

Admin selects the mode per merchant, at creation and later from the merchants
page.

**Wallet (default).** Unchanged behaviour: credit the recipient's Faya wallet.
Unregistered or sub-Tier-2 recipients fail with the existing item statuses
(`not_registered`, `kyc_ineligible`).

**Bank.** Pay the bank account stored on the record through the existing
Interswitch payout client (`Payout`, `NameEnquiry`, `ReconcilePayout`,
`GetBankCodes`):

- cache the provider bank list and resolve `Bank Name` to a NIP bank code; the
  template carries no bank code column;
- run name enquiry during verification and store the resolved account name so
  the records role is attesting a checked value;
- mark account number, bank name and bank code as mandatory for payability in
  bank mode;
- give batch items a transfer reference and reconciliation state, reusing the
  patterns in `internal/service/wallet_payouts.go` and `payout_reconcile.go`;
- this path works for recipients with no Faya account.

## 9. Admin and owner surfaces

- Admin, merchants page: members list per merchant (email, role, status, last
  login) with invite, reassign and revoke; toggles for `bulk_enabled` (already
  built), `bulk_location_required` (new) and payout mode (new). The same three
  settings appear on the create-merchant form.
- Merchant, Settings then Team (owner only): invite, reassign and revoke records
  and payments staff for their own merchant.
- Navigation renders by role: records sees Records and Locations, payments sees
  Payable recipients and Batches, the owner sees both. A direct URL without the
  role returns 403, mirroring `requireBulkPayments`.

## 10. Phases and acceptance

| Phase | Work | Done when |
| --- | --- | --- |
| 0 | Migrations 108-110, store layer, integration tests | Store tests cover member CRUD, record fields and batch fields |
| 1 | Member auth, session role, `requireMerchantRole`, nav split, Team page, admin member panel, invites, member MFA scope | Each role gets 403 on the other's routes; the owner passes both |
| 2 | Template importer, records list/edit/remove, verify and reject, masking, dedupe | The 170-row template imports; a duplicate BVN is flagged; the payments role sees no identity fields |
| 3 | Shared filter query, per-recipient history, batch preview with count and total | Filtering state then LGA then ward then booth returns the right rows and totals |
| 4 | Draft, submit, approve, reject, filter snapshot, geofence on and off release | An approved wallet batch pays only verified in-range recipients; a geofence-off batch pays all verified matches |
| 5 | Bank payout: bank code cache, name enquiry, bank-mode items, reconciliation | A bank-mode batch pays a recipient with no Faya account and reconciles by reference |
| 6 | Admin oversight, audit review, update `docs/user-scripts/BULK_PAYMENTS_USER_SCRIPT.md` | The user script describes both roles and both payout modes accurately |

Bank payout is not deferred: phase 5 is part of this work, sequenced after the
wallet approval flow so the shared lifecycle is proven before a second money
path is attached to it.

## 11. Risks and notes

- **BVN and NIN are regulated data.** Sealed columns, blind-index dedupe, masked
  display, access limited to the records role, and every record read logged. No
  export of raw identity numbers.
- **Bank payouts are a new money path.** Name enquiry, transfer references,
  per-item reconciliation, reversal handling and separate ledger legs all need
  their own tests; the wallet path must keep working unchanged alongside it.
- **Snapshot geography on batch items.** Recipients can be reassigned between
  locations, so each item keeps the state, LGA, ward, booth and location it was
  paid under.
- **Role collapse edge case.** A merchant whose `bulk_enabled` is revoked keeps
  member rows, but every bulk route 403s. Say so in the admin UI so revocation
  is not mistaken for data loss.
- **No image documents** are stored in this plan. If ID card scans are later
  required, that needs object storage, which the app does not have today.
