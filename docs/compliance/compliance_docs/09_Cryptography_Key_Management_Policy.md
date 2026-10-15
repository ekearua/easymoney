---
title: CRYPTOGRAPHY / KEY MANAGEMENT POLICY
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# CRYPTOGRAPHY / KEY MANAGEMENT POLICY

| | |
|---|---|
| **Document Owner** | Security / CISO |
| **Approved By** | Board of Directors |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 8.24 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| AES-256 | Advanced Encryption Standard with 256-bit keys |
| CI/CD | Continuous Integration / Continuous Delivery |
| CISO | Chief Information Security Officer |
| EC | Elliptic Curve (cryptography) |
| HSM | Hardware Security Module |
| ID | Identifier |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| JWT | JSON Web Token |
| KMS | Key Management Service |
| PII | Personally Identifiable Information |
| RS256/HS256 | JWT signing algorithms (RSA-SHA256 / HMAC-SHA256) |
| RSA-2048 | Rivest-Shamir-Adleman with 2048-bit keys |
| TLS | Transport Layer Security |

## 1. Purpose

Define the approved cryptographic controls and key lifecycle for protecting Xego customer data at rest and in transit.

## 2. Scope

All data at rest and in transit, all keys, certificates and secrets used by the platform, infrastructure and CI/CD.

## 3. Approved Controls

- Data in transit: TLS 1.2+ (1.3 preferred) with strong ciphers; internal service-to-service traffic also encrypted.
- Data at rest: PII columns encrypted with pgcrypto (AES-256) and/or volume/disk encryption; backups encrypted.
- Passwords/hashes: Argon2id or bcrypt (cost >= 10); never stored in plaintext.
- Tokens/sessions: JWT signed with RS256/HS256 with short expiry; secrets stored in a vault, never in source code.
- Signing: strong asymmetric keys; key size at least RSA-2048 or EC P-256.

## 4. Key Lifecycle

- Generation: within the vault/HSM; never in plaintext on disk or in repos.
- Distribution: only via approved secrets management; access logged and least-privilege.
- Storage: encrypted vault (e.g., Vault/cloud KMS); service accounts using short-lived credentials.
- Rotation: keys rotated on schedule (TLS annually, signing keys 12-18 months) and immediately on compromise.
- Backup: escrowed/backed-up keys recoverable via break-glass procedure.
- Destruction: secure deletion of keys and cryptographic material on decommissioning.

## 5. Responsibilities

| Role | Responsibility |
|---|---|
| CISO / Security | Ownership of this policy, key inventory, rotation schedule |
| Engineering | Use approved libraries and avoid custom crypto |
| Ops | TLS certificate lifecycle, vault operations, break-glass procedures |
| | |
| | |
| | |

## 6. References

ISO/IEC 27001:2022 Annex A 8.24; Asset Inventory.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
