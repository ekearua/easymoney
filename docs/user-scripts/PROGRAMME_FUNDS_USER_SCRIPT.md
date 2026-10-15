# Faya programme funds - the sponsored-spending script

A ready-to-use walk-through of Faya's restricted-funds feature, written the way each person actually experiences it: what an administrator approves, what a sponsor sets up, what a beneficiary sees, and - the part that matters - exactly where the money can and cannot go. Every dialogue line and screen below is the real product copy. Use it for sponsor onboarding, demos, and support training.

Programme funds are other people's money, lent for a purpose. A sponsor merchant (a ministry, a state scheme, an NGO) funds a **program**, each enrolled beneficiary holds a **purse**, and a purse can only be spent at the merchants that sponsor has approved. It cannot be withdrawn, sent to another person, or spent anywhere else - not by rule, but because a purse is its own ledger account. Unspent money goes home to the sponsor when the purse lapses.

Cast: **Ada**, a beneficiary; **Musa**, a sponsor merchant; **Admin**, a Faya administrator; **Faya**, the assistant; **[Page]**, a screen seen in a browser.

## Scene 1 - Admin approves who may sponsor

Restricted funds are gated from the top. A merchant cannot switch this on for itself; an administrator approves it. Admin opens **[Page - Admin -> Programs]** and uses the **Sponsor merchants** panel:

> **Faya:** Only approved sponsors can own restricted-fund programs and use the sponsor console. Approving a merchant here is what enables its access; revoking removes the console without touching its programs or balances.

Admin picks a merchant under **Approve sponsor** and taps **Approve sponsor** (or **Revoke** to take the console away again). Nothing else changes on revoke - programs, purses and balances stay; the merchant simply loses the console.

## Scene 2 - Musa creates a program

Musa signs in to the merchant dashboard and opens **Merchant -> Programs**. Until he is an approved sponsor the page is honest about why he is there:

> **Faya:** Sponsor tools are not enabled for this account. Restricted-fund programs are owned by sponsor merchants approved by an administrator - ask an administrator to approve your merchant account as a sponsor.

Once approved, the **Create program** form appears:

- **Code** - for example *agric-input-support*
- **Display name** - for example *Ministry of Agriculture input support*
- **Label beneficiaries see** - for example *Agric input funds*
- **Description** - what this round of funding is for
- **Purse validity in days (0 = no expiry)** - defaults to 0

He taps **Create program**, and it appears under **Your programs** with its code, name, label, validity and status. Before there is one:

> **Faya:** No programs yet.

The page states the deal up front:

> **Faya:** A program is restricted money you sponsor: you fund it, enrolled beneficiaries each hold a purse, and that purse can only be spent at the merchants on this allow-list. Money in a purse cannot be withdrawn or sent to another person - it is a separate ledger account precisely so that is true by construction.

## Scene 3 - Musa sets the rules

On **[Page - Program detail]** Musa configures three things.

**Status.** He can set a program **active** or **inactive** and tap **Set status**:

> **Faya:** Deactivating stops new spending from every purse in this program immediately. Balances are untouched - money already granted stays until it is spent or expires.

**Approved merchants.** Under **Approved merchants** he approves the shops the money may be spent at, and can **Block** (immediately, with a reason) or **Remove** any of them:

> **Faya:** Only these merchants can be paid from this program's purses. Blocking is immediate and applies to payments already drafted.

> **Faya:** No merchants approved yet - this program cannot fund anything until at least one is.

**Beneficiaries.** Under **Beneficiaries** he enrols people by phone number:

> **Faya:** A beneficiary must be enrolled before a purse can be opened for them. Removing an enrollment stops future grants but does not claw back money already granted.

> **Faya:** No beneficiaries enrolled yet.

Once a grant lands, the **Roll-up** panel shows Enrolled beneficiaries, Active purses, Approved merchants, Disbursed to beneficiaries, Spent at approved merchants, Returned to sponsor at expiry, and Currently held in purses.

## Scene 4 - The grant lands in Ada's purse

A grant is paid through the ordinary bulk-payment path: the sponsor starts a bulk payment to the enrolled beneficiaries, and - unlike a cash payout - each enrolled recipient is paid **into a purse** rather than a wallet. A recipient who is not enrolled is refused (there is nowhere restricted to put the money). The first disbursement to an enrolled beneficiary is what opens their purse:

