---
title: CUSTOMER DUE DILIGENCE (CDD) PROCEDURE
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# CUSTOMER DUE DILIGENCE (CDD) PROCEDURE

| | |
|---|---|
| **Document Owner** | Compliance |
| **Approved By** | CCO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually |
| **Regulatory Basis** | CBN AML/CFT Regulation 2009 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AML/CFT | Anti-Money Laundering / Combating the Financing of Terrorism |
| CBN | Central Bank of Nigeria |
| CCO | Chief Compliance Officer |
| CDD | Customer Due Diligence |
| CIP | Customer Identification Program |
| CIP/CDD | Customer Identification Program / Customer Due Diligence |
| DOB | Date of Birth |
| ID | Identifier |
| L0 | KYC tier L0 - basic / unverified (receive-only) |
| L0-L4 | KYC verification tiers L0-L4 (see individual entries) |
| NIN/BVN | National Identification Number / Bank Verification Number |
| OTP | One-Time Password |

## 1. Purpose

Standard operating procedure for identifying and verifying customers before account/transaction service, meeting CIP and CDD requirements.

## 2. Process

- 1. Capture: collect legal name, phone, DOB, address, occupation and stated purpose of account.
- 2. Verify identity: submit to a licensed Nigerian identity verification provider (e.g., Smile ID, Youverify) using government-issued ID + selfie; NIN/BVN checks where applicable.
- 3. Verify address / contact: via utility-style proof or confirmed device/phone binding (OTP).
- 4. Screen: sanctions and adverse media screening (see Sanctions / Screening Procedure).
- 5. Tier and score: assign L0-L4 tier and risk score per Customer Risk Scoring Methodology.
- 6. Record: store verification responses, decisions and evidence with the customer record (customer_verifications / kyc_profile tables).
- 7. No service before completion: unverified customers cannot hold a wallet or transact above L0.

## 3. CDD Record Contents

Verification provider reference, ID type/number, verification result and date, screening result, tier, risk score, approver, and basis for any refusal. Records retained 7 years per the Retention Schedule.

## 4. Re-verification

Re-verify at periodic intervals (per risk tier), on significant transaction-profile changes, or when verification data expires.

## 5. References

CBN AML/CFT Regulation 2009 (CIP/CDD); Customer Risk Scoring Methodology; Sanctions/Screening Procedure.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
