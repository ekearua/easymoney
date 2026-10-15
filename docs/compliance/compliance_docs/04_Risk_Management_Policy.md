---
title: RISK MANAGEMENT POLICY
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# RISK MANAGEMENT POLICY

| | |
|---|---|
| **Document Owner** | Chief Risk Officer / CISO / [Name] |
| **Approved By** | Board of Directors |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually or on material change |
| **Regulatory Basis** | ISO/IEC 27001:2022 Clauses 6.1–6.3; CBN AML/CFT RBS Regulation 2009 |

---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AML/CFT | Anti-Money Laundering / Combating the Financing of Terrorism |
| CBN | Central Bank of Nigeria |
| CCO | Chief Compliance Officer |
| CISO | Chief Information Security Officer |
| ISO | International Organization for Standardization |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KYC | Know Your Customer |
| ML/FT | Money Laundering / Financing of Terrorism |
| NDPR | Nigeria Data Protection Regulation 2019 |
| P0 | Risk priority P0 - extreme (immediate action) |
| P0/P1 | Risk priorities P0-P1 (extreme / high) |
| P1 | Risk priority P1 - high |
| P2 | Risk priority P2 - medium |
| P3 | Risk priority P3 - low |
| PCI | Payment Card Industry |
| PII | Personally Identifiable Information |
| RBS | Risk-Based Supervision (CBN regulatory framework) |
| STR | Suspicious Transaction Report |
| SoA | Statement of Applicability (ISO/IEC 27001:2022 control-applicability statement) |

## 1. Purpose

This policy defines how **Xego** identifies, assesses, treats, monitors and reports risks to its business, information assets, and regulatory obligations. It provides the methodology behind the Enterprise Risk Register, the Information Security Risk Assessment & Treatment Plan, and the institutional ML/FT risk assessment.

## 2. Scope

Applies to all business units and processes, with emphasis on:

- Information security and data protection risk (ISO 27001:2022 Cl. 6.1).
- Money laundering / terrorism financing risk (CBN AML/CFT Regulation 2009).
- Operational, technology, third-party and regulatory risk.

## 3. Risk Appetite

- **Xego has low tolerance** for risks that could: expose customer PII; enable money laundering or sanctions violations; compromise the integrity of the ledger; or cause material regulatory or reputational damage.
- Higher tolerance is limited to controlled, well-understood technology/operational risks that are continuously monitored with clear mitigation.

## 4. Risk Assessment Methodology

### 4.1 Identification
Risks are identified from business processes, threat sources, vulnerabilities, regulatory changes, incidents and third parties.

### 4.2 Analysis and Evaluation
Each risk is scored on a defined scale (e.g., 1–5) for likelihood and impact:

| Score | Likelihood | Impact |
|---|---|---|
| 1 | Rare | Negligible |
| 2 | Unlikely | Minor |
| 3 | Possible | Moderate |
| 4 | Likely | Major |
| 5 | Almost certain | Severe |

Risk rating = Likelihood × Impact. Ratings define priority:

| Rating | Priority | Action |
|---|---|---|
| 15–25 | **P0 / Extreme** | Immediate treatment and Board notification |
| 8–14 | **P1 / High** | Treatment within an agreed timescale |
| 3–7 | **P2 / Medium** | Managed through planned controls |
| 1–2 | **P3 / Low** | Accept and monitor |

### 4.3 Risk Sources (examples)
- **Technology**: unencrypted PII at rest, weak admin authentication, missing audit logs, rate-limit gaps, retention purge failures.
- **AML/CFT**: simulated KYC approvals, missing sanctions screening, no STR workflow.
- **Third party**: identity provider outage, cloud misconfiguration, data processor breaches.
- **Regulatory**: CBN examination findings, NDPR non-compliance, PCI scope breach.

## 5. Risk Treatment

Treatment options, in priority order:

1. **Avoid** — cease the activity or design it out.
2. **Reduce** — implement controls (technical, organisational, physical).
3. **Transfer / share** — insurance, contractual allocation to vendors.
4. **Accept** — only where within appetite and explicitly approved by the accountable owner.

Residual risk after treatment is re-assessed and recorded. Controls are documented in the Risk Treatment Plan and referenced to ISO 27001 Annex A where applicable.

## 6. Registers and Review

- **Enterprise Risk Register**: all material risks with owners, treatment and residual risk; reviewed by the Risk function at least quarterly.
- **InfoSec Risk Assessment & Treatment Plan**: basis for the Statement of Applicability (SoA); updated annually and on material change.
- **Institutional ML/FT Risk Assessment**: refreshed every 12–18 months or on material change; approved by the Board.
- Escalation of extreme (P0) risks to the Board within 5 business days.

## 7. Roles and Responsibilities

| Role | Responsibility |
|---|---|
| **Board** | Risk appetite, approval of assessments, review of P0/P1 risks. |
| **Risk / CISO / CCO** | Methodology, facilitation, aggregation and reporting. |
| **Business owners** | Owning and treating risks in their area. |
| **Audit** | Independent evaluation of the risk process (ISO 9.2). |

## 8. References

- ISO/IEC 27001:2022 Clauses 6.1–6.3, 9.3, Annex A 5.1–5.8
- CBN AML/CFT RBS Regulation 2009 (risk assessment)

## 9. Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for Board approval |
