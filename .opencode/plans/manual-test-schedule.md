# Plan: Progressive Tiered Manual Test Schedule (FINAL)

## Goal
Regenerate `MANUAL_TEST_SCHEDULE.md` and `MANUAL_TEST_SCHEDULE.xlsx` with:
1. 175 test cases reordered into a progressive, tiered user journey
2. Excel as a shareable multi-cycle test execution tracker

## New Phase Structure (15 phases, 175 cases)

| Phase | Title | Cases | Tier | Story |
|-------|-------|-------|------|-------|
| P1 | System Setup & Health | 5 | System | Set up the demo environment |
| P2 | Customer Onboarding (All Channels) | 10 | L0 | Customer discovers WhatsApp/Telegram/Instagram/TikTok |
| P3 | L0 — Card Checkout | 10 | L0 | Customer pays with card |
| P4 | L0 — Bank Transfer | 8 | L0 | Customer pays by bank transfer |
| P5 | L0 — Mobile Data Purchase | 8 | L0 | Customer buys mobile data |
| P6 | L0→L1 — Wallet & KYC Upgrade | 10 | L0→L1 | Admin advances to L1; wallet activates; pay from wallet |
| P7 | L1 — Invoices | 12 | L1 | Merchant sends invoice; customer pays |
| P8 | L1+ — Thrift Group Savings | 10 | L1+ | Customer becomes individual; creates/joins thrift |
| P9 | L2 — Individual Pay (C2C) | 6 | L2 | Customer advances to L2; sends money |
| P10 | Merchant Onboarding & Console | 12 | Merchant | Customer registers as merchant; admin approves |
| P11 | Merchant Services & Events | 10 | Merchant | Merchant sets up services and events |
| P12 | Partner API Integration | 22 | Merchant/API | Merchant integrates server-to-server |
| P13 | Settlements, Refunds & Disputes | 10 | Both | Settlements, refunds, disputes |
| P14 | Admin Console & RBAC | 20 | Admin | Operator management, KYC/KYB review, metrics |
| P15 | Security, Audit & Compliance | 12 | Admin/System | Audit, reconciliation, SIEM, encryption, retention |

## Excel Tracker Structure (3 types of sheets)

### "Dashboard" sheet
- Environment fields (URL, git commit, deployment date, Interswitch mode, DB, tester name)
- Per-cycle summary table: Cycle #, Date Range, Cases Executed, Passed, Failed, Blocked, Pass Rate %, Open Issues, Status
- Per-phase breakdown per cycle below
- Conditional formatting (red/yellow/green status)

### "Execution Log" sheet
- Filterable cycle tracker: Cycle | Phase | Case ID | Title | Priority | Result (dropdown) | Tested By | Date | Issue ID | Issue Description | Severity (dropdown) | Screenshots | Notes
- Data validation dropdowns on Result and Severity
- Auto-filters on every column
- Supports multiple cycles side-by-side via the Cycle filter

### Phase sheets (P1-P15) = Reference only
- Case details: ID, Module, Actor, Channel, Title, Preconditions, Steps, Expected Result, Priority
- No execution columns — clean reference for the tester
- Collapsible details per case (in MD) / row-per-case (in Excel)

## MD Structure
- Setup/prereqs section (same as current)
- Demo data reference
- Progressive phase sections with `<details>` collapsible cases
- Each phase has "Prerequisites: complete Phases 1..N-1" note
- Phase transition note: "On completing this phase, you unlock: [next phase description]"

## Implementation
1. Edit gen_schedule.py PHASES data structure to match new ordering
2. Renumber case IDs: TC-{phase}-{seq}
3. Add new Excel sheets: Dashboard, Execution Log
4. Add data validation dropdowns, conditional formatting, auto-filters
5. Re-run generator
6. Verify outputs
