#!/usr/bin/env python3
"""Generate markdown templates for the remaining 'To draft' compliance
documents (register rows 06-44, skipping 41 = NITDA registration, which is
'To obtain'). Each template reuses the branded cover + document-control block.
"""
import os

HERE = os.path.dirname(os.path.abspath(__file__))
COVER = """\
---
title: {title}
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{{=openxml}}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# {title}

| | |
|---|---|
| **Document Owner** | {owner} |
| **Approved By** | {approved} |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | {review} |
| **Regulatory Basis** | {basis} |
| **Roadmap ID** | {roadmap} |

---
"""

DOC_CONTROL = """\
## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
"""


def build(title, owner, approved, review, basis, roadmap, sections):
    out = [COVER.format(title=title, owner=owner, approved=approved,
                        review=review, basis=basis, roadmap=roadmap)]
    for heading, lines in sections:
        out.append("## " + heading)
        out.append("")
        out.extend(lines)
        out.append("")
    out.append(DOC_CONTROL)
    return "\n".join(out)


def table(header, rows, blank_rows=3):
    out = ["| " + " | ".join(header) + " |",
           "|" + "---|" * len(header)]
    for r in rows:
        out.append("| " + " | ".join(str(c) for c in r) + " |")
    for _ in range(blank_rows):
        out.append("| " + " | ".join("" for _ in header) + " |")
    return out


P = lambda *items: list(items)  # noqa: E731 - paragraphs/bullets list


def bullets(items):
    return ["- " + i for i in items]


DOCS = []

DOCS.append(("06_ISMS_Scope_Document", "ISMS SCOPE DOCUMENT", "CISO",
             "Board of Directors", "Annually or on material change",
             "ISO/IEC 27001:2022 Clause 4.3", "0.7", [
    ("1. Purpose", P("Define the boundaries of the Xego Information Security Management System (ISMS) in accordance with ISO/IEC 27001:2022 Clause 4.3, so that certification, audits and management review cover a clearly stated set of assets, locations, services and processes.")),
    ("2. Scope of the ISMS", P("The ISMS covers the Xego WhatsApp/Telme payment platform and its supporting organisation, specifically:", *bullets([
        "Services: account/onboarding, KYC/AML verification, wallet and payment transfers, invoice, data-order and thrift products, settlement and reconciliation.",
        "Systems: application services (internal/app, internal/service, internal/store), PostgreSQL 17 database(s), webhook receiver, scheduled workers (retention purge, reconciliation), CI/CD pipeline.",
        "Data assets: customer PII, KYC/AML records, ledger and payment events, chat/message history, audit logs, credentials and secrets.",
        "Locations: cloud hosting region(s) and data residency boundary (Phase 1 VPC); developer workstations in scope for handling of production data only via approved tooling.",
        "People: all employees, contractors and third parties with access to the above.",
        "Third parties: identity verification providers, cloud infrastructure, observability and email/support providers (see Supplier / DPA Register).",
    ]))),
    ("3. Exclusions", P("Explicitly out of scope with justification:", *bullets([
        "Physical sites of third-party cloud/data centres (shared responsibility - inherited controls).",
        "Customers' own devices and messaging infrastructure (WhatsApp), except for the APIs/connectors Xego controls.",
    ]))),
    ("4. Interfaces and Dependencies", P(*bullets([
        "Dependencies on external services (WhatsApp Business API, identity providers, banks/payment rails) are documented and risk-assessed in the Supplier / DPA Register.",
        "Interfaces between ISMS and AML/CFT program (KYC, STR/CTR) are coordinated by the CCO and CISO.",
    ]))),
    ("5. Review", P("This scope statement is reviewed annually or when the business, services or hosting change materially.")),
    ("6. References", P("ISO/IEC 27001:2022 Clause 4; Compliance Documents Register; Xego Compliance Roadmap item 0.7.")),
]))

DOCS.append(("07_Vendor_Third-Party_Risk_Policy", "VENDOR / THIRD-PARTY RISK MANAGEMENT POLICY", "Procurement / Compliance",
             "Board of Directors", "Annually or on material change",
             "ISO/IEC 27001:2022 Annex A 5.19-5.23; NDPR 2019", "0.9", [
    ("1. Purpose", P("Define how Xego selects, contracts, monitors and terminates third parties that process data or support the payment platform, protecting customer data and regulatory obligations.")),
    ("2. Scope", P("Applies to all suppliers, vendors, sub-processors and partners, including identity verification providers, cloud infrastructure, observability, email/support and any contractor with system access.")),
    ("3. Classification", P("Suppliers are classified by criticality and data exposure:", *table(
        ["Tier", "Examples", "Control Intensity"],
        [["Critical", "Identity verification (Smile ID/Youverify), cloud hosting, DB/backups", "Full due diligence, DPA, security assessment, quarterly monitoring"],
         ["Major", "Observability/SIEM, email, payment rails", "Due diligence, DPA, annual review"],
         ["Standard", "Office tools, non-sensitive SaaS", "Standard terms, register entry"]]))),
    ("4. Due Diligence", P(*bullets([
        "Critical/major suppliers undergo pre-contract due diligence: ownership, security practices, certifications (ISO 27001/SOC 2), breach history, sub-processor list.",
        "Verification providers must hold relevant licences/regulatory standing for identity services in Nigeria.",
        "Cloud hosting must satisfy data residency and encryption requirements.",
    ]))),
    ("5. Data Protection and DPAs", P(*bullets([
        "A binding Data Processing Agreement (DPA) is signed before any personal data is shared (NDPR-compliant).",
        "Cross-border transfer safeguards are recorded (standard contractual clauses or adequacy mechanisms).",
        "Sub-processor changes require notice/consent per the DPA.",
    ]))),
    ("6. Monitoring and Offboarding", P(*bullets([
        "Critical suppliers are reviewed quarterly (incidents, SLAs, control changes); major annually.",
        "On termination: credentials revoked, data returned/destroyed per DPA, residual access removed and logged.",
    ]))),
    ("7. References", P("ISO/IEC 27001:2022 Annex A 5.19-5.23; NDPR 2019; Supplier / DPA Register; Compliance Documents Register; Roadmap item 0.9.")),
]))

DOCS.append(("08_Access_Control_Policy", "ACCESS CONTROL POLICY", "Security / CISO",
             "Board of Directors", "Annually", "ISO/IEC 27001:2022 Annex A 5.15-5.18, 8.2", "1.2", [
    ("1. Purpose", P("Define how access to Xego systems and data is granted, managed, reviewed and revoked to enforce least privilege and protect customer PII, KYC records and the ledger.")),
    ("2. Scope", P("All users (staff, contractors, administrators, merchants, service accounts) and all systems (admin console, merchant portal, databases, CI/CD, observability, cloud accounts).")),
    ("3. Principles", P(*bullets([
        "Least privilege and need-to-know: access only to the minimum required to perform the role.",
        "Role-based access control (RBAC): named roles with predefined entitlements; no shared personal accounts.",
        "Segregation of duties: no single user can both approve a payment/KYC decision and execute it without a second control.",
        "Multi-factor authentication (MFA/TOTP or passkeys) for all admin and merchant logins and all privileged actions.",
    ]))),
    ("4. Joiner / Mover / Leaver", P(*bullets([
        "Joiner: request via Joiner-Mover-Leaver Register, HR verification, role-based provisioning, mandatory confidentiality agreement and training before access.",
        "Mover: entitlements re-aligned to the new role; obsolete access removed.",
        "Leaver: accounts disabled same-day; assets returned; access removed and logged in the JML Register.",
    ]))),
    ("5. Entitlement Reviews", P("Security reviews all privileged accounts and role memberships quarterly; any orphaned or excessive access is removed. Results recorded in the Privileged Access / Audit Log Register.")),
    ("6. Roles and Responsibilities", P(*table(
        ["Role", "Responsibility"],
        [["HR", "Verification, JML process, offboarding triggers"],
         ["Security / CISO", "Policy, entitlement reviews, privileged account oversight"],
         ["Engineering", "Technical enforcement, least-privilege defaults, approval flows"],
         ["Line managers", "Access justification and sign-off"],
         ["All users", "Use of MFA, reporting lost credentials, no password sharing"]]))),
    ("7. References", P("ISO/IEC 27001:2022 Annex A 5.15-5.18, 8.2-8.5; JML Register; Privileged Access / Audit Log Register; Roadmap items 1.1, 1.2, 5.5.")),
]))

DOCS.append(("09_Cryptography_Key_Management_Policy", "CRYPTOGRAPHY / KEY MANAGEMENT POLICY", "Security / CISO",
             "Board of Directors", "Annually", "ISO/IEC 27001:2022 Annex A 8.24", "1.7", [
    ("1. Purpose", P("Define the approved cryptographic controls and key lifecycle for protecting Xego customer data at rest and in transit.")),
    ("2. Scope", P("All data at rest and in transit, all keys, certificates and secrets used by the platform, infrastructure and CI/CD.")),
    ("3. Approved Controls", P(*bullets([
        "Data in transit: TLS 1.2+ (1.3 preferred) with strong ciphers; internal service-to-service traffic also encrypted.",
        "Data at rest: PII columns encrypted with pgcrypto (AES-256) and/or volume/disk encryption; backups encrypted.",
        "Passwords/hashes: Argon2id or bcrypt (cost >= 10); never stored in plaintext.",
        "Tokens/sessions: JWT signed with RS256/HS256 with short expiry; secrets stored in a vault, never in source code.",
        "Signing: strong asymmetric keys; key size at least RSA-2048 or EC P-256.",
    ]))),
    ("4. Key Lifecycle", P(*bullets([
        "Generation: within the vault/HSM; never in plaintext on disk or in repos.",
        "Distribution: only via approved secrets management; access logged and least-privilege.",
        "Storage: encrypted vault (e.g., Vault/cloud KMS); service accounts using short-lived credentials.",
        "Rotation: keys rotated on schedule (TLS annually, signing keys 12-18 months) and immediately on compromise.",
        "Backup: escrowed/backed-up keys recoverable via break-glass procedure.",
        "Destruction: secure deletion of keys and cryptographic material on decommissioning.",
    ]))),
    ("5. Responsibilities", P(*table(
        ["Role", "Responsibility"],
        [["CISO / Security", "Ownership of this policy, key inventory, rotation schedule"],
         ["Engineering", "Use approved libraries and avoid custom crypto"],
         ["Ops", "TLS certificate lifecycle, vault operations, break-glass procedures"]]))),
    ("6. References", P("ISO/IEC 27001:2022 Annex A 8.24; Asset Inventory; Roadmap items 1.6, 1.7.")),
]))

