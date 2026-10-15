---
title: PCI DSS SCOPE & CARDSHOLDER DATA ENVIRONMENT (CDE) INVENTORY
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# PCI DSS SCOPE & CARDSHOLDER DATA ENVIRONMENT (CDE) INVENTORY

| | |
|---|---|
| **Document Owner** | Security / CISO |
| **Approved By** | CISO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annual (or on material change) |
| **Regulatory Basis** | PCI DSS Requirements 1, 2, 3, 11 |

---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AOC | Attestation of Compliance |
| API | Application Programming Interface |
| CDE | Cardholder Data Environment (systems and network segments that store, process or transmit cardholder data) |
| CISO | Chief Information Security Officer |
| DB | Database |
| DDoS | Distributed Denial of Service |
| DSS | Data Security Standard |
| HSM | Hardware Security Module |
| KYC | Know Your Customer |
| PAN | Primary Account Number (cardholder account number) |
| PCI | Payment Card Industry |
| QSA | Qualified Security Assessor |
| RoC | Report on Compliance |
| SIEM | Security Information and Event Management |
| TLS | Transport Layer Security |
| VLAN | Virtual Local Area Network |
| WAF | Web Application Firewall |

## 1. Purpose

Defines the boundary of the Cardholder Data Environment (CDE), the systems and
data flows in scope for the PCI DSS Level 1 assessment, and the network
segmentation that keeps cardholder data isolated from the rest of the Xego
platform.

## 2. PCI DSS Level 1 Context

- Xego is a Level 1 merchant because its target transaction volumes exceed 6
  million card transactions per year (phase-3 capacity is materially above this
  threshold).
- Level 1 requires an assessment by a Qualified Security Assessor (QSA)
  producing a Report on Compliance (RoC) and a signed Attestation of Compliance
  (AOC). This document is the starting point for that engagement.
- The scope decision and validation type are recorded in the PCI DSS
  Attestation of Compliance (AOC).

## 3. Cardholder Data Environment (CDE) Boundary

| In scope | Out of scope |
|---|---|
| Hosted card-checkout iframe (payment provider hosted fields) | OLTP PostgreSQL database (never stores card data) |
| Tokenization service and token vault (Token DB) | Chat / conversation services (card data never solicited in chat) |
| API gateway paths that terminate CDE traffic | KYC / customer data stores |
| Admin console and vault-management interfaces | Analytics and reporting stores |
| Monitoring / SIEM that receives CDE logs | Infrastructure shared solely with out-of-scope services |
| Load balancer / edge paths into the PCI zone | |

- Any system that stores, processes or transmits cardholder data or security
  parameters is in scope; everything else is out of scope only where
  segmentation is proven.
- Scope is re-confirmed at least annually and on any material change (new
  service, new connection, cloud re-platforming).

## 4. Network Segmentation

- The tokenization service, token vault and hosted-fields termination run in a
  dedicated, isolated subnet / VLAN ("PCI zone") with no route to out-of-scope
  services.
- Segmentation is enforced with network access control lists and firewall
  rules: only required ports are allowed, everything else is denied by default.
- Egress from the PCI zone is limited to the card network endpoint (TLS) and
  the token substitution path back to the API gateway.
- All CDE traffic uses TLS; inter-service calls inside the PCI zone use
  mutual TLS (mTLS).
- A Web Application Firewall (WAF) and DDoS protection sit in front of the
  public-facing entry points (PCI DSS Requirement 1 and 6.6).
- Segmentation is validated before the QSA RoC, including evidence of
  blocking (no unintended traffic crosses the boundary).

## 5. Card Data Flows

1. The customer enters card details in the hosted card-checkout iframe, which
   posts directly to the tokenization service.
2. The tokenization service replaces the Primary Account Number (PAN) with a
   token; the PAN is never stored in the OLTP database or any chat message.
3. The PAN is discarded immediately after tokenization and is never logged,
   cached or rendered back to the merchant dashboard.
4. All other services consume the token only; cardholder data does not enter
   the Xego business logic or the KYC / conversation flows.
5. Reconciliation is token-based, with the three-way match performed on
   reference identifiers rather than PAN.

## 6. Systems & Components Inventory

| Component | Role | In scope | Owner | Zone |
|---|---|---|---|---|
| Hosted card-checkout iframe | Capture card details | Yes | Payment provider / Security | PCI zone termination |
| Tokenization service | PAN to token substitution, token vault access | Yes | Security | PCI zone |
| Token vault (Token DB) | Encrypted token store, HSM-backed keys | Yes | Security | PCI zone |
| API gateway (CDE paths) | Routes checkout and tokenization traffic | Yes | Security | PCI zone |
| Admin console | Vault / key management access | Yes | Security / Ops | PCI zone (restricted) |
| SIEM | Receives CDE audit logs | Yes | Security | Central (segment rules) |
| OLTP PostgreSQL | Business records, no card data | No | Ops | Out of scope |
| Chat services | Conversations, no card data | No | Engineering | Out of scope |

- The inventory aligns with the Asset Inventory; every component has an
  identified owner and an access review at least quarterly.

## 7. Change & Review Cadence

- The CDE boundary is reviewed annually and within 30 days of any material
  change (new integration, provider change, re-platforming, network change).
- Changes that affect scope are logged in the Change Management Procedure and
  reflected in this inventory before the next assessment.
- Segmentation rules are re-tested on change and at least quarterly.

## 8. References

PCI DSS Requirements 1-3, 6.6, 11; PCI DSS Attestation of Compliance (AOC);
PCI DSS Requirements 1-12 Compliance Matrix; Cryptography / Key Management
Policy; Asset Inventory; Change Management Procedure.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
