---
title: VENDOR / THIRD-PARTY RISK MANAGEMENT POLICY
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# VENDOR / THIRD-PARTY RISK MANAGEMENT POLICY

| | |
|---|---|
| **Document Owner** | Procurement / Compliance |
| **Approved By** | Board of Directors |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually or on material change |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 5.19-5.23; NDPR 2019 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| DB | Database |
| DPA | Data Processing Agreement |
| ID | Identifier |
| ISO | International Organization for Standardization |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| NDPR | Nigeria Data Protection Regulation 2019 |
| SIEM | Security Information and Event Management |
| SOC | System and Organization Controls (audit report, e.g., SOC 2) |

## 1. Purpose

Define how Xego selects, contracts, monitors and terminates third parties that process data or support the payment platform, protecting customer data and regulatory obligations.

## 2. Scope

Applies to all suppliers, vendors, sub-processors and partners, including identity verification providers, cloud infrastructure, observability, email/support and any contractor with system access.

## 3. Classification

Suppliers are classified by criticality and data exposure; the tier determines the level of due diligence, contractual protection and monitoring:

| Tier | Examples | Control Intensity |
|---|---|---|
| Critical | Identity verification (Smile ID, Youverify); cloud hosting / DB / backup and DR infrastructure | Full due diligence and security assessment; binding DPA; SOC 2 / ISO 27001 evidence; quarterly review; breach-notification clause |
| High | Observability / SIEM, payment rails and processors, email / support tooling | Due diligence and DPA; annual review; sub-processor change notification |
| Standard | Office productivity and non-sensitive SaaS | Standard commercial terms; register entry; annual confirmation |
| Low | One-off consultants or tools with no system or data access | Standard terms only |

Reclassify when supplier scope, data flows or risk profile change; the current assignment is recorded in the Supplier / DPA Register.

## 4. Due Diligence

- Critical/major suppliers undergo pre-contract due diligence: ownership, security practices, certifications (ISO 27001/SOC 2), breach history, sub-processor list.
- Verification providers must hold relevant licences/regulatory standing for identity services in Nigeria.
- Cloud hosting must satisfy data residency and encryption requirements.

## 5. Data Protection and DPAs

- A binding Data Processing Agreement (DPA) is signed before any personal data is shared (NDPR-compliant).
- Cross-border transfer safeguards are recorded (standard contractual clauses or adequacy mechanisms).
- Sub-processor changes require notice/consent per the DPA.

## 6. Monitoring and Offboarding

- Critical suppliers are reviewed quarterly (incidents, SLAs, control changes); major annually.
- On termination: credentials revoked, data returned/destroyed per DPA, residual access removed and logged.

## 7. References

ISO/IEC 27001:2022 Annex A 5.19-5.23; NDPR 2019; Supplier / DPA Register; Compliance Documents Register.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