DOCS.append(("10_Change_Management_Procedure", "CHANGE MANAGEMENT PROCEDURE", "Ops / Security",
             "Head of Engineering / CISO", "Annually", "ISO/IEC 27001:2022 Annex A 8.32", "1.14", [
    ("1. Purpose", P("Define how changes to the production platform, infrastructure and configuration are requested, assessed, approved, tested, implemented and reviewed, minimising the risk of outages and security regressions.")),
    ("2. Scope", P("Application releases, database schema changes, infrastructure/network changes, secrets and configuration, dependencies, and third-party integrations.")),
    ("3. Change Types", P(*table(
        ["Type", "Definition", "Approval"],
        [["Standard", "Pre-approved, low-risk, repeatable (routine patch, CI release)", "Pre-approved runbook / automated pipeline"],
         ["Minor", "Low impact, well-understood", "Engineering lead / senior on-call"],
         ["Major", "Cross-service or significant user impact", "Change Advisory Board (CAB) including Security"],
         ["Emergency", "Required to restore service or close an active incident", "Incident commander + CISO; post-change review within 5 days"]]))),
    ("4. Workflow", P(*bullets([
        "1. Record request: description, scope, owner, backout plan, rollback strategy.",
        "2. Assess impact and risk (security, data, availability) using the Risk Management Policy.",
        "3. Obtain approval per change type above.",
        "4. Test in staging that mirrors production (same OS, versions, configuration).",
        "5. Implement with monitoring; abort and roll back if KPIs breach thresholds.",
        "6. Post-change review: success criteria, lessons, records retained.",
    ]))),
    ("5. Records", P("All changes are recorded in the change log with approver, date, result and backout outcome; privileged change actions appear in the Privileged Access / Audit Log Register.")),
    ("6. References", P("ISO/IEC 27001:2022 Annex A 8.32; Incident Response Plan; Roadmap items 0.10, 1.14.")),
]))

DOCS.append(("11_Retention_Destruction_Schedule", "RETENTION & DESTRUCTION SCHEDULE", "Ops / Compliance",
             "CCO / CISO", "Annually", "MLPA 2004 record keeping; ISO/IEC 27001:2022 Annex A 8.10", "1.8", [
    ("1. Purpose", P("Define how long each class of data is retained and how it is securely destroyed, balancing regulatory minimums (e.g., 7-year KYC/transaction records under MLPA) with NDPR storage limitation.")),
    ("2. Retention Table", P(*table(
        ["Data Class", "Retention", "Destruction Method", "Basis"],
        [["KYC / identity / verification records", "7 years after account closure", "Secure erasure + DB purge, logged", "MLPA record keeping"],
         ["Ledger / transaction / payment events", "7 years", "Secure erasure, archived copy per policy", "MLPA record keeping"],
         ["STR / CTR records", "7 years", "Secure erasure", "MLPA 2004"],
         ["Chat / message content", "90 days", "Automated batch purge worker", "NDPR storage limitation"],
         ["Session / OTP records", "24 hours (or OTP validity)", "Automated expiry/purge", "Operational"],
         ["Audit logs (privileged actions)", "2 years minimum", "Secure erasure", "ISO A 8.15"],
         ["Support / dispute tickets", "3 years", "Secure erasure", "Operational / contractual"],
         ["Webhook / raw event logs", "90 days", "Batch purge / log rotation", "Operational"],
         ["Backups", "Per Backup & Restore Runbook", "Overwritten / secure destruction", "DR requirements"],
         ["Marketing / optional data", "Until consent withdrawn", "Secure erasure", "NDPR consent"],
         ["Screening / sanctions match records", "7 years", "Secure erasure", "CBN AML/CFT"]]))),
    ("3. Purge Controls", P(*bullets([
        "The 90-day purge runs automatically and in batches to avoid table locks at scale (refactor of PurgeBefore in internal/store/store.go:4158-4200).",
        "Every purge run is logged: records deleted, duration, errors, and any legal hold exclusions.",
        "Destruction of physical media/backups follows the DR offboarding procedures.",
    ]))),
    ("4. Legal / Regulatory Holds", P("On notice of litigation, investigation or CBN/NFIU examination, relevant records are placed on hold and not destroyed until the hold is lifted.")),
    ("5. Responsibilities", P(*table(
        ["Role", "Responsibility"],
        [["Compliance", "Schedule ownership, holds, MLPA minimums"],
         ["Ops / Engineering", "Purge automation, logging, backups"],
         ["Security", "Audit log retention, secure destruction oversight"]]))),
    ("6. References", P("MLPA 2004; NDPR 2019; ISO A 8.10; Data Inventory / Residency Register; Roadmap item 1.8.")),
]))

DOCS.append(("12_Incident_Response_Plan", "INCIDENT RESPONSE PLAN", "Security / CISO",
             "Board / Top Management", "Annually and after each major incident", "ISO/IEC 27001:2022 Annex A 5.24-5.28", "3.1", [
    ("1. Purpose", P("Define how Xego detects, reports, contains, eradicates, recovers from and learns from information security incidents, including personal data breaches.")),
    ("2. Severity Levels", P(*table(
        ["Level", "Description", "Examples"],
        [["L1 - Critical", "Impact on customer funds, PII, or availability", "Data breach, ransomware, ledger manipulation, unauthorised admin access"],
         ["L2 - Major", "Significant service or security degradation", "Sustained DDoS, verified KYC bypass"],
         ["L3 - Moderate", "Contained/limited impact", "Rate-limit bypass, minor misconfiguration"],
         ["L4 - Minor", "No material impact", "Single scan finding, phishing report"]]))),
    ("3. Roles and Contact", P(*table(
        ["Role", "Responsibility"],
        [["On-call engineer / SOC", "Detection, initial triage, containment"],
         ["Incident Commander (CISO or delegate)", "Coordination, decisions, communication"],
         ["CCO", "AML / data-breach notifications (NITDA, affected customers)"],
         ["PR / Management", "External and customer communication"],
         ["Legal", "Legal holds, regulatory guidance"]]))),
    ("4. Process", P(*bullets([
        "Detection & reporting: alerts from SIEM/monitoring, user reports; all incidents reported internally within 1 hour of detection.",
        "Triage & classification: severity, affected assets (customer PII? funds? ledger?), evidence preservation.",
        "Containment: isolate affected systems, revoke compromised credentials, enable WAF rules / rate limiting.",
        "Eradication & recovery: remove root cause, restore from verified backups per BCP/DR Plan, re-enable services.",
        "Communication: internal status updates; notify NITDA and affected data subjects per NDPR where a personal data breach occurs; notify customers/CBN for fund-related incidents per applicable rules.",
        "Post-incident review: root-cause analysis, corrective actions, lessons, incident report retained.",
    ]))),
    ("5. Evidence", P("Evidence (logs, snapshots, audit trail) is preserved for regulatory review and potential legal proceedings; chain of custody is recorded.")),
    ("6. References", P("ISO/IEC 27001:2022 Annex A 5.24-5.28; BCP/DR Plan; Privacy & Data Protection Policy; Roadmap items 3.1, 1.13.")),
]))

DOCS.append(("13_Business_Continuity_DR_Plan", "BUSINESS CONTINUITY / DISASTER RECOVERY PLAN", "Ops / Risk",
             "Board / Top Management", "Annually and after each drill or incident", "ISO/IEC 27001:2022 Annex A 5.29-5.30", "3.7", [
    ("1. Purpose", P("Define how Xego continues critical services and recovers within agreed targets during and after disruptions, including infrastructure failures, data centre loss and cyber incidents.")),
    ("2. Business Impact Summary", P(*table(
        ["Service", "RTO", "RPO", "Recovery Strategy"],
        [["Payment / transaction processing", "4 hours", "15 minutes", "Multi-AZ failover; PITR to WAL archive"],
         ["Ledger / reconciliation", "4 hours", "15 minutes", "Database PITR from WAL/S3"],
         ["Messaging / chat platform", "1 hour", "30 minutes", "Stateless services behind load balancer"],
         ["Admin / merchant portals", "2 hours", "15 minutes", "Multi-instance, LB failover"],
         ["Reporting / STR-CTR extraction", "12 hours", "24 hours", "Re-run from ledger"]]))),
    ("3. BCP Team and Activation", P(*bullets([
        "Incident Commander (Ops lead) activates the plan; BCP team comprises Ops, Security, Compliance, Engineering and PR.",
        "Activation triggers: data centre loss, prolonged outage beyond RTO threshold, major incident under the IRP, cloud provider regional failure.",
        "Status is tracked on an incident channel and reported to the Risk Committee and Board per severity.",
    ]))),
    ("4. Recovery Procedures", P(*bullets([
        "1. Declare disaster; notify BCP team and stakeholders.",
        "2. Promote standby environment / re-point DNS and load balancer to healthy instances.",
        "3. Restore database via PITR (base backup + WAL to target point).",
        "4. Validate data integrity, ledger consistency and reconciliation before resuming customer traffic.",
        "5. Resume traffic gradually; verify monitoring and webhook delivery.",
        "6. Post-recovery review and documentation.",
    ]))),
    ("5. Testing and Maintenance", P("Quarterly restore tests (see DR Restore Test Reports) verify RTO/RPO; results and corrective actions are reported to the Risk Committee.")),
    ("6. References", P("ISO/IEC 27001:2022 Annex A 5.29-5.30; Backup & Restore Runbook; Incident Response Plan; Roadmap items 1.10, 1.12, 3.7.")),
]))

