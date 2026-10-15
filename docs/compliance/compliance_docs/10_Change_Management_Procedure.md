---
title: CHANGE MANAGEMENT PROCEDURE
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# CHANGE MANAGEMENT PROCEDURE

| | |
|---|---|
| **Document Owner** | Ops / Security |
| **Approved By** | Head of Engineering / CISO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 8.32 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| CAB | Change Advisory Board |
| CI | Continuous Integration |
| CISO | Chief Information Security Officer |
| ID | Identifier |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| OS | Operating System |

## 1. Purpose

Define how changes to the production platform, infrastructure and configuration are requested, assessed, approved, tested, implemented and reviewed, minimising the risk of outages and security regressions.

## 2. Scope

Application releases, database schema changes, infrastructure/network changes, secrets and configuration, dependencies, and third-party integrations.

## 3. Change Types

| Type | Definition | Approval |
|---|---|---|
| Standard | Pre-approved, low-risk, repeatable (routine patch, CI release) | Pre-approved runbook / automated pipeline |
| Minor | Low impact, well-understood | Engineering lead / senior on-call |
| Major | Cross-service or significant user impact | Change Advisory Board (CAB) including Security |
| Emergency | Required to restore service or close an active incident | Incident commander + CISO; post-change review within 5 days |
| | | |
| | | |
| | | |

## 4. Workflow

- 1. Record request: description, scope, owner, backout plan, rollback strategy.
- 2. Assess impact and risk (security, data, availability) using the Risk Management Policy.
- 3. Obtain approval per change type above.
- 4. Test in staging that mirrors production (same OS, versions, configuration).
- 5. Implement with monitoring; abort and roll back if KPIs breach thresholds.
- 6. Post-change review: success criteria, lessons, records retained.

## 5. Records

All changes are recorded in the change log with approver, date, result and backout outcome; privileged change actions appear in the Privileged Access / Audit Log Register.

## 6. References

ISO/IEC 27001:2022 Annex A 8.32; Incident Response Plan.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
