---
title: PCI DSS REQUIREMENTS 1-12 COMPLIANCE MATRIX
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# PCI DSS REQUIREMENTS 1-12 COMPLIANCE MATRIX

| | |
|---|---|
| **Document Owner** | Security / CISO |
| **Approved By** | CISO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annual (or on material change) |
| **Regulatory Basis** | PCI DSS Requirement 12 |

---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AES-256 | Advanced Encryption Standard with 256-bit keys |
| AOC | Attestation of Compliance |
| ASV | Approved Scanning Vendor |
| CDE | Cardholder Data Environment (systems and network segments that store, process or transmit cardholder data) |
| CI/CD | Continuous Integration / Continuous Delivery |
| CISO | Chief Information Security Officer |
| DDoS | Distributed Denial of Service |
| DPA | Data Processing Agreement |
| DSS | Data Security Standard |
| HR | Human Resources |
| HSM | Hardware Security Module |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| MFA | Multi-Factor Authentication |
| PAN | Primary Account Number (cardholder account number) |
| PCI | Payment Card Industry |
| QSA | Qualified Security Assessor |
| RBAC | Role-Based Access Control |
| RoC | Report on Compliance |
| SAQ-A | Self-Assessment Questionnaire A (PCI DSS) |
| SDLC | Software Development Life Cycle |
| SIEM | Security Information and Event Management |
| SOC | System and Organization Controls (audit report, e.g., SOC 2) |
| TLS | Transport Layer Security |
| WAF | Web Application Firewall |

## 1. Purpose

Maps each PCI DSS requirement to its applicability, the Xego controls and
evidence that address it, and the status of that evidence. This matrix is the
working document for the Level 1 Qualified Security Assessor (QSA) engagement
and mirrors the Statement of Applicability approach used for ISO/IEC 27001.

## 2. Assessment Scope

- Xego is a Level 1 merchant (more than 6 million card transactions per year)
  and is assessed by a QSA who produces a Report on Compliance (RoC) and a
  signed Attestation of Compliance (AOC).
- The scope of the assessment is the Cardholder Data Environment (CDE) defined
  in the PCI DSS Scope & CDE Inventory. Systems proven to be out of scope via
  segmentation are marked accordingly.
- Status reflects current evidence; the matrix is updated as evidence matures
  and is re-confirmed at least annually.

## 3. Requirements 1-12 Compliance Matrix

| Req | Title | Applicable | Key Controls / Evidence | Evidence Owner | Status |
|---|---|---|---|---|---|
| 1 | Install and maintain network security controls | Yes | CDE segmentation (isolated PCI zone), default-deny ACLs, WAF + DDoS protection at the edge | Security | Planned |
| 2 | Apply secure configurations to all system components | Yes | Hardening baseline, vendor defaults removed, configuration change control | Ops / Security | Planned |
| 3 | Protect stored account data | Yes | PAN never stored; tokenization; AES-256 at rest; HSM-backed key management; token vault | Security | Planned |
| 4 | Protect cardholder data with strong cryptography during transmission | Yes | TLS 1.3 for all traffic; mTLS inside the PCI zone | Security | Planned |
| 5 | Protect all systems against malware and update software | Yes | Anti-malware / EDR, patch management, vulnerability lifecycle | Ops | Planned |
| 6 | Develop and maintain secure systems and software | Yes | SAST/DAST in CI/CD, secure coding standards, WAF, change management | Engineering | Planned |
| 7 | Restrict access to cardholder data by business need to know | Yes | RBAC, least privilege, entitlement reviews, access control policy | Security | Planned |
| 8 | Identify users and authenticate access to system components | Yes | MFA for admin and vault access, unique credentials, joiner-mover-leaver process | Security / HR | Planned |
| 9 | Restrict physical access to cardholder data | Not applicable (cloud) | Cloud provider SOC 2 attestation, supplier due diligence | Procurement | Not applicable |
| 10 | Log and monitor all access to system components and cardholder data | Yes | Tamper-evident audit logs, SIEM correlation, 1-year log retention | Security | Planned |
| 11 | Regularly test security systems and processes | Yes | Quarterly ASV scans, annual penetration test, segmentation validation, scan evidence retained | Security | Planned |
| 12 | Support information security with organizational policies and programs | Yes | Information security policy, risk assessment, incident response, training, supplier DPAs, AOC | Board / CISO | Planned |

## 4. Evidence Cross-Reference

| Requirement group | Supporting document |
|---|---|
| Req 1, 2, 3, 11 | PCI DSS Scope & CDE Inventory |
| Req 3, 4 | Cryptography / Key Management Policy |
| Req 7, 8 | Access Control Policy; Privileged Access / Audit Log Register |
| Req 6 | Change Management Procedure; secure SDLC practices |
| Req 10 | Privileged Access / Audit Log Register; InfoSec Metrics (monthly) |
| Req 11 | Vulnerability / Pen-Test Findings Register |
| Req 12 | Information Security Policy; Incident Response Plan; Supplier / DPA Register; PCI DSS Attestation of Compliance (AOC) |

## 5. Gap List

- SAQ-A is not applicable at Level 1; the QSA RoC replaces it.
- Quarterly ASV scan reports and the annual penetration test report must be
  retained with the AOC as evidence.
- The CDE boundary and segmentation evidence must be ready before the QSA
  site assessment.
- Where a control is partially implemented, the gap and remediation date are
  tracked in the Vulnerability / Pen-Test Findings Register.

## 6. Review & Update Cadence

- Updated on any material change to scope, controls or evidence.
- Re-confirmed annually in support of the Level 1 AOC.
- Status changes are approved by the CISO.

## 7. References

PCI DSS Requirements 1-12; PCI DSS Scope & CDE Inventory; PCI DSS Attestation
of Compliance (AOC); ISO/IEC 27001:2022 Statement of Applicability.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
