# Faya — external service cost model

Cost estimate for every external service the Faya user journey touches, at
1,000 / 5,000 / 10,000 / 50,000 / 100,000 registered users, with vendor
comparisons for compliance and server costs. Every unit price carries its
source; every total is derived from stated activity assumptions, so a number
can be disputed by disputing the assumption rather than the arithmetic.

Symbols used throughout: **₦** = Nigerian naira; **US$** = United States
dollars; **€** = euro. Exchange rate used throughout: **₦1,600 = US$1**
(illustrative; the rate moves).

**Out of scope by decision:** the payment rails (Interswitch card, transfer,
dedicated virtual account (DVA), and Nigeria Inter-Bank Settlement System (NIBSS) Instant Payment (NIP) fees) and
data vending (VTPass). Both are pass-through economics — recovered through
the fee split the platform already books per payment — so they are
operating-expense-neutral and excluded from this model entirely.

---

## 1. Service inventory (grounded in the code)

| Capability in the journey | Code seam | External service |
|---|---|---|
| WhatsApp chat | `internal/providers/whatsapp` | Meta Cloud API — per message |
| Instagram direct-message chat | `internal/providers/instagram` | Free (Meta Graph API) |
| Telegram chat | `internal/providers/telegram` | Free (Telegram Bot API) |
| TikTok chat | `internal/providers/tiktok` | Free (direct messages) |
| Bank Verification Number (BVN) and National Identification Number (NIN) lookup (Central Bank of Nigeria (CBN) Tier 2) | `internal/providers/identity` (ninbvnportal adapter today; Smile ID / Dojah / Prembly / Youverify interchangeable behind `ports.IdentityVerifier`) | Identity vendor per verified lookup |
| Politically exposed person (PEP) and sanctions screening | `internal/providers/screening` (generic HTTP adapter; `ports.SanctionsScreener`; vendor record: `AML_VENDOR_DECISION_RECORD.md`) | Anti-money-laundering (AML) vendor per search or monthly floor |
| Know Your Business (KYB) evidence: Corporate Affairs Commission (CAC) registry | `internal/providers/cac` | Registry application programming interface (API) per query |
| KYB evidence: Federal Inland Revenue Service (FIRS) Tax Identification Number (TIN) | `internal/providers/firs` | Registry API per query |
| Address / location confirmation | `internal/providers/geo` | Geocoding per query |
| Artificial intelligence (AI) ask-bar parsing, optical character recognition (OCR) of bills and identity documents, voice notes via speech-to-text (STT) | `internal/providers/ai` (OpenAI-compatible: chat + vision + Whisper; `ports.ChatAI/ImageReader/SpeechToText`) | OpenAI per token / per audio minute |
| Email codes, receipts, invoices | `internal/providers/email` (Simple Mail Transfer Protocol (SMTP)) | Relay per message |
| Short Message Service (SMS) fallback | `internal/providers/sms` | Gateway per segment |
| Merchant webhooks, ledger, wallets, monitoring, admin console | internal (PostgreSQL, Redis, Kafka self-hosted) | Infrastructure only |

Not billed per user: Telegram, Instagram, TikTok, merchant webhooks, the
admin console.

---

## 2. Activity assumptions per 1,000 registered users (monthly steady state)

- **Active payers**: 40 percent transact monthly → 400 per 1,000 users.
- **Payments per active payer**: 4 per month → **1,600 payments per 1,000 users**.
- **Average payment**: ₦5,000 → gross merchandise value (GMV) of ₦8 million per 1,000 users per month.
- **New registrations**: 10 percent of the base per month (100 per 1,000 users).
- **CBN Tier 2 upgrades (BVN and NIN)**: 25 percent of the new cohort → 25 BVN lookups plus 25 NIN lookups.
- **Screening**: every new user screened once (100) plus 50 rescreens of open high-risk cases = 150 searches.
- **Merchants**: 2 new KYB merchants per 1,000 users (one CAC check, one FIRS check, and one geocode each).
- **AI sessions**: 4,000 per 1,000 users — one ask-bar parse per payment (1,600), media OCR and STT (~267), assistant and menu chats (~2,100). Consistent with the `AI_TOKENS_PER_NGN` setting (250 tokens per naira) already in the application configuration.
**How the 14-message driver is derived (itemized send-path build-up).**
Only business-sent messages are billable; inbound customer messages are
free. Reading the code's actual send sites per payment:

| Component | Per payment | Code path |
|---|---|---|
| Interactive payment-page opener (button message) | 1 | `sendLink` / `SendCheckout` when the flow link is issued |
| Pending/status template after the gateway redirect | 1 | `SendTemplate` status update while the gateway processes |
| Payment-received (or automatic-refund) confirmation with receipt link | 1 | the `Payment received.` completion message |
| Failure and retry templates (expected share) | 0.5 | decline copy plus the reopen call to action |
| Receipt image or receipt message (Instagram and TikTok media caption) | 0.5 | `SendImage` receipt caption on the media channels |
| Business replies to customer questions (24-hour service window) | 0.5 | service-class replies; inbound messages are free |
| In-chat flow uplift (Telegram and TikTok run in chat, not on a page) | 0.5 | method-selection and confirmation turns |
| **Expected messages per payment** | **5.0** | sum of the components |

The expected path is 5.0 messages; the billing driver applies an editable
**2.8× planning contingency multiplier** for retries, duplicate attempts,
redelivered templates, and support surges, reaching **14**. Setting the
multiplier to 1 bills the bare expected path; every component is an
editable row in the workbook's Assumptions sheet. Chargeable volume:
1,600 payments × 14 = 22,400, minus the 1,000 free service messages per
number per month → 21,400 chargeable; plus 200 authentication messages.
- **SMS**: 300 segments. **Email**: 2,500 messages.

### Unit rate card (researched, September 2026)

| Service | Unit | Price | Source basis |
|---|---|---|---|
| WhatsApp — service/utility | message | US$0.0067 (₦11); first 1,000 service messages free per number per month | Meta per-message rate card for Nigeria, effective 1 October 2026 |
| WhatsApp — authentication | message | US$0.0145 (₦23) | same |
| WhatsApp — marketing | message | US$0.0516 (₦83) | same (marketing campaigns are excluded from this model) |
| SMS | 160-character segment | ₦4 | Nigerian published rates from Termii and Africa's Talking |
| Email | message | ₦0.5 | bulk SMTP relay |
| BVN lookup | lookup | US$0.40 (₦640) | Prembly "from US$0.50 per check", Youverify and Dojah ranges; US$0.40–0.80 typical |
| NIN lookup | lookup | US$0.40 (₦640) | same |
| PEP/sanctions screening search | search | US$0.25 (₦400) | API-vendor per-search norms; the ComplyAdvantage Starter floor applies as a minimum (see Section 5) |
| CAC status check | query | ₦500 | local registry API, typical |
| FIRS TIN check | query | ₦200 | typical |
| Geocoding | query | ₦50 | typical per-1,000 pricing |
| OpenAI chat and OCR (small vision model class, ~6,000 tokens per session) | session | ₦24 (US$0.015) | US$0.15 and US$0.60 per 1 million tokens (input/output); OCR and ask-bar parsing merged into one vision call |
| OpenAI Whisper STT | audio minute | US$0.006 (₦9.6) | published rate |

---

## 3. Monthly variable cost per 1,000 users (steady state)

| Line | Driver × rate | Cost (₦) |
|---|---|---|
| WhatsApp service/utility messages | 21,400 × ₦11 | 235,400 |
| WhatsApp authentication messages | 200 × ₦23 | 4,600 |
| SMS segments | 300 × ₦4 | 1,200 |
| Email messages | 2,500 × ₦0.5 | 1,250 |
| Bank Verification Number (BVN) lookups | 25 × ₦640 | 16,000 |
| National Identification Number (NIN) lookups | 25 × ₦640 | 16,000 |
| PEP/sanctions screening — onboarding | 100 × ₦400 | 40,000 |
| PEP/sanctions screening — rescreens | 50 × ₦400 | 20,000 |
| Corporate Affairs Commission (CAC) checks | 2 × ₦500 | 1,000 |
| Federal Inland Revenue Service (FIRS) TIN checks | 2 × ₦200 | 400 |
| Geocoding | 2 × ₦50 | 100 |
| OpenAI chat and OCR sessions | 4,000 × ₦24 | 96,000 |
| OpenAI Whisper speech-to-text | 100 minutes × ₦9.6 | 960 |
| Telegram / Instagram / TikTok | — | 0 |
| **Total per 1,000 users (Low–High band)** | | **₦432,910 base (₦388,710–858,620, ~US$243–537)** |

