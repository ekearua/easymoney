# Faya agent cash pickup - the send-cash-for-pickup script

A ready-to-use walk-through of Faya's local agent cash pickup feature ("send cash"), written the way the sender and the agent actually experience it: what Ada types in WhatsApp, what Faya answers, what the recipient does at the counter, and - the part that matters - exactly when money moves. Every dialogue line and screen below is the real product copy. Use it for sender education, agent onboarding, demos, and support training.

Cash pickup answers a simple problem: someone in the city wants to get money to a relative in a town where there is no bank branch and no app. Faya does it without the recipient ever needing a Xego account. The sender pays up front; Xego ring-fences the amount on a payable that belongs to that one pickup; a local agent fronts the cash and is owed it back; the recipient collects with a pickup code, a one-time code the sender forwards, and a government photo ID.

Cast: **Ada**, an urban sender; **Faya**, the assistant; **Chinedu**, the recipient; **Bola**, the agent at the counter; **[Page]**, a screen seen in a browser.

Reach it by typing `send cash` (also `cash pickup`, `send to agent`, `pay agent`). It is deliberately not a menu row: the main menu already sits at WhatsApp's ten-row limit.

## Scene 1 - Ada starts, and passes the identity gate

> **Ada:** send cash
>
> **Faya:** Complete individual verification (CBN Tier 2) before sending money to other individuals. [ Verify profile ]

Sending cash on someone's behalf needs the same CBN Tier 2 identity as paying another individual, so a new sender is routed to verification first (government photo ID, BVN, NIN). Once that is done:

> **Faya:** Your BVN and NIN are verified. Your Faya individual profile is now approved at CBN Tier 2.

Afterwards `send cash` opens the flow directly.

## Scene 2 - Ada names the recipient

> **Faya:** Send cash for pickup
>
> Enter the recipient's full name (as it appears on their ID):

> **Ada:** Chinedu Okafor
>
> **Faya:** Recipient: Chinedu Okafor
>
> Enter the recipient's phone number (e.g. 08012345678):

The name must match the ID the recipient presents at the counter - the agent checks it. The phone number is optional to the recipient (they have no account), but it is how Faya would reach them.

> **Ada:** 08031234567
>
> **Faya:** Recipient: Chinedu Okafor (+2348031234567)
>
> Enter the amount in Naira (e.g. 5000):

## Scene 3 - Ada chooses where the money is collected

> **Ada:** 15000
>
> **Faya:** Where should the recipient collect the cash?
>
> [ Choose area ]

The picker lists the regions that currently have an agent, each with its state and LGA. Ada taps one, or types a name like `Ibadan`.

Behind the tap, Faya assigns the least-loaded active agent in that region and works out the agent's commission. If nothing is available:

> **Faya:** No agent is available in Ibadan yet. Choose a different area.

And if the amount is above what that region's agent may pay out in one collection:

> **Faya:** Agents in Ibadan can pay out up to ₦20,000 per collection. Enter a smaller amount or choose another area.

## Scene 4 - Ada reviews and pays

Faya shows the full cost before anything moves. The sender pays the amount, the usual transfer fee, and the agent's fee on top:

> **Faya:** Review cash pickup
>
> Recipient: Chinedu Okafor (+2348031234567)
> Collect at: Ibadan
>
> Amount: ₦15,000
> Transfer fee: ₦270
> Agent fee: ₦75
> Total you pay: ₦15,345
>
> The recipient collects the full amount in cash from the agent with their pickup code and a valid ID.
>
> Send 1 to pay by bank transfer, 2 to pay from your wallet, or cancel to abort.

Whichever rail she picks, the checkout is the familiar one. On bank transfer:

> **Faya:** Your secure checkout is ready.
>
> Recipient: Chinedu Okafor
> Collect at: Ibadan
>
> Amount: ₦15,000
> Transfer fee: ₦270
> Agent fee: ₦75
> Total you pay: ₦15,345
>
> Faya verifies the payment before the pickup code is issued.

Crucially, **nothing is ring-fenced and no code is minted until the payment is verified**. A draft is a price, not a promise.

## Scene 5 - Xego mints the pickup code

The moment the payment is verified, the post-success hook opens the remittance and sends Ada the code:

> **Faya:** Your Faya cash pickup code for ₦15,000 to Chinedu Okafor is 483920. Give it to Chinedu Okafor at the agent when collecting. The code expires 18 Oct 2026 14:30. Request an OTP when the recipient is ready to collect.

The code is single-use and stored only as a salted hash - nobody at Xego can read it back, and it is the *claim* that only redeems inside the payout transaction. It never touches a wallet, so it can never be spent like cash anywhere else. The window is 72 hours by default.

