#!/usr/bin/env python3
"""Add a 'Definitions and Abbreviations' section to every compliance template
so no acronym/abbreviation is used without an in-document reference.

For each *.md it detects which glossary terms actually occur in the body and
injects a two-column table (Term | Definition) before the first numbered
section. Idempotent: skips files that already have the section.
"""
import glob
import os
import re

HERE = os.path.dirname(os.path.abspath(__file__))

PAT = re.compile(r"\b[A-Z][A-Z0-9]{1,}(?:[&\-/][A-Z0-9]+)*\b")
MIXED = re.compile(r"\b(?:SoA|RoC|IaC|DDoS|DoS|HSM)\b")

GLOSSARY = {
    # --- Regulatory & organisational ---
    "AML/CFT": "Anti-Money Laundering / Combating the Financing of Terrorism",
    "AML": "Anti-Money Laundering",
    "ML/FT": "Money Laundering / Financing of Terrorism",
    "CBN": "Central Bank of Nigeria",
    "MLPA": "Money Laundering (Prohibition) Act 2004",
    "NDPR": "Nigeria Data Protection Regulation 2019",
    "NITDA": "National Information Technology Development Agency (Nigeria's data-protection regulator)",
    "NFIU": "Nigerian Financial Intelligence Unit",
    "FATF": "Financial Action Task Force",
    "IFI": "International Financial Institutions",
    "FATF/IFI": "Financial Action Task Force / International Financial Institutions",
    "OFAC": "Office of Foreign Assets Control (U.S. Department of the Treasury)",
    "UN": "United Nations",
    "EU": "European Union",
    "OFAC/UN/EU": "OFAC, UN and EU sanctions lists (see individual entries)",
    "CBN/NFIU": "Central Bank of Nigeria / Nigerian Financial Intelligence Unit",
    "CBN/NFIU/NITDA": "Central Bank of Nigeria / Nigerian Financial Intelligence Unit / NITDA (see individual entries)",
    "RBS": "Risk-Based Supervision (CBN regulatory framework)",
    "B1402-B1403": "CBN-prescribed forms for Politically Exposed Person transaction returns",
    "CIP": "Customer Identification Program",
    "CDD": "Customer Due Diligence",
    "EDD": "Enhanced Due Diligence",
    "KYC": "Know Your Customer",
    "PEP": "Politically Exposed Person",
    "STR": "Suspicious Transaction Report",
    "CTR": "Currency Transaction Report",
    "STR/CTR": "Suspicious Transaction Report / Currency Transaction Report",
    "STR/CTR/PEP": "STR, CTR and PEP returns (see individual entries)",
    "CDD/EDD": "Customer Due Diligence / Enhanced Due Diligence",
    "CIP/CDD": "Customer Identification Program / Customer Due Diligence",
    "KYC/AML": "Know Your Customer / Anti-Money Laundering",
    "KYC/CDD/EDD": "Know Your Customer / Customer Due Diligence / Enhanced Due Diligence",
    "KYC/EDD": "Know Your Customer / Enhanced Due Diligence",
    "NIN/BVN": "National Identification Number / Bank Verification Number",
    "NGN": "Nigerian Naira",
    "DOB": "Date of Birth",
    "DBA": "Doing Business As (legal / trading name)",
    # --- ISO 27001 / ISMS ---
    "ISO": "International Organization for Standardization",
    "ISO/IEC": "International Organization for Standardization / International Electrotechnical Commission",
    "ISMS": "Information Security Management System",
    "SoA": "Statement of Applicability (ISO/IEC 27001:2022 control-applicability statement)",
    "A5": "ISO/IEC 27001:2022 Annex A Group A.5 - Organizational controls",
    "A6": "ISO/IEC 27001:2022 Annex A Group A.6 - People controls",
    "A7": "ISO/IEC 27001:2022 Annex A Group A.7 - Physical controls",
    "A8": "ISO/IEC 27001:2022 Annex A Group A.8 - Technological controls",
    "SDLC": "Software Development Life Cycle",
    "CAB": "Change Advisory Board",
    "CI": "Continuous Integration",
    "CI/CD": "Continuous Integration / Continuous Delivery",
    "IaC": "Infrastructure as Code",
    "RTO": "Recovery Time Objective",
    "RPO": "Recovery Point Objective",
    "RTO/RPO": "Recovery Time Objective / Recovery Point Objective",
    "BCP": "Business Continuity Plan",
    "DR": "Disaster Recovery",
    "BCP/DR": "Business Continuity Plan / Disaster Recovery",
    "IRP": "Incident Response Plan",
    "IR": "Incident Response",
    "ICT": "Information and Communication Technology",
    "PITR": "Point-in-Time Recovery",
    "WAL": "Write-Ahead Log",
    "PITR/WAL": "Point-in-Time Recovery using Write-Ahead Logs",
    "WAL/S3": "Write-Ahead Log archive on S3 (see S3)",
    "S3": "AWS Simple Storage Service (cloud object storage)",
    "GCS": "Google Cloud Storage",
    "S3/GCS": "AWS Simple Storage Service / Google Cloud Storage",
    "DDoS": "Distributed Denial of Service",
    "DoS": "Denial of Service",
    "HA": "High Availability",
    "LB": "Load Balancer",
    "DNS": "Domain Name System",
    "OS": "Operating System",
    # --- Engineering / security technology ---
    "DB": "Database",
    "API": "Application Programming Interface",
    "JSON": "JavaScript Object Notation",
    "ID": "Identifier",
    "IP": "Internet Protocol",
    "VPC": "Virtual Private Cloud",
    "WAF": "Web Application Firewall",
    "TLS": "Transport Layer Security",
    "MFA": "Multi-Factor Authentication",
    "TOTP": "Time-based One-Time Password",
    "MFA/TOTP": "Multi-Factor Authentication using TOTP (see individual entries)",
    "OTP": "One-Time Password",
    "SSO": "Single Sign-On",
    "SSO/MFA": "Single Sign-On / Multi-Factor Authentication",
    "RBAC": "Role-Based Access Control",
    "JML": "Joiner / Mover / Leaver",
    "NDA": "Non-Disclosure Agreement",
    "DPA": "Data Processing Agreement",
    "DPO": "Data Protection Officer",
    "CCO": "Chief Compliance Officer",
    "CISO": "Chief Information Security Officer",
    "CISO/CCO": "Chief Information Security Officer / Chief Compliance Officer",
    "CEO": "Chief Executive Officer",
    "CFO": "Chief Financial Officer",
    "HR": "Human Resources",
    "PR": "Public Relations",
    "IT": "Information Technology",
    "PII": "Personally Identifiable Information",
    "SIEM": "Security Information and Event Management",
    "KMS": "Key Management Service",
    "HSM": "Hardware Security Module",
    "AES-256": "Advanced Encryption Standard with 256-bit keys",
    "RSA-2048": "Rivest-Shamir-Adleman with 2048-bit keys",
    "EC": "Elliptic Curve (cryptography)",
    "JWT": "JSON Web Token",
    "RS256/HS256": "JWT signing algorithms (RSA-SHA256 / HMAC-SHA256)",
    "SOC": "System and Organization Controls (audit report, e.g., SOC 2)",
    # --- PCI DSS ---
    "PCI": "Payment Card Industry",
    "DSS": "Data Security Standard",
    "PCI DSS": "Payment Card Industry Data Security Standard",
    "CDE": "Cardholder Data Environment (systems and network segments that store, process or transmit cardholder data)",
    "PAN": "Primary Account Number (cardholder account number)",
    "VLAN": "Virtual Local Area Network",
    "mTLS": "Mutual Transport Layer Security (two-way certificate-based TLS)",
    "QSA": "Qualified Security Assessor",
    "SAQ-A": "Self-Assessment Questionnaire A (PCI DSS)",
    "RoC": "Report on Compliance",
    "AOC": "Attestation of Compliance",
    "ASV": "Approved Scanning Vendor",
    "CVSS": "Common Vulnerability Scoring System",
    "SAST": "Static Application Security Testing",
    "DAST": "Dynamic Application Security Testing",
    "DAST/SAST": "Dynamic / Static Application Security Testing",
    "DLP": "Data Loss Prevention",
    "MDM": "Mobile Device Management",
    "NTP": "Network Time Protocol",
    "OWASP": "Open Worldwide Application Security Project",
    "SDN": "Specially Designated Nationals (OFAC sanctions list)",
    "P2P": "Peer-to-Peer",
    "MTTR": "Mean Time To Resolve",
    # --- Risk / KYC tiers & priorities ---
    "L0": "KYC tier L0 - basic / unverified (receive-only)",
    "L1": "KYC tier L1 - NIN/BVN-verified (low limits)",
    "L2": "KYC tier L2 - government-ID verified (standard limits)",
    "L3": "KYC tier L3 - full CDD (higher limits)",
    "L4": "KYC tier L4 - enhanced due diligence (highest limits)",
    "L0-L4": "KYC verification tiers L0-L4 (see individual entries)",
    "P0": "Risk priority P0 - extreme (immediate action)",
    "P1": "Risk priority P1 - high",
    "P2": "Risk priority P2 - medium",
    "P3": "Risk priority P3 - low",
    "P0/P1": "Risk priorities P0-P1 (extreme / high)",
    "P0/P1/P2/P3": "Risk priorities P0-P1-P2-P3 (see individual entries)",
}


