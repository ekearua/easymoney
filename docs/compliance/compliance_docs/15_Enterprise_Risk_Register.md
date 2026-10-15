---
title: ENTERPRISE RISK REGISTER
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# ENTERPRISE RISK REGISTER

| | |
|---|---|
| **Document Owner** | Risk |
| **Approved By** | Board of Directors |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Quarterly review |
| **Regulatory Basis** | ISO/IEC 27001:2022 Clause 6.1 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AML/CFT | Anti-Money Laundering / Combating the Financing of Terrorism |
| CCO | Chief Compliance Officer |
| CISO | Chief Information Security Officer |
| ID | Identifier |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KYC | Know Your Customer |
| ML/FT | Money Laundering / Financing of Terrorism |
| P0 | Risk priority P0 - extreme (immediate action) |
| P1 | Risk priority P1 - high |
| P2 | Risk priority P2 - medium |
| P3 | Risk priority P3 - low |
| PII | Personally Identifiable Information |
| PITR/WAL | Point-in-Time Recovery using Write-Ahead Logs |

## 1. Purpose

Register of material risks to Xego with owners, treatment and residual risk, maintained in line with the Risk Management Policy.

## 2. Scoring

Likelihood and impact are scored 1-5 per the Risk Management Policy; rating = likelihood x impact (P0 >= 15, P1 8-14, P2 3-7, P3 1-2).

## 3. Register

| Risk ID | Category | Risk Description | L | I | Score | Owner | Treatment | Residual Risk | Review Date | Status |
|---|---|---|---|---|---|---|---|---|---|---|
| R-001 | AML/CFT | Simulated KYC approvals allow unverified onboarding | 4 | 5 | 20 | CCO | Real verification workflow + provider integration | Low | | Open |
| R-002 | Data Protection | Customer PII stored in plaintext across 8+ tables | 3 | 5 | 15 | CISO | pgcrypto encryption at rest + data minimisation | Medium | | Open |
| R-003 | Governance | No audit trail for privileged actions / KYC decisions | 3 | 4 | 12 | CISO | Audit logging for admin actions | Medium | | Open |
| R-004 | Operational | Unbounded purge deletes could lock tables at scale | 3 | 3 | 9 | Ops | Batch purge worker | Low | | Open |
| R-005 | Resilience | Single database is a single point of failure | 3 | 4 | 12 | Ops | PITR/WAL, multi-AZ, restore drills | Medium | | Open |
| | | | | | | | | | | |
| | | | | | | | | | | |
| | | | | | | | | | | |

## 4. Use

Extreme (P0) risks are escalated to the Board within 5 business days; the register is consolidated with the InfoSec Risk Assessment and the ML/FT risk assessment at least quarterly.

## 5. References

Risk Management Policy; InfoSec Risk Assessment & Treatment Plan; Institutional ML/FT Risk Assessment.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