At this point the money is held on a payable that belongs to this one pickup: Xego owes the recipient, and the agent's commission is already booked to the agent's float.

## Scene 6 - Ada requests the one-time code when Chinedu is ready

The sender is the second factor (D8): the one-time code goes to *Ada*, who forwards it, because the recipient has no account of their own. She asks for it when he is actually at the counter:

> **Ada:** otp
>
> **Faya:** Your OTP for the ₦15,000 cash pickup to Chinedu Okafor is 907144.
>
> It expires in 10 minutes. Share it with the recipient so they can collect at the agent.

Only one OTP is active at a time, and it is short-lived (10 minutes by default). If there is nothing waiting:

> **Faya:** You have no cash pickup waiting for collection. Send send cash to start one.

If she waits too long and the window has closed:

> **Faya:** That cash pickup has expired. Send send cash to start a new one.

## Scene 7 - Bola signs in to the agent console

The agent operates a web console at **/agent**. Bola signs in with the email or phone number tied to her agent identity:

> **[Page - Agent login]** Agent console
>
> Sign in to pay out cash pickups.
> - Email or phone
> - Password
> [ Sign in ]

If the deployment requires a second step, she completes the shared second-factor screen before landing on the redemption page.

## Scene 8 - Bola pays out the pickup

On **[Page - Cash pickup]** she sees the form and a display-only list of the agent's awaiting pickups (recipient, masked phone, amount, expiry). The list is for staffing only; the code is what authorises a payout.

> **Faya:** Pay out a pickup
>
> Check the recipient's government photo ID, then enter the pickup code the sender shared, the one-time code the sender forwarded, and the ID details. The recipient must be the named person.

Bola enters the six-digit pickup code, the six-digit OTP Ada forwarded, and the government ID Chinedu presents (NIN, BVN, driver's licence, voter's card or passport). Where the deployment has authenticator apps configured, the per-payout step-up also asks for a fresh code from Bola's own authenticator app - a distinct check from the recipient's OTP.

Bola taps **Pay out cash**. Inside one transaction, Faya:

- finds the one pickup awaiting collection that matches the code, for this agent only;
- verifies the OTP (attempt-capped, so guessing is bounded);
- captures the government ID - sealed on the row and reduced to a salted hash that keys the recipient's per-customer CBN ceiling across the whole agent network;
- screens the recipient against sanctions (fail-closed);
- checks the agent's status, per-payout and daily caps, and float;
- pays the cash, discharges the payable, and books the amount to the agent's float.

If everything passes, the console lands on the receipt.

## Scene 9 - The receipt the CBN requires

> **[Page - Cash pickup receipt]** Payout <id>
>
> - Recipient: Chinedu Okafor
> - Amount paid: ₦15,000
> - Agent: <agent name>
> - Region: Ibadan
> - ID presented: nin
> - Paid at: <timestamp>
> - Journal ref: <reference>

The page also prints the plain receipt text and a **Print** button. A receipt names the agent and the region, so a payout can always be tied to the counter that made it - exactly what the agent-banking guidelines ask for.

## Scene 10 - When a payout is refused

Every refusal is written to be read at a counter, not in a log. Bola sees the reason on the same page:

| Situation | What Bola sees |
| --- | --- |
| Code matches no pending pickup | That pickup code was not recognised. Check it and try again. |
| No active one-time code | There is no active one-time code for this pickup. Ask the sender to request one. |
| Wrong one-time code | That one-time code was not correct. |
| One-time code expired | That one-time code has expired. Ask the sender to request a new one. |
| Too many wrong one-time codes | Too many wrong one-time codes. Ask the sender to request a new one. |
| No government ID presented | A government photo ID is required to pay out. |
| Recipient flagged by screening | This payout is on hold pending a compliance review. |
| Recipient's CBN ceiling reached | This recipient has reached their cash-out limit for the period. |
| Pickup is not for this agent | This remittance is not assigned to your agent location. |
| Pickup already collected | This pickup was already redeemed. |
| Pickup expired or not yet claimable | This pickup has expired or is not yet claimable. |
| Agent not active | Your agent account is not active. Contact operations. |
| Above the agent's per-payout cap | This payout exceeds your agent's limit. |
| Float cannot cover the payout | Your agent float cannot cover this payout. Settle or top up first. |
| Feature switched off | Cash pickup at an agent is currently unavailable. |

Refusals never move money. The recipient's ID hash and a manual-review trail are retained, so a blocked attempt is auditable.

## Scene 11 - What happens to the money

Cash pickup reuses the ordinary collection rails and adds two new ledger accounts, so the journal always proves where every naira went:

- **The sender pays in.** The amount lands on `2100_customer_float`, the transfer fee books to `4100_sales_revenue`, and the amount is ring-fenced on `2350_remittance_payable:<pickup>` - Xego owes the recipient. The agent commission is booked to `2400_agent_float:<agent>` (Xego owes the agent).
- **The agent pays out.** The payable is discharged (dr `2350_remittance_payable`) and the amount is booked to the agent's float (cr `2400_agent_float`). The claim has shifted to the agent, who fronted the cash.
- **Xego settles the agent.** On a cycle, the agent's float is settled to their bank (dr `2400_agent_float`, cr `1100_operating_bank`). The agent float is never stored as a balance; it is always the sum of its ledger entries, so it can never drift from the book.
- **Unclaimed pickups refund the sender.** When the 72-hour window passes with no collection, the amount and the agent commission are reversed back to the sender's wallet automatically, and the pickup, its code and any OTP are closed. The transfer fee is retained. This sweep is idempotent, so a restart cannot double-refund.

The agent commission is added *on top of* the sender's existing transfer fee - cash pickup introduces no new remittance fee of its own.

## Status glossary

| Status | Who it applies to | What it means |
| --- | --- | --- |
| awaiting_pickup | remittance | Paid for; the code is live and the window is open. |
| paid | remittance | Collected at the agent; the payable is discharged. |
| expired | remittance | Window passed uncollected; the sender was refunded. |
| pending | pickup code | Minted and usable; not yet redeemed. |
| active | one-time code | Valid; superseded or expired as time passes. |

## The rules the whole script hangs on

- **The recipient needs no Xego account.** The money is held on a payable that belongs to the one pickup, redeemed by a code - so nothing has to be sent to a wallet the recipient may not have.
- **Three things must agree at the counter: the pickup code, the one-time code the sender forwards, and a government photo ID.** The sender is the second factor; the ID binds the payout to the named person and keys their CBN ceiling.
- **Only the assigned agent, in the assigned region, can pay out** - a code is not portable to another counter.
- **Money moves exactly once, inside one transaction.** A refusal moves nothing; an expiry is a balanced reversal; the journal is the record of both.

## Behind the curtain - why a code cannot be double-spent

The pickup code is never a bearer token out in the world; it is a claim redeemed only inside the single payout function. That function locks the agent's own pending pickups, compares the code hash in constant time, and only then moves money - so two consoles racing on the same code cannot both succeed, and a code cannot be spent as cash anywhere else. A remittance is a genuine payable (`2350_remittance_payable:<id>`), not a tag on a wallet, which is what lets expiry and refund be ordinary, provable movements.

## The dials - settings that shape the network

| Setting | Default | What it controls |
| --- | --- | --- |
| AGENT_NETWORK_ENABLED | true | Feature kill switch for redemption and settlement. |
| REMITTANCE_TTL | 72h | How long a pickup code stays collectable. |
| REMITTANCE_OTP_TTL | 10m | How long a one-time code is valid. |
| AGENT_ID_REQUIRED | true | Forces a government photo ID on every payout. |
| AGENT_DEFAULT_TIER | standard | Tier assigned when an agent is activated. |
| AGENT_ID_HASH_PEPPER | (unset) | Server secret keying the per-customer ID hash. |
| AGENT_STANDARD_PER_PAYOUT_KOBO | 2000000 | Standard tier: per-collection payout ceiling (₦20,000). |
| AGENT_STANDARD_DAILY_KOBO | 20000000 | Standard tier: agent daily ceiling (₦200,000). |
| AGENT_STANDARD_MAX_OUTSTANDING_KOBO | 50000000 | Standard tier: max outstanding float (₦500,000). |
| AGENT_STANDARD_COMMISSION_BPS | 50 | Standard tier: agent commission (0.50%). |
| AGENT_SUPER_* | - | Super tier: ₦50,000 / ₦600,000 / ₦1,500,000 / 40 bps. |
| AGENT_MAX_* | - | Max tier: ₦100,000 / ₦1,200,000 / ₦2,500,000 / 30 bps. |

Every per-tier number is bounded by the CBN ceilings during configuration validation: an agent may not pay out more than ₦1.2m a day, and a single recipient may not take more than ₦100,000 a day or ₦500,000 a week across the whole network.

## Command cheat sheet (chat)

| Ada types | What happens |
| --- | --- |
| `send cash` | Start a cash pickup (needs CBN Tier 2) |
| `cash pickup` / `send to agent` / `pay agent` | The same flow - reach it by typing, not from a menu row |
| `otp` / `get otp` / `pickup otp` | Mint the one-time code for the newest awaiting pickup |
| `status` | Replays the latest state |
| `cancel` | Drops the flow and returns to the menu |
