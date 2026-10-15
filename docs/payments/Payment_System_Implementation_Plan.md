---
title: XEGO FULL PAYMENT SYSTEM IMPLEMENTATION PLAN
subtitle: PCI DSS Level 1 and ISO/IEC 27001:2022 Aligned
author: Xego Compliance Program
date: July 2026
---

# XEGO FULL PAYMENT SYSTEM IMPLEMENTATION PLAN

| | |
|---|---|
| **Document Owner** | CISO / Head of Engineering |
| **Approved By** | CISO / CEO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Monthly (governance) / on material change |
| **Regulatory Basis** | PCI DSS (Level 1); ISO/IEC 27001:2022; CBN AML/CFT Regulation 2009; NDPR 2019 |

---

## 1. Purpose & Scope

This plan defines how Xego evolves its current Go monolith into the full
payment system described in the production payment-gateway blueprint, while
satisfying PCI DSS Level 1 (QSA-conducted RoC) and ISO/IEC 27001:2022
certification requirements. It is the program-level view: phases, workstreams,
dependencies, control mappings, staffing, budget and exit criteria.

The plan integrates three existing artifacts and does not duplicate them:

- `docs/cost-models/Xego_Production_Requirements.xlsx` — server footprint, service to
  server mapping, network rules, PCI compliance cost model.
- `reports/Compliance_Roadmap.xlsx` — roadmap items (0.1-5.7), codebase
  changes (C1-C30), reporting matrix, compliance documents register.
- `docs/payments/Remediation_Backlog.md` — the severity-sorted engineering backlog
  behind the C-items.
- `reports/Payment_Gateway_Architecture_Blueprint.docx` — the production
  payment-gateway blueprint (container/planes, data model, events, starter
  repo) that this plan and the requirements workbook are derived from;
  `reports/Blueprint_Reconciliation.md` cross-checks the current codebase
  against it.

Strategy: **incremental extraction.** The monolith stays the transaction
backbone and new payment capabilities are built first inside it; services are
extracted only when they become independently scalable or PCI-scoped. This
matches the phased-deployment guidance in the production requirements and
avoids a risky greenfield cut-over.

## 2. Target Architecture Summary

The blueprint specifies the following containers, grouped by plane:

| Plane | Containers |
|---|---|
| Edge | API Gateway (Caddy + Go), Admin / Operations Console |
| Merchant-facing | Merchant API, Checkout / Hosted Fields, Webhook Delivery |
| Payment core | Tokenization Service, Payment Orchestrator, Connector Services, Risk Engine |
| Financial plane | Ledger Service (append-only), Settlement Service, Payout Service |
| Assurance | Reconciliation Service, Disputes Service |
| Backbone | Event Bus (Kafka), OLTP PostgreSQL (+ replicas), Ledger DB, Token DB (PCI zone), Data Warehouse, Object Storage, Monitoring / SIEM |

Operational requirements from the blueprint that gate the design:
idempotency on all writes; ledger as an append-only journal with reversals;
outbox / event-driven integration; PCI scope minimization via hosted fields
and isolated tokenization; three-way reconciliation; webhook signing with
replay protection; tamper-evident operator audit logs.

## 3. Phased Delivery Plan

Phases align to the compliance roadmap (Phase 0-5). Each phase lists its
objective, engineering workstreams (mapped to C-items and roadmap IDs),
infrastructure changes, compliance controls, and exit criteria.

### Phase 0 - Governance & Planning (Month 0)

**Objective:** establish the program: ownership, ISMS scope, policy baseline,
and CI/CD foundation before any platform work.

**Workstreams (roadmap IDs):** CCO appointed + oversight committee (0.1, 0.4);
info-sec + AML/CFT policy approval (0.2-0.3); NFIU/CBN registration +
licensing review (0.5-0.6); ISMS scope + risk register + methodology (0.7);
DPAs + data residency (0.8-0.9); CI/CD pipeline hardened (0.10);
competence/screening policy (0.11); NDA baseline (0.12); secrets removed from
repo history (0.13).

**Infrastructure:** none new; CI build + test + lint on push; container
registry; image tagging by commit SHA.

**Compliance controls:**
- ISO/IEC 27001 Clauses 4-5 (context, scope, leadership), Annex A 5.1.
- CBN AML/CFT: board-approved program baseline.

**Exit criteria:** policies signed; CCO and CISO in role; ISMS scope and risk
methodology approved; CI/CD gate enforced; secrets removed from repo history
(C27).