### Unit economics at this steady state

| Metric | Value |
|---|---|
| Variable cost per payment | ₦432,910 ÷ 1,600 = **₦271 (₦243–537 Low–High)** |
| Variable cost per active payer (4 payments) | **₦1,082 per month** |
| Variable cost per registered user | **₦433 per month** |
| Cost share: WhatsApp | **55 percent** |
| Cost share: AI | **22 percent** |
| Cost share: screening | **14 percent** |
| Cost share: Know Your Customer (KYC) — BVN and NIN lookups | **7 percent** |

**Revenue cross-check.** At a 1.5–2 percent collection fee on ₦8 million of
GMV per 1,000 users, fee revenue is ₦120,000–160,000 per month against
₦433,000 of variable external cost — external cost is **~5.4 percent of
GMV**. That gap is closed by pricing and mix levers (Section 7), not by
infrastructure: infrastructure is a rounding error next to messaging.

---

## 4. Totals at scale (variable + infrastructure)

| Users | Variable services per month | Infrastructure per month (Section 6) | **All-in per month** | Per user per month | Per payment |
|---|---|---|---|---|---|
| **1,000** | ₦433,000 (US$271) | ₦80,000 (US$50) | **₦513,000 (US$321)** | ₦513 | ₦321 |
| **5,000** | ₦2.16 million (US$1,353) | ₦208,000 (US$130) | **₦2.37 million (US$1,483)** | ₦474 | ₦296 |
| **10,000** | ₦4.33 million (US$2,706) | ₦528,000 (US$330) | **₦4.86 million (US$3,036)** | ₦486 | ₦304 |
| **50,000** | ₦20.7 million (US$12,940)¹ | ₦1.6 million (US$1,000) | **₦22.3 million (US$13,940)** | ₦446 | ₦279 |
| **100,000** | ₦41.3 million (US$25,830)² | ₦4.8 million (US$3,000) | **₦46.1 million (US$28,830)** | ₦461 | ₦288 |

¹ at committed tiers: screening −20 percent, identity −20 percent.
² at committed tiers: screening −25 percent, identity −15 percent. The
100,000-user screening figure assumes rescreen cadence rises with
balance-held risk to ~12 screens per active user per year; trigger-based
rescreening as argued in `AML_VENDOR_DECISION_RECORD.md` would halve the
line instead of growing it.

**Headline: external cost per payment lands at ₦279–₦321 all-in across the
entire scale range.** Infrastructure is nearly flat in cash (₦80,000–₦4.8
million per month) but its share of the bill falls as volume grows — roughly
8–18 percent of variable cost, largest at 1,000 users; variable services
scale linearly with active users.

**Min–Max band.** Applying the rate card's Low–High ranges (the workbook's
Scale totals sheet computes all three columns) the all-in band is: 1,000
users ₦468,710–₦938,620; 5,000 users ₦2.15–4.50 million; 10,000 users
₦4.42–9.11 million; 50,000 users ₦20.1–41.5 million (after the footnote-1
discounts); 100,000 users ₦41.7–83.7 million (after the footnote-2
discounts). Per payment: ₦293–587 at 1,000 users falling to ₦261–523 at
100,000 users. The base case sits near the bottom of the band; the
high end is dominated by the screening range (₦400–1,600 per search), not
messaging.

---

## 5. Compliance vendor comparison (at our real volumes)