DOCS.append(("14_Backup_Restore_Runbook", "BACKUP & RESTORE RUNBOOK", "Ops",
             "Head of Ops / CISO", "Quarterly review", "ISO/IEC 27001:2022 Annex A 8.13", "1.12", [
    ("1. Purpose", P("Provide the operational procedures for backing up production data and restoring it within RTO/RPO targets.")),
    ("2. Backup Inventory", P(*table(
        ["Asset", "Backup Method", "Frequency", "Retention", "Location"],
        [["PostgreSQL database", "Base backup + continuous WAL archiving (wal_level=replica) to S3/GCS", "Base daily, WAL continuous", "Per retention schedule; PITR window", "Encrypted object storage, separate region"],
         ["Config / infrastructure as code", "Versioned in CI/CD", "On change", "Git history", "Repository"],
         ["Secrets", "Vault snapshot / KMS-managed", "On change", "Per key policy", "Vault / cloud KMS"],
         ["Certificates / keys", "Escrow copy", "On issuance", "Until renewal", "Vault"]]))),
    ("3. Restore Procedures", P(*bullets([
        "1. Verify backup existence and integrity for the target point-in-time.",
        "2. Provision/restore database from base backup.",
        "3. Replay WAL segments to the desired point-in-time (PITR).",
        "4. Run integrity checks (pg_dump/checksums, ledger reconciliation).",
        "5. Point application instances at restored DB after smoke tests.",
        "6. Record restore attempt, timings and success in DR Restore Test Reports.",
    ]))),
    ("4. Success Criteria", P("Restore completes within the RTO for the service and loses no more than the RPO; reconciliation matches the ledger.")),
    ("5. Monitoring", P("Backup jobs are monitored; failures alert on-call within 15 minutes; a missed backup is treated as an incident.")),
    ("6. References", P("ISO A 8.13; Business Continuity / DR Plan; Roadmap items 1.10, 1.12.")),
]))

DOCS.append(("15_Enterprise_Risk_Register", "ENTERPRISE RISK REGISTER", "Risk",
             "Board of Directors", "Quarterly review", "ISO/IEC 27001:2022 Clause 6.1", "0.7", [
    ("1. Purpose", P("Register of material risks to Xego with owners, treatment and residual risk, maintained in line with the Risk Management Policy.")),
    ("2. Scoring", P("Likelihood and impact are scored 1-5 per the Risk Management Policy; rating = likelihood x impact (P0 >= 15, P1 8-14, P2 3-7, P3 1-2).")),
    ("3. Register", P(*table(
        ["Risk ID", "Category", "Risk Description", "L", "I", "Score", "Owner", "Treatment", "Residual Risk", "Review Date", "Status"],
        [["R-001", "AML/CFT", "Simulated KYC approvals allow unverified onboarding", "4", "5", "20", "CCO", "Real verification workflow + provider integration (Roadmap 2.1-2.2)", "Low", "", "Open"],
         ["R-002", "Data Protection", "Customer PII stored in plaintext across 8+ tables", "3", "5", "15", "CISO", "pgcrypto encryption at rest + data minimisation (Roadmap 1.7)", "Medium", "", "Open"],
         ["R-003", "Governance", "No audit trail for privileged actions / KYC decisions", "3", "4", "12", "CISO", "Audit logging for admin actions (Roadmap 1.4)", "Medium", "", "Open"],
         ["R-004", "Operational", "Unbounded purge deletes could lock tables at scale", "3", "3", "9", "Ops", "Batch purge worker (Roadmap 1.8)", "Low", "", "Open"],
         ["R-005", "Resilience", "Single database is a single point of failure", "3", "4", "12", "Ops", "PITR/WAL, multi-AZ, restore drills (Roadmap 1.10, 1.12)", "Medium", "", "Open"]]))),
    ("4. Use", P("Extreme (P0) risks are escalated to the Board within 5 business days; the register is consolidated with the InfoSec Risk Assessment and the ML/FT risk assessment at least quarterly.")),
    ("5. References", P("Risk Management Policy; InfoSec Risk Assessment & Treatment Plan; Institutional ML/FT Risk Assessment; Roadmap item 0.7.")),
]))

DOCS.append(("16_InfoSec_Risk_Assessment_Treatment_Plan", "INFORMATION SECURITY RISK ASSESSMENT & TREATMENT PLAN", "CISO",
             "Board / Top Management", "Annually or on material change", "ISO/IEC 27001:2022 Clauses 6.1-6.3", "0.7", [
    ("1. Purpose", P("Document the ISO 27001:2022 Clause 6.1 risk assessment and treatment decisions that drive the Statement of Applicability and the ISMS control set.")),
    ("2. Method", P("Uses the 5x5 likelihood x impact matrix in the Risk Management Policy. Steps: identify assets/processes -> identify threats and vulnerabilities -> analyse (score) -> evaluate (accept/treat per appetite) -> select controls (Annex A) -> record residual risk -> monitor.")),
    ("3. Assessment", P(*table(
        ["Asset / Process", "Threat / Vulnerability", "L", "I", "Score", "Selected Control (Annex A)", "Owner", "Target", "Status"],
        [["Customer PII database", "Unauthorised access; plaintext storage", "3", "5", "15", "A 8.2 Access, A 8.24 Crypto, A 8.11/8.12 logging", "CISO", "Phase 1", "Open"],
         ["Admin / merchant auth", "Credential stuffing, weak login", "3", "4", "12", "A 8.5 MFA, A 8.6 capacity/rate limiting", "Engineering", "Phase 1", "Open"],
         ["Webhook receiver", "Forged webhook events", "3", "4", "12", "A 8.25/8.26 secure APIs, signature verification", "Engineering", "Phase 1", "Open"],
         ["CI/CD pipeline", "Malicious or misconfigured builds", "2", "4", "8", "A 8.28/8.29 secure SDLC, secrets vaulting", "Engineering", "Phase 1", "Open"],
         ["Backups", "Loss of PITR capability", "2", "5", "10", "A 8.13 backups, A 8.14 redundancy, drills", "Ops", "Phase 1", "Open"]]))),
    ("4. Treatment", P("Controls are implemented, transferred (e.g., inherited cloud controls) or accepted with recorded justification; residual risk is tracked in the Enterprise Risk Register.")),
    ("5. References", P("ISO 27001:2022 Clauses 6.1-6.3; Risk Management Policy; Statement of Applicability; Roadmap item 0.7.")),
]))

