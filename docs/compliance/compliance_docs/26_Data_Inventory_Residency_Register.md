---
title: DATA INVENTORY / RESIDENCY REGISTER
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# DATA INVENTORY / RESIDENCY REGISTER

| | |
|---|---|
| **Document Owner** | Compliance / Security |
| **Approved By** | DPO / CISO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Quarterly |
| **Regulatory Basis** | NDPR 2019; ISO/IEC 27001:2022 Annex A 5.34 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| CBN | Central Bank of Nigeria |
| CISO | Chief Information Security Officer |
| DPA | Data Processing Agreement |
| DPO | Data Protection Officer |
| ID | Identifier |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KYC | Know Your Customer |
| NDPR | Nigeria Data Protection Regulation 2019 |
| OTP | One-Time Password |
| PII | Personally Identifiable Information |
| VPC | Virtual Private Cloud |

## 1. Purpose

Inventory of the personal and financial data Xego processes, where it resides, its legal basis, retention, processors and owners - the basis for NDPR compliance and data minimisation.

## 2. Register

| Data Asset | Description | PII? | Storage Location | Legal Basis | Retention | Processors | DPA Ref | Owner |
|---|---|---|---|---|---|---|---|---|
| Customers | Profile, contact, tier, risk score | Yes | PostgreSQL (Phase 1 VPC) | Contract / legal obligation (KYC) | Per Retention Schedule | - | - | Compliance |
| KYC / verifications | Provider results, ID data, selfie refs | Yes | PostgreSQL + provider stores | Legal obligation (CBN) | 7 years | Smile ID / Youverify | | Compliance |
| Chat / messages | Conversation content | Yes | PostgreSQL | Contract (service provision) | 90 days | - | - | Ops |
| Ledger / payment events | Transactions, invoices, thrift | Yes | PostgreSQL | Contract / legal obligation | 7 years | - | - | Ops |
| Sessions / OTP | Session tokens, OTP hashes | Yes | PostgreSQL | Contract / security | 24 hours | - | - | Engineering |
| Audit logs | Privileged actions, KYC decisions | Yes | PostgreSQL / log store | Legal obligation / legitimate interest | 2 years+ | Observability provider | | Security |
| Webhook events | Inbound/outbound event payloads | Yes | PostgreSQL / log store | Contract | 90 days | Observability provider | | Engineering |
| | | | | | | | | |
| | | | | | | | | |
| | | | | | | | | |

## 3. Use

Drives the Retention Schedule, encryption scope (PII fields), DPA coverage and NDPR breach assessment. Reviewed quarterly and on any new processing.

## 4. References

NDPR 2019; Retention & Destruction Schedule; Privacy & Data Protection Policy.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
