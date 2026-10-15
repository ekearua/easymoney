---
title: PRIVACY & DATA PROTECTION POLICY
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# PRIVACY & DATA PROTECTION POLICY

| | |
|---|---|
| **Document Owner** | Compliance / Data Protection Officer (DPO) / [Name] |
| **Approved By** | Board of Directors |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually or on material change |
| **Regulatory Basis** | NDPR 2019; NITDA Guidance; ISO/IEC 27001:2022 Annex A 5.34; MLPA 2004 |

---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AML/CFT | Anti-Money Laundering / Combating the Financing of Terrorism |
| CBN | Central Bank of Nigeria |
| DPA | Data Processing Agreement |
| DPO | Data Protection Officer |
| ID | Identifier |
| IP | Internet Protocol |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KYC/AML | Know Your Customer / Anti-Money Laundering |
| MLPA | Money Laundering (Prohibition) Act 2004 |
| NDPR | Nigeria Data Protection Regulation 2019 |
| NIN/BVN | National Identification Number / Bank Verification Number |
| NITDA | National Information Technology Development Agency (Nigeria's data-protection regulator) |

## 1. Purpose

This policy sets out how **Xego** collects, uses, stores, shares and destroys personal data of customers, merchants and other individuals using the WhatsApp/Telme payment platform, in line with the Nigeria Data Protection Regulation (NDPR) 2019.

## 2. Scope

Applies to all personal data processed by Xego or on its behalf by processors (identity verification providers, cloud infrastructure, analytics, support tooling), regardless of medium.

## 3. Data Protection Principles

Xego processes personal data in accordance with the NDPR principles:

1. **Lawfulness and fairness.** Personal data is collected and processed only on a lawful basis (consent, contract, legal obligation, legitimate interest) and notified to the data subject.
2. **Purpose limitation.** Data is collected only for specified, explicit and legitimate purposes and not further processed in a manner incompatible with those purposes.
3. **Data minimisation.** Only the data necessary for the stated purposes is collected.
4. **Accuracy.** Reasonable steps keep personal data accurate and up to date.
5. **Storage limitation.** Data is kept no longer than necessary and is subject to documented retention/destruction schedules (see 90-day purge controls).
6. **Security, integrity and confidentiality.** Personal data is protected by appropriate technical and organisational measures, including encryption at rest and in transit.
7. **Accountability.** Xego demonstrates compliance through records, privacy impact assessments and DPA-registered processing.

## 4. Data We Process

- **Identity data**: name, phone number, date of birth, address, NIN/BVN references, government ID verification results.
- **KYC/AML data**: verification outcomes, risk scores, screening results, sanctions matches.
- **Financial data**: transaction, payment, invoice, data-order and thrift records.
- **Usage data**: device, IP address, session and service usage logs.

## 5. Lawful Basis and Consent

- Consent is obtained before processing where required (e.g., optional marketing), recorded with evidence, and withdrawable at any time.
- Contractual processing is limited to what is necessary to provide payment services.
- Statutory processing supports KYC/AML obligations under MLPA 2004 and CBN regulation.

## 6. Data Subject Rights

- **Access**: confirmation of processing and copies of personal data.
- **Correction**: rectification of inaccurate data.
- **Erasure ("right to be forgotten")**: deletion where lawful, subject to statutory retention.
- **Restriction / objection**: limited to applicable lawful grounds.
- **Portability**: machine-readable copies where technically feasible.
- Requests are handled within the statutory timeframe via a documented process.

## 7. Sharing and Processors

- Personal data is shared only with processors under binding Data Processing Agreements (DPAs) and only as necessary to provide services (e.g., identity verification, cloud hosting).
- Processors are assessed for security and compliance before engagement and monitored thereafter (see Vendor/Third-Party Risk Policy).
- Cross-border transfers occur only to jurisdictions/mechanisms acceptable under NDPR and with contractual safeguards.

## 8. Retention and Destruction

- Retention periods are defined per data class in the Retention & Destruction Schedule.
- Personal data no longer required is securely deleted or anonymised.
- Purging is automated and logged; see the 90-day purge worker in `internal/store/store.go`.

## 9. Data Breach Response

- Personal data breaches are detected, contained, investigated and documented under the Incident Response Plan.
- Notifications are made to NITDA and affected data subjects as required by NDPR.

## 10. Roles and Responsibilities

| Role | Responsibility |
|---|---|
| **DPO / Compliance** | Policy ownership, subject rights, NITDA liaison, breach notification. |
| **Security** | Technical controls, encryption, incident response. |
| **Engineering** | Data minimisation in design, secure data flows, purge automation. |
| **All staff** | Handle personal data in line with this policy. |

## 11. Review

Reviewed annually or upon material change to processing activities or regulation.

## 12. References

- NDPR 2019 and NITDA guidance
- ISO/IEC 27001:2022 Annex A 5.34
- MLPA 2004; CBN AML/CFT Regulation 2009

## 13. Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for Board approval |
