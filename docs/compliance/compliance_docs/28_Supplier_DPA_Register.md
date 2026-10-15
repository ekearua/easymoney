---
title: SUPPLIER / DPA REGISTER
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# SUPPLIER / DPA REGISTER

| | |
|---|---|
| **Document Owner** | Procurement / Compliance |
| **Approved By** | Procurement / DPO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Quarterly |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 5.19; NDPR 2019 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| API | Application Programming Interface |
| DB | Database |
| DPA | Data Processing Agreement |
| DPO | Data Protection Officer |
| ID | Identifier |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KYC | Know Your Customer |
| NDPR | Nigeria Data Protection Regulation 2019 |
| NIN/BVN | National Identification Number / Bank Verification Number |
| SIEM | Security Information and Event Management |

## 1. Purpose

Register of third parties with data access or service criticality, tracking due diligence, DPAs and review status.

## 2. Register

| Supplier | Service | Tier | DPA Signed (Y/N) | DPA Ref / Date | Data Accessed | Risk Class | Review Due | Status |
|---|---|---|---|---|---|---|---|---|
| Identity provider (Smile ID / Youverify) | KYC verification | Critical | | | ID data, selfie, NIN/BVN | High | | To onboard |
| Cloud provider | Hosting, DB, object storage | Critical | | | All production data | High | | To onboard |
| Observability / SIEM | Logs, metrics, alerts | Major | | | Logs, events | Medium | | To onboard |
| Email / support | Customer support | Major | | | Customer queries | Medium | | To onboard |
| Messaging API (WhatsApp Business) | Messaging channel | Critical | | | Chat content | High | | To onboard |
| | | | | | | | | |
| | | | | | | | | |
| | | | | | | | | |

## 3. Maintenance

Critical suppliers reviewed quarterly; major annually. DPA renewals tracked; sub-processors approved per DPA. Offboarding evidence recorded.

## 4. References

Vendor / Third-Party Risk Policy; Data Inventory / Residency Register.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
