---
title: STATEMENT OF APPLICABILITY (SoA)
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# STATEMENT OF APPLICABILITY (SoA)

| | |
|---|---|
| **Document Owner** | CISO |
| **Approved By** | Board / Top Management |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually and before each audit cycle |
| **Regulatory Basis** | ISO/IEC 27001:2022 Clause 6.1.3 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| A5 | ISO/IEC 27001:2022 Annex A Group A.5 - Organizational controls |
| A6 | ISO/IEC 27001:2022 Annex A Group A.6 - People controls |
| A7 | ISO/IEC 27001:2022 Annex A Group A.7 - Physical controls |
| A8 | ISO/IEC 27001:2022 Annex A Group A.8 - Technological controls |
| ASV | Approved Scanning Vendor |
| BCP/DR | Business Continuity Plan / Disaster Recovery |
| CBN/NFIU/NITDA | Central Bank of Nigeria / Nigerian Financial Intelligence Unit / NITDA (see individual entries) |
| CI/CD | Continuous Integration / Continuous Delivery |
| CISO | Chief Information Security Officer |
| CISO/CCO | Chief Information Security Officer / Chief Compliance Officer |
| DAST/SAST | Dynamic / Static Application Security Testing |
| DLP | Data Loss Prevention |
| DPA | Data Processing Agreement |
| HA | High Availability |
| HR | Human Resources |
| ICT | Information and Communication Technology |
| ID | Identifier |
| IRP | Incident Response Plan |
| ISMS | Information Security Management System |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| IaC | Infrastructure as Code |
| JML | Joiner / Mover / Leaver |
| JSON | JavaScript Object Notation |
| MDM | Mobile Device Management |
| MFA | Multi-Factor Authentication |
| NDA | Non-Disclosure Agreement |
| NTP | Network Time Protocol |
| OTP | One-Time Password |
| OWASP | Open Worldwide Application Security Project |
| PII | Personally Identifiable Information |
| RBAC | Role-Based Access Control |
| SAST | Static Application Security Testing |
| SIEM | Security Information and Event Management |
| SSO/MFA | Single Sign-On / Multi-Factor Authentication |
| SoA | Statement of Applicability (ISO/IEC 27001:2022 control-applicability statement) |
| TLS | Transport Layer Security |
| VPC | Virtual Private Cloud |
| WAF | Web Application Firewall |

## 1. Purpose

List every control in ISO/IEC 27001:2022 Annex A and state whether it is applicable to the Xego ISMS, with justification and implementation reference. Required for certification.

## 2. How to Use

For each control, set Applicable = Yes or No. Where Yes, cite the implementation reference (document, system control, or inherited cloud control). Where No, give justification (e.g., control not relevant to the service or provided by the cloud provider). Update before each surveillance/re-certification audit.

## 3. Controls

Applies to all 93 Annex A controls, grouped below by control group (A.5 Organizational, A.6 People, A.7 Physical, A.8 Technological). Complete the matrix; where a control is not applicable, state why (e.g., inherited from the cloud provider).

### 3.1 Organizational controls (A.5)

