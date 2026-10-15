---
title: BACKUP & RESTORE RUNBOOK
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# BACKUP & RESTORE RUNBOOK

| | |
|---|---|
| **Document Owner** | Ops |
| **Approved By** | Head of Ops / CISO |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Quarterly review |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 8.13 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| CI/CD | Continuous Integration / Continuous Delivery |
| CISO | Chief Information Security Officer |
| DB | Database |
| DR | Disaster Recovery |
| ID | Identifier |
| ISO | International Organization for Standardization |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| KMS | Key Management Service |
| PITR | Point-in-Time Recovery |
| RPO | Recovery Point Objective |
| RTO | Recovery Time Objective |
| RTO/RPO | Recovery Time Objective / Recovery Point Objective |
| S3/GCS | AWS Simple Storage Service / Google Cloud Storage |
| WAL | Write-Ahead Log |

## 1. Purpose

Provide the operational procedures for backing up production data and restoring it within RTO/RPO targets.

## 2. Backup Inventory

| Asset | Backup Method | Frequency | Retention | Location |
|---|---|---|---|---|
| PostgreSQL database | Base backup + continuous WAL archiving (wal_level=replica) to S3/GCS | Base daily, WAL continuous | Per retention schedule; PITR window | Encrypted object storage, separate region |
| Config / infrastructure as code | Versioned in CI/CD | On change | Git history | Repository |
| Secrets | Vault snapshot / KMS-managed | On change | Per key policy | Vault / cloud KMS |
| Certificates / keys | Escrow copy | On issuance | Until renewal | Vault |
| | | | | |
| | | | | |
| | | | | |

## 3. Restore Procedures

- 1. Verify backup existence and integrity for the target point-in-time.
- 2. Provision/restore database from base backup.
- 3. Replay WAL segments to the desired point-in-time (PITR).
- 4. Run integrity checks (pg_dump/checksums, ledger reconciliation).
- 5. Point application instances at restored DB after smoke tests.
- 6. Record restore attempt, timings and success in DR Restore Test Reports.

## 4. Success Criteria

Restore completes within the RTO for the service and loses no more than the RPO; reconciliation matches the ledger.

## 5. Monitoring

Backup jobs are monitored; failures alert on-call within 15 minutes; a missed backup is treated as an incident.

## 6. References

ISO A 8.13; Business Continuity / DR Plan.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