DOCS.append(("17_Statement_of_Applicability", "STATEMENT OF APPLICABILITY (SoA)", "CISO",
             "Board / Top Management", "Annually and before each audit cycle", "ISO/IEC 27001:2022 Clause 6.1.3", "4.5", [
    ("1. Purpose", P("List every control in ISO/IEC 27001:2022 Annex A and state whether it is applicable to the Xego ISMS, with justification and implementation reference. Required for certification.")),
    ("2. How to Use", P("For each control, set Applicable = Yes or No. Where Yes, cite the implementation reference (document, system control, roadmap item, or inherited cloud control). Where No, give justification (e.g., control not relevant to the service or provided by the cloud provider). Update before each surveillance/re-certification audit.")),
    ("3. Controls", P("Applies to all 93 Annex A controls (A5 Organizational, A6 People, A7 Physical, A8 Technological). Complete the matrix below:", *table(
        ["Control ID", "Control Title", "Applicable (Y/N)", "Justification / Reference", "Status"],
        [["A.5.1", "Policies for information security", "Y", "Information Security Policy; Roadmap 0.2", "Implemented"],
         ["A.5.2", "Information security roles and responsibilities", "Y", "CISO/CCO appointments; Roadmap 0.1-0.2", "Implemented"],
         ["A.5.3", "Segregation of duties", "Y", "Access Control Policy; RBAC; Roadmap 1.2", "Planned"],
         ["A.5.4", "Management responsibilities", "Y", "Policy approvals; management review", "Implemented"],
         ["A.5.5", "Contact with authorities", "Y", "CBN/NFIU/NITDA contact register", "Planned"],
         ["A.5.6", "Contact with special interest groups", "Y", "Industry bodies; threat intel", "Planned"],
         ["A.5.7", "Threat intelligence", "Y", "Observability/SIEM feeds; Roadmap 1.13", "Planned"],
         ["A.5.8", "Information security in project management", "Y", "Change Management Procedure", "Planned"],
         ["A.5.9", "Inventory of information and other associated assets", "Y", "Asset Inventory", "Planned"],
         ["A.5.10", "Acceptable use of information and other associated assets", "Y", "Acceptable Use / HR terms", "Planned"],
         ["A.5.11", "Return of assets", "Y", "JML Register; offboarding", "Planned"],
         ["A.5.12", "Classification of information", "Y", "Data classification in Data Inventory", "Planned"],
         ["A.5.13", "Labelling of information", "Y", "Data classification labels", "Planned"],
         ["A.5.14", "Information transfer", "Y", "TLS, DPA transfer safeguards", "Planned"],
         ["A.5.15", "Access control", "Y", "Access Control Policy", "Planned"],
         ["A.5.16", "Identity management", "Y", "JML Register; SSO/MFA", "Planned"],
         ["A.5.17", "Authentication information", "Y", "Crypto Policy; vault", "Planned"],
         ["A.5.18", "Access rights", "Y", "RBAC; entitlement reviews", "Planned"],
         ["A.5.19", "Information security in supplier relationships", "Y", "Vendor Risk Policy; DPA register", "Planned"],
         ["A.5.20", "Addressing information security within supplier agreements", "Y", "DPAs", "Planned"],
         ["A.5.21", "Managing information security in the ICT supply chain", "Y", "Supplier tiering; sub-processor list", "Planned"],
         ["A.5.22", "Monitoring, review and change management of supplier services", "Y", "Supplier reviews", "Planned"],
         ["A.5.23", "Information security for use of cloud services", "Y", "Cloud shared-responsibility assessment", "Planned"],
         ["A.5.24", "Information security incident management planning and preparation", "Y", "Incident Response Plan", "Planned"],
         ["A.5.25", "Assessment and decision on information security events", "Y", "IRP triage", "Planned"],
         ["A.5.26", "Response to information security incidents", "Y", "IRP", "Planned"],
         ["A.5.27", "Learning from information security incidents", "Y", "Post-incident reviews", "Planned"],
         ["A.5.28", "Collection of evidence", "Y", "IRP evidence handling", "Planned"],
         ["A.5.29", "Information security during disruption", "Y", "BCP/DR Plan", "Planned"],
         ["A.5.30", "ICT readiness for business continuity", "Y", "Restore drills", "Planned"],
         ["A.5.31", "Legal, statutory, regulatory and contractual requirements", "Y", "Regulatory Mapping sheet", "Implemented"],
         ["A.5.32", "Intellectual property rights", "Y", "HR/legal terms", "Planned"],
         ["A.5.33", "Protection of records", "Y", "Retention Schedule; ledger records", "Planned"],
         ["A.5.34", "Privacy and protection of PII", "Y", "Privacy & Data Protection Policy", "Implemented"],
         ["A.5.35", "Independent review of information security", "Y", "Internal Audit Reports", "Planned"],
         ["A.5.36", "Compliance with policies, rules and standards", "Y", "Policy compliance monitoring", "Planned"],
         ["A.5.37", "Documented operating procedures", "Y", "This document set", "Implemented"],
         ["A.6.1", "Screening", "Y", "HR screening process", "Planned"],
         ["A.6.2", "Terms and conditions of employment", "Y", "Employment contracts", "Implemented"],
         ["A.6.3", "Information security awareness, education and training", "Y", "Training Records / Competence Matrix", "Planned"],
         ["A.6.4", "Disciplinary process", "Y", "HR policy", "Planned"],
         ["A.6.5", "Responsibilities after termination or change of employment", "Y", "JML Register; offboarding", "Planned"],
         ["A.6.6", "Confidentiality or non-disclosure agreements", "Y", "NDA Register", "Implemented"],
         ["A.6.7", "Remote working", "Y", "Remote working / MDM policy", "Planned"],
         ["A.6.8", "Information security event reporting", "Y", "IRP reporting channels", "Planned"],
         ["A.7.1", "Physical security perimeters", "N", "Cloud hosting; inherited from provider", "Not applicable"],
         ["A.7.2", "Physical entry", "N", "Cloud data centres; inherited", "Not applicable"],
         ["A.7.3", "Securing offices, rooms and facilities", "N", "Cloud hosting; minimal office processing", "Not applicable"],
         ["A.7.4", "Physical security monitoring", "N", "Cloud data centres; inherited", "Not applicable"],
         ["A.7.5", "Protecting against physical and environmental threats", "N", "Cloud; inherited controls", "Not applicable"],
         ["A.7.6", "Working in secure areas", "N", "Cloud; inherited", "Not applicable"],
         ["A.7.7", "Clear desk and clear screen", "Y", "Office policy; device lock", "Planned"],
         ["A.7.8", "Equipment siting and protection", "N", "Cloud; inherited", "Not applicable"],
         ["A.7.9", "Security of assets off-premises", "Y", "Laptop encryption; MDM", "Planned"],
         ["A.7.10", "Storage media", "Y", "Backup media handling; secure destruction", "Planned"],
         ["A.7.11", "Supporting utilities", "N", "Cloud; inherited", "Not applicable"],
         ["A.7.12", "Cabling security", "N", "Cloud; inherited", "Not applicable"],
         ["A.7.13", "Equipment maintenance", "N", "Cloud; inherited", "Not applicable"],
         ["A.7.14", "Secure disposal or re-use of equipment", "N", "Cloud; provider DPA disposal terms", "Planned"],
         ["A.8.1", "User endpoint devices", "Y", "Endpoint/MDM policy", "Planned"],
         ["A.8.2", "Privileged access rights", "Y", "Access Control Policy; audit log", "Planned"],
         ["A.8.3", "Information access restriction", "Y", "RBAC; least privilege", "Planned"],
         ["A.8.4", "Access to source code", "Y", "Repo permissions; CI/CD controls", "Planned"],
         ["A.8.5", "Secure authentication", "Y", "MFA; OTP flow; Roadmap 1.1", "Planned"],
         ["A.8.6", "Capacity management", "Y", "Server Requirements sheet; PgBouncer tuning", "Planned"],
         ["A.8.7", "Protection against malware", "Y", "Endpoint protection; image scanning", "Planned"],
         ["A.8.8", "Management of technical vulnerabilities", "Y", "Vulnerability register; ASV scans", "Planned"],
         ["A.8.9", "Configuration management", "Y", "IaC; config review", "Planned"],
         ["A.8.10", "Information deletion", "Y", "Retention Schedule; purge worker", "Planned"],
         ["A.8.11", "Data masking", "Y", "Masking for support access", "Planned"],
         ["A.8.12", "Prevention of data leakage", "Y", "DLP controls; DPA restrictions", "Planned"],
         ["A.8.13", "Information backup", "Y", "Backup & Restore Runbook", "Planned"],
         ["A.8.14", "Redundancy of information processing facilities", "Y", "Multi-AZ; HA sizing", "Planned"],
         ["A.8.15", "Logging", "Y", "Structured JSON logs; audit register", "Planned"],
         ["A.8.16", "Monitoring activities", "Y", "Prometheus/Grafana; SIEM", "Planned"],
         ["A.8.17", "Clock synchronization", "Y", "NTP alignment", "Planned"],
         ["A.8.18", "Use of privileged utility programs", "Y", "Restricted admin tooling", "Planned"],
         ["A.8.19", "Installation of software on operational systems", "Y", "Change Management Procedure", "Planned"],
         ["A.8.20", "Networks security", "Y", "VPC/security groups/WAF", "Planned"],
         ["A.8.21", "Security of network services", "Y", "Managed network services assessment", "Planned"],
         ["A.8.22", "Segregation of networks", "Y", "VPC segmentation; public/private subnets", "Planned"],
         ["A.8.23", "Web filtering", "Y", "Network policy", "Planned"],
         ["A.8.24", "Use of cryptography", "Y", "Crypto Policy", "Planned"],
         ["A.8.25", "Secure development life cycle", "Y", "CI/CD gating; code review", "Planned"],
         ["A.8.26", "Application security requirements", "Y", "Threat modelling; OWASP", "Planned"],
         ["A.8.27", "Secure system architecture and engineering principles", "Y", "Clean/hexagonal architecture; security by design", "Planned"],
         ["A.8.28", "Secure coding", "Y", "Linters; SAST; code review", "Planned"],
         ["A.8.29", "Security testing in development and acceptance", "Y", "DAST/SAST; staging testing", "Planned"],
         ["A.8.30", "Outsourced development", "Y", "Vendor policy; third-party code review", "Planned"],
         ["A.8.31", "Separation of development, test and production environments", "Y", "Staging mirrors production; separate envs", "Planned"],
         ["A.8.32", "Change management", "Y", "Change Management Procedure", "Planned"],
         ["A.8.33", "Test information", "Y", "No real PII in tests", "Planned"],
         ["A.8.34", "Protection of information systems during audit testing", "Y", "Audit access controls", "Planned"]]))),
    ("4. References", P("ISO/IEC 27001:2022 Annex A; InfoSec Risk Assessment & Treatment Plan; Roadmap item 4.5.")),
]))

DOCS.append(("18_Institutional_ML_FT_Risk_Assessment", "INSTITUTIONAL ML/FT RISK ASSESSMENT", "Compliance (CCO)",
             "Board of Directors", "Every 12-18 months or on material change", "CBN AML/CFT Regulation 2009", "5.3", [
    ("1. Purpose", P("Assess the inherent and residual money-laundering / terrorism-financing risk of Xego across country, customer, product, service and channel dimensions, in line with CBN risk-based supervision.")),
    ("2. Methodology", P("Each risk factor is rated High / Medium / Low for inherent risk, mitigation is identified, and residual risk is determined. The Board approves the assessment; refreshes occur at least every 12-18 months and on material business change.")),
    ("3. Assessment Matrix", P(*table(
        ["Risk Factor", "Inherent Risk", "Rationale", "Mitigating Controls", "Residual Risk"],
        [["Country risk", "Low-Medium", "Nigeria operations; FATF/IFI reporting country", "CBN-compliant program, sanctions screening, STR reporting", "Low-Medium"],
         ["Customer base", "Medium", "Retail/P2P via WhatsApp, high volume, limited physical presence", "KYC L0-L4 ladder, ID verification provider, risk scoring", "Medium"],
         ["Products - transfers/P2P", "Medium", "Rapid fund movement, cross-border potential", "Transaction monitoring rules engine, limits, EDD on high risk", "Medium"],
         ["Products - thrift/data-order", "Low", "Controlled member flows", "CDD, monitoring", "Low"],
         ["Delivery channel - mobile/messaging", "Medium", "Remote onboarding, impersonation risk", "Verified identity, device/session security, OTP, rate limiting", "Medium"],
         ["Cash in/out via agents", "Not offered", "No cash handling", "N/A", "Low"],
         ["Virtual currencies", "Not offered", "No crypto", "N/A", "Low"]]))),
    ("4. Outputs and Use", P("The assessment sets the institution's ML/FT risk profile, drives the customer risk-scoring methodology and EDD thresholds, and informs the Compliance Monitoring Plan.")),
    ("5. References", P("CBN AML/CFT Regulation 2009; Customer Risk Scoring Methodology; Roadmap items 0.7, 5.3.")),
]))