Our volumes: **screening** at 150 searches per 1,000 users per month → 750
at 5,000 users, 1,500 at 10,000 users, 7,500 at 50,000 users, 15,000 at
100,000 users. **Identity** at 50 lookups per 1,000 users per month (25 BVN
plus 25 NIN) → 5,000 lookups per month at 100,000 users. **CAC, FIRS, and
geocoding**: negligible cost at every scale (2 merchants per 1,000 users →
₦1,500 per 1,000 users — keep any vendor with per-query billing; no
comparison matters here).

Prices below are quoted in the vendor's own currency; the naira figures in
this section use the editable exchange-rate cells (US$1 = ₦1,600, €1 =
₦1,750, each with a Low–High range — ₦1,500–1,800 and ₦1,650–1,950), so the
comparison reconverts as rates move.

### 5.1 AML screening (PEP, sanctions, adverse media)

| Vendor | Pricing shape | 1,000 users (150 searches) | 10,000 users (1,500 searches) | 100,000 users (15,000 searches) | Tradeoff |
|---|---|---|---|---|---|
| **ComplyAdvantage** | Starter floor US$99.99–179 per month (100 searches included), overage above | **US$179 floor** (₦286,000) — the floor dominates | ~US$180–400 | ~US$1,900–3,800 per month | Strongest adverse-media analysis of the API-first group; 49 list types; the payment screening product is new (May 2026) |
| **Napier AI** | Custom contract; real-time screening heritage; higher floor | ~US$1,000–2,000 floor (estimated) | custom | custom (likely US$2,000–5,000 per month) | Best real-time payment-screening pedigree; every capability from one engine; contract minimums likely exceed ComplyAdvantage at low volume |
| **dilisense** | First 100 searches free per month, then €0.10 per call, falling to ~€0.01 at volume | **€5–15** (₦8,000–24,000) | ~€140 | ~€150–1,500 | Cheapest at low volume; thinner adverse media; no payment-screening module; weaker list provenance |
| **OpenSanctions (self-hosted)** | Free/open data; our infrastructure only | **₦0 licence** | ₦0 | ₦0 (plus ~US$50–100 per month infrastructure) | No adverse media, no politically-exposed-person relatives and close associates (RCA) depth, no list-version provenance, no service-level agreement (SLA) — fails CBN Baseline Standards expectations for a production financial institution; development and simulation use only |

**Reading:** at 1,000–10,000 users, dilisense or the ComplyAdvantage floor
are both affordable (₦8,000–286,000 per month — a fraction of one month's
WhatsApp bill, shrinking relative to messaging as volume grows). At 100,000
users, ComplyAdvantage at roughly US$2,000–4,000 per month is 0.5–0.9
percent of the all-in external bill. **Recommendation stands as in
`AML_VENDOR_DECISION_RECORD.md`: ComplyAdvantage primary (trial-gated),
Napier alternate, dilisense as the budget fallback if the ComplyAdvantage
trial fails its criteria, OpenSanctions for non-production only.** The
decision is not cost-driven at any scale here — the spread between cheapest
and most expensive is smaller than one month's WhatsApp spend — so it should
be decided on coverage and provenance (Section 7 criteria in the vendor
record), not price.

### 5.2 Identity (BVN and NIN)

| Vendor | BVN | NIN | Monthly at 100,000 users (5,000 lookups) | What the price buys |
|---|---|---|---|---|
| **ninbvnportal (current adapter)** | ~₦200–400 | ~₦200–400 | ₦1–2 million | Raw record lookup only — name and date-of-birth match; no photo, no liveness |
| **Dojah** | US$0.20–0.50 | US$0.30–0.60 | ₦2.4–4.8 million | Lookup and verification APIs, document verification, easy swap behind `ports.IdentityVerifier` |
| **Prembly (IdentityPass)** | from US$0.50 | from US$0.50 | ₦4–8 million | Same class as Dojah plus background checks |
| **Youverify** | ~US$0.40–0.80 | ~US$0.40–0.80 | ₦4–8 million | KYC suite including identity-document matching and address verification |
| **Smile ID** | ~US$0.60–1.60 | ~US$0.60–1.60 | ₦6–16 million | Full electronic Know Your Customer (eKYC): document image plus selfie liveness — the CBN-strongest evidence bundle |