def used_terms(body):
    terms = set(m.group(0) for m in PAT.finditer(body))
    terms |= set(m.group(0) for m in MIXED.finditer(body))
    return terms


def build_section(body):
    terms = sorted(t for t in used_terms(body) if t in GLOSSARY)
    lines = ["## Definitions and Abbreviations", "", "| Term | Definition |",
             "|---|---|"]
    for t in terms:
        lines.append("| %s | %s |" % (t, GLOSSARY[t]))
    lines.append("")
    return "\n".join(lines), terms


def main():
    files = sorted(glob.glob(os.path.join(HERE, "*.md")))
    total_terms = 0
    for f in files:
        with open(f, encoding="utf-8") as fh:
            text = fh.read()
        if "## Definitions and Abbreviations" in text:
            print("skip (already has section):", os.path.basename(f))
            continue
        body = text
        if "```{=openxml}" in body:
            body = body.split("```{=openxml}", 1)[1]
        section, terms = build_section(body)
        idx = text.find("\n## ")
        if idx == -1:
            print("WARN no section heading to insert before:", os.path.basename(f))
            continue
        new_text = text[:idx] + "\n" + section + text[idx:]
        with open(f, "w", encoding="utf-8") as fh:
            fh.write(new_text)
        print("%-55s %d terms" % (os.path.basename(f), len(terms)))
        total_terms += len(terms)
    print("done, total terms across docs:", total_terms)


if __name__ == "__main__":
    main()
