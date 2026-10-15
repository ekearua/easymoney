---
title: ACCESS CONTROL POLICY
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# ACCESS CONTROL POLICY

| | |
|---|---|
| **Document Owner** | Security / CISO |
| **Approved By** | Board of Directors |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 5.15-5.18, 8.2 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| CI/CD | Continuous Integration / Continuous Delivery |
| CISO | Chief Information Security Officer |
| HR | Human Resources |
| ID | Identifier |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| JML | Joiner / Mover / Leaver |
| KYC | Know Your Customer |
| MFA | Multi-Factor Authentication |
| MFA/TOTP | Multi-Factor Authentication using TOTP (see individual entries) |
| PII | Personally Identifiable Information |
| RBAC | Role-Based Access Control |

## 1. Purpose

Define how access to Xego systems and data is granted, managed, reviewed and revoked to enforce least privilege and protect customer PII, KYC records and the ledger.

## 2. Scope

All users (staff, contractors, administrators, merchants, service accounts) and all systems (admin console, merchant portal, databases, CI/CD, observability, cloud accounts).

## 3. Principles

- Least privilege and need-to-know: access only to the minimum required to perform the role.
- Role-based access control (RBAC): named roles with predefined entitlements; no shared personal accounts.
- Segregation of duties: no single user can both approve a payment/KYC decision and execute it without a second control.
- Multi-factor authentication (MFA/TOTP or passkeys) for all admin and merchant logins and all privileged actions.

## 4. Joiner / Mover / Leaver

- Joiner: request via Joiner-Mover-Leaver Register, HR verification, role-based provisioning, mandatory confidentiality agreement and training before access.
- Mover: entitlements re-aligned to the new role; obsolete access removed.
- Leaver: accounts disabled same-day; assets returned; access removed and logged in the JML Register.

## 5. Entitlement Reviews

Security reviews all privileged accounts and role memberships quarterly; any orphaned or excessive access is removed. Results recorded in the Privileged Access / Audit Log Register.

## 6. Roles and Responsibilities

| Role | Responsibility |
|---|---|
| HR | Verification, JML process, offboarding triggers |
| Security / CISO | Policy, entitlement reviews, privileged account oversight |
| Engineering | Technical enforcement, least-privilege defaults, approval flows |
| Line managers | Access justification and sign-off |
| All users | Use of MFA, reporting lost credentials, no password sharing |
| | |
| | |
| | |

## 7. References

ISO/IEC 27001:2022 Annex A 5.15-5.18, 8.2-8.5; JML Register; Privileged Access / Audit Log Register.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