**Reading:** the cheap end (ninbvnportal, Dojah base) covers the *record
match* that CBN Tier 2 legally requires; the premium end (Smile ID) adds
liveness and photo evidence that materially reduces impersonation fraud but
costs 4–8 times more. The `ports.IdentityVerifier` boundary makes this a
per-tier decision: the inexpensive record lookup for Tier 2, full eKYC
reserved for Tier 3 and high-risk users only — that hybrid keeps the
100,000-user identity line at ₦2.4–4.8 million instead of ₦6–16 million.

### 5.3 What compliance costs as a share of the bill

| Scale | Screening per month | Identity per month | Compliance share of variable cost |
|---|---|---|---|
| 1,000 users | ₦60,000 | ₦32,000 | 21 percent (a ComplyAdvantage-floor month: up to 74 percent) |
| 10,000 users | ₦600,000 | ₦320,000 | 21 percent |
| 100,000 users | ₦4.5 million² | ₦2.7 million² | 17 percent |

Compliance is the steadiest, most negotiable ~17–21 percent of the bill.
WhatsApp and AI are the volatile ~77–80 percent.

### 5.4 Fee earnings per payment (fees versus the external bill)

The expense model in Sections 3–5 is only half the picture; the other half
is what we collect. The Assumptions sheet now carries an editable **platform
fees** block (defaults from `internal/service/fees.go`, `XEGO_FEE_*`):

| Rail | Fee | Cap | Effective at the ₦5,000 average payment |
|---|---|---|---|
| Card or wallet checkout | 200 bps + ₦100 fixed | ₦3,500 | ₦200 |
| In-app bank-transfer (DVA) | 150 bps | ₦1,500 | ₦75 |
| Bank-transfer checkout | 180 bps | ₦2,500 | ₦90 |

With the default rail mix (60% card/wallet, 25% in-app transfer, 15%
transfer checkout) the **blended collection fee is ₦152.25 per payment** →
**₦243,600 per 1,000 users per month** — about 56 percent of the base
variable bill (₦432,910) before any fee restructuring.

The Scale totals sheet now includes **Fee earnings per month** and **Net per
payment (earnings − all-in)** columns so fees can be read against cost at
every scale. At the current fee configuration net per payment is negative
at every scale (−₦168 at 1,000 users, −₦127 to −₦151 elsewhere in the range):
collection fees do **not** cover the external bill. That gap is
lever 5 in Section 7 — raise bps/caps or add merchant subscriptions — and it
is now visible in the model, not buried in the GMV cross-check.

---

## 6. Server cost comparison (across four hosting families)

Same capacity ladder, four pricing regimes, shown in United States dollars
(USD) with naira equivalents at the editable exchange-rate cell (US$1 =
₦1,600, range ₦1,500–1,800). Specifications per scale
(Section 7) are held constant; only the hosting family changes. Latency
note: Nigerian users feel the roughly 120–180 millisecond round-trip time
(RTT) to European and American regions on page loads, but the chat-first
journey hides most of it (webhooks and asynchronous messaging dominate; only
the browser payment pages are round-trip-time sensitive).

| Scale | Hyperscale (Amazon Web Services, Google Cloud Platform, Microsoft Azure) | Value cloud (Hetzner, DigitalOcean, Vultr) | Oracle Cloud (ARM A1 instances) | Nigerian local infrastructure as a service (IaaS; Layer3 and similar) |
|---|---|---|---|---|
| 1,000 (1 application server + 1 database) | US$85 | US$25 | US$12 | US$60 |
| 5,000 (2 application servers + larger database + Redis and Kafka) | US$220 | US$70 | US$35 | US$150 |
| 10,000 (2 application servers, 16-gigabyte (GB) database, 3-node Kafka) | US$500 | US$160 | US$80 | US$350 |
| 50,000 (3 application servers, 32-GB database plus replica) | US$1,400 | US$450 | US$220 | US$1,000 |
| 100,000 (multiple availability zones (AZ), 64-GB database plus synchronous replica, 1-terabyte (TB) storage) | US$3,800 | US$1,200 | US$600 | US$2,800 |

