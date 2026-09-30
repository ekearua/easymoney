# Faya — external service cost model

Cost estimate for every external service the Faya user journey touches, at
1k / 5k / 10k / 50k / 100k registered users, with vendor comparisons for
compliance and server costs. Every unit price carries its source; every
total is derived from stated activity assumptions, so a number can be
disputed by disputing the assumption rather than the arithmetic.

Exchange rate used throughout: **₦1,600 = $1** (illustrative; the rate moves).

**Out of scope by decision:** the payment rails (Interswitch card/transfer/
DVA/NIP fees) and data vending (VTPass). Both are pass-through economics —
recovered through the fee split the platform already books per payment — so
they are opex-neutral and excluded from this model entirely.

---

## 1. Service inventory (grounded in the code)

| Capability in the journey | Code seam | External service |
|---|---|---|
| WhatsApp chat | `internal/providers/whatsapp` | Meta Cloud API — per message |
| Instagram DM chat | `internal/providers/instagram` | Free (Graph API) |
| Telegram chat | `internal/providers/telegram` | Free (Bot API) |
| TikTok chat | `internal/providers/tiktok` | Free (DMs) |
| BVN/NIN lookup (CBN Tier 2) | `internal/providers/identity` (ninbvnportal adapter today; Smile ID / Dojah / Prembly / Youverify interchangeable behind `ports.IdentityVerifier`) | Identity vendor per verified lookup |
| PEP/sanctions screening | `internal/providers/screening` (generic HTTP adapter; `ports.SanctionsScreener`; vendor record: `AML_VENDOR_DECISION_RECORD.md`) | AML vendor per search or monthly floor |
| KYB evidence: CAC registry | `internal/providers/cac` | Registry API per query |
| KYB evidence: FIRS TIN | `internal/providers/firs` | Registry API per query |
| Address / location confirmation | `internal/providers/geo` | Geocoding per query |
| AI ask-bar parse, OCR of bills/IDs, voice notes (STT) | `internal/providers/ai` (OpenAI-compatible: chat + vision + Whisper; `ports.ChatAI/ImageReader/SpeechToText`) | OpenAI per token / per audio minute |
| Email codes, receipts, invoices | `internal/providers/email` (SMTP) | Relay per message |
| SMS fallback | `internal/providers/sms` | Gateway per segment |
| Merchant webhooks, ledger, wallets, monitoring, admin | internal (Postgres, Redis, Kafka self-hosted) | Infra only |

Not billed per user: Telegram, Instagram, TikTok, merchant webhooks, the
admin console.

---

## 2. Activity assumptions per 1,000 registered users (monthly steady state)

- **Active payers**: 40% transact monthly → 400 per 1k users.
- **Payments per active payer**: 4/month → **1,600 payments per 1k users**.
- **Average payment**: ₦5,000 → GMV ₦8m per 1k users/month.
- **New registrations**: 10% of base per month (100 per 1k).
- **Tier-2 upgrades (BVN+NIN)**: 25% of the new cohort → 25 BVN + 25 NIN lookups.
- **Screening**: every new user screened once (100) + 50 rescreens of open high-risk cases = 150 searches.
- **Merchants**: 2 new KYB merchants per 1k users (CAC + FIRS + geocode each).
- **AI sessions**: 4,000 per 1k users — one ask-bar parse per payment (1,600), media OCR/STT (~267), assistant/menu chats (~2,100). Consistent with the `AI_TOKENS_PER_NGN=250` accounting already in config.
- **WhatsApp messages**: ~14 chargeable messages per payment (receipts, status templates, service replies) → 22,400, minus the 1,000 free service messages per number → 21,400 chargeable; plus 200 authentication messages.
- **SMS**: 300 segments. **Email**: 2,500 messages.

### Unit rate card (researched, September 2026)