DOCS.append(("19_Customer_Risk_Scoring_Methodology", "CUSTOMER RISK SCORING METHODOLOGY", "Compliance (CCO)",
             "CCO / Board", "Annually", "CBN AML/CFT CDD/EDD", "2.4", [
    ("1. Purpose", P("Define how customers are scored and tiered (L0-L4) so that CDD/EDD intensity and limits are proportionate to risk.")),
    ("2. KYC Ladder", P(*table(
        ["Tier", "Verification Level", "Example Limits", "Risk"],
        [["L0", "Phone + basic details only", "No wallet / receive only", "Low"],
         ["L1", "NIN/BVN verification", "Low send limits", "Low"],
         ["L2", "Government ID + selfie (provider verification)", "Standard limits", "Medium"],
         ["L3", "Full CDD: ID, address, occupation, purpose", "Higher limits", "Medium-High"],
         ["L4", "EDD: source of funds, senior mgmt approval (PEP/high risk)", "Highest / approved limits", "High"]]))),
    ("3. Risk Factors and Weights", P(*table(
        ["Factor", "Indicator", "Points"],
        [["Geography", "High-risk / sanctioned-adjacent jurisdiction", "+3"],
         ["Occupation / sector", "Cash-intensive, gambling, crypto-adjacent", "+2"],
         ["Product usage", "Rapid pass-through, structuring-like patterns", "+2"],
         ["PEP status", "PEP or related", "+3 (auto EDD)"],
         ["Transaction behaviour", "Unusual frequency/value vs profile", "+2"],
         ["Negative news / screening hit", "Sanctions or adverse media", "+3"]]))),
    ("4. Decision Rules", P(*bullets([
        "Score 0-2: Low (standard CDD, standard monitoring).",
        "Score 3-5: Medium (standard CDD + periodic review).",
        "Score 6+: High (EDD per EDD Procedure; senior management approval required).",
        "Any sanctions match: refuse/freeze and report per Screening Procedure.",
    ]))),
    ("5. Review Triggers", P("Re-score on material changes (new products, location change, adverse events, monitoring alerts, threshold crossings) and on periodic rescreening.")),
    ("6. References", P("CBN AML/CFT Regulation 2009; CDD and EDD Procedures; Roadmap items 2.4, 5.2.")),
]))

DOCS.append(("20_CDD_Procedure", "CUSTOMER DUE DILIGENCE (CDD) PROCEDURE", "Compliance",
             "CCO", "Annually", "CBN AML/CFT Regulation 2009", "2.5", [
    ("1. Purpose", P("Standard operating procedure for identifying and verifying customers before account/transaction service, meeting CIP and CDD requirements.")),
    ("2. Process", P(*bullets([
        "1. Capture: collect legal name, phone, DOB, address, occupation and stated purpose of account.",
        "2. Verify identity: submit to a licensed Nigerian identity verification provider (e.g., Smile ID, Youverify) using government-issued ID + selfie; NIN/BVN checks where applicable.",
        "3. Verify address / contact: via utility-style proof or confirmed device/phone binding (OTP).",
        "4. Screen: sanctions and adverse media screening (see Sanctions / Screening Procedure).",
        "5. Tier and score: assign L0-L4 tier and risk score per Customer Risk Scoring Methodology.",
        "6. Record: store verification responses, decisions and evidence with the customer record (customer_verifications / kyc_profile tables - Roadmap 1.9).",
        "7. No service before completion: unverified customers cannot hold a wallet or transact above L0.",
    ]))),
    ("3. CDD Record Contents", P("Verification provider reference, ID type/number, verification result and date, screening result, tier, risk score, approver, and basis for any refusal. Records retained 7 years per the Retention Schedule.")),
    ("4. Re-verification", P("Re-verify at periodic intervals (per risk tier), on significant transaction-profile changes, or when verification data expires.")),
    ("5. References", P("CBN AML/CFT Regulation 2009 (CIP/CDD); Customer Risk Scoring Methodology; Sanctions/Screening Procedure; Roadmap items 2.1, 2.2, 2.5.")),
]))

DOCS.append(("21_EDD_Procedure", "ENHANCED DUE DILIGENCE (EDD) PROCEDURE", "Compliance",
             "CCO", "Annually", "CBN AML/CFT Regulation 2009", "2.6", [
    ("1. Purpose", P("Defines additional due diligence applied to higher-risk customers and relationships.")),
    ("2. Triggers", P(*bullets([
        "PEP or PEP-related customer (auto-EDD).",
        "Customer risk score >= 6 (High).",
        "Adverse sanctions or negative-news match not resolved as false positive.",
        "High-risk geography, complex or unusually large transactions, unexplained fund flows.",
        "Any relationship subject to a CBN/NFIU directive.",
    ]))),
    ("3. Enhanced Measures", P(*bullets([
        "Senior management (CCO or delegate) approval before the relationship or a significant transaction proceeds.",
        "Establish source of funds / source of wealth with documentation.",
        "Enhanced ongoing monitoring (all transactions reviewed, not just alerts).",
        "More frequent periodic review (e.g., every 6 months).",
        "Additional verification of beneficial ownership for legal persons.",
    ]))),
    ("4. Record", P("An EDD file is maintained per customer with approval, documents, rationale, monitoring findings and review dates; retained 7 years.")),
    ("5. Refusal / Exit", P("Where EDD cannot be completed satisfactorily, the relationship is refused or exited and consideration given to an STR per the STR workflow.")),
    ("6. References", P("CBN AML/CFT Regulation 2009; CDD Procedure; Customer Risk Scoring Methodology; Roadmap items 2.6, 2.8.")),
]))

DOCS.append(("22_Sanctions_Screening_Procedure", "SANCTIONS / SCREENING PROCEDURE", "Compliance",
             "CCO", "Annually", "OFAC/UN/EU lists; CBN AML/CFT Regulation 2009", "2.3", [
    ("1. Purpose", P("Define how Xego screens customers and beneficial owners against sanctions and watchlists, and how matches are handled and reported.")),
    ("2. List Sources", P("OFAC SDN list, UN consolidated list, EU consolidated list, and CBN-referenced lists. Screening uses the integrated provider (Roadmap 2.3) and is refreshed when lists update.")),
    ("3. Screening Points", P(*bullets([
        "At onboarding (before service).",
        "On beneficial-owner identification.",
        "Periodic rescreen of the full base (see Roadmap 5.2).",
        "On significant name/data changes.",
    ]))),
    ("4. Match Handling", P(*bullets([
        "Exact match: freeze/refuse service immediately; report to CBN/NFIU as applicable; record.",
        "Partial/fuzzy match: analyst review against list details (DOB, address, ID); resolve as hit or false positive.",
        "False positive: documented rationale; retain record.",
        "Confirmed hit: freeze funds/accounts, file report, notify CCO and AML/CFT Oversight Committee.",
    ]))),
    ("5. Record Keeping", P("All screening events (matched party, list, action taken) are logged; records retained 7 years.")),
    ("6. References", P("OFAC/UN/EU guidance; CBN AML/CFT Regulation 2009; Roadmap items 2.3, 5.2.")),
]))

DOCS.append(("23_STR_CTR_Workflow_Procedure", "STR / CTR WORKFLOW PROCEDURE", "Compliance",
             "CCO", "Annually", "Money Laundering (Prohibition) Act 2004", "2.8", [
    ("1. Purpose", P("Defines end-to-end handling of suspicious and currency transactions, from detection to statutory filing.")),
    ("2. Definitions", P(*bullets([
        "STR: Suspicious Transaction Report filed to NFIU (and informed to CBN) within 24 hours of detection of a suspicious transaction.",
        "CTR: Currency Transaction Report filed at/above the prescribed threshold (aggregate daily amounts also count).",
        "Tipping-off: disclosing an STR/CTR or investigation to the subject is prohibited.",
    ]))),
    ("3. STR Workflow", P(*bullets([
        "1. Detection: monitoring rules engine or staff observation generates an alert (Roadmap 2.7).",
        "2. Triage: analyst reviews transaction history, customer profile and CDD file.",
        "3. Investigation: gather evidence, document rationale, escalate to CCO.",
        "4. Decision: file STR or close with rationale.",
        "5. Filing: submit to NFIU within 24 hours of detection; inform CBN; record in the Suspicious Transaction Register.",
        "6. Review: notify the AML/CFT Oversight Committee of material cases.",
    ]))),
    ("4. CTR Workflow", P(*bullets([
        "1. Threshold event detected (single or aggregate daily).",
        "2. Verify customer and transaction details.",
        "3. File CTR; record in the Currency Transaction Register.",
    ]))),
    ("5. Records", P("Registers and filed returns retained 7 years; report generation is automated via Roadmap 1.15.")),
    ("6. References", P("MLPA 2004; Suspicious Transaction Register; Currency Transaction Register; Roadmap items 2.7, 2.8, 5.1.")),
]))