### Phase 1 - Foundation Hardening + Payment Core (Months 1-3)

**Objective:** secure the existing monolith to production standard and run
the first in-house payment core inside it, with card data kept off Xego
servers via hosted fields.

**Engineering workstreams (C-items):**
- MFA for admin + merchant (C1); RBAC (C2).
- Rate limiting on public endpoints (C3); distributed limiter (C25).
- Tamper-evident admin audit log (C4); event tamper-evidence (C26).
- Structured JSON logging + correlation IDs (C5); error/PII sanitization (C30).
- Secrets management + rotation + history purge (C6, C27); webhook secret out
  of URL (C28).
- Encryption at rest (C7); encryption in transit DB + SMTP (C22).
- Retention purge batching + coverage (C8); legal/regulatory holds (C29).
- Backups / WAL / PITR (C23); migration runner ordering (C24).
- HTTP server hardening (C19).
- KYC data model migration 021+ (C9, schema foundation only).

**Infrastructure (production-requirements Phase 1):** 3 servers - load
balancer + app replica, app + payment + risk, database + events +
tokenization (PCI-isolated subnet). PgBouncer pool=30; PostgreSQL
max_connections raised; `sslmode=require`; WAF in front of edge.

**Compliance controls:**
- PCI DSS Req 3 (tokenization, no PAN at rest), Req 4 (TLS), Req 6 (secure
  SDLC), Req 7-8 (access + MFA), Req 10 (logging), Req 11 (scanning).
- ISO Annex A 5.9, 5.15-5.18, 5.24-5.28, 8.2-8.5, 8.8, 8.10, 8.13, 8.15-8.16,
  8.20, 8.24, 8.32.

**Exit criteria:** penetration test of the hardened build passes; secrets
fully removed from history; at-rest and in-transit encryption live; admin
audit log operational; payment core + hosted-fields checkout accepted for
card capture in the PCI-isolated tokenization zone.

### Phase 2 - KYC/CDD/AML + Extract Payment Services (Months 3-6)

**Objective:** replace simulated KYC with the real L0-L4 ladder, screening and
monitoring, and extract the payment core into standalone services.

**Engineering workstreams (C-items):**
- Real KYC state machine L0-L4 (C10); NIN/BVN verification (C11).
- Sanctions/PEP screening (C12); ML/FT risk scoring (C13).
- Transaction monitoring rules engine (C14); STR/CTR/PEP generators (C15).
- Data-subject rights: export + erasure endpoints (C20).
- Chat content guard (C18).
- Extract Payment Orchestrator, Connector Services, Tokenization Service
  (PCI zone); introduce Kafka event bus (payments.v1, tokens.v1, risk.v1).

**Infrastructure (production-requirements Phase 2):** 3 servers with payment
services extracted; Kafka 3-broker; Redis for rate limiting + risk; PgBouncer
pool=40.

**Compliance controls:**
- CBN CIP/CDD/EDD, sanctions/PEP screening, risk-based approach, suspicious
  activity monitoring, MLPA reporting.
- PCI DSS: scope of payment core now explicit; Req 11 quarterly ASV begins.
- ISO Annex A 8.8, 8.15-8.16, 8.20.

**Exit criteria:** NIN/BVN verification live; screening + risk scoring
operating; test STR filed to NFIU sandbox; KYC review queue operational;
payment core services extracted with working Kafka events.

### Phase 3 - ISMS Full Operation + PCI + Financial Plane (Months 6-12)

**Objective:** run the ISMS in full, complete the PCI DSS Level 1 evidence
pack, and extract the financial plane (ledger, settlement, payout,
reconciliation, disputes, webhook delivery).

**Engineering workstreams (C-items):**
- Append-only ledger service (C16); three-way reconciliation (C17).
- Extract Ledger, Settlement, Payout, Reconciliation, Disputes, Webhook
  Delivery, Admin Console.
- SIEM integration; quarterly ASV scans; annual penetration test (doc 38,
  roadmap 3.2).
- PCI Scope & CDE Inventory (doc 45); Requirements 1-12 Compliance Matrix
  (doc 46); AOC template (doc 44).

**Infrastructure (production-requirements Phase 3):** 9 servers - dedicated
LB, 4 app replicas, risk + tokenization, OLTP primary + 2 read replicas,
ledger DB + Kafka + Redis, monitoring + storage; PgBouncer pool=75.

**Compliance controls:**
- PCI DSS Req 1-12 complete evidence set for Level 1 QSA RoC; CDE boundary
  validated; segmentation evidence ready.