| Service | Unit | Price | Source basis |
|---|---|---|---|
| WhatsApp — service/utility | message | $0.0067 (₦11); first 1,000 service msgs free per number/month | Meta per-message rate card for Nigeria, effective 1 Oct 2026 |
| WhatsApp — authentication | message | $0.0145 (₦23) | same |
| WhatsApp — marketing | message | $0.0516 (₦83) | same (excluded: marketing excluded from model) |
| SMS | segment | ₦4 | Termii / Africa's Talking NG published rates |
| Email | message | ₦0.5 | bulk SMTP relay |
| BVN lookup | lookup | $0.40 (₦640) | Prembly "from $0.50/check", Youverify/Dojah ranges; $0.40–0.80 typical |
| NIN lookup | lookup | $0.40 (₦640) | same |
| PEP/sanctions screen | search | $0.25 (₦400) | API-vendor per-search norms; CA Starter floor applies as minimum (see §4) |
| CAC status check | query | ₦500 | local registry API typical |
| FIRS TIN check | query | ₦200 | typical |
| Geocoding | query | ₦50 | typical per-1k pricing |
| OpenAI chat+OCR (mini-class vision, ~6k tokens/session) | session | ₦24 ($0.015) | $0.15/$0.60 per 1M tokens; OCR+parse merged into one vision call |
| OpenAI Whisper STT | audio minute | $0.006 (₦9.6) | published rate |

---

## 3. Monthly variable cost per 1,000 users (steady state)

| Line | Driver × rate | Cost (₦) |
|---|---|---|
| WhatsApp service/utility | 21,400 × ₦11 | 235,400 |
| WhatsApp authentication | 200 × ₦23 | 4,600 |
| SMS | 300 × ₦4 | 1,200 |
| Email | 2,500 × ₦0.5 | 1,250 |
| BVN lookups | 25 × ₦640 | 16,000 |
| NIN lookups | 25 × ₦640 | 16,000 |
| Screening — onboarding | 100 × ₦400 | 40,000 |
| Screening — rescreens | 50 × ₦400 | 20,000 |
| CAC checks | 2 × ₦500 | 1,000 |
| FIRS TIN | 2 × ₦200 | 400 |
| Geocoding | 2 × ₦50 | 100 |
| OpenAI chat/OCR | 4,000 × ₦24 | 96,000 |
| OpenAI Whisper | 100 min × ₦9.6 | 960 |
| Telegram / Instagram / TikTok | — | 0 |
| **Total per 1k users** | | **₦432,910 (~$271)** |

### Unit economics at this steady state

| Metric | Value |
|---|---|
| Variable cost per payment | ₦432,910 ÷ 1,600 = **₦271** |
| Variable cost per active payer (4 payments) | **₦1,082/month** |
| Variable cost per registered user | **₦433/month** |
| Cost share: WhatsApp | **55%** |
| Cost share: AI | **22%** |
| Cost share: screening | **14%** |
| Cost share: KYC (BVN+NIN) | **7%** |

**Revenue cross-check.** At a 1.5–2% collection fee on ₦8m GMV per 1k users,
fee revenue is ₦120k–160k/month against ₦433k of variable external cost —
external cost is **~5.4% of GMV**. That gap is closed by pricing/mix levers
(§7), not by infra: infra is a rounding error next to messaging.

---

## 4. Totals at scale (variable + infra)

| Users | Variable services/mo | Infra/mo (§6) | **All-in/mo** | Per user/mo | Per payment |
|---|---|---|---|---|---|
| **1k** | ₦433k ($271) | ₦80k ($50) | **₦513k ($321)** | ₦513 | ₦321 |
| **5k** | ₦2.16m ($1,353) | ₦208k ($130) | **₦2.37m ($1,483)** | ₦474 | ₦296 |
| **10k** | ₦4.33m ($2,706) | ₦528k ($330) | **₦4.86m ($3,036)** | ₦486 | ₦304 |
| **50k** | ₦20.7m ($12,940)¹ | ₦1.6m ($1,000) | **₦22.3m ($13,940)** | ₦446 | ₦279 |
| **100k** | ₦41.3m ($25,830)² | ₦4.8m ($3,000) | **₦46.1m ($28,830)** | ₦461 | ₦288 |
² at committed tiers: screening −20%, identity −20% (50k) and screening −25%,
identity −15% (100k). The 100k screening figure assumes rescreen cadence rises
with balance-held risk to ~12 screens/active user/year; trigger-based
rescreening as argued in AML_VENDOR_DECISION_RECORD.md would halve the line
instead of growing it.

**Headline: external cost per payment lands at ₦280–₦320 all-in across the
entire scale range.** Infra is nearly flat (5–8% of variable cost even at
100k users); variable services scale linearly with active users.

---

## 5. Compliance vendor comparison (at our real volumes)

Our volumes: **screening** 150 searches/1k users/mo → 750 @ 5k, 1.5k @ 10k,
7.5k @ 50k, 15k @ 100k. **Identity** 50 lookups/1k users/mo (25 BVN + 25 NIN)
→ 5k lookups/mo at 100k. **CAC/FIRS/geo**: negligible cost at every scale
(2 merchants/1k users → ₦1.5k/1k users — keep any vendor with per-query
billing; no comparison matters here).

