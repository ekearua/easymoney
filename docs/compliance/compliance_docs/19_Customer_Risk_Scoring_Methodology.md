---
title: CUSTOMER RISK SCORING METHODOLOGY
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# CUSTOMER RISK SCORING METHODOLOGY

| | |
|---|---|
| **Document Owner** | Compliance (CCO) |
| **Approved By** | CCO / Board |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually |
| **Regulatory Basis** | CBN AML/CFT CDD/EDD |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AML/CFT | Anti-Money Laundering / Combating the Financing of Terrorism |
| CBN | Central Bank of Nigeria |
| CCO | Chief Compliance Officer |
| CDD | Customer Due Diligence |
| CDD/EDD | Customer Due Diligence / Enhanced Due Diligence |
| EDD | Enhanced Due Diligence |
| ID | Identifier |
| KYC | Know Your Customer |
| L0 | KYC tier L0 - basic / unverified (receive-only) |
| L0-L4 | KYC verification tiers L0-L4 (see individual entries) |
| L1 | KYC tier L1 - NIN/BVN-verified (low limits) |
| L2 | KYC tier L2 - government-ID verified (standard limits) |
| L3 | KYC tier L3 - full CDD (higher limits) |
| L4 | KYC tier L4 - enhanced due diligence (highest limits) |
| NIN/BVN | National Identification Number / Bank Verification Number |
| PEP | Politically Exposed Person |

## 1. Purpose

Define how customers are scored and tiered (L0-L4) so that CDD/EDD intensity and limits are proportionate to risk.

## 2. KYC Ladder

| Tier | Verification Level | Example Limits | Risk |
|---|---|---|---|
| L0 | Phone + basic details only | No wallet / receive only | Low |
| L1 | NIN/BVN verification | Low send limits | Low |
| L2 | Government ID + selfie (provider verification) | Standard limits | Medium |
| L3 | Full CDD: ID, address, occupation, purpose | Higher limits | Medium-High |
| L4 | EDD: source of funds, senior mgmt approval (PEP/high risk) | Highest / approved limits | High |
| | | | |
| | | | |
| | | | |

## 3. Risk Factors and Weights

| Factor | Indicator | Points |
|---|---|---|
| Geography | High-risk / sanctioned-adjacent jurisdiction | +3 |
| Occupation / sector | Cash-intensive, gambling, crypto-adjacent | +2 |
| Product usage | Rapid pass-through, structuring-like patterns | +2 |
| PEP status | PEP or related | +3 (auto EDD) |
| Transaction behaviour | Unusual frequency/value vs profile | +2 |
| Negative news / screening hit | Sanctions or adverse media | +3 |
| | | |
| | | |
| | | |

## 4. Decision Rules

- Score 0-2: Low (standard CDD, standard monitoring).
- Score 3-5: Medium (standard CDD + periodic review).
- Score 6+: High (EDD per EDD Procedure; senior management approval required).
- Any sanctions match: refuse/freeze and report per Screening Procedure.

## 5. Review Triggers

Re-score on material changes (new products, location change, adverse events, monitoring alerts, threshold crossings) and on periodic rescreening.

## 6. References

CBN AML/CFT Regulation 2009; CDD and EDD Procedures.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
