# Faya bulk payments - the location-aware disbursement script

A ready-to-use walk-through of Faya's bulk payment feature, written the way the merchant and the recipient actually experience it: what Musa taps, what Faya sends, what Ada sees, and - the part that matters - exactly when money moves. Every dialogue line and screen below is the real product copy. Use it for merchant onboarding, demos, and support training.

A bulk payment is one action by which a merchant pays many recipients the same amount, but only to the people who are physically at a chosen place when the window closes. It runs in two stages: stage 1 asks each person to share where they are, and stage 2, at the end of a short time window, pays everyone who answered and qualifies. No one is paid on a guess, and the merchant is only debited for the payments that actually land.

Cast: **Musa**, a merchant; **Faya**, the assistant; **Ada**, a recipient; **[Page]**, a screen seen in a browser.

## Scene 1 - Musa marks the place to pay from

Musa signs in to the merchant dashboard and opens **Merchant -> Payment locations**. A location is not a payment by itself; it is the point that decides who counts as present. Each location carries a name, a latitude and longitude, and a radius in metres, and he can keep as many as he needs (branches, pick-up points, event stands).

On **[Page - Payment locations]** he fills the **New location** form:

- **Name** - for example *Ikeja City Mall*
- **Latitude** - for example *6.6018*
- **Longitude** - for example *3.3515*
- **Radius (metres)** - defaults to the house default of 50

He taps **Save location** and the page confirms with:

> **Faya:** Location saved.

Below the form the locations he already has are listed with their coordinates, radius, a link to their recipients, and actions. Until he has one:

> **Faya:** No locations yet. Add one to start a bulk payment.

Only the recipients who end up inside that radius circle will be eligible to be paid.

## Scene 2 - Musa adds the people who should be paid

Musa opens a location to manage its people on **[Page - Center detail]**. There are three ways to build the list, and all of them land in the same table with a **Source** column so he can see where each number came from:

- Type a number into **Add a recipient**, with an optional name.
- Upload a **CSV or XLSX** file - column A is the phone number and column B (optional) is the name.
- Add them one at a time as he meets people.

After an upload or edit the page confirms with:

> **Faya:** Recipients updated.

Two rules keep this list clean. A phone number belongs to exactly one location per merchant, so the same person is never double-counted across centers. And a number with no Faya account yet is still kept: Faya can target it, and stage 1 invites that person to finish onboarding before they can be paid.

A center that has no recipients shows:

> **Faya:** No recipients yet.

## Scene 3 - Musa starts the batch

Still on the center page, under **Start a bulk payment**, Musa enters the **amount per recipient** in kobo (for example *50000* for five hundred naira) and taps **Start batch**. The page is explicit about the deal:

> **Faya:** Each eligible recipient inside the radius receives the same amount.

Before anything is queued, Faya runs three guards, and each one is answered in plain language rather than a stack trace:

| Guard | What Musa sees if it trips |
| --- | --- |
| The location has at least one recipient | No recipients yet. |
| The recipient count is within the cap | The location has too many recipients for one batch. |
| The business wallet covers the full batch | Business wallet balance is too low for this batch. |

If the batch total sits above the merchant's assigned payout ceiling under its KYB profile, the batch is still created but is stamped for review, and every screen that shows it carries:

> **Faya:** Flagged for compliance review (batch total exceeds the payout ceiling).

Every batch gets a reference of the form **BP-<timestamp>-<ref>**, and that reference is what appears on statements, receipts, and the merchant's batch list.

## Scene 4 - Stage 1 keeps asking, everyone, all at once

The moment the batch is created, stage 1 is queued and runs within seconds. Faya reaches each recipient on the channel it already has for them:

- A number already on WhatsApp gets a native **share your location** request in the chat.
- A number already on Telegram gets the same request on Telegram.
- A number with no Faya account yet gets an invitation to join first.
- A number whose channel cannot send a native location gets a one-tap browser link instead.

The location request reads:

> **Faya:** Share your current location to receive a payment. This link is valid for a few minutes.

And the invitation for a brand-new number reads:

> **Faya:** You have a payment waiting. Create or open your Faya account and share your location to receive it.

Note: the invitation is templated, and the shipped default still reads *"Create or open your Xego account..."*. Set **BULK_STAGE1_TEMPLATE** to the Faya wording, or fix the default, before a public campaign so the invite does not carry the old brand.

Anyone who cannot send a native location is given the fallback page, **[Page - Confirm your location]**, which shows the heading **Confirm your location** and the line:

> **Faya:** Share your current location to receive your payment. This link is valid for a few minutes.

with a single **Share my location** button. Nothing is requested on page load; the location is only captured after an explicit tap.

To stay polite to the messaging platforms, Faya paces stage 1: messages are drained in chunks of a few hundred and throttled to a fixed number per second, with each send retried a few times before it is given up on. Large lists therefore fan out steadily rather than in one blast.

## Scene 5 - Ada answers

Ada replies to the request. If she taps the pin and shares her location natively, or opens the link and taps **Share my location**, the browser sends back only her coordinates and their accuracy - nothing else - and the page tells her:

> **Faya:** Location received. You can close this page.

If she declines the prompt, or the browser cannot help, the page says so plainly:

> **Faya:** Location permission was not granted.

If the window has already closed, or the request has already been used, the page is honest about it rather than pretending:

> **Faya:** This location request is no longer open.

Whatever the channel, the reply is matched back to the one open request for that phone number, and the coordinates plus their measured accuracy are stored against that person's line in the batch.

## Scene 6 - Stage 2 pays the people who showed up

When the window elapses - five minutes by default - stage 2 runs. Faya walks the batch one last time:

- Anyone who never answered expires; they are not paid.
- Anyone who answered is checked in order: are they at least **Tier 2**? is their accuracy good enough? are they inside the radius? The first check that fails sets their status and their reason, and no money moves for them.
- Everyone who passes is paid: the merchant's business wallet is debited and the recipient's wallet is credited, as **one balanced ledger pair per person**. This is deliberate - if a payment cannot be made, the debit simply never happens. Musa is only ever charged for money that actually landed.

The recipient sees the amount and the batch reference in their wallet and receipt. The batch is then finalised: **completed** if everyone eligible was paid, **partial** if some were paid and some were not, and **failed** if nobody was paid.

On **[Page - Batch detail]** Musa watches the whole thing as a set of counts - Targets, Requested, Responded, Eligible, Paid, Failed, Expired, and Debited - above a table of every recipient with their channel, current status, the distance Faya measured, the accuracy they reported, and, for anyone not paid, the reason.

## Scene 7 - What each status means

| Status | Who it applies to | What it means |
| --- | --- | --- |
| pending | item | Recorded but not yet asked. |
| location_requested | item | Faya has asked; waiting for a share. |
| location_received | item | A location came back and is being checked. |
| expired | item | No location before the window closed. |
| out_of_range | item | Answered, but outside the radius. |
| poor_accuracy | item | Answered, but the fix was too coarse to trust. |
| kyc_ineligible | item | Inside the radius, but below Tier 2. |
| not_registered | item | No Faya account to credit yet. |
| paid | item | Credited; the ledger pair is written. |
| failed | item | Could not be paid (send failure, or a program rule). |

## The two rules

Everything above collapses into two promises the feature keeps to the merchant:

- **Money moves only for a person who answered in time, inside the radius, at Tier 2.** A batch is a set of invitations, not a set of debits.
- **One balanced ledger pair per payment, so the wallet is only debited for money that lands.** The summary counts on the batch page are the auditable record of both.

## Behind the curtain - how a batch keeps its place

A batch is deliberately not a long-running process. Creating it enqueues a one-off stage 1 job; stage 1 enqueues a one-off stage 2 job scheduled for the end of the window. A small ticker claims due jobs safely (even with several servers running), so a restart never loses a batch and two servers never pay the same one twice. Sends are spread across a worker pool so one large batch cannot starve the rest of the platform. The captured location, the computed distance, and the reason for every non-payment are all retained for the manual AML review trail.

## The dials - settings that shape a batch

| Setting | Default | What it controls |
| --- | --- | --- |
| BULK_RADIUS_M | 50 | Default radius for a new location, in metres. |
| BULK_MAX_ACCURACY_M | 50 | Coarsest location fix that is still trusted. |
| BULK_CHUNK_SIZE | 500 | Recipients handled per send chunk. |
| BULK_WORKER_CONCURRENCY | 8 | Parallel send workers per server. |
| BULK_SEND_RATE_PER_SEC | 20 | Outbound messages per second. |
| BULK_MAX_TARGETS | 100000 | Largest single batch. |
| BULK_LOCATION_TTL | 5m | How long recipients have to answer. |
| BULK_STAGE1_TEMPLATE | (unset) | Custom wording for the stage 1 invite. |