- ISO Annex A 5.29-5.30 (BCP/DR), 8.13 (backup), 5.24-5.28 (IR), 8.15-8.16
  (monitoring), 8.24 (crypto).
- NDPR: NITDA registration; retention aligned to legal holds (C29).

**Exit criteria:** DR drill passed; reconciliation automated; BCP tested;
quarterly ASV scans passing; CDE inventory and Requirements 1-12 matrix
approved; evidence pack handed to QSA.

### Phase 4 - Independent Assurance & Certification (Months 12-15)

**Objective:** obtain ISO/IEC 27001:2022 certification and PCI DSS Level 1
QSA RoC + AOC.

**Workstreams (roadmap IDs):** internal audit programme (4.2); management
review (4.4); ISO Stage 1 & 2 audit evidence packs (4.5); CBN AML/CFT
examination evidence pack (4.6); PCI Level 1 assessment + AOC (4.7, 3.9).

**Compliance controls:**
- ISO Clauses 9-10 (performance evaluation, improvement), Stage 1 + Stage 2.
- PCI DSS Level 1: QSA RoC + signed AOC; quarterly ASV scan evidence.
- CBN AML/CFT independent testing.

**Exit criteria:** ISO 27001 certificate issued; PCI AOC signed by QSA; AML/CFT
independent testing completed.

### Phase 5 - Ongoing Operations (Month 15+)

**Objective:** institutionalize the operating rhythm.

**Workstreams (roadmap IDs):** monthly STR/CTR/PEP returns (5.1-5.2); risk
refresh 12-18 months (5.3); rescreening continuous (5.4); training (5.5);
regulatory watch (5.6); info-sec metrics (5.7).

**Compliance controls:** annual PCI re-certification; annual ISO surveillance
audit; quarterly ASV scans; management reviews.

**Exit criteria:** compliance operating rhythm institutionalized; no overdue
regulatory returns.

## 4. PCI DSS Scope & Controls

- **CDE boundary:** the PCI-isolated tokenization subnet only - hosted-fields
  termination, tokenization service, token DB, admin vault interfaces, SIEM
  ingest of CDE logs. Everything else is out of scope only where segmentation
  is proven (doc 45).
- **Scope minimization:** hosted card-checkout keeps the PAN on the provider
  side; Xego stores tokens, never PAN. Phase 1 minimizes scope; the Level 1
  QSA RoC path is engaged at Phase 3 (volume exceeds 6M transactions/yr).
- **Segmentation:** isolated VLAN / subnet with default-deny ACLs, mTLS inside
  the PCI zone, WAF + DDoS at the edge, egress limited to the card-network
  endpoint.
- **Requirement mapping:** Req 1-12 are tracked per-requirement with evidence
  owners in the Requirements 1-12 Compliance Matrix (doc 46); evidence
  retained with the AOC (doc 44).

## 5. ISO 27001 Annex A Mapping

| Annex A group | Workstreams | Phase |
|---|---|---|
| A.5 Organizational | policy, risk, assets, access, supplier, IR, compliance, PII (0.1-0.9, C4, C20) | 0-3 |
| A.6 People | screening, training, NDAs, remote working (0.11-0.12, 5.5) | 0, 5 |
| A.7 Physical | cloud provider responsibilities, media (1.10-1.11) | 1 |
| A.8 Technological | C1-C30 (auth, crypto, logging, SDLC, backup, monitoring) | 1-3 |

The Statement of Applicability (roadmap 4.5) records applicability and
justifications; the risk treatment plan (0.7) drives residual-risk decisions.

## 6. Service Extraction Order & Sequencing

| Service | Extracted (phase) | Dependencies | DB ownership | Event topics |
|---|---|---|---|---|
| Tokenization Service | 1 | hosted-fields, PCI zone | Token DB | tokens.v1 |
| Payment Orchestrator | 2 | connectors, risk | OLTP (payments) | payments.v1 |
| Connector Services | 2 | bank/card APIs | none (stateless) | connectors.v1 |
| Risk Engine | 2 | Redis + OLTP replica | Redis | risk.v1 |
| Ledger Service | 3 | payment events | Ledger DB (append-only) | ledger.v1 |
| Settlement Service | 3 | ledger | OLTP (settlements) | settlements.v1 |
| Payout Service | 3 | bank APIs | OLTP (payouts) | payouts.v1 |
| Reconciliation Service | 3 | ledger + OLTP replica | OLTP (reconciliation) | reconciliation.v1 |
| Disputes Service | 3 | card schemes | OLTP (disputes) | disputes.v1 |
| Webhook Delivery | 3 | payment events | OLTP (webhooks) | webhooks.v1 |
| Admin Console | 3 | OLTP replica + ledger | read-only | audit.v1 |