> **Faya:** No purses opened yet. A purse is created automatically the first time a disbursement reaches an enrolled beneficiary.

The **Purses** panel then lists each beneficiary with their balance, expiry, anything returned, and status. The money is now Ada's to spend on the programme's purpose - and nothing else.

## Scene 5 - Ada checks what she holds

In chat, Ada sends `programme funds` (or `program funds`, or taps **Programme funds - Restricted balances and where to spend them** on the menu). Faya answers from the same rows the spend gate reads, so the listing can never promise a merchant the gate would refuse:

> **Faya:** Programme funds
>
> This money can only be spent at the approved merchants below. It can't be withdrawn, sent to another person, or spent anywhere else.
>
> Ministry of Agriculture input support
> Balance: ₦12,500
> Expires: 30 Jun 2026
> Spend at: Agro Seeds Depot, Agric Inputs Ltd, Green Farm Supplies

If a programme has no approved merchants just now, the same line says so plainly rather than pretending:

> **Faya:** Spend at: no merchants are approved for this programme right now

If Ada holds nothing at all, the command still explains itself, because anyone curious enough can tap it:

> **Faya:** You don't have any programme funds right now.
>
> Programme funds are paid to you by an organisation (like the Ministry of Agriculture) and can only be spent at the merchants that organisation approves. They appear here as soon as a payment to you lands.

The same balances appear on **My details**, in chat and on the web page, rendered from one shared formatter so the two surfaces cannot disagree:

> **Faya:** Programme funds: Ministry of Agriculture input support - balance ₦12,500, expires 30 Jun 2026, spend at 3 approved merchants

## Scene 6 - Ada pays from programme funds

When Ada pays a merchant the programme approves, Faya draws on the purse first - the whole amount, or as much of it as the purse holds. On the wallet review she sees exactly which money is about to move:

> **Faya:** Review your Faya payment:
>
> Merchant: Agro Seeds Depot
> Amount: ₦1,500
> From programme funds: ₦1,500
>
> This purchase is paid from Ministry of Agriculture input support, not your wallet, expires 30 Jun 2026.
>
> Wallet balance: ₦25,000
>
> Pay instantly from your Faya wallet?

When the purse covers only part of the purchase, the rest comes from the wallet - and the collection fee is charged **only on the wallet share**, never on restricted money:

> **Faya:** Review your Faya payment:
>
> Merchant: Agric Inputs Ltd
> Amount: ₦10,000
> From programme funds: ₦3,000
> From your wallet: ₦7,000
> Collection fee: ₦240
> Total to pay: ₦10,240
>
> Ministry of Agriculture input support funds ₦3,000 of this purchase; the rest comes from your wallet. Purse expires 30 Jun 2026.
>
> Wallet balance: ₦25,000
>
> Pay instantly from your Faya wallet?

Confirming is one tap ("Pay now"). A fully purse-funded payment ends with:

> **Faya:** ✅ Paid from your programme funds.
>
> Merchant: Agro Seeds Depot
> Amount: ₦1,500
>
> This came from restricted programme funding, so it stayed inside the programme.
>
> Receipt: [ View receipt ]

A split payment ends with both sources named:

> **Faya:** ✅ Payment complete.
>
> Merchant: Agric Inputs Ltd
> Total: ₦10,240
>
> ₦3,000 came from your restricted programme funds and ₦7,240 from your wallet.
>
> Receipt: [ View receipt ]

Paying an invoice works the same way, with the source named on the button's review line:

> **Faya:** Pay invoice INV-2043 from your programme funds:
>
> Merchant: Agro Seeds Depot
> Amount: ₦1,500
>
> Ministry of Agriculture input support available: ₦12,500, expires 30 Jun 2026. This payment will be made from that programme funding, not your wallet.
>
> Pay instantly?

On the browser flow the done-page carries the same story:

> **Faya:** Your programme-funds payment was completed. This money stayed inside the programme. Check WhatsApp for your receipt.

## Scene 7 - When the money can't be spent

Four things can stop a purse payment mid-flight. Every one of them stops the payment and takes nothing - the message says so, and means it, because the allow-list and the balances are re-checked **inside** the transaction that moves the money:

- The programme was paused by its sponsor:
  > **Faya:** This programme has been paused by the sponsor, so its funds can't be spent right now. Nothing was taken. Type MENU to pay another way.
- The merchant was removed from the allow-list between draft and confirm:
  > **Faya:** This merchant is no longer approved to accept the programme funding that would have paid for this, so the payment was stopped and nothing was taken. Type MENU to choose another way to pay.
- The purse lapsed before Ada confirmed:
  > **Faya:** Those programme funds have expired and went back to the sponsor, so this purchase can't be paid from them. Nothing was taken. Type MENU to pay another way.
- The programme was closed outright:
  > **Faya:** Those programme funds are closed, so this purchase can't be paid from them. Nothing was taken. Choose another payment method.

## Scene 8 - Unspent grants go home

Money a beneficiary never spends is not lost to the sponsor. A sweep runs every six hours and returns the balance on any lapsed purse to the sponsor that granted it, closing the purse but keeping its history. Ada sees where the unspent grant went:

> **Faya:** You have no programme funds left.
>
> Ministry of Agriculture input support: returned ₦2,000 on 1 Aug 2026

and on **My details**:

> **Faya:** Programme funds (returned): Ministry of Agriculture input support - unspent ₦2,000 returned to the sponsor on 1 Aug 2026

Musa sees the same money land back on his side, in the Roll-up under **Returned to sponsor at expiry**.

## The rules the feature keeps

- **Restricted money stays restricted by construction.** A purse is a separate ledger account that ordinary wallet cash-out cannot reach, so the restriction is structural, not a policy that could be forgotten.
- **Nothing is taken when a check fails.** Whether the programme is paused, the merchant de-listed, or the purse lapsed - the payment stops with nothing debited, and the customer is told plainly.
- **The wallet share pays the fee; restricted money never does.** A fully purse-funded purchase is fee-free, and a split charges the collection fee only on the wallet remainder.
- **A grant does not eat the personal limit.** Only the wallet portion of a payment counts against the payer's own tier allowance; the restricted programme portion does not.
- **Someone is always accountable for who may sponsor.** Only an administrator approves a sponsor, and each sponsor can only touch its own programs.

## Command cheat sheet

| Who | Types or taps | What happens |
| --- | --- | --- |
| Ada | `programme funds` / `program funds` / `menu_programs` | Lists her purses: balance, expiry and where to spend |
| Ada | Menu -> **Programme funds** | Same listing, with the spendable merchant list |
| Ada | Wallet review -> **Pay now** | Draws from the purse first, wallet for any remainder |
| Ada | `menu` / `cancel` | Home · abandon the current flow |
| Musa | Merchant -> **Programs** | Sponsor console: programs, merchants, beneficiaries, purses |
| Musa | **Create program** | Opens a new restricted-fund program |
| Musa | **Approve** / **Block** / **Remove** a merchant | Sets the allow-list; blocking is immediate |
| Musa | **Enroll** by phone | Makes a beneficiary eligible to receive a purse |
| Admin | Admin -> **Programs** -> **Sponsor merchants** | Approves or revokes a sponsor merchant |

**The one rule the whole script hangs on:** the money is only ever as spendable as the sponsor allows and as the purse still holds; every payment re-checks both inside the transaction, so a restricted purchase either completes correctly or takes nothing at all.

## Behind the curtain - how a purse keeps its promise

Every purse is a ledger account in its own family (`2302_program_purse:<id>`), and its balance is summed from the ledger rather than stored, so there is no cached number to drift. The only code that can authorise a purse payment lives inside the money-movement transaction: if the payee is not on the allow-list, or the purse or programme is not live, the debit simply never happens. Refunds are purse-aware too - returning a purse-funded purchase puts the money back in the purse, still restricted, rather than turning it into spendable cash. Sponsoring credits are replay-safe, bulk disbursements cannot double-fund on replay, and the expiry sweep is idempotent, so a partial failure can never return the same money twice. The captured allow-list state, the spend decision and every return are retained for the audit trail.

## The dials - settings that shape a programme

| Setting | Default | What it controls |
| --- | --- | --- |
| Purse validity (days) | per program, 0 = no expiry | When an unspent purse lapses and returns to the sponsor |
| Expiry sweep interval | every 6 hours | How often lapsed purses are returned to their sponsors |
