# Faya payments — the customer conversation script

A ready-to-use script of every payment conversation a customer has with
Faya, written exactly as the customer experiences it: what they send, what
Faya answers, and which page opens. Every dialogue line below is the actual
product copy. Use it for campaigns, demos, and support training.

Channels: **WhatsApp and Instagram** open a private payment page in the
chat; **Telegram and TikTok** run the whole journey inside the conversation —
on Telegram the card button opens the secure card page directly.

Cast: **Ada**, a customer; **Faya**, the assistant; **[Page]**, a screen Ada
sees in her browser.

---

## Scene 1 — Hello, and the menu

> **Ada:** hi
>
> **Faya:** Welcome to Faya — a simple way to pay merchants.
>
> What name should we use on your receipts?

> **Ada:** Ada Obi, ada@example.com
>
> **Faya:** Thanks, Ada. Sending your confirmation code now.
>
> **Faya:** Enter the 6-digit code we sent to ada@example.com. You can also
> type RESEND or CHANGE EMAIL.

Ada taps the code from her email, then sends her details **in one message**:

> **Ada:** DOB: 1992-05-24
> Gender: Female
> 14 Admiralty Way, Lekki, Lagos
>
> **Faya:** As part of the CBN Tier 1 check, send these three in one message:
> · Date of birth as YYYY-MM-DD
> · Gender: MALE or FEMALE
> · Your residential address
>
> **Faya:** You're all set. Your account is confirmed (CBN Tier 1) and your
> Faya wallet is now active.

The menu is home base:

> **Faya:** Welcome back to Faya. What would you like to do? [ Open menu ]
> ▸ Make payment — Pay a merchant securely
> ▸ Pay an individual — Send money to someone's bank
> ▸ Fund wallet — Add money to your Faya wallet
> ▸ Buy Data — MTN, Airtel, Glo, 9mobile
> ▸ Merchant services · Thrift contributions · My details · Ask Faya · Help

Typing `menu` returns here from anywhere; `cancel` stops whatever is in
progress; `status` replays the latest payment; `help` prints every command.

---

## Scene 2 — Pay a merchant

> **Ada:** pay
>
> **Faya:** Pay a merchant securely — choose who to pay in your browser.
> [ Open payments ]

**[Page · Pay a merchant]** One page, everything on it:

> Tell Faya who to pay and how much — type it below, snap the bill with the
> icons, or say it out loud. Nothing is charged until you tap a payment
> method.

Ada types straight into the ask bar at the top:

> **[Ada types]** pay lagos lunchbox 2500

The merchant **Lagos Lunchbox** is already selected and the amount is
already **₦2,500**. (Lagos Lunchbox sells a Jollof combo, so a single item
step appears first — merchants without a catalogue skip straight past it.)

Ada reviews the summary and taps a payment button — each button's name is
the method:

- **Pay with card** — card details are entered on the bank's own secure
  page, never stored by Faya
- **Bank transfer** — a one-time account number just for this payment
- **Pay from wallet** — balance shown inline

**[Page · Card]** Ada pays on the secure card page and lands straight back
in the chat.

> **Faya:** Payment received.
>
> Merchant: Lagos Lunchbox
> Amount: ₦2,500
>
> Receipt: [ View receipt ]
>
> Open WhatsApp anytime and send MENU for more options.

If the wallet is short of the amount, the page says so plainly —
*"Your wallet has ₦3,200, which is less than ₦3,500. Top it up first, or
choose another payment method."* — with the other options still one tap
away.

---

## Scene 3 — Pay an individual (sending money to a person)

> **Ada:** pay individual
>
> **Faya:** Complete individual verification (CBN Tier 2) before sending
> money to other individuals. [ Verify profile ]

The gate is real: the link opens the verification flow, which asks for a
government-issued photo ID, her BVN and her NIN. Once verified:

> **Faya:** Your BVN and NIN are verified. Your Faya individual profile is
> now approved at CBN Tier 2.

Next time, `pay individual` opens the recipient page. Ada can type the
details, snap a photo of the beneficiary's account details, or speak them.
A garbled account number gets a friendly re-ask, never an error. She
confirms the resolved bank, account and name, enters ₦10,000, and pays:

> **Faya:** Your secure checkout is ready.
>
> Recipient: Chinedu Okafor
> Bank: GTBank
> Account: ••1234
>
> Amount: ₦10,000
> Collection fee: ₦10
> Total you pay: ₦10,010
>
> Recipient receives: ₦10,000 (after NIP fee)
>
> Faya verifies the payment with the payment processor before disbursing to
> the recipient's account.

On completion, the receipt link arrives in the chat — the same
**Payment received** message as Scene 2, with the recipient in place of the
merchant.

---

## Scene 4 — Fund the wallet first, pay from it later

> **Ada:** fund wallet
>
> **Faya:** Fund your Faya wallet
>
> Current balance: ₦2,500
>
> Enter the amount in Naira you'd like to add (e.g. 5000). Your wallet is
> credited in full as soon as the payment is verified.

Ada enters ₦5,000 and chooses **Bank transfer**:

> **Faya:** Your secure checkout is ready.
>
> Top-up amount: ₦5,000
> Method: Bank transfer
>
> Your Faya wallet will be credited ₦5,000 as soon as Faya verifies the
> payment.

She sends the money from her banking app, and the wallet is credited the
moment the payment is verified — her new balance shows on **My details**
and on every **Pay from wallet** button from then on.

If she abandons a payment and reopens an old link, it reopens exactly where
she left off, notice and all — never a dead form.

---

## Scene 5 — Buy data (the same rails, a different product)

> **Ada:** buy data
>
> **Faya:** Choose the mobile network for this data purchase.
>
> **Faya:** Choose a MTN data plan.
>
> Search tip: if you don't see what you want, type a size or keyword like
> 1GB, 2GB, weekly, monthly, SME, social, or night.

Network → plan → phone number → confirm, then the familiar card /
transfer / wallet buttons. Receipt on completion.

---

## Scene 6 — When things go wrong

- **Card declined** → the card page shows the reason — *"The payment was
  not completed (code X). Please try again or choose another payment
  method."* — and the review page reopens with every payment option still
  one tap away.
- **Payment link expired** → reopening it shows *"This link has expired —
  Reopen the flow from your Faya chat to get a fresh link. Your details are
  safe and nothing was charged."*
- **Already paid once, paid again by mistake** → the duplicate is reversed
  automatically: *"The payment you retried was already completed, so this
  one was reversed automatically. No action needed."*
- **Mid-flow confusion** → `back` moves one step, `menu` resets, `cancel`
  drops the flow. Money never moves until a payment button is tapped — that
  tap *is* the payment.

---

## Command cheat sheet (all channels)

| Ada types | What happens |
|---|---|
| `pay` | Link to the merchant payment page |
| `pay individual` / `send money` | Individual transfer (needs CBN Tier 2) |
| `fund wallet` / `top up` / `add money` | Wallet top-up via card or transfer |
| `buy data` | Data purchase flow |
| `menu` / `exit` / `back` | Home · abandon · one step back |
| `status` | Latest payment state + receipt link |
| `help` | This list |
| `cancel` | Drop the current flow and return to the menu |

**The one rule the whole script hangs on:** chat gets you to the right
page, pages do the collecting, and the payment happens when a payment
button is tapped — one tap, one payment, one receipt.