### 5.1 AML screening (PEP/sanctions/adverse media)

| Vendor | Pricing shape | 1k users (150 searches) | 10k (1.5k) | 100k (15k) | Tradeoff |
|---|---|---|---|---|---|
| **ComplyAdvantage** | Starter floor $99.99–179/mo (100 searches incl.), overage above | **$179 floor** (₦286k) — floor dominates | ~$180–400 | ~$1.9k–3.8k | Strongest adverse-media NLP of the API-first group; 49 list types; payment screening product is new (May 2026) |
| **Napier AI** | Custom contract; real-time screening heritage; higher floor | ~$1–2k floor (est.) | custom | custom (likely $2–5k/mo) | Best real-time payment-screening pedigree; every capability from one engine; contract minimums likely exceed CA at low volume |
| **dilisense** | First 100 searches free, then €0.10/call → ~€0.01 at volume | **€5–15** (₦8–24k) | ~€140 | ~€150–1.5k | Cheapest at low volume; thinner adverse-media and no payment-screening module; list provenance weaker |
| **OpenSanctions (self-hosted)** | Free/open data; our infra only | **₦0 licence** | ₦0 | ₦0 (+ ~$50–100/mo infra) | No adverse media, no PEP RCAs depth, no list-version provenance, no SLA — fails CBN Baseline Standards expectations for a production FI; dev/simulation only |

**Reading:** at 1k–10k users, dilisense or the CA floor are both affordable
(₦8k–286k/mo — a fraction of one month's WhatsApp bill, shrinking relative
to messaging as volume grows). At 100k users, CA at ~$2–4k/mo is 0.5–0.9% of
the all-in external bill. **Recommendation stands as in
`AML_VENDOR_DECISION_RECORD.md`: ComplyAdvantage primary (trial-gated),
Napier alternate, dilisense as the budget fallback if the CA trial fails its
criteria, OpenSanctions for non-production only.** The decision is not
cost-driven at any scale here — the spread between cheapest and most
expensive is smaller than one month's WhatsApp spend — so it should be
decided on coverage/provenance (§7 criteria in the vendor record), not price.

### 5.2 Identity (BVN/NIN)

| Vendor | BVN | NIN | 100k-scale monthly (5k lookups) | What the price buys |
|---|---|---|---|---|
| **ninbvnportal (current adapter)** | ~₦200–400 | ~₦200–400 | ₦1–2m | Raw record lookup only — name/DOB match; no photo, no liveness |
| **Dojah** | $0.20–0.50 | $0.30–0.60 | ₦2.4–4.8m | Lookup + verification APIs, document verification, easy swap behind `ports.IdentityVerifier` |
| **Prembly (IdentityPass)** | from $0.50 | from $0.50 | ₦4–8m | Same class as Dojah + background checks |
| **Youverify** | ~$0.40–0.80 | ~$0.40–0.80 | ₦4–8m | KYC suite incl. ID document matching, address verification |
| **Smile ID** | ~$0.60–1.60 | ~$0.60–1.60 | ₦6–16m | Full eKYC: document + selfie liveness, the CBN-strongest evidence bundle |

**Reading:** the cheap end (ninbvnportal, Dojah base) covers the *record
match* that Tier 2 legally requires; the premium end (Smile ID) adds
liveness/photo evidence that materially reduces impersonation fraud but
costs 4–8× more. The `ports.IdentityVerifier` boundary makes this a per-tier
decision: cheap lookup for Tier 2, full eKYC reserved for Tier 3 / high-risk
users only — that hybrid keeps the 100k-scale identity line at ₦2.4–4.8m
instead of ₦6–16m.

### 5.3 What compliance costs as a share of the bill

| Scale | Screening/mo | Identity/mo | Compliance share of variable cost |
|---|---|---|---|
| 1k | ₦60k | ₦32k | 21% (CA-floor month: up to 74%) |
| 10k | ₦600k | ₦320k | 21% |
| 100k | ₦4.8m | ₦3.2m | 19% |

Compliance is the steadiest, most negotiable ~20% of the bill. WhatsApp and
AI are the volatile ~77%.

---

## 6. Server cost comparison (across four hosting families)