Kafka topic families: payments.v1, tokens.v1, ledger.v1, settlements.v1,
payouts.v1, reconciliation.v1, disputes.v1, webhooks.v1. Every event uses the
common envelope (event_id, event_type, event_version, occurred_at, producer,
tenant_id, correlation_id, causation_id, trace_id, data).

## 7. Infrastructure & Deployment

- **Server footprint:** Phase 1 = 3 servers ($260-480/mo), Phase 2 = 3
  servers ($410-760/mo), Phase 3 = 9 servers ($1,200-2,200/mo); PCI
  compliance adds $1,142-3,667/mo.
- **PostgreSQL:** PgBouncer transaction mode (pool 30 -> 75), max_connections
  150-300, streaming read replicas, `sslmode=require` (verify-full in prod),
  WAL archiving + PITR, 30-day backup retention, RPO < 1h, RTO < 15 min.
- **Edge:** Caddy TLS/HSTS, HTTP/3, WAF + DDoS protection, security headers.
- **Network:** allow only 80/443 inbound; DB ports private-VPC only; egress
  HTTPS-only; PCI zone isolated.

## 8. Data & Migration

- **OLTP:** payments, sessions, webhooks, inbound messages (90-day
  configurable, with legal holds per C29).
- **Ledger DB:** append-only journal, 7-year minimum (MLPA record keeping),
  reversals via offsetting entries.
- **Token DB:** PCI-scoped token vault, HSM-backed keys, cardholder-data
  retention policy.
- **Data Warehouse:** historical analytics, compliance reporting, 7-year
  retention.
- **Encryption:** at rest via KMS-managed keys (C7); in transit TLS/mTLS (C22).

## 9. Staffing & RACI

| Workstream | Owner | Support |
|---|---|---|
| Payment core | Engineering Lead | Security |
| Ledger / financial plane | Engineering Lead | Finance/Treasury |
| KYC/AML | Compliance | Engineering |
| PCI evidence | CISO | Security / Finance |
| ISMS | CISO | Compliance / Audit |
| Infrastructure | Ops/Infra | Engineering |
| Budget | Finance/Treasury | Board |

## 10. Budget & Cost Model

From the production-requirements workbook: Phase 1 infra $260-480/mo + PCI
$1,142/mo; Phase 2 $410-760/mo + $2,400/mo; Phase 3 $1,200-2,200/mo +
$3,667/mo. PCI year-1 total (QSA audit + compliance + tools): $18,500-73,000.
Ongoing annual: $13,700-44,000.

## 11. Risk Register & Dependencies

| Risk / dependency | Impact | Mitigation |
|---|---|---|
| QSA lead time (3-6 months) | Certification slip | Engage QSA at Phase 2; start CDE evidence early |
| Card-scheme certification (3-6 months) | Card processing delayed | Begin at Phase 1; hosted fields in interim |
| Bank API readiness (GTB, Access, etc.) | Payouts/settlement delayed | Parallel integration; fallback provider |
| Kafka operability on VPS | Event backbone instability | Use managed Kafka option or KRaft single-broker first |
| sslmode change breaks connections | Outage | Stage in test env; verify PgBouncer config |
| 90-day purge vs 7-year ledger conflict | Non-compliance | Legal holds + archive to ledger (C29) |

## 12. Governance & Reporting Cadence

- Monthly program review (roadmap Reporting Matrix).
- Quarterly ASV scans + management review (ISO 9.3).
- Annual ISO surveillance audit + PCI re-certification.
- Regulatory returns: STR/CTR/PEP monthly; risk refresh 12-18 months.

## 13. Exit Criteria & Certification Milestones

| Milestone | Phase | Gate |
|---|---|---|
| Hardened build passes pentest | 1 | Release to production |
| NIN/BVN verification live | 2 | KYC queue operating |
| Test STR filed to NFIU sandbox | 2 | Regulatory signal |
| PCI evidence pack complete | 3 | QSA engagement starts |
| DR drill passed | 3 | BCP declared operational |
| ISO 27001 certificate issued | 4 | Certification |
| PCI Level 1 QSA RoC + AOC | 4 | Compliance |
| Operating rhythm institutionalized | 5 | Ongoing |

## 14. Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