| Control ID | Control Title | Applicable (Y/N) | Justification / Reference | Status |
|---|---|---|---|---|
| A.5.1 | Policies for information security | Y | Information Security Policy | Implemented |
| A.5.2 | Information security roles and responsibilities | Y | CISO/CCO appointments | Implemented |
| A.5.3 | Segregation of duties | Y | Access Control Policy; RBAC | Planned |
| A.5.4 | Management responsibilities | Y | Policy approvals; management review | Implemented |
| A.5.5 | Contact with authorities | Y | CBN/NFIU/NITDA contact register | Planned |
| A.5.6 | Contact with special interest groups | Y | Industry bodies; threat intel | Planned |
| A.5.7 | Threat intelligence | Y | Observability/SIEM feeds | Planned |
| A.5.8 | Information security in project management | Y | Change Management Procedure | Planned |
| A.5.9 | Inventory of information and other associated assets | Y | Asset Inventory | Planned |
| A.5.10 | Acceptable use of information and other associated assets | Y | Acceptable Use / HR terms | Planned |
| A.5.11 | Return of assets | Y | JML Register; offboarding | Planned |
| A.5.12 | Classification of information | Y | Data classification in Data Inventory | Planned |
| A.5.13 | Labelling of information | Y | Data classification labels | Planned |
| A.5.14 | Information transfer | Y | TLS, DPA transfer safeguards | Planned |
| A.5.15 | Access control | Y | Access Control Policy | Planned |
| A.5.16 | Identity management | Y | JML Register; SSO/MFA | Planned |
| A.5.17 | Authentication information | Y | Crypto Policy; vault | Planned |
| A.5.18 | Access rights | Y | RBAC; entitlement reviews | Planned |
| A.5.19 | Information security in supplier relationships | Y | Vendor Risk Policy; DPA register | Planned |
| A.5.20 | Addressing information security within supplier agreements | Y | DPAs | Planned |
| A.5.21 | Managing information security in the ICT supply chain | Y | Supplier tiering; sub-processor list | Planned |
| A.5.22 | Monitoring, review and change management of supplier services | Y | Supplier reviews | Planned |
| A.5.23 | Information security for use of cloud services | Y | Cloud shared-responsibility assessment | Planned |
| A.5.24 | Information security incident management planning and preparation | Y | Incident Response Plan | Planned |
| A.5.25 | Assessment and decision on information security events | Y | IRP triage | Planned |
| A.5.26 | Response to information security incidents | Y | IRP | Planned |
| A.5.27 | Learning from information security incidents | Y | Post-incident reviews | Planned |
| A.5.28 | Collection of evidence | Y | IRP evidence handling | Planned |
| A.5.29 | Information security during disruption | Y | BCP/DR Plan | Planned |
| A.5.30 | ICT readiness for business continuity | Y | Restore drills | Planned |
| A.5.31 | Legal, statutory, regulatory and contractual requirements | Y | Regulatory obligations register; compliance mapping | Implemented |
| A.5.32 | Intellectual property rights | Y | HR/legal terms | Planned |
| A.5.33 | Protection of records | Y | Retention Schedule; ledger records | Planned |
| A.5.34 | Privacy and protection of PII | Y | Privacy & Data Protection Policy | Implemented |
| A.5.35 | Independent review of information security | Y | Internal Audit Reports | Planned |
| A.5.36 | Compliance with policies, rules and standards | Y | Policy compliance monitoring | Planned |
| A.5.37 | Documented operating procedures | Y | This document set | Implemented |

### 3.2 People controls (A.6)

| Control ID | Control Title | Applicable (Y/N) | Justification / Reference | Status |
|---|---|---|---|---|
| A.6.1 | Screening | Y | HR screening process | Planned |
| A.6.2 | Terms and conditions of employment | Y | Employment contracts | Implemented |
| A.6.3 | Information security awareness, education and training | Y | Training Records / Competence Matrix | Planned |
| A.6.4 | Disciplinary process | Y | HR policy | Planned |
| A.6.5 | Responsibilities after termination or change of employment | Y | JML Register; offboarding | Planned |
| A.6.6 | Confidentiality or non-disclosure agreements | Y | NDA Register | Implemented |
| A.6.7 | Remote working | Y | Remote working / MDM policy | Planned |
| A.6.8 | Information security event reporting | Y | IRP reporting channels | Planned |

### 3.3 Physical controls (A.7)

