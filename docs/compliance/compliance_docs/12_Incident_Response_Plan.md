---
title: INCIDENT RESPONSE PLAN
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# INCIDENT RESPONSE PLAN

| | |
|---|---|
| **Document Owner** | Security / CISO |
| **Approved By** | Board / Top Management |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually and after each major incident |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 5.24-5.28 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AML | Anti-Money Laundering |
| BCP/DR | Business Continuity Plan / Disaster Recovery |
| CBN | Central Bank of Nigeria |
| CCO | Chief Compliance Officer |
| CISO | Chief Information Security Officer |
| DDoS | Distributed Denial of Service |
| ID | Identifier |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KYC | Know Your Customer |
| L1 | KYC tier L1 - NIN/BVN-verified (low limits) |
| L2 | KYC tier L2 - government-ID verified (standard limits) |
| L3 | KYC tier L3 - full CDD (higher limits) |
| L4 | KYC tier L4 - enhanced due diligence (highest limits) |
| NDPR | Nigeria Data Protection Regulation 2019 |
| NITDA | National Information Technology Development Agency (Nigeria's data-protection regulator) |
| PII | Personally Identifiable Information |
| PR | Public Relations |
| SIEM | Security Information and Event Management |
| SOC | System and Organization Controls (audit report, e.g., SOC 2) |
| WAF | Web Application Firewall |

## 1. Purpose

Define how Xego detects, reports, contains, eradicates, recovers from and learns from information security incidents, including personal data breaches.

## 2. Severity Levels

| Level | Description | Examples |
|---|---|---|
| L1 - Critical | Impact on customer funds, PII, or availability | Data breach, ransomware, ledger manipulation, unauthorised admin access |
| L2 - Major | Significant service or security degradation | Sustained DDoS, verified KYC bypass |
| L3 - Moderate | Contained/limited impact | Rate-limit bypass, minor misconfiguration |
| L4 - Minor | No material impact | Single scan finding, phishing report |
| | | |
| | | |
| | | |

## 3. Roles and Contact

| Role | Responsibility |
|---|---|
| On-call engineer / SOC | Detection, initial triage, containment |
| Incident Commander (CISO or delegate) | Coordination, decisions, communication |
| CCO | AML / data-breach notifications (NITDA, affected customers) |
| PR / Management | External and customer communication |
| Legal | Legal holds, regulatory guidance |
| | |
| | |
| | |

## 4. Process

- Detection & reporting: alerts from SIEM/monitoring, user reports; all incidents reported internally within 1 hour of detection.
- Triage & classification: severity, affected assets (customer PII? funds? ledger?), evidence preservation.
- Containment: isolate affected systems, revoke compromised credentials, enable WAF rules / rate limiting.
- Eradication & recovery: remove root cause, restore from verified backups per BCP/DR Plan, re-enable services.
- Communication: internal status updates; notify NITDA and affected data subjects per NDPR where a personal data breach occurs; notify customers/CBN for fund-related incidents per applicable rules.
- Post-incident review: root-cause analysis, corrective actions, lessons, incident report retained.

## 5. Evidence

Evidence (logs, snapshots, audit trail) is preserved for regulatory review and potential legal proceedings; chain of custody is recorded.

## 6. References

ISO/IEC 27001:2022 Annex A 5.24-5.28; BCP/DR Plan; Privacy & Data Protection Policy.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
