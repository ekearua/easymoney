---
title: ASSET INVENTORY
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# ASSET INVENTORY

| | |
|---|---|
| **Document Owner** | Security |
| **Approved By** | CISO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Quarterly |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 5.9 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| API | Application Programming Interface |
| CI/CD | Continuous Integration / Continuous Delivery |
| CISO | Chief Information Security Officer |
| ID | Identifier |
| ISO | International Organization for Standardization |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KMS | Key Management Service |
| PII | Personally Identifiable Information |
| PITR | Point-in-Time Recovery |
| VPC | Virtual Private Cloud |
| WAL | Write-Ahead Log |

## 1. Purpose

Inventory of information assets owned or operated by Xego, with ownership and criticality, satisfying Annex A 5.9.

## 2. Inventory

| Asset ID | Type | Name | Owner | Criticality | Location | Backed Up? | Status |
|---|---|---|---|---|---|---|---|
| A-001 | Service | Core API (internal/app) | Engineering | Critical | Cloud VPC (Phase 1) | N/A (stateless) | In production |
| A-002 | Service | Worker (purge, reconciliation) | Engineering | High | Cloud VPC | N/A | In production |
| A-003 | Database | PostgreSQL 17 (primary + PITR) | Ops | Critical | Cloud VPC / object store | Yes (WAL) | In production |
| A-004 | Secrets | Vault / cloud KMS | Security | Critical | Vault / KMS | Yes | In production |
| A-005 | Repository | whatsapp-payment-demo | Engineering | High | Git host | Yes (git) | In production |
| A-006 | Data | Customer PII tables | Compliance | Critical | PostgreSQL | Yes | In production |
| A-007 | Pipeline | CI/CD (GitHub Actions) | Engineering | High | SaaS | N/A | In production |
| | | | | | | | |
| | | | | | | | |
| | | | | | | | |

## 3. Maintenance

Updated on commissioning/decommissioning; reconciled with cloud account resources quarterly; each asset has an accountable owner.

## 4. References

ISO A 5.9; Data Inventory / Residency Register.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