| Control ID | Control Title | Applicable (Y/N) | Justification / Reference | Status |
|---|---|---|---|---|
| A.7.1 | Physical security perimeters | N | Cloud hosting; inherited from provider | Not applicable |
| A.7.2 | Physical entry | N | Cloud data centres; inherited | Not applicable |
| A.7.3 | Securing offices, rooms and facilities | N | Cloud hosting; minimal office processing | Not applicable |
| A.7.4 | Physical security monitoring | N | Cloud data centres; inherited | Not applicable |
| A.7.5 | Protecting against physical and environmental threats | N | Cloud; inherited controls | Not applicable |
| A.7.6 | Working in secure areas | N | Cloud; inherited | Not applicable |
| A.7.7 | Clear desk and clear screen | Y | Office policy; device lock | Planned |
| A.7.8 | Equipment siting and protection | N | Cloud; inherited | Not applicable |
| A.7.9 | Security of assets off-premises | Y | Laptop encryption; MDM | Planned |
| A.7.10 | Storage media | Y | Backup media handling; secure destruction | Planned |
| A.7.11 | Supporting utilities | N | Cloud; inherited | Not applicable |
| A.7.12 | Cabling security | N | Cloud; inherited | Not applicable |
| A.7.13 | Equipment maintenance | N | Cloud; inherited | Not applicable |
| A.7.14 | Secure disposal or re-use of equipment | Y | Laptop / MDM policy; secure media destruction | Planned |

### 3.4 Technological controls (A.8)

| Control ID | Control Title | Applicable (Y/N) | Justification / Reference | Status |
|---|---|---|---|---|
| A.8.1 | User endpoint devices | Y | Endpoint/MDM policy | Planned |
| A.8.2 | Privileged access rights | Y | Access Control Policy; audit log | Planned |
| A.8.3 | Information access restriction | Y | RBAC; least privilege | Planned |
| A.8.4 | Access to source code | Y | Repo permissions; CI/CD controls | Planned |
| A.8.5 | Secure authentication | Y | MFA; OTP flow | Planned |
| A.8.6 | Capacity management | Y | Capacity plan; PgBouncer tuning | Planned |
| A.8.7 | Protection against malware | Y | Endpoint protection; image scanning | Planned |
| A.8.8 | Management of technical vulnerabilities | Y | Vulnerability register; ASV scans | Planned |
| A.8.9 | Configuration management | Y | IaC; config review | Planned |
| A.8.10 | Information deletion | Y | Retention Schedule; purge worker | Planned |
| A.8.11 | Data masking | Y | Masking for support access | Planned |
| A.8.12 | Prevention of data leakage | Y | DLP controls; DPA restrictions | Planned |
| A.8.13 | Information backup | Y | Backup & Restore Runbook | Planned |
| A.8.14 | Redundancy of information processing facilities | Y | Multi-AZ; HA sizing | Planned |
| A.8.15 | Logging | Y | Structured JSON logs; audit register | Planned |
| A.8.16 | Monitoring activities | Y | Prometheus/Grafana; SIEM | Planned |
| A.8.17 | Clock synchronization | Y | NTP alignment | Planned |
| A.8.18 | Use of privileged utility programs | Y | Restricted admin tooling | Planned |
| A.8.19 | Installation of software on operational systems | Y | Change Management Procedure | Planned |
| A.8.20 | Networks security | Y | VPC/security groups/WAF | Planned |
| A.8.21 | Security of network services | Y | Managed network services assessment | Planned |
| A.8.22 | Segregation of networks | Y | VPC segmentation; public/private subnets | Planned |
| A.8.23 | Web filtering | Y | Network policy | Planned |
| A.8.24 | Use of cryptography | Y | Crypto Policy | Planned |
| A.8.25 | Secure development life cycle | Y | CI/CD gating; code review | Planned |
| A.8.26 | Application security requirements | Y | Threat modelling; OWASP | Planned |
| A.8.27 | Secure system architecture and engineering principles | Y | Clean/hexagonal architecture; security by design | Planned |
| A.8.28 | Secure coding | Y | Linters; SAST; code review | Planned |
| A.8.29 | Security testing in development and acceptance | Y | DAST/SAST; staging testing | Planned |
| A.8.30 | Outsourced development | Y | Vendor policy; third-party code review | Planned |
| A.8.31 | Separation of development, test and production environments | Y | Staging mirrors production; separate envs | Planned |
| A.8.32 | Change management | Y | Change Management Procedure | Planned |
| A.8.33 | Test information | Y | No real PII in tests | Planned |
| A.8.34 | Protection of information systems during audit testing | Y | Audit access controls | Planned |

## 4. References

ISO/IEC 27001:2022 Annex A; InfoSec Risk Assessment & Treatment Plan.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
