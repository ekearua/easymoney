---
title: INFORMATION SECURITY POLICY
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# INFORMATION SECURITY POLICY

| | |
|---|---|
| **Document Owner** | Chief Information Security Officer (CISO) / [Name] |
| **Approved By** | Board of Directors / Top Management |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually or on material change |
| **Regulatory Basis** | ISO/IEC 27001:2022 Clause 5.2; Annex A 5.1 |

---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AML/CFT | Anti-Money Laundering / Combating the Financing of Terrorism |
| CBN | Central Bank of Nigeria |
| CCO | Chief Compliance Officer |
| CI/CD | Continuous Integration / Continuous Delivery |
| CISO | Chief Information Security Officer |
| DSS | Data Security Standard |
| HR | Human Resources |
| ISMS | Information Security Management System |
| ISO | International Organization for Standardization |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KYC | Know Your Customer |
| KYC/AML | Know Your Customer / Anti-Money Laundering |
| KYC/EDD | Know Your Customer / Enhanced Due Diligence |
| MLPA | Money Laundering (Prohibition) Act 2004 |
| NDPR | Nigeria Data Protection Regulation 2019 |
| PCI | Payment Card Industry |
| PII | Personally Identifiable Information |
| RTO/RPO | Recovery Time Objective / Recovery Point Objective |
| SDLC | Software Development Life Cycle |

## 1. Purpose

This policy defines the information security objectives and management commitment of **Xego** ([Legal Entity Name]) for the WhatsApp/Telme payment platform and all supporting systems, people, processes and third parties. It is the top-level statement that anchors the Information Security Management System (ISMS).

## 2. Scope

Applies to:

- All employees, contractors, consultants and interns, and any third party granted access to Xego systems or data.
- All information assets: customer PII, payment and ledger data, KYC/AML records, source code, credentials, configuration and logs.
- All systems and infrastructure: application servers, databases, secrets, CI/CD pipelines, cloud accounts, workstations and mobile devices used for Xego business.
- All locations from which Xego data is accessed or processed.

## 3. Policy Statements

1. **Management commitment.** Top management will provide the resources, budget and authority required to implement and continually improve the ISMS, and will review its suitability at planned intervals.
2. **Risk-based approach.** Security controls shall be selected and implemented based on a documented risk assessment, treating information security as a business-enabling risk discipline, not a compliance exercise.
3. **Legal and regulatory compliance.** Xego will comply with all applicable laws and regulations, including the CBN AML/CFT Regulation 2009, MLPA 2004, NDPR 2019 and PCI DSS (where in scope).
4. **Defence in depth.** Layered technical, physical, organisational and administrative controls will protect the confidentiality, integrity and availability of information.
5. **Customer data protection.** Customer PII is protected as an asset of the highest value. Data minimisation, encryption at rest and in transit, least privilege and documented retention/destruction apply.
6. **People.** Security is everyone's responsibility. Roles with security impact are defined, screened on hire, trained annually, and bound by confidentiality agreements. Breaches of policy are subject to disciplinary action.
7. **Incident response.** All information security incidents will be reported, contained, investigated, documented and used for improvement.
8. **Continuous improvement.** Objectives, metrics and corrective actions will be tracked, and the policy and ISMS improved on the basis of findings, incidents and changing threats.

## 4. Objectives (Baseline)

- Zero material breach of customer PII.
- 100% of security-impacting roles screened and trained before access is granted.
- 100% of secrets managed in a vault; no plaintext credentials in code or repositories.
- Audit trail for every privileged action and KYC decision.
- Recovery objectives (RTO/RPO) met in scheduled restore tests.
- Annual independent testing and ISO 27001:2022 certification path maintained.

## 5. Roles and Responsibilities

| Role | Responsibility |
|---|---|
| **Board / Top Management** | Approve policy, provide resources, review ISMS at planned intervals. |
| **CISO / Security** | Implement and operate controls; risk treatment; reporting. |
| **CCO (AML/CFT)** | AML/CFT program, filings and KYC/EDD oversight. |
| **Engineering / Ops** | Secure SDLC, access control, monitoring, change and incident management. |
| **HR** | Screening, NDAs, training records, joiner/mover/leaver process. |
| **All personnel** | Comply with policy, report incidents and suspected weaknesses. |

## 6. Review and Governance

- Reviewed annually or upon material change (regulation, technology, business model).
- Reviewed alongside the ISMS management review (ISO 9.3) and Board meetings.
- Supported by the documented procedures listed in the Compliance Documents Register.

## 7. References

- ISO/IEC 27001:2022 (Clause 5.2; Annex A 5.1, 6, 7, 8)
- CBN AML/CFT Regulation 2009; MLPA 2004
- Nigeria Data Protection Regulation (NDPR) 2019
- PCI DSS (where in scope)

## 8. Acknowledgement

All personnel must read and acknowledge this policy. Acknowledgement is recorded by HR.

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for Board approval |