DOCS.append(("24_Suspicious_Transaction_Register", "SUSPICIOUS TRANSACTION REGISTER", "Compliance",
             "CCO", "Continuous", "MLPA 2004", "5.1", [
    ("1. Purpose", P("Chronological register of every suspicious transaction identified and STR filed.")),
    ("2. Register", P(*table(
        ["Register ID", "Date Opened", "Customer ID", "Trigger / Alerts", "Description of Suspicion", "Decision", "STR Filed (Y/N)", "STR Reference", "Date Filed", "Case Officer", "Status"],
        [["STR-0001", "", "CUST-", "Rule 7: rapid pass-through", "Multiple same-day inbound/outbound transfers with no economic purpose", "File STR", "Y", "", "", "", "In progress"],
         ["STR-0002", "", "CUST-", "Manual referral", "", "Under investigation", "", "", "", "", "Open"]]))),
    ("3. Maintenance", P("Entries are made within 24 hours of detection; the register is reviewed by the CCO and reported to the AML/CFT Oversight Committee quarterly. Retained 7 years.")),
    ("4. References", P("STR/CTR Workflow Procedure; MLPA 2004; Roadmap item 5.1.")),
]))

DOCS.append(("25_Currency_Transaction_Register", "CURRENCY TRANSACTION REGISTER", "Compliance",
             "CCO", "Continuous", "MLPA 2004", "5.1", [
    ("1. Purpose", P("Register of currency transactions at or above the prescribed threshold (including daily aggregates).")),
    ("2. Register", P(*table(
        ["Register ID", "Date", "Customer ID", "Transaction Reference", "Amount", "Currency", "Source", "Destination", "CTR Filed (Y/N)", "CTR Reference", "Officer", "Status"],
        [["CTR-0001", "", "CUST-", "", "", "NGN", "", "", "", "", "", "Open"],
         ["CTR-0002", "", "CUST-", "", "", "NGN", "", "", "", "", "", "Open"]]))),
    ("3. Maintenance", P("Completed at threshold detection, aggregated daily; reviewed monthly by the CCO. Retained 7 years.")),
    ("4. References", P("STR/CTR Workflow Procedure; MLPA 2004; Roadmap item 5.1.")),
]))

DOCS.append(("26_Data_Inventory_Residency_Register", "DATA INVENTORY / RESIDENCY REGISTER", "Compliance / Security",
             "DPO / CISO", "Quarterly", "NDPR 2019; ISO/IEC 27001:2022 Annex A 5.34", "0.9", [
    ("1. Purpose", P("Inventory of the personal and financial data Xego processes, where it resides, its legal basis, retention, processors and owners - the basis for NDPR compliance and data minimisation.")),
    ("2. Register", P(*table(
        ["Data Asset", "Description", "PII?", "Storage Location", "Legal Basis", "Retention", "Processors", "DPA Ref", "Owner"],
        [["Customers", "Profile, contact, tier, risk score", "Yes", "PostgreSQL (Phase 1 VPC)", "Contract / legal obligation (KYC)", "Per Retention Schedule", "-", "-", "Compliance"],
         ["KYC / verifications", "Provider results, ID data, selfie refs", "Yes", "PostgreSQL + provider stores", "Legal obligation (CBN)", "7 years", "Smile ID / Youverify", "", "Compliance"],
         ["Chat / messages", "Conversation content", "Yes", "PostgreSQL", "Contract (service provision)", "90 days", "-", "-", "Ops"],
         ["Ledger / payment events", "Transactions, invoices, thrift", "Yes", "PostgreSQL", "Contract / legal obligation", "7 years", "-", "-", "Ops"],
         ["Sessions / OTP", "Session tokens, OTP hashes", "Yes", "PostgreSQL", "Contract / security", "24 hours", "-", "-", "Engineering"],
         ["Audit logs", "Privileged actions, KYC decisions", "Yes", "PostgreSQL / log store", "Legal obligation / legitimate interest", "2 years+", "Observability provider", "", "Security"],
         ["Webhook events", "Inbound/outbound event payloads", "Yes", "PostgreSQL / log store", "Contract", "90 days", "Observability provider", "", "Engineering"]]))),
    ("3. Use", P("Drives the Retention Schedule, encryption scope (PII fields), DPA coverage and NDPR breach assessment. Reviewed quarterly and on any new processing.")),
    ("4. References", P("NDPR 2019; Retention & Destruction Schedule; Privacy & Data Protection Policy; Roadmap item 0.9.")),
]))

DOCS.append(("27_Asset_Inventory", "ASSET INVENTORY", "Security",
             "CISO", "Quarterly", "ISO/IEC 27001:2022 Annex A 5.9", "0.8", [
    ("1. Purpose", P("Inventory of information assets owned or operated by Xego, with ownership and criticality, satisfying Annex A 5.9.")),
    ("2. Inventory", P(*table(
        ["Asset ID", "Type", "Name", "Owner", "Criticality", "Location", "Backed Up?", "Status"],
        [["A-001", "Service", "Core API (internal/app)", "Engineering", "Critical", "Cloud VPC (Phase 1)", "N/A (stateless)", "In production"],
         ["A-002", "Service", "Worker (purge, reconciliation)", "Engineering", "High", "Cloud VPC", "N/A", "In production"],
         ["A-003", "Database", "PostgreSQL 17 (primary + PITR)", "Ops", "Critical", "Cloud VPC / object store", "Yes (WAL)", "In production"],
         ["A-004", "Secrets", "Vault / cloud KMS", "Security", "Critical", "Vault / KMS", "Yes", "In production"],
         ["A-005", "Repository", "whatsapp-payment-demo", "Engineering", "High", "Git host", "Yes (git)", "In production"],
         ["A-006", "Data", "Customer PII tables", "Compliance", "Critical", "PostgreSQL", "Yes", "In production"],
         ["A-007", "Pipeline", "CI/CD (GitHub Actions)", "Engineering", "High", "SaaS", "N/A", "In production"]]))),
    ("3. Maintenance", P("Updated on commissioning/decommissioning; reconciled with cloud account resources quarterly; each asset has an accountable owner.")),
    ("4. References", P("ISO A 5.9; Data Inventory / Residency Register; Roadmap item 0.8.")),
]))

DOCS.append(("28_Supplier_DPA_Register", "SUPPLIER / DPA REGISTER", "Procurement / Compliance",
             "Procurement / DPO", "Quarterly", "ISO/IEC 27001:2022 Annex A 5.19; NDPR 2019", "0.9", [
    ("1. Purpose", P("Register of third parties with data access or service criticality, tracking due diligence, DPAs and review status.")),
    ("2. Register", P(*table(
        ["Supplier", "Service", "Tier", "DPA Signed (Y/N)", "DPA Ref / Date", "Data Accessed", "Risk Class", "Review Due", "Status"],
        [["Identity provider (Smile ID / Youverify)", "KYC verification", "Critical", "", "", "ID data, selfie, NIN/BVN", "High", "", "To onboard"],
         ["Cloud provider", "Hosting, DB, object storage", "Critical", "", "", "All production data", "High", "", "To onboard"],
         ["Observability / SIEM", "Logs, metrics, alerts", "Major", "", "", "Logs, events", "Medium", "", "To onboard"],
         ["Email / support", "Customer support", "Major", "", "", "Customer queries", "Medium", "", "To onboard"],
         ["Messaging API (WhatsApp Business)", "Messaging channel", "Critical", "", "", "Chat content", "High", "", "To onboard"]]))),
    ("3. Maintenance", P("Critical suppliers reviewed quarterly; major annually. DPA renewals tracked; sub-processors approved per DPA. Offboarding evidence recorded.")),
    ("4. References", P("Vendor / Third-Party Risk Policy; Data Inventory / Residency Register; Roadmap item 0.9.")),
]))

DOCS.append(("29_Privileged_Access_Audit_Log_Register", "PRIVILEGED ACCESS / AUDIT LOG REGISTER", "Security",
             "CISO", "Monthly review", "ISO/IEC 27001:2022 Annex A 8.15", "1.4", [
    ("1. Purpose", P("Defines which privileged actions are logged and provides the review register proving accountability (Roadmap 1.4).")),
    ("2. Events Logged", P(*bullets([
        "Admin and merchant login/logout (success and failure).",
        "KYC verification decisions (approve/reject, reviewer).",
        "Permission/role changes (grant/revoke).",
        "Payment and ledger events (create, cancel, reverse).",
        "Sensitive data access and export.",
        "Configuration, secrets and infra changes.",
    ]))),
    ("3. Logging Requirements", P("Structured JSON logs with actor, action, object, timestamp, outcome and IP; correlation IDs across services; retained per Retention Schedule; centralised in SIEM (Roadmap 1.13).")),
    ("4. Review Register", P(*table(
        ["Event ID", "Timestamp", "Actor", "Action", "Object", "IP", "Outcome", "Source Table", "Reviewed"],
        [["EV-0001", "", "admin@xego", "login", "admin console", "", "success", "admin sessions", "Y"],
         ["EV-0002", "", "compliance@xego", "kyc_decision", "customer_verifications", "", "approved", "kyc events", "Y"],
         ["EV-0003", "", "svc-payments", "payment_created", "ledger", "", "success", "payment_events", "N"]]))),
    ("5. References", P("ISO A 8.15; Access Control Policy; Roadmap items 1.4, 1.5, 1.13.")),
]))