(Assumes managed PostgreSQL on the hyperscale column; self-managed on the
value, ARM, and local columns — that operational tradeoff is part of the
price.)

**Reading:**
- **Infrastructure is the smallest bill line at every scale** — using the
  planning figures from Section 4, about 16 percent of the all-in at 1,000
  users, falling to 7–11 percent above 5,000 users. Even the most expensive
  family (hyperscale, fully managed) stays under a quarter of the bill at
  entry scale. Optimise service costs first.
- **Oracle ARM is 6 times cheaper than hyperscale** at 100,000 users
  (US$600 versus US$3,800 per month) and is a legitimate production choice
  for this stack (the Go binary, PostgreSQL, and Kafka all run comfortably
  on Ampere cores); the tradeoff is self-managed PostgreSQL
  high-availability (HA) discipline.
- **Value cloud (Hetzner, DigitalOcean)** is the pragmatic middle: about
  US$1,200 per month at 100,000 users with better tooling than Oracle at
  three times the price.
- **Nigerian local IaaS** costs about 2.3 times value-cloud, buys ~5–20
  milliseconds of latency and a clean Nigeria Data Protection Act 2023
  (NDPA) data-residency story. If residency becomes a regulatory requirement
  for payment data, this column stops being optional — price it into the
  licensing decision, not the infrastructure one.
- **Recommended shape:** start hyperscale (free tier and credits) → value
  cloud from ~10,000 users → Nigerian local (or a hybrid: application on
  value cloud, PostgreSQL in Nigeria) only when residency or latency
  evidence demands it.

---

## 7. Capacity gates, levers, and one-time costs

### Capacity gates (what forces each step)

- **1,000 users** — everything at entry tiers; in-process event bus and rate limiter.
- **5,000 users** — WhatsApp crosses the free tier; identity vendors offer committed tiers (~10–15 percent off); Kafka moves from in-process to a single broker.
- **10,000 users** — Meta business verification and daily message-limit tiers become binding; screening moves from monthly floor to committed per-search pricing.
- **50,000 users** — identity and screening annual commitments (~20 percent off); dedicated PostgreSQL server plus replica; partitioning and the AML archive ledger must exist (the five-year retention floor grows ~200,000 immutable rows per month at this scale).
- **100,000 users** — every vendor on annual committed pricing; WhatsApp quality-rating management becomes an operational discipline (a dropped rating throttles the whole channel); multiple availability zones everywhere.

### Levers, ranked by naira impact

1. **WhatsApp template discipline** — 55 percent of variable cost. Every saved status template is ₦11; marketing blasts (₦83 per message) stay a board-level decision: a single 100,000-user campaign costs ₦8.3 million, ~20 percent of a month's entire bill.
2. **AI session cost** — 22 percent of variable. Prompt caching plus a smaller classifier model → up to −50 percent of the ₦96,000 per-1,000-users line.
3. **Screening cadence** — trigger-based rescreening (as argued in the vendor record) halves the screening line compared with calendar rescreening.
4. **Identity tiering** — the inexpensive record match for Tier 2, full eKYC only for Tier 3 and high-risk users: keeps the 100,000-user identity line at ₦2.4–4.8 million, not ₦6–16 million.
5. **Fee configuration as the revenue lever** — the `XEGO_FEE_*` settings, wallet-float yield, and merchant subscriptions are what close the 5.4-percent-of-GMV gap, not cost cuts.

### One-time setup (not monthly)

| Item | Cost | When |
|---|---|---|
| Meta business verification + WhatsApp Business Platform API (WABA) | Free (direct Cloud API) | once |
| Identity/screening vendor integration + service-level agreement (SLA) tier | US$0–1,000 | once |
| CAC and FIRS application programming interface (API) accounts | ₦0–100,000 | once |
| Domain name, Transport Layer Security (TLS) certificate, Domain Name System (DNS) | ~₦50,000 per year | annual |

### Excluded from this model

Team (engineering, money laundering reporting officer (MLRO), support),
marketing campaigns, fraud and chargeback provision, settlement float
working capital, CBN licence fees, and the pass-through rail economics
(Interswitch, VTPass) named at the top.
