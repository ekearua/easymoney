---
title: ISMS SCOPE DOCUMENT
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# ISMS SCOPE DOCUMENT

| | |
|---|---|
| **Document Owner** | CISO |
| **Approved By** | Board of Directors |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually or on material change |
| **Regulatory Basis** | ISO/IEC 27001:2022 Clause 4.3 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AML/CFT | Anti-Money Laundering / Combating the Financing of Terrorism |
| API | Application Programming Interface |
| CCO | Chief Compliance Officer |
| CI/CD | Continuous Integration / Continuous Delivery |
| CISO | Chief Information Security Officer |
| DPA | Data Processing Agreement |
| ID | Identifier |
| ISMS | Information Security Management System |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KYC | Know Your Customer |
| KYC/AML | Know Your Customer / Anti-Money Laundering |
| PII | Personally Identifiable Information |
| STR/CTR | Suspicious Transaction Report / Currency Transaction Report |
| VPC | Virtual Private Cloud |

## 1. Purpose

Define the boundaries of the Xego Information Security Management System (ISMS) in accordance with ISO/IEC 27001:2022 Clause 4.3, so that certification, audits and management review cover a clearly stated set of assets, locations, services and processes.

## 2. Scope of the ISMS

The ISMS covers the Xego WhatsApp/Telme payment platform and its supporting organisation, specifically:
- Services: account/onboarding, KYC/AML verification, wallet and payment transfers, invoice, data-order and thrift products, settlement and reconciliation.
- Systems: application services (internal/app, internal/service, internal/store), PostgreSQL 17 database(s), webhook receiver, scheduled workers (retention purge, reconciliation), CI/CD pipeline.
- Data assets: customer PII, KYC/AML records, ledger and payment events, chat/message history, audit logs, credentials and secrets.
- Locations: cloud hosting region(s) and data residency boundary (Phase 1 VPC); developer workstations in scope for handling of production data only via approved tooling.
- People: all employees, contractors and third parties with access to the above.
- Third parties: identity verification providers, cloud infrastructure, observability and email/support providers (see Supplier / DPA Register).

## 3. Exclusions

Explicitly out of scope with justification:
- Physical sites of third-party cloud/data centres (shared responsibility - inherited controls).
- Customers' own devices and messaging infrastructure (WhatsApp), except for the APIs/connectors Xego controls.

## 4. Interfaces and Dependencies

- Dependencies on external services (WhatsApp Business API, identity providers, banks/payment rails) are documented and risk-assessed in the Supplier / DPA Register.
- Interfaces between ISMS and AML/CFT program (KYC, STR/CTR) are coordinated by the CCO and CISO.

## 5. Review

This scope statement is reviewed annually or when the business, services or hosting change materially.

## 6. References

ISO/IEC 27001:2022 Clause 4; Compliance Documents Register.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
