---
title: BUSINESS CONTINUITY / DISASTER RECOVERY PLAN
subtitle: Xego WhatsApp/Telme Payment Platform
author: Xego Compliance Program
date: July 2026
---

```{=openxml}
<w:p><w:r><w:br w:type="page"/></w:r></w:p>
```

# BUSINESS CONTINUITY / DISASTER RECOVERY PLAN

| | |
|---|---|
| **Document Owner** | Ops / Risk |
| **Approved By** | Board / Top Management |
| **Version** | 0.1 (draft for approval) |
| **Effective Date** | [DD Month YYYY] |
| **Next Review** | Annually and after each drill or incident |
| **Regulatory Basis** | ISO/IEC 27001:2022 Annex A 5.29-5.30 |
---

## Definitions and Abbreviations

| Term | Definition |
|---|---|
| BCP | Business Continuity Plan |
| DNS | Domain Name System |
| DR | Disaster Recovery |
| ID | Identifier |
| IRP | Incident Response Plan |
| ISO/IEC | International Organization for Standardization / International Electrotechnical Commission |
| LB | Load Balancer |
| PITR | Point-in-Time Recovery |
| PR | Public Relations |
| RPO | Recovery Point Objective |
| RTO | Recovery Time Objective |
| RTO/RPO | Recovery Time Objective / Recovery Point Objective |
| WAL | Write-Ahead Log |
| WAL/S3 | Write-Ahead Log archive on S3 (see S3) |

## 1. Purpose

Define how Xego continues critical services and recovers within agreed targets during and after disruptions, including infrastructure failures, data centre loss and cyber incidents.

## 2. Business Impact Summary

| Service | RTO | RPO | Recovery Strategy |
|---|---|---|---|
| Payment / transaction processing | 4 hours | 15 minutes | Multi-AZ failover; PITR to WAL archive |
| Ledger / reconciliation | 4 hours | 15 minutes | Database PITR from WAL/S3 |
| Messaging / chat platform | 1 hour | 30 minutes | Stateless services behind load balancer |
| Admin / merchant portals | 2 hours | 15 minutes | Multi-instance, LB failover |
| Reporting / STR-CTR extraction | 12 hours | 24 hours | Re-run from ledger |
| | | | |
| | | | |
| | | | |

## 3. BCP Team and Activation

- Incident Commander (Ops lead) activates the plan; BCP team comprises Ops, Security, Compliance, Engineering and PR.
- Activation triggers: data centre loss, prolonged outage beyond RTO threshold, major incident under the IRP, cloud provider regional failure.
- Status is tracked on an incident channel and reported to the Risk Committee and Board per severity.

## 4. Recovery Procedures

- 1. Declare disaster; notify BCP team and stakeholders.
- 2. Promote standby environment / re-point DNS and load balancer to healthy instances.
- 3. Restore database via PITR (base backup + WAL to target point).
- 4. Validate data integrity, ledger consistency and reconciliation before resuming customer traffic.
- 5. Resume traffic gradually; verify monitoring and webhook delivery.
- 6. Post-recovery review and documentation.

## 5. Testing and Maintenance

Quarterly restore tests (see DR Restore Test Reports) verify RTO/RPO; results and corrective actions are reported to the Risk Committee.

## 6. References

ISO/IEC 27001:2022 Annex A 5.29-5.30; Backup & Restore Runbook; Incident Response Plan.

## Document Control

| Version | Date | Author | Change Description |
|---|---|---|---|
| 0.1 | | | Initial draft for approval |
