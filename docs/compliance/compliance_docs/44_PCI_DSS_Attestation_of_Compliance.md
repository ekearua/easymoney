---
title: PCI DSS ATTESTATION OF COMPLIANCE (AOC)
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# PCI DSS ATTESTATION OF COMPLIANCE (AOC)

| | |
|---|---|
| **Document Owner** | Security / Finance |
| **Approved By** | CISO / CFO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annual |
| **Regulatory Basis** | PCI DSS Level 1; PCI DSS Req 12 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AOC | Attestation of Compliance |
| ASV | Approved Scanning Vendor |
| CFO | Chief Financial Officer |
| CISO | Chief Information Security Officer |
| DBA | Doing Business As (legal / trading name) |
| DSS | Data Security Standard |
| ID | Identifier |
| PCI | Payment Card Industry |
| QSA | Qualified Security Assessor |
| RoC | Report on Compliance |
| SAQ-A | Self-Assessment Questionnaire A (PCI DSS) |

## 1. Purpose

Template for the annual PCI DSS Level 1 Attestation of Compliance and the
scope decision behind it.

## 2. Scope Decision

- Xego is a Level 1 merchant (more than 6 million card transactions per year),
  which requires a Report on Compliance (RoC) conducted by a Qualified
  Security Assessor (QSA) and this signed Attestation of Compliance (AOC).
- The SAQ-A route is used only if the platform is fully outsourced and never
  stores, processes or transmits cardholder data on its systems (cards not
  solicited in chat) and relies on a validated third party; at Level 1
  transaction volumes this route does not apply.
- The assessment scope is defined in the PCI DSS Scope & CDE Inventory and the
  requirement-by-requirement status is tracked in the PCI DSS Requirements
  1-12 Compliance Matrix.

## 3. AOC Contents

| Section | Content |
|---|---|
| Merchant / entity details | Legal name, DBA, contact |
| Validation type | Level 1 - RoC by QSA |
| Scope | Cardholder data environment, segmentation |
| Requirement status 1-12 | Each requirement: compliant / not applicable |
| Quarterly ASV scans | Passing scan evidence (Req 11) |
| QSA RoC evidence | RoC conclusion, compensating controls, QSA signature |
| Attestation | Signatures, date, evidence of compensating controls |
| | |
| | |
| | |

## 4. Record Keeping

AOC, RoC, ASV scan reports and evidence retained per PCI requirements;
findings tracked in the Vulnerability Register.

## 5. References

PCI DSS Requirements 1-12; PCI DSS Scope & CDE Inventory; PCI DSS Requirements
1-12 Compliance Matrix; Vulnerability / Pen-Test Findings Register.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