DOCS.append(("30_Joiner_Mover_Leaver_Register", "JOINER / MOVER / LEAVER (JML) REGISTER", "HR / Security",
             "HR", "Continuous", "ISO/IEC 27001:2022 Annex A 8.2", "5.5", [
    ("1. Purpose", P("Evidence that access is provisioned, changed and revoked in line with the Access Control Policy.")),
    ("2. Register", P(*table(
        ["Employee / Contractor", "Action", "Date", "Role / Entitlements Granted", "Access Requested", "Access Revoked (Date)", "Account Disabled (Date)", "Manager Sign-off", "HR / IT Sign-off", "Status"],
        [["", "Join", "", "", "", "", "", "", "", "Open"],
         ["", "Move", "", "", "", "", "", "", "", "Open"],
         ["", "Leave", "", "", "", "", "", "", "", "Open"]]))),
    ("3. Process Controls", P(*bullets([
        "Join: access only after screening, NDA and training complete.",
        "Move: entitlements realigned; old access removed same day.",
        "Leave: accounts disabled on the day of departure; assets returned; audit of residual access.",
    ]))),
    ("4. References", P("Access Control Policy; NDA Register; Roadmap items 5.5, 0.12.")),
]))

DOCS.append(("31_Training_Records_Competence_Matrix", "TRAINING RECORDS / COMPETENCE MATRIX", "HR / Compliance",
             "HR / CCO", "Annual (documented)", "ISO/IEC 27001:2022 Clauses 7.2-7.3; CBN AML/CFT", "0.11", [
    ("1. Purpose", P("Defines the competence and training required for each role and records completion, satisfying ISO 7.2/7.3 and CBN AML/CFT training requirements.")),
    ("2. Competence Matrix", P(*table(
        ["Role", "Required Competence", "Training Required", "Frequency", "Assessment Method"],
        [["All staff", "AML/CFT awareness, security awareness, incident reporting", "AML/CFT + security awareness", "On hire, then annually", "Test / quiz"],
         ["Compliance team", "CDD/EDD, STR/CTR, screening, PEP", "Advanced AML/CFT", "On hire, then annually", "Test + practical case"],
         ["Security / engineering", "Secure SDLC, incident response, access control", "Secure coding, IR, ISO 27001", "On hire, then annually", "Certification / drill"],
         ["Operations", "Change management, backup/restore, DR", "Change + DR training", "On hire, then annually", "Drill participation"],
         ["Executives / Board", "Governance, risk, AML oversight", "Governance & risk", "Annually", "Attendance"]]))),
    ("3. Training Log", P(*table(
        ["Date", "Course", "Attendees", "Format", "Assessment / Test", "Result", "Records Location"],
        [["", "", "", "", "", "", ""],
         ["", "", "", "", "", "", ""]]))),
    ("4. Records", P("Attendance, materials and results are kept by HR; AML/CFT training records are available to regulators on request (CBN).")),
    ("5. References", P("ISO 27001:2022 Clauses 7.2-7.3; CBN AML/CFT Regulation 2009 (training); Roadmap items 0.11, 3.3, 5.4.")),
]))

DOCS.append(("32_NDA_Register", "CONFIDENTIALITY / NDA REGISTER", "HR",
             "HR", "On hire and on change", "ISO/IEC 27001:2022 Annex A 6.6", "0.12", [
    ("1. Purpose", P("Evidence that all personnel and contractors are bound by confidentiality obligations before receiving access (Annex A 6.6).")),
    ("2. Register", P(*table(
        ["Person", "Role", "Date Signed", "NDA Version", "Copy Location", "Contractor?", "Termination Date", "Status"],
        [["", "", "", "0.1", "HR file", "No", "", "Active"],
         ["", "", "", "0.1", "HR file", "Yes", "", "Active"]]))),
    ("3. Process", P("NDA signed before system access is provisioned (JML gate); records retained for the duration of employment/engagement plus the retention period.")),
    ("4. References", P("ISO A 6.6; JML Register; Roadmap item 0.12.")),
]))

DOCS.append(("33_STR_Submissions", "SUSPICIOUS TRANSACTION REPORT (STR) SUBMISSIONS", "Compliance",
             "CCO", "Within 24h of detection", "MLPA 2004", "5.1", [
    ("1. Purpose", P("Template and control sheet for STR filings to NFIU (informed to CBN), which must occur within 24 hours of detecting a suspicious transaction.")),
    ("2. Filing Requirements", P(*bullets([
        "File via the NFIU designated channel; inform the CBN in writing.",
        "Include: customer details, transaction(s), amount, basis for suspicion, supporting references.",
        "Do not tip off the customer.",
        "Record the filing in the Suspicious Transaction Register.",
    ]))),
    ("3. Submission Log", P(*table(
        ["STR Ref", "Date Detected", "Date Filed", "Customer ID", "Transaction Details", "Amount", "Basis of Suspicion", "Status", "Regulator Ref"],
        [["STR-", "", "", "CUST-", "", "", "", "Filed", ""],
         ["STR-", "", "", "CUST-", "", "", "", "In progress", ""]]))),
    ("4. References", P("MLPA 2004; STR/CTR Workflow Procedure; Suspicious Transaction Register; Roadmap item 5.1.")),
]))

DOCS.append(("34_CTR_Submissions", "CURRENCY TRANSACTION REPORT (CTR) SUBMISSIONS", "Compliance",
             "CCO", "At/above threshold (aggregate daily too)", "MLPA 2004", "5.1", [
    ("1. Purpose", P("Template and control sheet for CTR filings at/above the prescribed currency transaction threshold.")),
    ("2. Filing Requirements", P(*bullets([
        "Capture single transactions at/above threshold and daily aggregates that cross it.",
        "File via the designated channel; record in the Currency Transaction Register.",
        "Retain supporting records for 7 years.",
    ]))),
    ("3. Submission Log", P(*table(
        ["CTR Ref", "Transaction Date", "Filed Date", "Customer ID", "Amount", "Currency", "Threshold Basis", "Status", "Regulator Ref"],
        [["CTR-", "", "", "CUST-", "", "NGN", "", "Filed", ""],
         ["CTR-", "", "", "CUST-", "", "NGN", "", "In progress", ""]]))),
    ("4. References", P("MLPA 2004; STR/CTR Workflow Procedure; Currency Transaction Register; Roadmap item 5.1.")),
]))

DOCS.append(("35_PEP_Transaction_Returns", "PEP TRANSACTION RETURNS", "Compliance",
             "CCO", "Monthly", "CBN AML/CFT Regulation 2009 (B1402-B1403)", "5.1", [
    ("1. Purpose", P("Monthly returns to the CBN of all transactions involving Politically Exposed Persons.")),
    ("2. Requirements", P(*bullets([
        "Identify PEPs via screening and EDD flagging.",
        "Transactions with PEPs require senior management approval (see EDD Procedure).",
        "Compile and submit the monthly return to the CBN; retain 7 years.",
    ]))),
    ("3. Return Log", P(*table(
        ["Return Month", "PEP Name", "PEP Relationship", "Country", "Transaction Count", "Total Amount", "Senior Mgmt Approval", "Filed Date", "CBN Acknowledgement"],
        [["2026-", "", "", "", "", "", "", "", ""],
         ["2026-", "", "", "", "", "", "", "", ""]]))),
    ("4. References", P("CBN AML/CFT Regulation 2009 (B1402-B1403); EDD Procedure; Roadmap item 5.1.")),
]))

DOCS.append(("36_Management_Review_Minutes", "MANAGEMENT REVIEW MINUTES", "Board / Top Management",
             "Board", "Planned intervals (e.g., annually)", "ISO/IEC 27001:2022 Clause 9.3", "4.4", [
    ("1. Purpose", P("Template for the ISMS management review that evaluates suitability, adequacy and effectiveness (ISO 9.3).")),
    ("2. Agenda / Inputs (ISO 9.3.2)", P(*bullets([
        "Status of actions from previous reviews.",
        "Changes in external/internal issues and interested-party expectations.",
        "Feedback on ISMS performance: internal audits, compliance, incidents, monitoring metrics, risk assessment status.",
        "Nonconformities and corrective actions.",
        "Opportunities for improvement and resourcing needs.",
    ]))),
    ("3. Minutes", P(*table(
        ["Item", "Discussion / Evidence", "Decision", "Action Owner", "Due Date", "Status"],
        [["Previous actions", "", "", "", "", "Open"],
         ["Audit results", "", "", "", "", "Open"],
         ["Incidents & corrective actions", "", "", "", "", "Open"],
         ["Risk assessment update", "", "", "", "", "Open"],
         ["Objectives / resources", "", "", "", "", "Open"],
         ["Management decision", "", "", "", "", "Open"]]))),
    ("4. Sign-off", P("Reviewed and approved by the Board / Top Management; minutes retained as certification evidence.")),
    ("5. References", P("ISO 27001:2022 Clause 9.3; Internal Audit Reports; InfoSec Metrics; Roadmap item 4.4.")),
]))

DOCS.append(("37_Internal_Audit_Reports", "INTERNAL AUDIT REPORTS", "Audit",
             "Audit / Board", "Per audit cycle", "ISO/IEC 27001:2022 Clause 9.2; CBN AML/CFT independent testing", "4.2", [
    ("1. Purpose", P("Template for internal audit and independent testing of the ISMS and AML/CFT program (ISO 9.2 and CBN 12-18 month independent testing).")),
    ("2. Audit Plan", P(*table(
        ["Area", "Scope", "Auditor", "Date", "Method", "Status"],
        [["ISMS governance", "Policy, scope, roles, SoA", "", "", "Document review", "Planned"],
         ["Access control", "RBAC, JML, privileged accounts", "", "", "Sampling + interviews", "Planned"],
         ["KYC/AML", "CDD/EDD, screening, STR/CTR", "", "", "File sampling", "Planned"],
         ["Operations", "Change mgmt, backups, DR", "", "", "Walkthrough + evidence", "Planned"],
         ["Vendor management", "Suppliers, DPAs", "", "", "Register review", "Planned"]]))),
    ("3. Findings", P(*table(
        ["Finding ID", "Severity", "Description", "Evidence", "Recommendation", "Owner", "Due Date", "Status"],
        [["F-001", "High", "", "", "", "", "", "Open"],
         ["F-002", "Medium", "", "", "", "", "", "Open"],
         ["F-003", "Low", "", "", "", "", "", "Open"]]))),
    ("4. Report Structure", P("Executive summary; audit objective/scope; methodology; findings by severity; management responses; corrective action plan; conclusion. Reports go to the Board/committee; follow-up confirms closure.")),
    ("5. References", P("ISO 27001:2022 Clause 9.2; CBN AML/CFT independent testing; Roadmap items 4.1, 4.2.")),
]))

