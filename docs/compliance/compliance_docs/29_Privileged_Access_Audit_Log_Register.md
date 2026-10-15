---
title: PRIVILEGED ACCESS / AUDIT LOG REGISTER
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# PRIVILEGED ACCESS / AUDIT LOG REGISTER

| | |
|---|---|
| **Document Owner** | Security |
| **Approved By** | CISO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Monthly review |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 8.15 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| CISO | Chief Information Security Officer |
| ID | Identifier |
| IP | Internet Protocol |
| ISO | International Organization for Standardization |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| JSON | JavaScript Object Notation |
| KYC | Know Your Customer |
| SIEM | Security Information and Event Management |

## 1. Purpose

Defines which privileged actions are logged and provides the review register proving accountability.

## 2. Events Logged

- Admin and merchant login/logout (success and failure).
- KYC verification decisions (approve/reject, reviewer).
- Permission/role changes (grant/revoke).
- Payment and ledger events (create, cancel, reverse).
- Sensitive data access and export.
- Configuration, secrets and infra changes.

## 3. Logging Requirements

Structured JSON logs with actor, action, object, timestamp, outcome and IP; correlation IDs across services; retained per Retention Schedule; centralised in SIEM.

## 4. Review Register

| Event ID | Timestamp | Actor | Action | Object | IP | Outcome | Source Table | Reviewed |
|---|---|---|---|---|---|---|---|---|
| EV-0001 | | admin@xego | login | admin console | | success | admin sessions | Y |
| EV-0002 | | compliance@xego | kyc_decision | customer_verifications | | approved | kyc events | Y |
| EV-0003 | | svc-payments | payment_created | ledger | | success | payment_events | N |
| | | | | | | | | |
| | | | | | | | | |
| | | | | | | | | |

## 5. References

ISO A 8.15; Access Control Policy.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
