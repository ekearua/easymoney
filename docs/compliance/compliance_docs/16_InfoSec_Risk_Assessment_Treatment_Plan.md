---
title: INFORMATION SECURITY RISK ASSESSMENT & TREATMENT PLAN
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# INFORMATION SECURITY RISK ASSESSMENT & TREATMENT PLAN

| | |
|---|---|
| **Document Owner** | CISO |
| **Approved By** | Board / Top Management |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually or on material change |
| **Regulatory Basis** | ISO/IEC 27001:2022 Clauses 6.1-6.3 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| CI/CD | Continuous Integration / Continuous Delivery |
| CISO | Chief Information Security Officer |
| ID | Identifier |
| ISMS | Information Security Management System |
| ISO | International Organization for Standardization |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| MFA | Multi-Factor Authentication |
| PII | Personally Identifiable Information |
| PITR | Point-in-Time Recovery |
| SDLC | Software Development Life Cycle |

## 1. Purpose

Document the ISO 27001:2022 Clause 6.1 risk assessment and treatment decisions that drive the Statement of Applicability and the ISMS control set.

## 2. Method

Uses the 5x5 likelihood x impact matrix in the Risk Management Policy. Steps: identify assets/processes -> identify threats and vulnerabilities -> analyse (score) -> evaluate (accept/treat per appetite) -> select controls (Annex A) -> record residual risk -> monitor.

## 3. Assessment

| Asset / Process | Threat / Vulnerability | L | I | Score | Selected Control (Annex A) | Owner | Target | Status |
|---|---|---|---|---|---|---|---|---|
| Customer PII database | Unauthorised access; plaintext storage | 3 | 5 | 15 | A 8.2 Access, A 8.24 Crypto, A 8.11/8.12 logging | CISO | Phase 1 | Open |
| Admin / merchant auth | Credential stuffing, weak login | 3 | 4 | 12 | A 8.5 MFA, A 8.6 capacity/rate limiting | Engineering | Phase 1 | Open |
| Webhook receiver | Forged webhook events | 3 | 4 | 12 | A 8.25/8.26 secure APIs, signature verification | Engineering | Phase 1 | Open |
| CI/CD pipeline | Malicious or misconfigured builds | 2 | 4 | 8 | A 8.28/8.29 secure SDLC, secrets vaulting | Engineering | Phase 1 | Open |
| Backups | Loss of PITR capability | 2 | 5 | 10 | A 8.13 backups, A 8.14 redundancy, drills | Ops | Phase 1 | Open |
| | | | | | | | | |
| | | | | | | | | |
| | | | | | | | | |

## 4. Treatment

Controls are implemented, transferred (e.g., inherited cloud controls) or accepted with recorded justification; residual risk is tracked in the Enterprise Risk Register.

## 5. References

ISO 27001:2022 Clauses 6.1-6.3; Risk Management Policy; Statement of Applicability.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