DOCS.append(("38_Vulnerability_PenTest_Findings_Register", "VULNERABILITY / PEN-TEST FINDINGS REGISTER", "Security",
             "CISO", "Quarterly (scans) / Annual (pentest)", "PCI DSS Req 11; ISO/IEC 27001:2022 Annex A 8.8", "3.2", [
    ("1. Purpose", P("Tracks findings from quarterly ASV vulnerability scans, annual penetration tests and internal scans, with remediation.")),
    ("2. Scan Schedule", P("Quarterly external ASV scans (PCI Req 11); annual penetration test; monthly internal scans; scans on significant changes.")),
    ("3. Register", P(*table(
        ["Finding ID", "Source", "Date", "Severity (CVSS)", "Asset", "Description", "Remedy", "Owner", "Due Date", "Status"],
        [["V-0001", "ASV Q1", "", "High (7.5)", "", "", "", "", "", "Open"],
         ["V-0002", "Pentest 2026", "", "Critical (9.0)", "", "", "", "", "", "Open"],
         ["V-0003", "Internal scan", "", "Medium (5.0)", "", "", "", "", "", "Open"]]))),
    ("4. Remediation Policy", P("Critical within 72h; High within 30 days; Medium within 90 days; Low reviewed next cycle. Re-scan confirms closure; evidence retained for PCI AOC.")),
    ("5. References", P("PCI DSS Req 11; ISO A 8.8; Roadmap items 3.2, 3.9.")),
]))

DOCS.append(("39_DR_Restore_Test_Reports", "DISASTER RECOVERY RESTORE TEST REPORTS", "Ops / Risk",
             "Risk Committee", "Quarterly", "ISO/IEC 27001:2022 Annex A 5.29-5.30", "3.7", [
    ("1. Purpose", P("Evidence that recovery objectives are tested and met each quarter (ISO A 5.29/5.30).")),
    ("2. Test Log", P(*table(
        ["Test ID", "Date", "Scope", "RTO Achieved", "RPO Achieved", "Targets", "Result", "Issues", "Corrective Action", "Sign-off"],
        [["DR-2026-Q1", "", "DB PITR restore + app failover", "", "", "4h / 15min", "Pass/Fail", "", "", ""],
         ["DR-2026-Q2", "", "Full environment restore", "", "", "4h / 15min", "Pass/Fail", "", "", ""],
         ["DR-2026-Q3", "", "Backup integrity + restore", "", "", "4h / 15min", "Pass/Fail", "", "", ""],
         ["DR-2026-Q4", "", "Full failover drill", "", "", "4h / 15min", "Pass/Fail", "", "", ""]]))),
    ("3. Procedure", P("Each test: announce, restore from base backup + WAL replay, validate integrity and reconciliation, measure RTO/RPO, restore service, document results and corrective actions in this report and the Risk register.")),
    ("4. References", P("BCP/DR Plan; Backup & Restore Runbook; Roadmap items 1.12, 3.7.")),
]))

DOCS.append(("40_InfoSec_Metrics", "INFORMATION SECURITY METRICS (MONTHLY)", "Security",
             "CCO / Board", "Monthly", "ISO/IEC 27001:2022 Clause 9.1", "5.7", [
    ("1. Purpose", P("Monthly security KPIs to evaluate ISMS performance and drive improvement (ISO 9.1).")),
    ("2. Metrics", P(*table(
        ["Metric", "Definition", "Target", "Current", "Trend", "Owner"],
        [["Security incidents", "Count of incidents by severity", "<= 1 per month (L1: 0)", "", "", "Security"],
         ["Mean time to resolve (MTTR)", "Hours from detection to resolution", "<= 24h", "", "", "Security"],
         ["Privileged actions logged", "% of admin/KYC actions in audit log", "100%", "", "", "Security"],
         ["Critical vulnerabilities open", "Open critical/ASV findings", "0", "", "", "Security"],
         ["Access reviews completed", "% quarterly entitlement reviews done", "100%", "", "", "Security"],
         ["MFA coverage", "% of admin/merchant accounts with MFA", "100%", "", "", "Engineering"],
         ["Backup restore success", "Successful restore tests", ">= 1 per quarter", "", "", "Ops"],
         ["Training completion", "% staff completed annual training", "100%", "", "", "HR/Compliance"],
         ["Change management", "% changes with approval + backout", "100%", "", "", "Ops"]]))),
    ("3. Use", P("Metrics feed the management review and InfoSec dashboards (Grafana); breaches of target trigger corrective actions.")),
    ("4. References", P("ISO 27001:2022 Clause 9.1; Management Review Minutes; Roadmap items 5.7, 1.13.")),
]))

DOCS.append(("42_ISO27001_Audit_Evidence_Packs", "ISO 27001:2022 STAGE 1 & 2 AUDIT EVIDENCE PACKS", "CISO",
             "CISO / Board", "Before each audit cycle", "ISO/IEC 27001:2022", "4.5", [
    ("1. Purpose", P("Defines the evidence to prepare for ISO 27001:2022 Stage 1 (readiness) and Stage 2 (certification) audits.")),
    ("2. Stage 1 (Readiness) Content", P(*bullets([
        "ISMS scope document; Information Security Policy; risk assessment + treatment plan; Statement of Applicability.",
        "Management commitment records (policy approvals, management review).",
        "Documented procedures list (this document set).",
        "Internal audit schedule and first audit evidence.",
    ]))),
    ("3. Stage 2 (Certification) Content", P(*bullets([
        "Evidence for each applicable Annex A control (see SoA) - typically 3-6 months of records: audit logs, JML register, training records, incident and change logs, backup/drill reports, vulnerability register.",
        "Internal audit reports and management review minutes.",
        "Corrective action records.",
        "Observations of the live ISMS in operation.",
    ]))),
    ("4. Repository", P("Evidence is organised per SoA control in the compliance documents folder/DR, versioned and retained for the certification cycle.")),
    ("5. References", P("ISO 27001:2022; Statement of Applicability; Internal Audit Reports; Management Review Minutes; Roadmap item 4.5.")),
]))

DOCS.append(("43_CBN_AML_CFT_Examination_Evidence_Pack", "CBN AML/CFT EXAMINATION EVIDENCE PACK", "Compliance",
             "CCO", "Before each CBN examination", "CBN AML/CFT Regulation 2009", "4.6", [
    ("1. Purpose", P("Checklist of documents and records to present for a CBN AML/CFT examination.")),
    ("2. Contents", P(*bullets([
        "CCO appointment letter and AML/CFT Oversight Committee charter.",
        "Board-approved AML/CFT Policy and Program; institutional ML/FT risk assessment.",
        "CDD/EDD procedures and sample customer files (tiers L0-L4).",
        "Suspicious and Currency Transaction Registers; STR/CTR/PEP returns filed.",
        "Sanctions screening records and match handling.",
        "Independent testing / internal audit reports and remediation.",
        "Training records and competence matrix.",
        "Incident reports and any regulatory correspondence.",
        "Record keeping / retention evidence.",
    ]))),
    ("3. Repository", P("Packs are assembled per examination; the same evidence serves NFIU requests. All records retained 7 years.")),
    ("4. References", P("CBN AML/CFT Regulation 2009; MLPA 2004; Roadmap item 4.6.")),
]))

DOCS.append(("44_PCI_DSS_Attestation_of_Compliance", "PCI DSS ATTESTATION OF COMPLIANCE (AOC)", "Security / Finance",
             "CISO / CFO", "Annual", "PCI DSS Req 12", "3.9", [
    ("1. Purpose", P("Template for the annual PCI DSS Attestation of Compliance and the scope decision behind it.")),
    ("2. Scope Decision", P(*bullets([
        "If the platform never stores, processes or transmits cardholder data on its systems (cards not solicited in chat; Roadmap 2.10) and relies on a validated third party: complete SAQ-A.",
        "If transaction volume exceeds 1 million transactions per year or scope is broader: engage a Qualified Security Assessor (QSA) for a Report on Compliance (RoC) and Attestation.",
    ]))),
    ("3. AOC Contents", P(*table(
        ["Section", "Content"],
        [["Merchant / entity details", "Legal name, DBA, contact"],
         ["Validation type", "SAQ-A / RoC by QSA"],
         ["Scope", "Cardholder data environment, segmentation"],
         ["Requirement status 1-12", "Each requirement: compliant / not applicable"],
         ["Quarterly ASV scans", "Passing scan evidence (Req 11)"],
         ["Attestation", "Signatures, date, evidence of compensating controls"]]))),
    ("4. Record Keeping", P("AOC, ASV scan reports and evidence retained per PCI requirements; findings tracked in the Vulnerability Register.")),
    ("5. References", P("PCI DSS Requirements 1-12; Vulnerability / Pen-Test Findings Register; Roadmap items 3.9, 2.10.")),
]))


def main():
    n = 0
    for filename, title, owner, approved, review, basis, roadmap, sections in DOCS:
        text = build(title, owner, approved, review, basis, roadmap, sections)
        path = os.path.join(HERE, filename + ".md")
        with open(path, "w", encoding="utf-8") as f:
            f.write(text)
        n += 1
        print("wrote", filename + ".md")
    print("total templates:", n)


if __name__ == "__main__":
    main()