Same capacity ladder, four pricing regimes. Specs per scale (§7) are held
constant; only the hosting family changes. Latency note: NG users feel the
~120–180ms RTT to EU/US regions on page loads, but the chat-first journey
hides most of it (webhooks and async messaging dominate; only the browser
payment pages are RTT-sensitive).

| Scale | Hyperscale (AWS/GCP/Azure) | Value cloud (Hetzner/DO/Vultr) | Oracle Cloud (ARM A1) | NG local IaaS (Layer3 etc.) |
|---|---|---|---|---|
| 1k (1 app + 1 db) | $85 | $25 | $12 | $60 |
| 5k (2 app + bigger db + redis/kafka) | $220 | $70 | $35 | $150 |
| 10k (2 app, 16GB db, 3×kafka) | $500 | $160 | $80 | $350 |
| 50k (3 app, 32GB db + replica) | $1,400 | $450 | $220 | $1,000 |
| 100k (3 app multi-AZ, 64GB db + sync replica, 1TB) | $3,800 | $1,200 | $600 | $2,800 |

(Assumes managed Postgres on hyperscale; self-managed on the value/ARM/local
columns — that operational tradeoff is part of the price.)

**Reading:**
- **Infra is 5–10% of the all-in external bill at every scale.** Even the
  most expensive family (hyperscale, fully managed) is a rounding error
  against the WhatsApp + AI lines. Optimize service costs first.
- **Oracle ARM is 6× cheaper than hyperscale** at 100k users ($600 vs
  $3,800/mo) and is a legitimate production choice for this stack (Go binary
  + Postgres + Kafka all run comfortably on Ampere cores); the tradeoff is
  self-managed Postgres HA discipline.
- **Value cloud (Hetzner/DO)** is the pragmatic middle: ~$1,200/mo at 100k
  with better tooling than Oracle at 3× the price.
- **NG local IaaS** costs ~2.3× value-cloud, buys ~5–20ms latency and a clean
  NDPA data-residency story. If residency becomes a regulatory requirement
  for payment data, this column stops being optional — price it into the
  licensing decision, not the infra one.
- **Recommended shape:** start hyperscale (free tier/credits) → value cloud
  from ~10k users → NG local (or hybrid: app on value cloud, Postgres in NG)
  only when residency or latency evidence demands it.

---

## 7. Capacity gates, levers, and one-time costs

### Capacity gates (what forces each step)

- **1k** — everything at entry tiers; in-proc bus/limiter.
- **5k** — WhatsApp crosses the free tier; identity vendors offer committed tiers (~10–15% off); Kafka moves from in-proc to single broker.
- **10k** — Meta business-verification and daily message-limit tiers become binding; screening moves from monthly floor to committed per-search pricing.
- **50k** — identity/screening annual commitments (~20% off); dedicated Postgres + replica; partitioning and the AML archive ledger must exist (the 5-year retention floor grows ~200k immutable rows/month at this scale).
- **100k** — every vendor on annual committed pricing; WhatsApp quality-rating management becomes an operational discipline (a dropped rating throttles the whole channel); multi-AZ everything.

### Levers, ranked by naira impact

1. **WhatsApp template discipline** — 55% of variable cost. Every saved status template is ₦11; marketing blasts (₦83/msg) stay a board-level decision: a single 100k-user campaign costs ₦8.3m, ~20% of a month's entire bill.
2. **AI session cost** — 22% of variable. Prompt caching + a smaller classifier model → up to −50% of the ₦96k/1k-users line.
3. **Screening cadence** — trigger-based rescreening (as argued in the vendor record) halves the screening line vs calendar rescreening.
4. **Identity tiering** — cheap record-match for Tier 2, full eKYC only for Tier 3/high-risk: keeps the 100k identity line at ₦2.4–4.8m, not ₦6–16m.
5. **Fee config as the revenue lever** — the `XEGO_FEE_*` knobs, wallet-float yield, and merchant subscriptions are what close the 5.4%-of-GMV gap, not cost cuts.

### One-time setup (not monthly)

| Item | Cost | When |
|---|---|---|
| Meta business verification + WABA | Free (direct Cloud API) | once |
| Identity/screening vendor integration + SLA tier | $0–1,000 | once |
| CAC/FIRS API accounts | ₦0–100k | once |
| Domain, TLS, DNS | ~₦50k/yr | annual |

### Excluded from this model

Team (engineering, MLRO, support), marketing campaigns, fraud/chargeback
provision, settlement float working capital, CBN licence fees, and the
pass-through rail economics (Interswitch, VTPass) named at the top.
