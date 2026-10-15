#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Generates Compliance_Roadmap.xlsx - Xego Financial Institution Compliance Roadmap
covering ISO/IEC 27001:2022 and CBN AML/CFT Regulatory Framework (MLPA 2004 / CBN Reg 2009).

Sheets:
  1. Overview          - purpose, current state vs target, phase summary
  2. Roadmap           - full phased action plan (main sheet)
  3. Server Requirements - infrastructure + deployment specs per phase
  4. Reporting Matrix  - regulatory report obligations
  5. Department Responsibilities - ownership matrix
  6. Codebase Changes  - engineering changes with file references
  7. Regulatory Mapping - ISO 27001 + AML/CFT clauses mapped to actions
"""

import os
import sys

# Broken lxml C extension segfaults this Python when openpyxl probes it at import
# time. Force openpyxl's stdlib ElementTree fallback before importing openpyxl.
os.environ["OPENPYXL_LXML"] = "False"
os.environ["OPENPYXL_DEFUSEDXML"] = "False"
sys.modules.setdefault("lxml", None)

from openpyxl import Workbook
from openpyxl.styles import Font, PatternFill, Alignment, Border, Side
from openpyxl.utils import get_column_letter

# ----------------------------------------------------------------------------
# Style helpers
# ----------------------------------------------------------------------------
TITLE_FONT = Font(name="Calibri", size=16, bold=True, color="FFFFFF")
SUBTITLE_FONT = Font(name="Calibri", size=11, italic=True, color="FFFFFF")
HEADER_FONT = Font(name="Calibri", size=11, bold=True, color="FFFFFF")
BODY_FONT = Font(name="Calibri", size=10)
BODY_BOLD = Font(name="Calibri", size=10, bold=True)
SMALL_FONT = Font(name="Calibri", size=9)
NOTE_FONT = Font(name="Calibri", size=9, italic=True, color="595959")

DARK = PatternFill("solid", fgColor="1F3864")      # dark navy - title/header
NAVY = PatternFill("solid", fgColor="2E5E8C")       # header fill
GRAY = PatternFill("solid", fgColor="D9E2F3")       # section header
LIGHT = PatternFill("solid", fgColor="F2F6FB")      # zebra row

PHASE_FILLS = {
    "0": PatternFill("solid", fgColor="FCE4D6"),   # light orange
    "1": PatternFill("solid", fgColor="DDEBF7"),   # light blue
    "2": PatternFill("solid", fgColor="E2EFDA"),   # light green
    "3": PatternFill("solid", fgColor="FFF2CC"),   # light yellow
    "4": PatternFill("solid", fgColor="EAD1DC"),   # light pink
    "5": PatternFill("solid", fgColor="EDEDED"),   # light gray
}

P0_FILL = PatternFill("solid", fgColor="FFC7CE")   # red - urgent
P1_FILL = PatternFill("solid", fgColor="FFEB9C")   # yellow - important
P2_FILL = PatternFill("solid", fgColor="C6EFCE")   # green - normal

THIN = Side(style="thin", color="B0B0B0")
BORDER = Border(left=THIN, right=THIN, top=THIN, bottom=THIN)

WRAP = Alignment(wrap_text=True, vertical="top")
WRAP_CENTER = Alignment(wrap_text=True, vertical="center", horizontal="center")


def style_header(ws, row, ncols, fill=NAVY):
    for c in range(1, ncols + 1):
        cell = ws.cell(row=row, column=c)
        cell.font = HEADER_FONT
        cell.fill = fill
        cell.alignment = WRAP_CENTER
        cell.border = BORDER


def style_body(ws, r0, r1, ncols):
    for r in range(r0, r1 + 1):
        for c in range(1, ncols + 1):
            cell = ws.cell(row=r, column=c)
            cell.font = BODY_FONT
            cell.alignment = WRAP
            cell.border = BORDER
            if (r - r0) % 2 == 1:
                cell.fill = LIGHT


def set_widths(ws, widths):
    for i, w in enumerate(widths, start=1):
        ws.column_dimensions[get_column_letter(i)].width = w


def title_block(ws, title, subtitle, ncols):
    ws.merge_cells(start_row=1, start_column=1, end_row=1, end_column=ncols)
    c = ws.cell(row=1, column=1, value=title)
    c.font = TITLE_FONT
    c.fill = DARK
    c.alignment = Alignment(vertical="center", horizontal="left")
    ws.row_dimensions[1].height = 28
    ws.merge_cells(start_row=2, start_column=1, end_row=2, end_column=ncols)
    s = ws.cell(row=2, column=1, value=subtitle)
    s.font = SUBTITLE_FONT
    s.fill = DARK
    s.alignment = Alignment(vertical="center", horizontal="left")
    ws.row_dimensions[2].height = 20


wb = Workbook()

# ============================================================================
# SHEET 1: OVERVIEW
# ============================================================================
ws = wb.active
ws.title = "Overview"
N = 6
title_block(ws, "XEGO - FINANCIAL INSTITUTION COMPLIANCE ROADMAP",
            "ISO/IEC 27001:2022 (ISMS) + CBN AML/CFT Regulatory Framework (MLPA 2004 / CBN AML/CFT Regulation 2009) | Prepared: 2026",
            N)
set_widths(ws, [22, 60, 60, 34, 22, 22])

r = 4
ws.merge_cells(start_row=r, start_column=1, end_row=r, end_column=N)
c = ws.cell(row=r, column=1, value="1. PURPOSE AND SCOPE")
c.font = BODY_BOLD; c.fill = GRAY; c.alignment = WRAP
ws.row_dimensions[r].height = 18
r += 1
purpose = ("This roadmap defines the complete program to take the Xego WhatsApp/Telme payment platform from a "
           "functional demo to a compliant financial-institution-grade service. It covers governance, engineering "
           "(codebase), infrastructure & deployment, KYC/CDD/AML, reporting, audit, certification and every "
           "affected department. It is mapped 1:1 to ISO/IEC 27001:2022 clauses and Annex A controls, and to the "
           "CBN AML/CFT risk-based supervision requirements for financial institutions.")
ws.merge_cells(start_row=r, start_column=1, end_row=r, end_column=N)
c = ws.cell(row=r, column=1, value=purpose); c.alignment = WRAP; c.font = BODY_FONT
ws.row_dimensions[r].height = 58
r += 2

ws.merge_cells(start_row=r, start_column=1, end_row=r, end_column=N)
c = ws.cell(row=r, column=1, value="2. CURRENT STATE VS TARGET")
c.font = BODY_BOLD; c.fill = GRAY; c.alignment = WRAP
r += 1
ov_hdr = ["Capability", "Current State (Today)", "Required Target State", "Gap / Risk", "Priority", "Phase"]
for i, h in enumerate(ov_hdr, 1):
    ws.cell(row=r, column=i, value=h)
style_header(ws, r, N)
r += 1
overview_rows = [
    ["KYC / Identity Verification",
     "Email OTP only; KYC status hardcoded 'approved_simulated'; no NIN/BVN/document verification, no source of funds",
     "Real L0-L4 KYC ladder: account confirmed, email OTP, NIN/BVN verified, document + selfie, source of funds; manual review workflow",
     "Customers unverified; fraud, money-laundering and regulatory exposure", "P0", "Phase 2"],
    ["Sanctions / PEP Screening",
     "None",
     "Screen all customers at onboarding and periodically against OFAC/UN/CBN lists; PEP detection and monthly returns",
     "Facilitating sanctioned/PEP activity; CBN penalties", "P0", "Phase 2"],
    ["AML Transaction Monitoring",
     "None",
     "Rule engine: velocity, structuring, round amounts, high-risk counterparties; STR generation to NFIU within 24h",
     "Undetected money laundering; criminal liability under MLPA 2004", "P0", "Phase 2"],
    ["Audit Trail",
     "Payment state transitions logged (payment_events); no admin-action log, no KYC/screening audit",
     "Tamper-evident, hash-chained audit log for all financial and admin actions; 7-year retention for financial records",
     "No evidence for regulators, disputes, or investigations", "P0", "Phase 1"],
    ["Access Control",
     "bcrypt + session cookies, CSRF, CSP; single admin account",
     "Role-based access control (RBAC), MFA for admin/merchant, API-key scoping, least privilege",
     "Insider risk, credential compromise", "P0", "Phase 1"],
    ["Rate Limiting / Abuse Control",
     "Login endpoints only (5 failures / 15 min)",
     "Rate limiting on webhooks, payment return, receipt scan, reader scan and all public endpoints",
     "Webhook replay, DoS, brute-force", "P0", "Phase 1"],
    ["Encryption",
     "TLS in transit (Caddy); no encryption at rest",
     "TLS 1.3 everywhere, mTLS between services, encryption at rest for PII and financial data",
     "Data breach impact and regulator findings", "P0", "Phase 1"],
    ["Data Retention & Privacy",
     "90-day purge worker; batches in single transaction; profiles/invoices/thrift not purged; no purge audit",
     "Batched, audited purges extended to all PII; NDPR-compliant; documented retention schedule",
     "Table-lock risk; over-retention of PII", "P1", "Phase 1"],
    ["Deployment & Infrastructure",
     "Single VPS + Caddy + Docker Compose",
     "VPC with private subnets, Cloudflare WAF, secrets management, WAL archiving + PITR, DR drills, staging env",
     "Single point of failure; no DR; secrets exposure", "P0", "Phase 1"],
    ["Regulatory Reporting",
     "None",
     "CTR, STR, PEP monthly returns; management reviews; audit reports",
     "Non-compliance penalties by CBN/NFIU", "P0", "Phase 2"],
    ["Governance",
     "No compliance officer, no policies, no risk register",
     "CCO appointed (GM level+), board oversight committee, ISMS policy set, risk register",
     "No accountability or oversight", "P0", "Phase 0"],
]
for row in overview_rows:
    for i, v in enumerate(row, 1):
        ws.cell(row=r, column=i, value=v)
    r += 1
style_body(ws, 8, r - 1, N)
ws.freeze_panes = "A8"
# prioritize P0 fill
for rr in range(8, r):
    if ws.cell(row=rr, column=5).value == "P0":
        ws.cell(row=rr, column=5).fill = P0_FILL
    elif ws.cell(row=rr, column=5).value == "P1":
        ws.cell(row=rr, column=5).fill = P1_FILL
r += 1

ws.merge_cells(start_row=r, start_column=1, end_row=r, end_column=N)
c = ws.cell(row=r, column=1, value="3. PHASE SUMMARY")
c.font = BODY_BOLD; c.fill = GRAY; c.alignment = WRAP
r += 1
ph_hdr = ["Phase", "Timeline", "Focus", "Key Outcomes", "Departments Involved", "Milestone / Exit Criteria"]
for i, h in enumerate(ph_hdr, 1):
    ws.cell(row=r, column=i, value=h)
style_header(ws, r, N)
r += 1
phases = [
    ["Phase 0 - Governance & Planning", "Month 0",
     "Board/executive setup, policy approval, CCO appointment, licensing review",
     "CCO in role; InfoSec + AML/CFT policies approved; ISMS scope & risk methodology approved; DPA executed; CI/CD hardened",
     "Board, Compliance, Risk, Legal, Engineering, HR",
     "Policies signed; CCO appointed; risk register created"],
    ["Phase 1 - Engineering & Deployment Hardening", "Months 1-3",
     "Codebase security, infrastructure hardening, secrets management, audit logging, encryption, rate limiting, MFA/RBAC",
     "MFA + RBAC live; webhook rate limits; admin audit log; at-rest encryption; VPC + WAF + PITR; 3-server production",
     "Engineering, Ops/Infra, Security, Compliance",
     "Penetration test of hardened build passes; secrets removed from repo"],
    ["Phase 2 - KYC/CDD/AML Program", "Months 3-6",
     "Real KYC verification, sanctions/PEP screening, risk scoring, transaction monitoring, STR workflow",
     "NIN/BVN verification live; screening + risk scoring; STR/CTR/PEP reporting capability; EDD procedures",
     "Engineering, Compliance, Support, Legal",
     "KYC queue operating; test STR filed to NFIU sandbox"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12",
     "Incident response, SIEM, secure SDLC, ledger & reconciliation, BCP/DR, PCI DSS Level 1 scope, NDPR",
     "Ledger + three-way reconciliation; IR plan; quarterly ASV + annual pentest; BCP tested; NDPR registered",
     "Security, Engineering, Finance/Treasury, Risk, Legal",
     "DR drill passed; reconciliation automated"],
    ["Phase 4 - Independent Assurance & Certification", "Months 12-15",
     "Internal audits, gap closure, external certification, CBN examination prep",
     "ISO 27001 certified; independent AML/CFT testing completed; PCI DSS Level 1 QSA RoC + AOC",
     "Audit, Compliance, Board, External bodies",
     "ISO 27001 certificate issued"],
    ["Phase 5 - Ongoing Operations", "Month 15+",
     "Continuous monitoring, reporting, training, regulatory watch, refresh cycles",
     "Monthly STR/CTR/PEP returns; 12-18 month risk refresh; annual training; rescreening continuous",
     "All departments",
     "Compliance operating rhythm institutionalized"],
]
for row in phases:
    for i, v in enumerate(row, 1):
        ws.cell(row=r, column=i, value=v)
    r += 1
style_body(ws, r - len(phases), r - 1, N)

# ============================================================================
# SHEET 2: ROADMAP (main)
# ============================================================================
ws = wb.create_sheet("Roadmap")
N = 12
title_block(ws, "DETAILED COMPLIANCE ROADMAP - ACTION PLAN",
            "One row per action. Status column is the working tracking field.",
            N)
hdr = ["Phase", "Timeline", "ID", "Lead Department", "Supporting Departments",
       "Action", "Clear Description (What To Do)", "Regulatory Basis",
       "Priority", "Deliverable / Output", "Prerequisites / Dependencies", "Status"]
for i, h in enumerate(hdr, 1):
    ws.cell(row=3, column=i, value=h)
style_header(ws, 3, N)
set_widths(ws, [24, 12, 7, 22, 24, 34, 62, 34, 9, 38, 30, 14])

roadmap = [
    # ---------------- PHASE 0 ----------------
    ["Phase 0 - Governance & Planning", "Month 0", "0.1", "Board / Executive",
     "Compliance",
     "Appoint Chief Compliance Officer (CCO)",
     "Formally appoint a qualified individual at General Manager level or above to lead AML/CFT compliance as required by the CBN. Issue an appointment letter defining authority, duties and resource budget (staff, systems, budget). The CCO coordinates day-to-day AML/CFT compliance and reports to the board.",
     "CBN AML/CFT Regulation 2009 - Chief Compliance Officer requirement",
     "P0", "CCO appointment letter; job description; board resolution", "Board approval", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.2", "Board / Executive",
     "Security, Compliance",
     "Approve Information Security Policy",
     "Top management approves an information security policy appropriate to the organisation: states security objectives, commits to satisfying applicable requirements and to continual improvement. Policy is published and communicated to all staff.",
     "ISO 27001:2022 Clause 5.2; Annex A 5.1",
     "P0", "Signed InfoSec Policy document", "CCO / CISO assigned", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.3", "Board / Executive",
     "Compliance, Legal",
     "Approve AML/CFT Policy and Program",
     "Approve a written AML/CFT compliance program: risk-based CDD, suspicious activity monitoring/reporting, internal controls, designated compliance officer, independent testing, staff training. Program is board-approved and tailored to the institution's ML/FT risk profile.",
     "CBN AML/CFT Regulation 2009 - minimum program requirements",
     "P0", "Signed AML/CFT Policy + Program document", "Board approval", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.4", "Board / Executive",
     "Compliance",
     "Establish AML/CFT Oversight Committee",
     "Stand up a board-level committee (or board sub-committee) that reviews compliance initiatives, deficiencies, corrective actions and all regulator returns. Committee meets on a defined cadence and maintains meeting minutes.",
     "CBN AML/CFT Regulation 2009; ISO 27001:2022 5.3",
     "P0", "Charter; meeting schedule; minutes", "CCO appointed", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.5", "Compliance",
     "Legal",
     "Register with NFIU and CBN AML/CFT Office",
     "Complete regulatory registrations: register as a reporting institution with the Nigerian Financial Intelligence Unit (NFIU) and the CBN AML/CFT Office. Confirm reporting channels and formats for STRs, CTRs and PEP returns.",
     "MLPA 2004; CBN AML/CFT Regulation 2009",
     "P0", "NFIU/CBN registration confirmations", "Legal entity in place", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.6", "Compliance",
     "Legal, Board",
     "Licensing and authorisation review",
     "Determine the licences required to operate as a payment provider in Nigeria (e.g., Payment Service Provider / Payment Solution Service Provider under CBN guidelines). Engage CBN and obtain necessary approvals before live-money operation. The current build is not a licensed payment processor.",
     "CBN Payment System Guidelines; README 'Production limitations'",
     "P0", "Licence application(s); CBN approval", "CCO in role", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.7", "Risk",
     "Compliance, Board",
     "Establish risk register and ISMS risk assessment methodology",
     "Create an enterprise risk register and define the information security risk assessment process: risk criteria, risk acceptance criteria, likelihood/impact scales, risk owners. This is the basis for the ISO 27001 risk treatment plan and Statement of Applicability (SoA).",
     "ISO 27001:2022 Clause 6.1; ISO 31000:2018",
     "P0", "Risk register; risk assessment methodology document", "InfoSec policy approved", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.8", "Legal",
     "Procurement, Compliance",
     "Execute data protection agreements with all providers",
     "Sign DPAs with every third party handling data: cloud/VPS provider, WhatsApp/Meta, Telegram, Paystack, VTPass, SMTP/email provider, monitoring tools. Cover confidentiality, data security, breach notification and data residency.",
     "ISO 27001:2022 5.19-5.20, 5.34; NDPR",
     "P0", "Signed DPAs (all providers)", "Legal entity in place", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.9", "Legal",
     "Compliance",
     "Confirm data residency and cross-border transfer arrangements",
     "Document where customer PII and financial data reside and the legal basis for any cross-border transfers (e.g., Meta/WhatsApp). Establish Nigerian data residency where required and record transfers in the processing register.",
     "NDPR 2019; ISO 27001:2022 5.14, 5.34",
     "P1", "Data residency map; transfer register", "DPAs signed", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.10", "Engineering",
     "Security",
     "Harden CI/CD pipeline",
     "Automated build, test and vet on every push (GitHub Actions/GitLab CI); lint and security scanning gates; container registry with images tagged by commit SHA; rolling deploys with automatic rollback on health-check failure; migrations run before app rollout with locking.",
     "ISO 27001:2022 8.25-8.32; XLSX Component Specs (CI/CD)",
     "P0", "CI/CD pipeline; rollback runbook", "Source control in place", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.11", "HR",
     "Security",
     "Define competence matrix and screening policy",
     "Determine necessary competence for roles affecting information security performance; define background verification checks for all candidates proportional to role risk (pre-employment and ongoing). Document evidence of competence.",
     "ISO 27001:2022 7.2, 6.1, 6.6",
     "P1", "Competence matrix; screening policy", "HR lead assigned", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.12", "HR",
     "Legal",
     "Execute NDAs and code of conduct",
     "All personnel and contractors sign confidentiality/non-disclosure agreements reflecting the organisation's protection needs; adopt an information security code of conduct and acceptable-use rules.",
     "ISO 27001:2022 6.6, 5.10",
     "P1", "Signed NDAs; code of conduct", "HR lead assigned", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "0.13", "Engineering",
     "Security",
     "Remove in-repo secrets (critical hygiene)",
     "Delete and rotate all secrets currently committed to the repository: db_password.txt, 'Whatsapp Business Token.txt', 'whatsapp hash.txt' and any .env values. Add to .gitignore, purge git history, rotate the WhatsApp token and DB password, and store secrets in the secrets manager instead.",
     "ISO 27001:2022 8.24; access control principles",
     "P0", "Secrets removed from history; keys rotated", "Secrets manager selected", "Not Started"],

    # ---------------- PHASE 1 ----------------
    ["Phase 1 - Engineering & Deployment Hardening", "Months 1-3", "1.1", "Engineering",
     "Security",
     "Implement MFA for admin and merchant accounts",
     "Add multi-factor authentication (TOTP app or passkeys) for all admin and merchant logins in addition to the existing bcrypt password. Enforce on every login and on privileged actions. Store authenticator secrets encrypted.",
     "ISO 27001:2022 8.5 (secure authentication), 5.16-5.17; KYC/Fraud Recommendations docx",
     "P0", "MFA enrollment + login flow", "0.10 CI/CD; sessions code (app.go auth handlers)", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.2", "Engineering",
     "Security",
     "Implement role-based access control (RBAC)",
     "Introduce roles (admin, compliance officer, support, read-only auditor, merchant) with least-privilege permissions on the admin and merchant consoles. Replace the single-admin model; scope API keys per merchant.",
     "ISO 27001:2022 8.2, 8.3, 5.15, 5.18",
     "P0", "RBAC model + middleware; role matrix", "1.1 MFA", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.3", "Engineering",
     "Security",
     "Add rate limiting to all public endpoints",
     "Extend rate limiting beyond /admin/login and /merchant/login to webhooks (/webhooks/*), /payments/return, /receipts/*, /invoices/*, /scan/* and /api/readers/scan. Use per-IP + per-phone/per-token limiting with exponential backoff and a shared store.",
     "ISO 27001:2022 8.20; webhook requirements checklist item 17",
     "P0", "Rate-limit middleware + config", "None", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.4", "Engineering",
     "Audit",
     "Build tamper-evident admin-action audit log",
     "Record every privileged action (admin/merchant login, KYC decision, payout, refund, data purge, config change): who, what, when, before/after state, IP, correlation ID. Chain entries with hashes to make the log tamper-evident. Retain per retention policy and feed to SIEM.",
     "ISO 27001:2022 8.15; PCI DSS Req 10 (audit logging)",
     "P0", "audit_logs table + middleware + query UI", "1.2 RBAC", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.5", "Engineering",
     "Ops",
     "Adopt structured JSON logging with correlation IDs",
     "Emit structured JSON logs from all services/workers with request correlation IDs. Never log secrets, tokens, card data or raw PII. Route to log aggregation (ELK/Loki/CloudWatch) with 1-year retention for compliance evidence.",
     "ISO 27001:2022 8.15, 8.16; PCI DSS Req 10",
     "P0", "Log schema; correlation IDs; log retention policy", "0.10 CI/CD", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.6", "Engineering",
     "Security, Ops",
     "Implement secrets management",
     "Move configuration secrets from .env to Docker secrets or a cloud KMS/vault. Rotate the database password every 90 days. Encrypt secrets at rest and restrict access to them. Audit secret access.",
     "ISO 27001:2022 8.24, 5.15; XLSX Secrets Management",
     "P0", "Secrets vault; rotation schedule", "0.13 secrets cleanup", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.7", "Engineering",
     "Security",
     "Implement encryption at rest for PII and financial data",
     "Encrypt PII at rest: use pgcrypto column encryption (or disk/volume encryption) for high-sensitivity fields (legal name, DOB, email, addresses, phone) in users, individual_profiles, merchant_registrations, invoices, data_orders and sms_requests. Manage keys via KMS/HSM with key rotation.",
     "ISO 27001:2022 8.24; PCI DSS Req 3 (if card data); NDPR",
     "P0", "Encryption keys + column encryption migration", "1.6 secrets management", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.8", "Engineering",
     "Compliance",
     "Fix retention purge: batching, coverage, audit",
     "Refactor PurgeBefore to delete in batches (avoid table-lock risk at scale), extend purge to individual_profiles, invoices and thrift PII, and record every purge run in the audit log. Align retention periods to the documented schedule (90-day PII; financial records per regulatory minimums).",
     "ISO 27001:2022 8.10 (information deletion), 5.34; NDPR",
     "P1", "Batched purge + coverage + purge audit", "1.4 audit log", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.9", "Engineering",
     "Compliance",
     "Create KYC data model (migration 021+)",
     "Add tables for the real KYC program: customer_verifications, kyc_profiles, screening_results, risk_events, authenticators, trusted_devices, manual_review_cases (per KYC/Fraud Recommendations docx). Migrate existing individual_profiles data into the new model.",
     "CBN AML/CFT (CIP/CDD); KYC/Fraud Recommendations docx",
     "P0", "Migrations 021+; KYC schema", "None", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.10", "Ops / Infrastructure",
     "Security",
     "Provision production VPC with private subnets and firewalls",
     "Deploy 3-server Phase 1 footprint (2 app + 1 DB) inside a VPC. Public access limited to ports 80/443 on Caddy. Database ports (5432/5433/6432) reachable only from app subnet. Configure cloud firewall + host firewall to deny all else.",
     "ISO 27001:2022 8.20-8.22; XLSX Network Topology",
     "P0", "VPC + security groups + firewall rules", "0.10 CI/CD", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.11", "Ops / Infrastructure",
     "Security",
     "Deploy Cloudflare WAF / DDoS protection",
     "Put Cloudflare (or cloud provider DDoS shield) in front of Caddy. Enable WAF rules (OWASP core), rate limiting at the edge, bot management and TLS. This is required for PCI DSS and protects against volumetric and application-layer attacks.",
     "ISO 27001:2022 8.23, 8.20; PCI DSS Req 6.6 / 11",
     "P0", "WAF live on production domain", "Domain + DNS (XLSX prerequisite)", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.12", "Ops / Infrastructure",
     "Risk",
     "Enable WAL archiving, PITR and daily backups",
     "Set wal_level=replica, enable continuous WAL archiving to S3/GCS, daily full backups via pgBackRest/WAL-G with 30-day retention and versioning. Configure streaming replication to a standby for failover. Target RPO < 1 hour, RTO < 15 minutes.",
     "ISO 27001:2022 8.13; XLSX Backup & Recovery",
     "P0", "Backup job + restore runbook + RPO/RTO verified", "1.10 VPC", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.13", "Ops / Infrastructure",
     "Security",
     "Deploy SIEM / log aggregation + monitoring",
     "Stand up ELK/Loki for log aggregation, Prometheus + Grafana for metrics (latency, queue depth, workers, DB pool), UptimeRobot for health probes on /health/ready (alert within 60s), and Sentry for error tracking. Enforce 1-year log retention.",
     "ISO 27001:2022 8.15-8.16; PCI DSS Req 10/11",
     "P1", "Dashboards + alerts + log retention", "1.5 structured logging", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.14", "Ops / Infrastructure",
     "Engineering",
     "Build staging environment mirroring production",
     "Create a staging environment that mirrors production (same OS, versions, topology, config management) for pre-production testing of code, migrations and configuration. Isolated from production data; synthetic data only.",
     "ISO 27001:2022 8.31 (separation of environments)",
     "P1", "Staging environment + deploy workflow", "1.10 VPC", "Not Started"],
    ["Phase 0 - Governance & Planning", "Month 0", "1.15", "Compliance",
     "Engineering",
     "Build CTR / PEP reporting data exports",
     "Implement query/report capability to extract Currency Transaction Report (CTR) data at the NGN threshold and monthly PEP transaction returns in the regulator-required format, from audited payment and ledger data.",
     "MLPA 2004; CBN AML/CFT Regulation 2009 (reporting)",
     "P1", "CTR + PEP report generators", "1.9 KYC model; ledger data", "Not Started"],

    # ---------------- PHASE 2 ----------------
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.1", "Engineering",
     "Compliance",
     "Implement real KYC approval workflow (L0-L4)",
     "Replace the simulated 'approved_simulated' status with a real verification ladder: L0 account confirmed, L1 email OTP, L2 NIN/BVN verified, L3 document + selfie, L4 source of funds. KYC approval becomes a controlled state transition with audit and manual-review support.",
     "CBN AML/CFT (CIP); KYC/Fraud Recommendations docx",
     "P0", "KYC state machine + approval flow", "1.9 KYC data model", "Not Started"],
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.2", "Engineering",
     "Compliance, Legal",
     "Integrate NIN / BVN verification provider",
     "Integrate a Nigerian identity verification provider (Smile ID, Youverify, Dojah, or Prembly/Identitypass) for NIN and BVN verification with liveness/document checks. Store verification references and results in customer_verifications.",
     "CBN AML/CFT (CIP verification through documents); docx",
     "P0", "Verification provider integration + test results", "2.1 KYC workflow", "Not Started"],
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.3", "Engineering",
     "Compliance",
     "Integrate sanctions / PEP screening",
     "Screen all customers and beneficial owners against OFAC, UN, EU and CBN lists at onboarding and on a periodic rescreening schedule. Support decision states: clear, possible_match, strong_match, blocked, manually_cleared. Escalate strong matches to manual review.",
     "CBN AML/CFT Regulation 2009 - sanctions; OFAC compliance section; docx",
     "P0", "Screening integration + screening_results table", "1.9 KYC data model", "Not Started"],
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.4", "Engineering",
     "Risk",
     "Implement customer ML/FT risk scoring",
     "Build a risk-scoring engine (low/medium/high) using customer profile (occupation, business type, geographic location), products used, transaction volume/velocity and screening results. Drive CDD vs EDD and monitoring intensity from the score.",
     "CBN AML/CFT Regulation 2009 - risk-based approach; docx",
     "P0", "Risk scoring model + risk_events table", "2.1 KYC workflow", "Not Started"],
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.5", "Compliance",
     "Support, Risk",
     "Document CDD / EDD procedures",
     "Write procedures for standard CDD (identity, address, occupation, purpose of account, expected activity) and Enhanced Due Diligence triggers: PEPs, high-risk jurisdictions, cash-intensive businesses, shell companies, NGOs, embassies, foreign correspondent and private banking relationships.",
     "CBN AML/CFT Regulation 2009 (CDD/EDD, sections 5-42)",
     "P0", "CDD/EDD procedure documents", "2.4 risk scoring", "Not Started"],
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.6", "Compliance",
     "Legal, Engineering",
     "Establish beneficial ownership verification for business accounts",
     "For merchants and business accounts, collect and verify beneficial ownership (owners, controllers, signatories) per CBN rules. Cross-check against CAC where possible. Record in the KYC model and rescreen periodically.",
     "CBN AML/CFT Regulation 2009 - beneficial ownership",
     "P0", "Beneficial ownership capture flow + records", "1.9 KYC data model", "Not Started"],
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.7", "Engineering",
     "Risk, Compliance",
     "Implement transaction monitoring rules engine",
     "Build a rules engine over payment, invoice, data-order and thrift transactions: velocity/structuring detection, round amounts, high-risk counterparties, unusual hours, dormant-account activity, large currency aggregation. Generate alerts to manual review; support case management.",
     "CBN AML/CFT Regulation 2009 - suspicious activity monitoring; docx",
     "P0", "Rules engine + alert/case management", "2.4 risk scoring; ledger events", "Not Started"],
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.8", "Compliance",
     "Engineering, Legal",
     "Implement STR workflow to NFIU",
     "Define the Suspicious Transaction Report workflow: alert to case, investigation, decision, and filing to NFIU (with CBN informed) within 24 hours of detection. Maintain case files, evidence and timestamps. Test filing in NFIU sandbox.",
     "MLPA 2004; CBN AML/CFT Regulation 2009 - STR; EFCC Act",
     "P0", "STR workflow + case file + filing evidence", "2.7 rules engine", "Not Started"],
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.9", "Support / Customer Ops",
     "Compliance",
     "Run KYC review queue and manual-review cases",
     "Operate the KYC queue: review verification results, escalate failed/mismatched verifications, run manual-review cases (screening matches, EDD, high-risk accounts). Maintain SLAs and decision records. All decisions audited.",
     "CBN AML/CFT (CDD); ISO 27001:2022 8.15",
     "P1", "Operating KYC queue + SLA reports", "2.1-2.3 build complete", "Not Started"],
    ["Phase 2 - KYC / CDD / AML Program", "Months 3-6", "2.10", "Engineering",
     "Compliance",
     "Enforce no-PIN/no-card/no-OTP policy in chat",
     "Confirm the product never solicits card details, PINs, CVVs or chat OTPs; route all sensitive flows to the secure web surface. Add automated content checks that block/sanitize any such data sent in chat and log the attempt.",
     "PCI DSS; CBN consumer protection; docx recommendation",
     "P1", "Chat content guard + policy", "None", "Not Started"],

    # ---------------- PHASE 3 ----------------
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.1", "Security",
     "Ops, Engineering, Legal",
     "Implement incident response plan and SIEM alerting",
     "Document the incident response plan (roles, severity levels, containment, eradication, recovery, communication, evidence collection per Annex A 5.28). Connect SIEM alerting for anomalies, error-rate spikes, login failures and audit-log anomalies. Run a tabletop exercise.",
     "ISO 27001:2022 5.24-5.28; PCI DSS Req 12",
     "P0", "IR plan + SIEM alert rules + tabletop report", "1.13 SIEM", "Not Started"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.2", "Security",
     "Engineering, Ops",
     "Run vulnerability scanning and penetration testing program",
     "Quarterly ASV vulnerability scans (PCI DSS Requirement 11) and an annual independent penetration test on all services. Remediate findings within SLA; track in a vulnerability register.",
     "ISO 27001:2022 8.8; PCI DSS Req 11",
     "P0", "Scan reports + remediation register", "1.11 WAF; staging env", "Not Started"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.3", "Compliance",
     "HR, Security",
     "Deliver ISMS and AML/CFT awareness training",
     "Train all personnel whose duties require knowledge of the MLPA/AML/CFT rules and the ISMS: policy awareness, suspicious transaction indicators, incident reporting channels, data handling. Tailor by role (tellers/support vs engineering vs finance). Document attendance and test results.",
     "ISO 27001:2022 6.3, 7.3; CBN AML/CFT Regulation 2009 - training",
     "P1", "Training plan + materials + attendance records", "0.11 competence matrix", "Not Started"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.4", "Engineering",
     "Security",
     "Adopt secure SDLC (threat modeling, secure code review, security testing in CI)",
     "Formalise the secure development lifecycle per Annex A 8.25-8.29: security requirements on new features, threat modeling, secure coding standards, automated security testing (SAST/SCA) in CI, security testing before acceptance, and separation of dev/test/prod.",
     "ISO 27001:2022 8.25-8.29, 8.31-8.32",
     "P1", "SDLC policy + SAST/SCA gates + code review checklist", "0.10 CI/CD", "Not Started"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.5", "Finance / Treasury",
     "Engineering",
     "Build append-only ledger service",
     "Implement double-entry bookkeeping as an append-only ledger (journal entries, batches, balance snapshots). Reversals via offsetting entries, never UPDATE/DELETE. This is the financial source of truth for audits and reconciliation.",
     "CBN record-keeping requirements; XLSX Ledger Service / Data Model",
     "P0", "Ledger service + ledger DB + journal schema", "1.4 audit log", "Not Started"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.6", "Finance / Treasury",
     "Engineering",
     "Implement three-way reconciliation",
     "Daily automated three-way reconciliation: internal records vs ledger entries vs bank/processor statements. Weekly manual review. Escalate breaks to finance and compliance. Retain reports for the record-keeping period.",
     "CBN AML/CFT (paper trail); XLSX Reconciliation Service; PCI Req (monitoring)",
     "P0", "Reconciliation service + daily runs + reports", "3.5 ledger", "Not Started"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.7", "Risk",
     "Ops, Security",
     "Establish business continuity and ICT readiness",
     "Write the business continuity plan covering information security during disruption (Annex A 5.29) and ICT readiness for continuity (5.30). Define RTO/RPO targets, run quarterly DR restore drills to a separate region, and document results.",
     "ISO 27001:2022 5.29-5.30; XLSX Disaster Recovery Drill",
     "P0", "BCP + quarterly DR drill reports", "1.12 backups/PITR", "Not Started"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.8", "Legal",
     "Compliance, Security",
     "Achieve NDPR compliance and publish privacy notices",
     "Register with NITDA under NDPR; publish a privacy policy and consent mechanisms; document lawful bases for processing; establish PII breach notification procedure; align the 90-day retention with the NDPR principles and record processing activities.",
     "NDPR 2019; ISO 27001:2022 5.34",
     "P1", "NDPR registration; privacy policy; ROPA", "0.9 data residency", "Not Started"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.9", "Security / Compliance",
     "Finance, Board",
     "Determine and implement PCI DSS Level 1 scope",
     "Xego is a Level 1 merchant (more than 6M card transactions/yr), requiring a QSA-conducted RoC and AOC. Use hosted card-checkout to keep card data off Xego servers and minimize scope; segment the network and isolate tokenization in a dedicated VLAN with AES-256/HSM key management. Maintain the CDE Inventory and the Requirements 1-12 Compliance Matrix as the evidence pack for the QSA.",
     "PCI DSS Requirements 1-12; PCI DSS Level 1 (QSA RoC)",
     "P0", "PCI Level 1 scope + CDE inventory + Req 1-12 matrix + segmentation", "1.10 VPC; 1.11 WAF", "Not Started"],
    ["Phase 3 - ISMS Full Operation + PCI", "Months 6-12", "3.10", "Ops / Infrastructure",
     "Engineering",
     "Run capacity management and load testing",
     "Apply the XLSX tuning: PgBouncer pool=75, max_client=2000, PG max_connections=150, shared_buffers=6GB on 16GB RAM, HTTP timeouts (ReadHeader 5s/Read 15s/Write 45s/Idle 60s), worker concurrency per phase. Run load tests and record capacity plans.",
     "ISO 27001:2022 8.6; XLSX Concurrency & Pool Tuning",
     "P1", "Tuned config + load test report + capacity plan", "1.10-1.13 infra", "Not Started"],

    # ---------------- PHASE 4 ----------------
    ["Phase 4 - Independent Assurance & Certification", "Months 12-15", "4.1", "Audit",
     "Compliance",
     "Conduct independent AML/CFT compliance testing",
     "Commission independent testing (internal audit dept, external auditors, or qualified independent party) of the AML/CFT program: risk assessment reasonableness, transaction testing, STR/CTR/PEP accuracy and timeliness, staff training adequacy, MIS integrity. Frequency: every 12-18 months per sound practice.",
     "CBN AML/CFT Regulation 2009 - independent testing",
     "P0", "Independent testing report + corrective actions", "Phase 2 complete", "Not Started"],
    ["Phase 4 - Independent Assurance & Certification", "Months 12-15", "4.2", "Audit",
     "Security",
     "Conduct internal ISMS audit",
     "Plan and run internal audits at planned intervals to confirm the ISMS conforms to ISO 27001 and to the organisation's own requirements, and is effectively implemented. Report results to relevant management; keep documented evidence.",
     "ISO 27001:2022 Clause 9.2",
     "P0", "Internal audit programme + reports", "Phase 3 complete", "Not Started"],
    ["Phase 4 - Independent Assurance & Certification", "Months 12-15", "4.3", "Compliance",
     "All departments",
     "Close pre-certification gaps",
     "Resolve all findings from internal audits, vulnerability scans, penetration tests and independent testing before the external certification audit. Update the Statement of Applicability (SoA) and risk treatment plan; obtain risk-owner sign-off on residual risks.",
     "ISO 27001:2022 6.1.3, 10.2",
     "P0", "Gap-closure register; updated SoA", "4.1, 4.2 reports", "Not Started"],
    ["Phase 4 - Independent Assurance & Certification", "Months 12-15", "4.4", "Board",
     "Compliance, Audit",
     "Conduct formal management review",
     "Top management reviews the ISMS at planned intervals covering: previous action status, internal/external changes, feedback on performance (nonconformities, monitoring, audits, objectives), interested-party feedback, risk assessment status, and improvement opportunities. Record decisions and evidence.",
     "ISO 27001:2022 Clause 9.3",
     "P0", "Management review minutes + decisions", "4.2 internal audit", "Not Started"],
    ["Phase 4 - Independent Assurance & Certification", "Months 12-15", "4.5", "External",
     "Compliance, Board",
     "Pass ISO/IEC 27001:2022 certification audit",
     "Engage an accredited certification body (e.g., BSI, SGS, TUV) for Stage 1 (documentation) and Stage 2 (implementation) audits. Remedy any nonconformities within the certification body's timelines and obtain the certificate.",
     "ISO/IEC 27001:2022",
     "P0", "ISO 27001 certificate", "4.3 gap closure", "Not Started"],
    ["Phase 4 - Independent Assurance & Certification", "Months 12-15", "4.6", "Compliance",
     "Legal, Finance",
     "Prepare for CBN / NFIU examination",
     "Assemble evidence packs for a CBN AML/CFT examination: policies, program, CCO role, training records, independent testing reports, STR/CTR/PEP returns, risk assessment, CIP/CDD records, audit findings. Conduct a mock examination.",
     "CBN AML/CFT Regulation 2009; MLPA 2004",
     "P0", "Examination evidence pack + mock exam report", "Phase 2 complete", "Not Started"],
    ["Phase 4 - Independent Assurance & Certification", "Months 12-15", "4.7", "External",
     "Security, Finance",
     "Obtain PCI DSS Level 1 attestation",
     "Have a Qualified Security Assessor complete the Level 1 assessment, and obtain the Attestation of Compliance on the Report on Compliance. Keep ASV scans, the CDE Inventory and the Requirements 1-12 Compliance Matrix current.",
     "PCI DSS Level 1",
     "P1", "AOC + current ASV scans + RoC", "3.9 PCI Level 1 scope", "Not Started"],

    # ---------------- PHASE 5 ----------------
    ["Phase 5 - Ongoing Operations", "Month 15+", "5.1", "Compliance",
     "Finance, Legal",
     "Submit monthly/ongoing regulatory returns",
     "File STRs (within 24h of detection), CTRs (at threshold), and monthly PEP transaction returns to NFIU and CBN on the required cadence. Maintain submission evidence and a returns register.",
     "MLPA 2004; CBN AML/CFT Regulation 2009",
     "P0", "Returns register + filing evidence", "1.15 reporting; 2.8 STR", "Not Started"],
    ["Phase 5 - Ongoing Operations", "Month 15+", "5.2", "Compliance",
     "Engineering",
     "Run continuous sanctions / PEP rescreening",
     "Periodically rescreen the full customer and beneficial-owner base against updated sanctions/PEP lists. Handle newly-flagged accounts per policy (freeze/review/report as applicable). Document rescreening runs.",
     "CBN AML/CFT Regulation 2009; OFAC compliance program",
     "P1", "Rescreening job + exception reports", "2.3 screening", "Not Started"],
    ["Phase 5 - Ongoing Operations", "Month 15+", "5.3", "Risk",
     "Compliance",
     "Refresh ML/FT risk assessment every 12-18 months",
     "Re-run the institution-level ML/FT risk assessment at least every 12-18 months, or when new products/services are introduced, high-risk customers open/close accounts, or on M&A. Update risk scoring models and monitoring intensity accordingly.",
     "CBN AML/CFT Regulation 2009 - updating risk assessment",
     "P1", "Updated risk assessment + approval", "2.4 risk scoring", "Not Started"],
    ["Phase 5 - Ongoing Operations", "Month 15+", "5.4", "Compliance",
     "HR, All departments",
     "Deliver annual compliance and security training",
     "Run annual AML/CFT and information security awareness training with testing; onboard new staff with orientation training; provide CCO-specific training on regulatory changes. Update materials as rules change. Keep records.",
     "CBN AML/CFT Regulation 2009 - training; ISO 27001:2022 6.3, 7.2-7.3",
     "P1", "Annual training records", "3.3 training program", "Not Started"],
    ["Phase 5 - Ongoing Operations", "Month 15+", "5.5", "HR",
     "Security, IT",
     "Operate joiners/movers/leavers controls",
     "Apply access-control lifecycle for all staff: provisioning on join, re-certification on role change, revocation and asset return on termination. Include disciplinary process for policy violations (Annex A 6.4) and post-termination duties (6.5).",
     "ISO 27001:2022 5.11, 6.4, 6.5, 8.2-8.3",
     "P1", "Access review reports; leaver checklist", "1.2 RBAC", "Not Started"],
    ["Phase 5 - Ongoing Operations", "Month 15+", "5.6", "Legal",
     "Compliance",
     "Maintain regulatory and threat watch",
     "Monitor CBN directives, NDPR/NITDA updates, FATF recommendations, EFCC/NFIU notices, and emerging threats. Translate changes into policy/system updates and communicate to affected stakeholders.",
     "ISO 27001:2022 5.31, 5.7; NDPR",
     "P2", "Regulatory watch log + impact assessments", "None", "Not Started"],
    ["Phase 5 - Ongoing Operations", "Month 15+", "5.7", "Audit",
     "Compliance",
     "Run the annual ISMS audit cycle",
     "Execute the recurring internal audit programme and management reviews; track corrective actions to closure; support the ISO 27001 surveillance/re-certification audits.",
     "ISO 27001:2022 Clauses 9-10",
     "P1", "Audit programme calendar + reports", "4.2, 4.4", "Not Started"],
]

r = 4
phase_row_ranges = {}
current_phase = None
phase_start = 4
for row in roadmap:
    for i, v in enumerate(row, 1):
        ws.cell(row=r, column=i, value=v)
    if row[0] != current_phase:
        if current_phase is not None:
            phase_row_ranges[current_phase] = (phase_start, r - 1)
        current_phase = row[0]
        phase_start = r
    r += 1
phase_row_ranges[current_phase] = (phase_start, r - 1)

style_body(ws, 4, r - 1, N)
for phase, (p0, p1) in phase_row_ranges.items():
    key = phase.split(" ")[1].rstrip("-")
    fill = PHASE_FILLS.get(key)
    if fill:
        for rr in range(p0, p1 + 1):
            ws.cell(row=rr, column=1).fill = fill
for rr in range(4, r):
    pr = ws.cell(row=rr, column=9).value
    cell = ws.cell(row=rr, column=9)
    if pr == "P0":
        cell.fill = P0_FILL
    elif pr == "P1":
        cell.fill = P1_FILL
    elif pr == "P2":
        cell.fill = P2_FILL

ws.freeze_panes = "A4"
ws.auto_filter.ref = f"A3:{get_column_letter(N)}3"
for rr in range(4, r):
    ws.row_dimensions[rr].height = None

# ============================================================================
# SHEET 3: SERVER REQUIREMENTS
# ============================================================================
ws = wb.create_sheet("Server Requirements")
N = 7
title_block(ws, "SERVER / INFRASTRUCTURE REQUIREMENTS",
            "Deployment footprint per phase, based on Xego_Production_Requirements.xlsx",
            N)
hdr = ["Phase", "Server / Component", "Spec (CPU / RAM / Disk)", "Count",
       "Purpose", "Ports (Firewall)", "Compliance Rationale"]
for i, h in enumerate(hdr, 1):
    ws.cell(row=3, column=i, value=h)
style_header(ws, 3, N)
set_widths(ws, [18, 30, 34, 10, 48, 24, 52])

servers = [
    ["Phase 1 (3 servers)", "App Server x2", "4 vCPU / 8 GB / 5 GB SSD / 1 Gbps", "2",
     "Xego app replicas behind Caddy; zero-downtime deploys; horizontal scaling",
     "8081 internal only", "ISO 8.1, 8.6; separation of app from DB"],
    ["Phase 1 (3 servers)", "Database Server", "8 vCPU / 16 GB / 200 GB NVMe (3,000-10,000 IOPS)", "1",
     "PostgreSQL 17 OLTP primary + PgBouncer; WAL archiving for PITR",
     "5432/6432 private only", "ISO 8.13 backup; DB never internet-exposed"],
    ["Phase 2 (3 servers)", "App Server x2 (scaled)", "4 vCPU / 8 GB / 5 GB SSD / 1 Gbps", "2",
     "App replicas; worker concurrency per phase tuning",
     "8081 internal only", "ISO 8.6 capacity mgmt"],
    ["Phase 2 (3 servers)", "Database Server (scaled)", "8 vCPU / 16 GB / 200 GB NVMe", "1",
     "OLTP primary + PgBouncer pool=40; read replica to standby",
     "5432/6432 private only", "ISO 8.13; replication for HA"],
    ["Phase 3 (9 servers)", "App Server x4", "4 vCPU / 8 GB / 5 GB SSD / 1 Gbps", "4",
     "4 app replicas, HTTP/3 via Caddy; 29 goroutine workers each; 75k concurrent capacity",
     "8081 internal only", "ISO 8.6; capacity for 375K users"],
    ["Phase 3 (9 servers)", "OLTP Primary DB", "8 vCPU / 16 GB / 200 GB NVMe", "1",
     "Transaction writes; PgBouncer pool=75; max_connections=150; shared_buffers=6GB",
     "5432/6432 private only", "ISO 8.13; single source of truth"],
    ["Phase 3 (9 servers)", "OLTP Read Replica x2", "8 vCPU / 16 GB / 200 GB NVMe", "2",
     "Read-heavy admin/reporting/reconciliation; streaming replica",
     "5433/5434 private only", "ISO 8.6; isolation of read workloads"],
    ["Phase 3 (9 servers)", "Ledger DB", "4 vCPU / 8 GB / 200 GB NVMe", "1",
     "Append-only double-entry ledger; 7-year financial records",
     "5435 private only", "CBN record-keeping; append-only audit"],
    ["Phase 3 (9 servers)", "Tokenization VPS (PCI zone)", "Dedicated, isolated subnet", "1",
     "Card tokenization vault (if card data in scope); AES-256 + HSM keys",
     "8085 private (PCI VLAN)", "PCI DSS; network segmentation"],
    ["Phase 3 (9 servers)", "Kafka + Redis + PgBouncer", "4 vCPU / 8 GB each approx.", "~2",
     "Event bus (30-day topics), rate-limit/session cache, connection pooling",
     "9092/6379/6432 private only", "ISO 8.6, 8.20; capacity"],
    ["All phases", "Caddy Reverse Proxy", "2 vCPU / 512 MB", "1",
     "TLS termination (Let's Encrypt), HTTP/3, compression, rate limiting",
     "80, 443, 443/udp public", "TLS 1.3 in transit (ISO 8.24)"],
    ["All phases", "Cloudflare WAF / DDoS", "Managed service", "-",
     "WAF rules, DDoS absorption, bot management, edge rate limiting",
     "n/a (proxy)", "PCI Req 6.6/11; ISO 8.23"],
]

r = 4
for row in servers:
    for i, v in enumerate(row, 1):
        ws.cell(row=r, column=i, value=v)
    r += 1
style_body(ws, 4, r - 1, N)
for rr in range(4, r):
    key = ws.cell(row=rr, column=1).value.split(" ")[1].rstrip("()")
    fill = PHASE_FILLS.get(key)
    if fill:
        ws.cell(row=rr, column=1).fill = fill
r += 1

ws.merge_cells(start_row=r, start_column=1, end_row=r, end_column=N)
c = ws.cell(row=r, column=1, value="NON-NEGOTIABLE COMPLIANCE INFRASTRUCTURE (ALL PHASES)")
c.font = BODY_BOLD; c.fill = GRAY
r += 1
infra_hdr = ["Area", "Requirement", "Target", "", "", "", "Rationale"]
for i, h in enumerate(infra_hdr, 1):
    ws.cell(row=r, column=i, value=h if i in (1, 2, 3, 7) else "")
style_header(ws, r, N)
r += 1
infra = [
    ["Networking", "VPC with private subnets", "DB never internet-exposed; only 80/443 public", "ISO 8.20-8.22"],
    ["TLS", "TLS 1.3 everywhere", "Caddy auto-TLS + DB SSL verify-full + mTLS internal", "ISO 8.24"],
    ["Backups", "WAL archiving + PITR", "Daily full + continuous WAL; 30-day retention; RPO<1h, RTO<15m", "ISO 8.13"],
    ["DR", "Quarterly restore drills", "Restore test to separate region; documented results", "ISO 5.29-5.30"],
    ["Monitoring", "SIEM/log aggregation + metrics", "ELK/Loki + Prometheus/Grafana + UptimeRobot; 1-year log retention", "ISO 8.15-8.16; PCI Req 10/11"],
    ["Secrets", "KMS / Docker secrets", "No secrets in repo/env in production; rotate DB password 90 days", "ISO 8.24"],
    ["Staging", "Prod-mirror environment", "Same OS/versions/topology; synthetic data", "ISO 8.31"],
    ["Cost (reference)", "Phase 1: $260-480/mo; Phase 2: $410-760/mo; Phase 3: $1,200-2,200/mo", "From XLSX cost model", "Budget approval"],
]
for row in infra:
    vals = [row[0], row[1], row[2], "", "", "", row[3]]
    for i, v in enumerate(vals, 1):
        ws.cell(row=r, column=i, value=v)
    r += 1
style_body(ws, r - len(infra), r - 1, N)
ws.freeze_panes = "A4"

# ============================================================================
# SHEET 4: REPORTING MATRIX
# ============================================================================
ws = wb.create_sheet("Reporting Matrix")
N = 8
title_block(ws, "REGULATORY REPORTING MATRIX",
            "Every report/return the institution owes regulators, boards or management.",
            N)
hdr = ["Report / Return", "Description (What Is Filed)", "Owner", "Frequency", "Recipient",
       "Content / Trigger", "Regulatory Basis", "Status"]
for i, h in enumerate(hdr, 1):
    ws.cell(row=3, column=i, value=h)
style_header(ws, 3, N)
set_widths(ws, [26, 44, 22, 18, 26, 40, 30, 12])

reports = [
    ["Suspicious Transaction Report (STR)",
     "Report of any known or suspected money-laundering or financing-of-terrorism activity to the NFIU, informing the CBN",
     "Compliance (CCO)", "Within 24h of detection", "NFIU + CBN",
     "Customer details, transaction(s), basis for suspicion, amounts, dates", "MLPA 2004; CBN AML/CFT Regulation 2009", "Not Started"],
    ["Currency Transaction Report (CTR)",
     "Report of currency transactions at/above the NGN statutory threshold",
     "Compliance", "Per threshold event (aggregate daily also considered)", "CBN + NFIU",
     "Customer, amount, currency, source, transaction details", "MLPA 2004; CBN AML/CFT Regulation 2009", "Not Started"],
    ["PEP Transaction Returns",
     "Monthly returns of all transactions with politically exposed persons",
     "Compliance", "Monthly", "CBN",
     "All PEP-related transactions; PEP identification and risk rating", "CBN AML/CFT Regulation 2009 (B1402-B1403)", "Not Started"],
    ["OFAC / Sanctions Findings",
     "Reporting of sanctioned-party matches and frozen assets",
     "Compliance", "As detected", "CBN / NFIU as applicable",
     "Matched party, list, account, action taken", "CBN AML/CFT Regulation 2009 - OFAC compliance", "Not Started"],
    ["Management Review",
     "ISMS suitability/adequacy/effectiveness review: actions, changes, performance, audit results, risks, improvements",
     "Board / Top Management", "Planned intervals (e.g., annually)", "Top management",
     "ISO 9.3.2 inputs + decisions", "ISO 27001:2022 Clause 9.3", "Not Started"],
    ["Internal Audit Reports",
     "ISMS internal audit results; AML/CFT independent testing findings",
     "Audit", "12-18 months (AML); planned (ISO)", "Board / designated committee", "Audit scope, findings, corrective actions", "ISO 9.2; CBN AML/CFT independent testing", "Not Started"],
    ["InfoSec Metrics",
     "Security KPIs: incidents, error rates, scan findings, login failures, patch status",
     "Security", "Monthly", "CCO / Board",
     "Trends vs thresholds; corrective actions", "ISO 8.15-8.16, 9.1", "Not Started"],
    ["PCI ASV Scan + Level 1 Attestation",
     "Quarterly vulnerability scans, annual penetration test and Level 1 (QSA RoC) attestation",
     "Security / Finance", "Quarterly (scan) / Annual (QSA RoC + AOC)", "QSA, card schemes",
     "Scan results, remediation, RoC/AOC", "PCI DSS Level 1; Req 11", "Not Started"],
    ["DR Restore Test Report",
     "Quarterly disaster recovery restore drill results",
     "Ops / Risk", "Quarterly", "Risk Committee",
     "Restore success, RTO/RPO achieved, gaps", "ISO 5.29-5.30", "Not Started"],
    ["Risk Assessment Refresh",
     "Updated ML/FT and info-security risk assessments",
     "Risk", "12-18 months or on material change", "Board",
     "Risk profile, controls, residual risk", "ISO 6.1; CBN AML/CFT risk assessment", "Not Started"],
    ["Training Records",
     "AML/CFT and ISMS awareness training evidence",
     "HR / Compliance", "Annual (documented)", "Regulators on request", "Attendees, dates, materials, test results", "CBN AML/CFT training; ISO 7.2-7.3", "Not Started"],
]
r = 4
for row in reports:
    for i, v in enumerate(row, 1):
        ws.cell(row=r, column=i, value=v)
    r += 1
style_body(ws, 4, r - 1, N)
ws.freeze_panes = "A4"
ws.auto_filter.ref = f"A3:{get_column_letter(N)}3"

# ============================================================================
# SHEET 5: DEPARTMENT RESPONSIBILITIES
# ============================================================================
ws = wb.create_sheet("Department Responsibilities")
N = 5
title_block(ws, "DEPARTMENT RESPONSIBILITY MATRIX",
            "What each department owns across the compliance program.",
            N)
hdr = ["Department", "Primary Responsibilities", "Key Deliverables", "Point-of-Contact Role", "Affected By Phases"]
for i, h in enumerate(hdr, 1):
    ws.cell(row=3, column=i, value=h)
style_header(ws, 3, N)
set_widths(ws, [26, 62, 48, 30, 16])

depts = [
    ["Compliance / AML",
     "CIP/CDD/EDD program, sanctions/PEP screening oversight, STR/CTR/PEP filings, CCO duties, regulator liaison (CBN/NFIU), policy ownership, KYC queue ownership, EDD decisions",
     "AML/CFT policy, STR workflow, returns register, examination evidence pack, screening exception reports",
     "Chief Compliance Officer", "0-5"],
    ["Information Security / IT",
     "ISMS operation, ISO 27001, SIEM/alerting, encryption & key management, access control administration, incident response, vulnerability management",
     "InfoSec policy, IR plan, SoA, vulnerability register, security metrics",
     "CISO / InfoSec Lead", "0-5"],
    ["Engineering / Development",
     "All codebase changes, KYC & screening implementation, audit logging, RBAC/MFA, rate limiting, encryption migrations, secure SDLC, ledger & reconciliation services, CI/CD",
     "Migrations 021+, feature releases, SAST/SCA gates, deployment pipeline",
     "Engineering Lead", "0-4"],
    ["Operations / Infrastructure",
     "VPC, firewalls, WAF, backups/PITR, DR drills, monitoring stack, PostgreSQL/PgBouncer tuning, capacity management, staging environment",
     "Infrastructure-as-code, backup job, DR drill reports, capacity plan",
     "Infra / DevOps Lead", "1-5"],
    ["Risk Management",
     "Enterprise + ML/FT risk assessments, fraud rule engine oversight, business continuity, RTO/RPO, residual-risk acceptance",
     "Risk register, risk assessments, BCP, risk reports",
     "Chief Risk Officer", "0-5"],
    ["Finance / Treasury",
     "Ledger operation, settlement, three-way reconciliation, financial record keeping (7-year audit trail), PCI cost ownership",
     "Reconciliation reports, journal integrity, financial statements",
     "Finance / Treasury Head", "3-5"],
    ["Legal",
     "DPAs, licensing, NDPR registration, privacy notices, regulatory filings, breach notification, regulatory watch",
     "Signed DPAs, licences, ROPA, privacy policy",
     "General Counsel / Legal Lead", "0-5"],
    ["Audit",
     "Independent AML/CFT testing, internal ISMS audits, tracking corrective actions, liaison with external auditors/certification bodies",
     "Audit reports, gap-closure register, audit programme",
     "Head of Internal Audit", "4-5"],
    ["Human Resources",
     "Screening, NDAs, competence matrix, training & awareness, disciplinary process, joiners/movers/leavers controls",
     "Training records, access reviews, leaver checklist",
     "HR Lead", "0-5"],
    ["Customer Support / Ops",
     "KYC review queue, manual-review cases, customer communication on verification, complaints handling, disputes intake",
     "KYC SLA reports, case outcomes, complaint log",
     "Support / Ops Manager", "2-5"],
    ["Board / Executive",
     "Policy approval, CCO appointment, management review, oversight, resource allocation, risk appetite",
     "Signed policies, committee charter, management review minutes",
     "CEO / Board", "0-5"],
]
r = 4
for row in depts:
    for i, v in enumerate(row, 1):
        ws.cell(row=r, column=i, value=v)
    r += 1
style_body(ws, 4, r - 1, N)
ws.freeze_panes = "A4"

# ============================================================================
# SHEET 6: CODEBASE CHANGES
# ============================================================================
ws = wb.create_sheet("Codebase Changes")
N = 8
title_block(ws, "CODEBASE CHANGES (ENGINEERING BACKLOG)",
            "Concrete engineering items with current file locations. file:line references are best-effort and may shift.",
            N)
hdr = ["ID", "Change", "Current State", "Target State", "File / Location (ref)", "Priority", "Phase", "Regulatory Basis"]
for i, h in enumerate(hdr, 1):
    ws.cell(row=3, column=i, value=h)
style_header(ws, 3, N)
set_widths(ws, [7, 34, 40, 44, 40, 9, 14, 30])

code = [
    ["C1", "MFA for admin + merchant",
     "bcrypt password + session cookie only; single admin account",
     "TOTP/passkey required on login; authenticator secrets encrypted",
     "app.go auth handlers (login ~819-852, ~928); admin_sessions/merchant_sessions tables", "P0", "Phase 1", "ISO 8.5"],
    ["C2", "RBAC",
     "requireAdmin / requireMerchant middleware only",
     "Role model + permission checks; API-key scoping per merchant",
     "app.go:1631-1645 (requireAdmin), app.go:1662-1678 (requireMerchant)", "P0", "Phase 1", "ISO 8.2, 8.3, 5.15"],
    ["C3", "Rate limiting on public endpoints",
     "loginLimiter only on /admin/login and /merchant/login (5 fails / 15 min)",
     "Rate limit webhooks, /payments/return, /receipts, /invoices, /scan, /api/readers/scan",
     "app.go:1889-1931 (loginLimiter); app.go:216,242 (routes)", "P0", "Phase 1", "ISO 8.20"],
    ["C4", "Tamper-evident admin-action audit log",
     "payment_events only (store.go:3589-3625); no admin-action log",
     "audit_logs table + hash-chaining + middleware on privileged actions",
     "internal/store/store.go; new migration 021+", "P0", "Phase 1", "ISO 8.15; PCI Req 10"],
    ["C5", "Structured JSON logging + correlation IDs",
     "stdout logs; no correlation IDs; PII could leak to logs",
     "JSON schema, correlation IDs, PII redaction, 1-year retention",
     "all services (app.go, providers/*)", "P0", "Phase 1", "ISO 8.15-8.16"],
    ["C6", "Secrets management",
     ".env file; secrets committed in repo (db_password.txt, tokens)",
     "Docker secrets/KMS; rotation; no secrets in repo or env",
     "config.go; .env.example; repo hygiene", "P0", "Phase 1", "ISO 8.24"],
    ["C7", "Encryption at rest",
     "PII plaintext across users, individual_profiles, invoices, data_orders, sms_requests",
     "pgcrypto column encryption or volume encryption; KMS-managed keys",
     "migrations 001, 008, 011, 012, 013; store.go models", "P0", "Phase 1", "ISO 8.24; NDPR"],
    ["C8", "Retention purge: batching + coverage + audit",
     "PurgeBefore single transaction (store.go:4158-4200); daily worker (app.go:275-295)",
     "Batched deletes, extend to profiles/invoices/thrift, purge audit trail",
     "store.go PurgeBefore; app.go retentionTicker", "P1", "Phase 1", "ISO 8.10; NDPR"],
    ["C9", "KYC data model (migrations 021+)",
     "individual_profiles.kyc_status hardcoded 'approved_simulated' (store.go:2012,2018)",
     "customer_verifications, kyc_profiles, screening_results, risk_events, authenticators, trusted_devices, manual_review_cases",
     "internal/store/migrations/ (new)", "P0", "Phase 1", "CBN CIP/CDD"],
    ["C10", "Real KYC state machine (L0-L4)",
     "verification_level tops at email_confirmed (store.go:597-764)",
     "L0-L4 ladder with controlled transitions, approval workflow, audit",
     "internal/service/conversation.go (KYC gate ~708, ~3674); domain models", "P0", "Phase 2", "CBN CIP/CDD"],
    ["C11", "NIN/BVN verification integration",
     "None (simulated)",
     "Provider integration (Smile ID/Youverify/Dojah/Prembly) with liveness + document checks",
     "new provider package internal/providers/verify", "P0", "Phase 2", "CBN CIP verification"],
    ["C12", "Sanctions/PEP screening",
     "None",
     "Screen at onboarding + periodic rescreen; decision states clear/possible/strong/blocked/manually_cleared",
     "new screening service + screening_results table", "P0", "Phase 2", "CBN OFAC compliance"],
    ["C13", "Customer ML/FT risk scoring",
     "None",
     "Low/medium/high scoring model driving CDD vs EDD and monitoring",
     "risk_events table; risk scoring service", "P0", "Phase 2", "CBN risk-based approach"],
    ["C14", "Transaction monitoring rules engine",
     "None",
     "Velocity, structuring, round amounts, high-risk counterparties; alert + case management",
     "new internal/service/monitoring", "P0", "Phase 2", "CBN suspicious activity monitoring"],
    ["C15", "STR/CTR/PEP report generators",
     "None",
     "Extract regulator-format reports from audited payment/ledger data",
     "new internal/service/reporting; ledger events", "P1", "Phase 2", "MLPA 2004 reporting"],
    ["C16", "Ledger service (append-only)",
     "payments state machine only (domain/payment.go); no journal",
     "Double-entry append-only journal; reversals via offsetting entries",
     "new internal/service/ledger; ledger DB", "P0", "Phase 3", "CBN record-keeping"],
    ["C17", "Three-way reconciliation",
     "reconcile command exists for Paystack (cmd/demo reconcile)",
     "Internal vs ledger vs bank automated daily + weekly manual",
     "cmd/demo/reconcile; new service", "P0", "Phase 3", "CBN paper trail; PCI"],
    ["C18", "Chat content guard (no PIN/card/OTP)",
     "Help text only (conversation.go:3557)",
     "Automated block/sanitize of card/PIN/OTP data sent in chat; log attempts",
     "internal/service/conversation.go inbound handlers", "P1", "Phase 2", "PCI; CBN consumer protection"],
    ["C19", "HTTP server hardening (timeouts)",
     "Defaults (some unset)",
     "ReadHeaderTimeout 5s, Read 15s, Write 45s, Idle 60s, MaxHeaderBytes 1MB, shutdown 15s",
     "cmd/demo/main.go http.Server config", "P1", "Phase 3", "ISO 8.6; XLSX tuning"],
    ["C20", "Data-subject rights (access/erasure)",
     "No export or erasure endpoints; no consent records",
     "Data export endpoint; erasure flow with 30-day legal hold; consent timestamps",
     "new handlers; users/consent schema migration 021+", "P1", "Phase 2", "NDPR 3.1; ISO 8.10"],
    ["C21", "OTP hardening",
     "Unsalted SHA-256 codes (conversation.go:3978); resend resets attempt counter (conversation.go:622,755); demo in-chat path (config.go:87)",
     "bcrypt/argon2 with salt; resend cooldown; force-disable in-chat delivery in production",
     "conversation.go OTP flow; email_verification_codes table", "P0", "Phase 1", "PCI 8.3, 8.1.6; ISO 8.5"],
    ["C22", "Encryption in transit (DB + SMTP)",
     "sslmode=disable in compose DSNs (compose.yaml:23,35); smtp.PlainAuth without enforced TLS (smtp.go:36)",
     "sslmode=require/verify-full for PostgreSQL; STARTTLS with tls.Config for SMTP",
     "compose.yaml; internal/providers/email/smtp.go", "P0", "Phase 1", "ISO 8.24; PCI Req 4.1"],
    ["C23", "Backups / WAL / PITR",
     "No pg_dump/WAL archiving/restore in code or compose; RTO/RPO not in config",
     "Base backup + WAL archive to S3/GCS; restore drills; RTO/RPO configuration",
     "deploy scripts; new cmd; postgres config", "P1", "Phase 1", "ISO 8.13, 5.30"],
    ["C24", "Migration runner ordering + locking",
     "Lexical filename sort (store.go:549); no advisory lock; no down-migrations",
     "Zero-padded numeric sort; advisory lock around apply; down/rollback support",
     "internal/store/store.go Migrate", "P1", "Phase 1", "ISO 8.32"],
    ["C25", "Distributed rate limiting",
     "In-memory loginLimiter resets on restart (app.go:1894-1931); single-instance only",
     "Redis-backed limiter shared across replicas",
     "internal/app/app.go; new store backing", "P1", "Phase 1", "ISO 8.5; PCI Req 8.1.6"],
    ["C26", "Payment events tamper-evidence",
     "payment_events plain editable rows; no actor/IP/hash-chain (store.go:3570-3806)",
     "Actor + IP columns; hash chain/signature; append-only enforcement",
     "store.go event writers; migration 021+", "P0", "Phase 1", "PCI Req 10.3, 10.5; ISO 8.15"],
    ["C27", "Secrets rotation + git history purge",
     "Secrets committed in early git history; still on disk (gitignored)",
     "Rotate all keys/tokens; rewrite history (filter-repo); verify no residual blobs",
     "repo hygiene; config.go; .env.example", "P0", "Phase 1", "ISO 8.24"],
    ["C28", "Webhook secret out of URL",
     "VTPass secret accepted via ?secret= query param (app.go:617)",
     "Require secret in header; do not log URLs",
     "internal/app/app.go webhook handlers", "P1", "Phase 1", "ISO 8.24"],
    ["C29", "Retention legal/regulatory holds",
     "90-day hard-delete of payments conflicts with MLPA 7-year record keeping (store.go:4189); no hold mechanism",
     "Archive payments to append-only ledger; legal-hold flag and list; align retention with MLPA",
     "internal/store/store.go PurgeBefore", "P1", "Phase 1", "ISO 8.10; MLPA 2004"],
    ["C30", "Error / PII sanitization",
     "Provider error strings stored verbatim in webhook_deliveries.error_message / last_error (store.go:3965,3989)",
     "Redact/allowlist error fields; centralized log-masking policy",
     "internal/store/store.go; internal/app/app.go", "P1", "Phase 1", "ISO 8.16"],
]
r = 4
for row in code:
    for i, v in enumerate(row, 1):
        ws.cell(row=r, column=i, value=v)
    r += 1
style_body(ws, 4, r - 1, N)
for rr in range(4, r):
    pr = ws.cell(row=rr, column=6).value
    cell = ws.cell(row=rr, column=6)
    if pr == "P0":
        cell.fill = P0_FILL
    elif pr == "P1":
        cell.fill = P1_FILL
ws.freeze_panes = "A4"
ws.auto_filter.ref = f"A3:{get_column_letter(N)}3"

# ============================================================================
# SHEET 7: REGULATORY MAPPING
# ============================================================================
ws = wb.create_sheet("Regulatory Mapping")
N = 5
title_block(ws, "REGULATORY MAPPING",
            "Every regulatory requirement mapped to where it is addressed in this roadmap.",
            N)
hdr = ["Regulation / Clause", "Requirement / Control", "Where Addressed (Roadmap ID)", "Status", "Notes"]
for i, h in enumerate(hdr, 1):
    ws.cell(row=3, column=i, value=h)
style_header(ws, 3, N)
set_widths(ws, [30, 52, 30, 12, 40])

mapping = [
    ["ISO 27001:2022 Clause 4", "Context of the organization, scope of ISMS", "0.7", "Not Started", "ISMS scope statement"],
    ["ISO 27001:2022 Clause 5", "Leadership, policy, roles & responsibilities", "0.1, 0.2, 0.4", "Not Started", "Policy + CCO/CISO"],
    ["ISO 27001:2022 Clause 6", "Risk assessment & treatment, objectives, planning", "0.7, 4.3", "Not Started", "SoA + risk treatment plan"],
    ["ISO 27001:2022 Clause 7", "Resources, competence, awareness, communication, documentation", "0.11, 0.12, 3.3, 5.4", "Not Started", "Training + docs control"],
    ["ISO 27001:2022 Clause 8", "Operational planning & control, risk assessment & treatment", "3.1-3.10", "Not Started", "Implement controls"],
    ["ISO 27001:2022 Clause 9", "Performance evaluation, internal audit, management review", "4.2, 4.4, 5.7", "Not Started", "Audit + review"],
    ["ISO 27001:2022 Clause 10", "Continual improvement, nonconformity & corrective action", "4.3, 5.7", "Not Started", "Corrective action loop"],
    ["ISO Annex A 5 (Organizational)", "Policies, roles, asset mgmt, access, supplier security, incident mgmt, compliance, PII", "0.2, 0.8, 1.2, 3.1, 3.4, 3.8, 5.6", "Not Started", "37 controls"],
    ["ISO Annex A 6 (People)", "Screening, employment terms, training, disciplinary, NDAs, remote working, event reporting", "0.11, 0.12, 3.3, 5.4, 5.5", "Not Started", "8 controls"],
    ["ISO Annex A 7 (Physical)", "Physical perimeters, entry, monitoring, utilities, disposal", "1.10, 1.11 (cloud responsibilities)", "Not Started", "14 controls"],
    ["ISO Annex A 8 (Technological)", "Endpoint, privileged access, access restriction, source code, authentication, capacity, malware, vulns, logging, monitoring, cryptography, secure SDLC", "1.1-1.9, 3.2, 3.4, 3.10, C1-C30", "Not Started", "34 controls"],
    ["CBN AML/CFT - CIP", "Written Customer Identification Program, verify identity of each customer", "2.1, 2.2, C9, C10, C11", "Not Started", "Real KYC ladder"],
    ["CBN AML/CFT - CDD/EDD", "Customer due diligence; enhanced due diligence for higher risk", "2.4, 2.5, 2.6", "Not Started", "Risk-based"],
    ["CBN AML/CFT - Risk Assessment", "Two-step risk assessment, update every 12-18 months", "0.7, 5.3", "Not Started", "Institution + customer level"],
    ["CBN AML/CFT - STR", "Report suspicious transactions to NFIU, inform CBN, within 24h", "2.8, 5.1", "Not Started", "STR workflow"],
    ["CBN AML/CFT - CTR", "Currency transaction reports at threshold", "1.15, 5.1", "Not Started", "Report generators"],
    ["CBN AML/CFT - PEP", "Identify PEPs, senior mgmt approval, monthly returns to CBN", "2.3, 5.1, 5.2", "Not Started", "Screening + returns"],
    ["CBN AML/CFT - CCO", "Designate qualified individual (GM+) for AML/CFT compliance", "0.1", "Not Started", "Appointment"],
    ["CBN AML/CFT - Internal Controls", "System of internal controls ensuring ongoing compliance", "1.4, 2.7, 3.1", "Not Started", "Controls + monitoring"],
    ["CBN AML/CFT - Independent Testing", "Independent audit of program every 12-18 months", "4.1", "Not Started", "Independent test"],
    ["CBN AML/CFT - Training", "Train appropriate personnel; document programs", "3.3, 5.4", "Not Started", "Records"],
    ["CBN AML/CFT - Record Keeping", "Maintain paper trail for MLPA/regulatory needs", "3.5, 3.6, C16, C17", "Not Started", "Ledger + 7-year records"],
    ["CBN AML/CFT - OFAC/Sanctions", "OFAC monitoring, reporting, compliance program", "2.3, 5.2", "Not Started", "Screening"],
    ["NDPR 2019", "NITDA registration, lawful processing, privacy, breach notification", "0.9, 3.8", "Not Started", "Privacy program"],
    ["PCI DSS Level 1", "Requirements 1-12: segmentation, encryption, access, logging, scanning, IR; QSA RoC + AOC", "3.2, 3.9, 4.7", "Not Started", "QSA RoC (Level 1) path"],
]
r = 4
for row in mapping:
    for i, v in enumerate(row, 1):
        ws.cell(row=r, column=i, value=v)
    r += 1
style_body(ws, 4, r - 1, N)
ws.freeze_panes = "A4"
ws.auto_filter.ref = f"A3:{get_column_letter(N)}3"

# ============================================================================
# SHEET 8: COMPLIANCE DOCUMENTS REGISTER
# ============================================================================
ws = wb.create_sheet("Compliance Documents")
N = 9
title_block(ws, "COMPLIANCE DOCUMENTS REGISTER",
            "Every policy, procedure, register, report and evidence pack needed to run and prove compliance. "
            "Starter templates for all documents (except NITDA registration) are drafted in docs/compliance/compliance_docs/.",
            N)
hdr = ["Document / Register", "Category", "Owner (Dept)", "Regulatory Basis", "Template Status", "Phase", "Description / Key Content", "Roadmap Link", "Template File (docs/compliance/compliance_docs/)"]
for i, h in enumerate(hdr, 1):
    ws.cell(row=3, column=i, value=h)
style_header(ws, 3, N)
set_widths(ws, [38, 15, 20, 30, 15, 8, 58, 12, 44])

docs = [
    ["Information Security Policy", "Policy", "Board / Top Mgmt", "ISO 27001:2022 Cl. 5.2; Annex A 5.1", "Draft available", "0", "Top-management approved statement of security objectives, scope, roles, responsibilities and review cadence.", "0.2"],
    ["AML/CFT Compliance Policy & Program", "Policy", "Board / CCO", "CBN AML/CFT Regulation 2009", "Draft available", "0", "Written risk-based program: CDD/EDD, STR/CTR, PEP, record keeping, training, independent testing.", "0.3"],
    ["Privacy & Data Protection Policy", "Policy", "Compliance / Legal", "NDPR 2019; ISO A 5.34", "Draft available", "0", "Lawful basis, collection limits, consent, data-subject rights, retention, breach notification.", "3.8"],
    ["Risk Management Policy", "Policy", "Board / Risk", "ISO 27001:2022 Cl. 6.1", "Draft available", "0", "Risk appetite, assessment methodology, treatment options, re-assessment triggers.", "0.7"],
    ["AML/CFT Oversight Committee Charter", "Governance", "Board", "CBN AML/CFT Regulation 2009", "Draft available", "0", "Mandate, composition, meeting cadence, escalation to CCO/CEO/Board.", "0.4"],
    ["ISMS Scope Document", "Governance", "CISO", "ISO 27001:2022 Cl. 4.3", "Draft available", "0", "Boundaries of the ISMS: systems, locations, processes, interfaces and exclusions.", "0.7"],
    ["Vendor / Third-Party Risk Policy + DPAs", "Policy", "Procurement / Compliance", "ISO A 5.19-5.23; NDPR", "Draft available", "0", "Supplier classification, due diligence, data-processing agreements, ongoing monitoring.", "0.9"],
    ["Access Control Policy", "Policy", "Security", "ISO A 5.15-5.18, A 8.2", "Draft available", "1", "Least privilege, RBAC roles, joiner/mover/leaver, entitlement review frequency.", "1.2"],
    ["Cryptography / Key Management Policy", "Policy", "Security", "ISO A 8.24", "Draft available", "1", "Approved algorithms, key lifecycle, at-rest and in-transit encryption standards.", "1.7"],
    ["Change Management Procedure", "Procedure", "Ops / Security", "ISO A 8.32", "Draft available", "1", "Change classification, approval, testing, backout and post-change review.", "1.14"],
    ["Retention & Destruction Schedule", "Procedure", "Ops / Compliance", "MLPA record keeping; ISO A 8.10", "Draft available", "1", "Retention periods per data class, secure destruction, purge controls (90-day worker).", "1.8"],
    ["Incident Response Plan", "Procedure", "Security", "ISO A 5.24-5.28", "Draft available", "3", "Roles, severity levels, containment, communication, evidence handling, post-incident review.", "3.1"],
    ["Business Continuity / DR Plan", "Procedure", "Ops / Risk", "ISO A 5.29-5.30", "Draft available", "3", "RTO/RPO targets, BCP team, DR runbooks, restore drill schedule.", "3.7"],
    ["Backup & Restore Runbook", "Procedure", "Ops", "ISO A 8.13", "Draft available", "1", "Backup schedule, WAL/PITR archiving, quarterly restore test, RTO/RPO targets.", "1.12"],
    ["Enterprise Risk Register", "Risk", "Risk", "ISO 27001:2022 Cl. 6.1", "Draft available", "0", "Risk ID, likelihood/impact, owner, treatment decision, residual risk.", "0.7"],
    ["InfoSec Risk Assessment & Treatment Plan", "Risk", "CISO", "ISO 27001:2022 Cl. 6.1-6.3", "Draft available", "0", "Full risk assessment, treatment decisions, basis for the Statement of Applicability.", "0.7"],
    ["Statement of Applicability (SoA)", "Risk", "CISO", "ISO 27001:2022 Cl. 6.1.3", "Draft available", "4", "Every Annex A control listed as applicable / not-applicable with justification and references.", "4.5"],
    ["Institutional ML/FT Risk Assessment", "Risk", "Compliance", "CBN AML/CFT Regulation 2009", "Draft available", "0", "Country, customer, product, service and channel risk assessment; refresh every 12-18 months.", "5.3"],
    ["Customer Risk Scoring Methodology", "Risk", "Compliance", "CBN CDD/EDD", "Draft available", "2", "L0-L4 KYC ladder, risk factors, scoring weights, review triggers.", "2.4"],
    ["CDD Procedure", "Procedure", "Compliance", "CBN AML/CFT Regulation 2009", "Draft available", "2", "Identity, address, occupation and purpose-of-account verification steps and record keeping.", "2.5"],
    ["EDD Procedure (PEP / High-Risk)", "Procedure", "Compliance", "CBN AML/CFT Regulation 2009", "Draft available", "2", "Enhanced checks, senior-management approval, ongoing monitoring of higher-risk customers.", "2.6"],
    ["Sanctions / Screening Procedure", "Procedure", "Compliance", "OFAC/UN/EU lists; CBN", "Draft available", "2", "List sources, match handling, freezing and reporting of hits.", "2.3"],
    ["STR / CTR Workflow Procedure", "Procedure", "Compliance", "MLPA 2004", "Draft available", "2", "Alert to case, investigation, filing; 24-hour STR deadline; CTR thresholds.", "2.8"],
    ["Suspicious Transaction Register", "Register", "Compliance", "MLPA 2004", "Draft available", "2", "Chronological record of every STR identified/filed.", "5.1"],
    ["Currency Transaction Register", "Register", "Compliance", "MLPA 2004", "Draft available", "2", "CTR-threshold transactions including daily aggregates.", "5.1"],
    ["Data Inventory / Residency Register", "Register", "Compliance / Security", "NDPR; ISO A 5.34", "Draft available", "0", "Where customer PII resides, lawful basis, processor list.", "0.9"],
    ["Asset Inventory", "Register", "Security", "ISO A 5.9", "Draft available", "0", "Servers, services, repositories, accounts with owners.", "0.8"],
    ["Supplier / DPA Register", "Register", "Procurement", "ISO A 5.19; NDPR", "Draft available", "0", "Vendors, signed DPAs, renewal dates, risk classification.", "0.9"],
    ["Privileged Access / Audit Log Register", "Register", "Security", "ISO A 8.15", "Draft available", "1", "Admin logins, KYC decisions, permission changes, payment events.", "1.4"],
    ["Joiner-Mover-Leaver Register", "Register", "HR / Security", "ISO A 8.2", "Draft available", "1", "Provisioning, role changes and offboarding evidence.", "5.5"],
    ["Training Records / Competence Matrix", "Register", "HR / Compliance", "ISO 7.2-7.3; CBN", "Draft available", "0", "Required competence, attendees, dates, materials, test results.", "0.11"],
    ["NDA Register", "Register", "HR", "ISO A 6.1", "Draft available", "0", "Signed confidentiality agreements for all staff and contractors.", "0.12"],
    ["STR Submissions", "Report", "Compliance", "MLPA 2004", "Draft available", "5", "Filed to NFIU / CBN within 24 hours of detection.", "5.1"],
    ["CTR Submissions", "Report", "Compliance", "MLPA 2004", "Draft available", "5", "Filed at / above the currency transaction threshold.", "5.1"],
    ["PEP Transaction Returns", "Report", "Compliance", "CBN B1402-B1403", "Draft available", "5", "Monthly PEP transaction returns to the CBN.", "5.1"],
    ["Management Review Minutes", "Report", "Board", "ISO 27001:2022 Cl. 9.3", "Draft available", "4", "Review of audit results, incidents, risk, objectives and decisions.", "4.4"],
    ["Internal Audit Reports", "Report", "Audit", "ISO 9.2; CBN independent testing", "Draft available", "4", "Audit scope, findings, corrective actions and follow-up.", "4.2"],
    ["Vulnerability / Pen-Test Findings Register", "Report", "Security", "PCI Req 11; ISO A 8.8", "Draft available", "3", "Quarterly ASV scans, annual penetration test, remediation tracking.", "3.2"],
    ["DR Restore Test Reports", "Report", "Ops / Risk", "ISO A 5.29-5.30", "Draft available", "3", "Quarterly restore drill results vs RTO/RPO targets.", "3.7"],
    ["InfoSec Metrics (monthly)", "Report", "Security", "ISO 27001:2022 Cl. 9.1", "Draft available", "3", "KPIs: incidents, scan failures, error rates, access-review completion.", "5.7"],
    ["NITDA Registration / Certificate", "Evidence", "Compliance", "NDPR 2019", "To obtain", "0", "Register as data controller/processor with NITDA.", "3.8"],
    ["ISO 27001 Stage 1 & 2 Audit Evidence Packs", "Evidence", "CISO", "ISO 27001:2022", "Draft available", "4", "Documented ISMS, SoA, risk treatment and records for the certification body.", "4.5"],
    ["CBN AML/CFT Examination Evidence Pack", "Evidence", "Compliance", "CBN AML/CFT Regulation 2009", "Draft available", "4", "Policies, program, registers, returns, testing and training evidence.", "4.6"],
    ["PCI DSS Attestation of Compliance (AOC) - Level 1", "Evidence", "Security / Finance", "PCI DSS Level 1; Req 12", "Draft available", "3", "Level 1 (more than 6M transactions/yr): QSA-conducted RoC and signed AOC; SAQ-A only if fully outsourced.", "3.9"],
    ["PCI DSS Scope & CDE Inventory", "Governance", "Security / CISO", "PCI DSS Req 1, 2, 11", "Draft available", "3", "Cardholder Data Environment boundary, network segmentation (isolated PCI zone), in-scope systems, card data flows, tokenization.", "3.9"],
    ["PCI DSS Requirements 1-12 Compliance Matrix", "Evidence", "Security / CISO", "PCI DSS Req 12", "Draft available", "3", "Req-by-req applicability, key controls, evidence owners and status for the Level 1 QSA RoC.", "3.9"],
]
DOC_FILES = {
    "Information Security Policy": "01_Information_Security_Policy.md",
    "AML/CFT Compliance Policy & Program": "02_AML_CFT_Compliance_Policy_and_Program.md",
    "Privacy & Data Protection Policy": "03_Privacy_and_Data_Protection_Policy.md",
    "Risk Management Policy": "04_Risk_Management_Policy.md",
    "AML/CFT Oversight Committee Charter": "05_AML_CFT_Oversight_Committee_Charter.md",
    "ISMS Scope Document": "06_ISMS_Scope_Document.md",
    "Vendor / Third-Party Risk Policy + DPAs": "07_Vendor_Third-Party_Risk_Policy.md",
    "Access Control Policy": "08_Access_Control_Policy.md",
    "Cryptography / Key Management Policy": "09_Cryptography_Key_Management_Policy.md",
    "Change Management Procedure": "10_Change_Management_Procedure.md",
    "Retention & Destruction Schedule": "11_Retention_Destruction_Schedule.md",
    "Incident Response Plan": "12_Incident_Response_Plan.md",
    "Business Continuity / DR Plan": "13_Business_Continuity_DR_Plan.md",
    "Backup & Restore Runbook": "14_Backup_Restore_Runbook.md",
    "Enterprise Risk Register": "15_Enterprise_Risk_Register.md",
    "InfoSec Risk Assessment & Treatment Plan": "16_InfoSec_Risk_Assessment_Treatment_Plan.md",
    "Statement of Applicability (SoA)": "17_Statement_of_Applicability.md",
    "Institutional ML/FT Risk Assessment": "18_Institutional_ML_FT_Risk_Assessment.md",
    "Customer Risk Scoring Methodology": "19_Customer_Risk_Scoring_Methodology.md",
    "CDD Procedure": "20_CDD_Procedure.md",
    "EDD Procedure (PEP / High-Risk)": "21_EDD_Procedure.md",
    "Sanctions / Screening Procedure": "22_Sanctions_Screening_Procedure.md",
    "STR / CTR Workflow Procedure": "23_STR_CTR_Workflow_Procedure.md",
    "Suspicious Transaction Register": "24_Suspicious_Transaction_Register.md",
    "Currency Transaction Register": "25_Currency_Transaction_Register.md",
    "Data Inventory / Residency Register": "26_Data_Inventory_Residency_Register.md",
    "Asset Inventory": "27_Asset_Inventory.md",
    "Supplier / DPA Register": "28_Supplier_DPA_Register.md",
    "Privileged Access / Audit Log Register": "29_Privileged_Access_Audit_Log_Register.md",
    "Joiner-Mover-Leaver Register": "30_Joiner_Mover_Leaver_Register.md",
    "Training Records / Competence Matrix": "31_Training_Records_Competence_Matrix.md",
    "NDA Register": "32_NDA_Register.md",
    "STR Submissions": "33_STR_Submissions.md",
    "CTR Submissions": "34_CTR_Submissions.md",
    "PEP Transaction Returns": "35_PEP_Transaction_Returns.md",
    "Management Review Minutes": "36_Management_Review_Minutes.md",
    "Internal Audit Reports": "37_Internal_Audit_Reports.md",
    "Vulnerability / Pen-Test Findings Register": "38_Vulnerability_PenTest_Findings_Register.md",
    "DR Restore Test Reports": "39_DR_Restore_Test_Reports.md",
    "InfoSec Metrics (monthly)": "40_InfoSec_Metrics.md",
    "NITDA Registration / Certificate": "N/A - regulatory registration (NITDA)",
    "ISO 27001 Stage 1 & 2 Audit Evidence Packs": "42_ISO27001_Audit_Evidence_Packs.md",
    "CBN AML/CFT Examination Evidence Pack": "43_CBN_AML_CFT_Examination_Evidence_Pack.md",
    "PCI DSS Attestation of Compliance (AOC) - Level 1": "44_PCI_DSS_Attestation_of_Compliance.md",
    "PCI DSS Scope & CDE Inventory": "45_PCI_DSS_Scope_CDE_Inventory.md",
    "PCI DSS Requirements 1-12 Compliance Matrix": "46_PCI_DSS_Requirements_Compliance_Matrix.md",
}
for row in docs:
    row.append(DOC_FILES.get(row[0], "N/A"))
r = 4
for row in docs:
    for i, v in enumerate(row, 1):
        ws.cell(row=r, column=i, value=v)
    r += 1
style_body(ws, 4, r - 1, N)
for rr in range(4, r):
    phase = str(ws.cell(row=rr, column=6).value)
    if phase in PHASE_FILLS:
        ws.cell(row=rr, column=6).fill = PHASE_FILLS[phase]
    if ws.cell(row=rr, column=5).value == "Draft available":
        ws.cell(row=rr, column=5).font = Font(name="Calibri", size=10, bold=True, color="1F7A1F")
ws.freeze_panes = "A4"
ws.auto_filter.ref = f"A3:{get_column_letter(N)}3"

# ----------------------------------------------------------------------------
# Workbook properties + save
# ----------------------------------------------------------------------------
wb.properties.title = "Xego Financial Institution Compliance Roadmap"
wb.properties.subject = "ISO 27001:2022 + CBN AML/CFT Compliance Roadmap"
wb.properties.creator = "Xego Compliance Program"
wb.properties.description = (
    "Comprehensive compliance roadmap for Xego covering governance, engineering (codebase), "
    "infrastructure & deployment, KYC/CDD/AML, reporting, audit, certification and all affected departments."
)

out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "Compliance_Roadmap.xlsx")
wb.save(out)
print("Wrote:", out)
