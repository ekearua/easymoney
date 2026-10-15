---
title: RETENTION & DESTRUCTION SCHEDULE
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# RETENTION & DESTRUCTION SCHEDULE

| | |
|---|---|
| **Document Owner** | Ops / Compliance |
| **Approved By** | CCO / CISO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually |
| **Regulatory Basis** | MLPA 2004 record keeping; ISO/IEC 27001:2022 Annex A 8.10 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AML/CFT | Anti-Money Laundering / Combating the Financing of Terrorism |
| CBN | Central Bank of Nigeria |
| CBN/NFIU | Central Bank of Nigeria / Nigerian Financial Intelligence Unit |
| CCO | Chief Compliance Officer |
| CISO | Chief Information Security Officer |
| CTR | Currency Transaction Report |
| DB | Database |
| DR | Disaster Recovery |
| ID | Identifier |
| ISO | International Organization for Standardization |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KYC | Know Your Customer |
| MLPA | Money Laundering (Prohibition) Act 2004 |
| NDPR | Nigeria Data Protection Regulation 2019 |
| OTP | One-Time Password |
| STR | Suspicious Transaction Report |

## 1. Purpose

Define how long each class of data is retained and how it is securely destroyed, balancing regulatory minimums (e.g., 7-year KYC/transaction records under MLPA) with NDPR storage limitation.

## 2. Retention Table

| Data Class | Retention | Destruction Method | Basis |
|---|---|---|---|
| KYC / identity / verification records | 7 years after account closure | Secure erasure + DB purge, logged | MLPA record keeping |
| Ledger / transaction / payment events | 7 years | Secure erasure, archived copy per policy | MLPA record keeping |
| STR / CTR records | 7 years | Secure erasure | MLPA 2004 |
| Chat / message content | 90 days | Automated batch purge worker | NDPR storage limitation |
| Session / OTP records | 24 hours (or OTP validity) | Automated expiry/purge | Operational |
| Audit logs (privileged actions) | 2 years minimum | Secure erasure | ISO A 8.15 |
| Support / dispute tickets | 3 years | Secure erasure | Operational / contractual |
| Webhook / raw event logs | 90 days | Batch purge / log rotation | Operational |
| Backups | Per Backup & Restore Runbook | Overwritten / secure destruction | DR requirements |
| Marketing / optional data | Until consent withdrawn | Secure erasure | NDPR consent |
| Screening / sanctions match records | 7 years | Secure erasure | CBN AML/CFT |
| | | | |
| | | | |
| | | | |

## 3. Purge Controls

- The 90-day purge runs automatically and in batches to avoid table locks at scale (refactor of PurgeBefore in internal/store/store.go:4158-4200).
- Every purge run is logged: records deleted, duration, errors, and any legal hold exclusions.
- Destruction of physical media/backups follows the DR offboarding procedures.

## 4. Legal / Regulatory Holds

On notice of litigation, investigation or CBN/NFIU examination, relevant records are placed on hold and not destroyed until the hold is lifted.

## 5. Responsibilities

| Role | Responsibility |
|---|---|
| Compliance | Schedule ownership, holds, MLPA minimums |
| Ops / Engineering | Purge automation, logging, backups |
| Security | Audit log retention, secure destruction oversight |
| | |
| | |
| | |

## 6. References

MLPA 2004; NDPR 2019; ISO A 8.10; Data Inventory / Residency Register.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
