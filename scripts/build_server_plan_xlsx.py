#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Build docs/cost-models/Xego_Server_and_Hosting_Plan.xlsx - a three-sheet workbook:

  "Server Requirements" - right-sized per-phase server plan plus a compliance-safe
                          scaling path (in-country 2027 target topology)
  "Local Hosting"       - Nigerian VPS / colocation reference: the 2027 in-country
                          target build (reuses the researched content)
  "Cloud First Deploy"  - GCP africa-south1 hybrid launch (managed LB + managed
                          Postgres, app VMs), CI/CD config and the dated 2027
                          data-plane cutover (compliance posture B)

Raw OpenXML, stdlib only (openpyxl segfaults on this box). Run from the repo root:

    python scripts/build_server_plan_xlsx.py

The hosting content is imported from scripts/add_local_hosting_sheet.py so the
researched prices have a single source of truth; sections 1 and 3 are overridden
with the optimized Phase-1 sizing.
"""
import importlib.util
import os
import re
import zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(os.path.dirname(HERE), "docs", "cost-models", "Xego_Server_and_Hosting_Plan.xlsx")
NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
RELNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

# ---------------------------------------------------------------------------
# import the hosting sheet's researched content (same columns, same style names)
# ---------------------------------------------------------------------------
_spec = importlib.util.spec_from_file_location(
    "add_local_hosting_sheet", os.path.join(HERE, "add_local_hosting_sheet.py"))
_alh = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_alh)

HOST_COLS = _alh.COLS
HOST_ROWS = _alh.ROWS

# ---------------------------------------------------------------------------
# shared style palette (name -> cellXf index in STYLES_XML below)
# ---------------------------------------------------------------------------
STYLES = {
    "title": 1, "sub": 2, "sec": 3, "th": 4, "td": 5, "num": 6, "tot": 7,
    "note": 8, "pass": 9, "warn": 10, "na": 11, "term": 12, "numtot": 13,
}
MERGED = {"title", "sub", "sec", "note"}
ROW_HEIGHTS = {"title": 30, "sub": 46, "sec": 22}

STYLES_XML = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="%s">
<fonts count="4">
 <font><sz val="11"/><name val="Calibri"/></font>
 <font><b/><color rgb="FFFFFFFF"/><sz val="11"/><name val="Calibri"/></font>
 <font><b/><color rgb="FF1F4E79"/><sz val="11"/><name val="Calibri"/></font>
 <font><b/><sz val="13"/><name val="Calibri"/></font>
</fonts>
<fills count="8">
 <fill><patternFill patternType="none"/></fill>
 <fill><patternFill patternType="gray125"/></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FF2E75B6"/><bgColor indexed="64"/></patternFill></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FFD9E2F3"/><bgColor indexed="64"/></patternFill></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FFFFF2CC"/><bgColor indexed="64"/></patternFill></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FFC6EFCE"/><bgColor indexed="64"/></patternFill></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FFFFEB9C"/><bgColor indexed="64"/></patternFill></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FFFFC7CE"/><bgColor indexed="64"/></patternFill></fill>
</fills>
<borders count="2">
 <border><left/><right/><top/><bottom/><diagonal/></border>
 <border><left style="thin"><color auto="1"/></left><right style="thin"><color auto="1"/></right><top style="thin"><color auto="1"/></top><bottom style="thin"><color auto="1"/></bottom><diagonal/></border>
</borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="14">
 <xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
 <xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment horizontal="left" vertical="center" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="0" fillId="4" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="2" fillId="3" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="center" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment horizontal="center" vertical="center" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1" applyAlignment="1"><alignment horizontal="right" vertical="top" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="2" fillId="3" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="0" fillId="5" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="0" fillId="6" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="0" fillId="7" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="2" fillId="0" borderId="1" xfId="0" applyFont="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>
 <xf numFmtId="0" fontId="2" fillId="3" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment horizontal="right" vertical="top" wrapText="1"/></xf>
</cellXfs>
</styleSheet>''' % NS

# ===========================================================================
# SHEET 1: Server Requirements (optimized + compliance-safe scaling)
# ===========================================================================
SERVER_COLS = [("A", 12), ("B", 30), ("C", 8), ("D", 8), ("E", 26), ("F", 40), ("G", 34), ("H", 16)]

SERVER_ROWS = [
    ("title", ["Server Requirements & Compliance-Safe Scaling Plan"]),
    ("sub", ["Phase-1 boxes are right-sized to Phase-1 load (~1.8M txns/yr, ~2k concurrent) plus HA floors - not Phase-3 capacity. "
             "The data plane is provisioned in-country on day 1 and scaled up / replicated in place, so growth never forces a cross-border "
             "data migration (CBN Circular PSS/DIR/PUB/CIR/001/004 - payment data generated in Nigeria stored/managed in Nigeria by 1 Jan 2027). "
             "First deploy goes to the managed cloud (posture B - see the Cloud First Deploy sheet): a temporary cross-border residence "
             "allowed only under that sheet's dated data-plane cutover plan."]),
    ("spacer", []),

    ("sec", ["1. Optimized sizing by phase"]),
    ("th", ["Phase", "Users / concurrency", "Servers", "vCPU", "RAM (GB)", "Disk", "Workload it must carry", "Compliance posture"]),
    ("td", ["Phase 1 (launch)", "10k users / ~2k concurrent", ("3", "num"), ("8", "num"), ("16", "num"),
            "240 GB (incl. 60 GB staging)", "Monolith + payment core; 2 app replicas, 29 worker slots, pool=30",
            ("In-country DB; SAQ-A scope", "pass")]),
    ("td", ["Phase 2 (growth)", "50-100k users / ~5k concurrent", "3 -> 5 (+1 app, +1 replica)", ("16", "num"), ("32", "num"),
            "~520 GB (incl. staging)", "Extract core services; add app servers + DB RAM; add read replica",
            ("Replica in-country; app out of CDE", "pass")]),
    ("td", ["Phase 3 (scale)", "75k concurrent / 13.5B txn", ("9", "num"), ("50", "num"), ("90", "num"),
            "1,420 GB", "Dedicated LB, 4 app replicas, 2 DB boxes, monitoring/storage box",
            ("Full PCI DSS L1; all in-country", "pass")]),
    ("tot", ["TOTAL (Phase 1)", "10k users / ~2k concurrent", ("3", "numtot"), ("8", "numtot"), ("16", "numtot"),
             "240 GB (incl. 60 GB staging)", "Scale out app, scale up DB in place", ""]),
    ("note", ["First draft used Phase-3-class Phase-1 boxes (20 vCPU / 36 GB / 450 GB). The optimized build is ~1/3 the compute, "
              "and Phase 1 and Phase 2 share the same data plane; Phase 3 scales out rather than replaces."]),
    ("spacer", []),

    ("sec", ["2. Phase 1 (launch) - server-by-server breakdown (3 servers)"]),
    ("th", ["Server", "Role", "vCPU", "RAM (GB)", "Disk", "Key services", "Purpose / scaling headroom", ""]),
    ("td", ["Server 1", "Edge / load balancer + app replica 1", ("2", "num"), ("4", "num"), "40 GB SSD (100 provisioned)",
            "Caddy (TLS, reverse proxy), app replica 1", "Single public entry point; add a replica on a new box - no data impact", ""]),
    ("td", ["Server 2", "App replica 2 + background workers", ("2", "num"), ("4", "num"), "40 GB SSD (100 provisioned)",
            "app replica 2, payment/risk/outbox workers; hosts the P1 async standby",
            "Second app instance for HA; scale out by adding app boxes", ""]),
    ("td", ["Server 3", "Primary database + backup staging (data plane)", ("4", "num"), ("8", "num"),
            "100 GB data + 60 GB staging (160 GB NVMe)",
            "PostgreSQL 17 primary, PgBouncer, token DB, WAL + daily base backup staging",
            "System of record; scale up RAM/disk in place; later add an in-country read replica", ""]),
    ("tot", ["TOTAL", "3 servers", ("8", "numtot"), ("16", "numtot"), "240 GB (incl. 60 GB staging)", "", ""]),
    ("note", ["Server 3 is the compliance anchor: it holds the system of record and the in-country backup target. Growing it, or adding an "
              "in-country replica, is an in-place change - it never moves the data across a border."]),
    ("note", ["Event bus: the in-memory Kafka-compatible bus is the default (EVENT_BUS=memory) and needs no broker. A single Kafka broker "
              "is optional at launch and can co-locate on Server 3 until the first external consumer exists."]),
    ("spacer", []),

    ("sec", ["3. Phase 2 (growth) - server-by-server breakdown (5 servers)"]),
    ("th", ["Server", "Role", "vCPU", "RAM (GB)", "Disk", "Key services", "What changed from Phase 1", ""]),
    ("td", ["Server 1", "Edge / load balancer + app replica 1", ("2", "num"), ("4", "num"), "40 GB SSD (100 provisioned)",
            "Caddy (TLS), app replica 1", "Unchanged", ""]),
    ("td", ["Server 2", "App replica 2 + background workers", ("2", "num"), ("4", "num"), "40 GB SSD (100 provisioned)",
            "app replica 2, payment/risk/outbox workers", "Unchanged; P1 async standby retires to the new DB replica", ""]),
    ("td", ["Server 3", "Primary database (scaled up)", ("8", "num"), ("16", "num"),
            "220 GB data + 60 GB staging (280 GB NVMe)",
            "PostgreSQL 17 primary, PgBouncer (pool -> 40), token DB, WAL/base backup staging",
            "RAM 8 -> 16 GB and disk grown in place; stream replication to Server 4", ""]),
    ("td", ["Server 4 (new)", "Read replica + reporting", ("2", "num"), ("4", "num"), "120 GB NVMe",
            "In-country streaming replica; admin / reporting reads", "Offloads read traffic from the primary; residency preserved", ""]),
    ("td", ["Server 5 (new)", "App replica 3 + extracted services", ("2", "num"), ("4", "num"), "40 GB SSD",
            "app replica 3; Payment Orchestrator + Risk extracted (tokenization stays on the data plane with its DB)",
            "Service extraction begins; tokenization stays isolated to the data plane until Server 6 at Phase 3", ""]),
    ("tot", ["TOTAL", "5 servers", ("16", "numtot"), ("32", "numtot"), "520 GB (incl. 60 GB staging)", "", ""]),
    ("note", ["Phase 2 keeps the same in-country data plane (Server 3) and adds Server 4 as an in-country read replica, so scaling reads "
              "never crosses a border. The app plane grows by one box (Server 5) while services are extracted."]),
    ("note", ["Event bus: the in-memory bus still suffices at low volume. Add a single Kafka broker (co-located or on a small box) at ~5k "
              "users, when the first external consumer appears; the 3-node Kafka backbone remains Phase 3."]),
    ("spacer", []),

    ("sec", ["4. Phase 3 (scale) - server-by-server breakdown (9 servers)"]),
    ("th", ["Server", "Role", "vCPU", "RAM (GB)", "Disk", "Key services", "Purpose", ""]),
    ("td", ["Server 1", "Dedicated load balancer / API gateway", ("2", "num"), ("2", "num"), "40 GB SSD",
            "TLS termination, routing, rate limiting", "Dedicated edge, decoupled from app", ""]),
    ("td", ["Server 2", "App replica 1", ("4", "num"), ("8", "num"), "80 GB SSD",
            "App replica 1 (stateless)", "Horizontal app scale-out", ""]),
    ("td", ["Server 3", "App replica 2", ("4", "num"), ("8", "num"), "80 GB SSD",
            "App replica 2 (stateless)", "Horizontal app scale-out", ""]),
    ("td", ["Server 4", "App replica 3", ("4", "num"), ("8", "num"), "80 GB SSD",
            "App replica 3 (stateless)", "Horizontal app scale-out", ""]),
    ("td", ["Server 5", "App replica 4", ("4", "num"), ("8", "num"), "80 GB SSD",
            "App replica 4 (stateless)", "Horizontal app scale-out", ""]),
    ("td", ["Server 6", "Risk + Tokenization + token DB", ("8", "num"), ("16", "num"), "200 GB NVMe",
            "Risk engine, tokenization service, token database", "Keeps card data off the app tier; isolated CDE", ""]),
    ("td", ["Server 7", "Primary database", ("12", "num"), ("16", "num"), "400 GB NVMe",
            "PostgreSQL 17 primary, PgBouncer", "System of record", ""]),
    ("td", ["Server 8", "Database standby / read replica (synchronous)", ("8", "num"), ("16", "num"), "400 GB NVMe",
            "Synchronous replica; failover target and read offload", "HA + read scaling, in-country", ""]),
    ("td", ["Server 9", "Monitoring + object storage / backup", ("4", "num"), ("8", "num"), "60 GB + backup storage",
            "Prometheus/Grafana, logs, 3-node Kafka event backbone, MinIO backup target", "Observability and in-country backup (closes G16)", ""]),
    ("tot", ["TOTAL", "9 servers", ("50", "numtot"), ("90", "numtot"), "1,420 GB", "", ""]),
    ("note", ["Phase 3 is the full PCI DSS Level 1 topology: dedicated edge, 4 stateless app replicas, an isolated risk/token box, a "
              "primary + synchronous standby database pair, and a monitoring/backup box. Every node that holds payment data stays in-country."]),
    ("spacer", []),

    ("sec", ["5. Compliance-safe scaling path (what triggers the next step)"]),
    ("th", ["Step", "Trigger", "Action", "Compliance impact", "Relative cost", "", "", ""]),
    ("td", ["1 -> 2a: app scale-out", "Peak CPU > ~60% or >2k concurrent sustained",
            "Add 1 app server (app replica 3) behind Caddy",
            ("None - stateless app; hosted fields keep card data off app servers", "pass"), "Low", "", "", ""]),
    ("td", ["1 -> 2b: DB scale-up", "DB cache hit rate drops / disk > ~60%",
            "Raise Server-3 RAM to 16 GB and grow disk in place",
            ("None - same box, same country", "pass"), "Low", "", "", ""]),
    ("td", ["2 -> 2c: read replica", "Reporting / admin reads compete with writes",
            "Add an in-country streaming read replica",
            ("Residency preserved (replica in Nigeria)", "pass"), "Medium", "", "", ""]),
    ("td", ["2 -> 2d: DB disk scale-up", "Data volume approaches ~60% of the DB data tier (100 GB at launch)",
            "Grow Server-3 disk in place; archive aged WAL/base copies to in-country cold storage",
            ("None - same box; archive target stays in Nigeria", "pass"), "Low", "", "", ""]),
    ("td", ["2 -> 2e: backup staging growth", "Daily WAL + base backups exceed the 60 GB staging tier",
            "Expand backup staging / object storage in-country",
            ("Residency preserved (G16 target in Nigeria)", "pass"), "Low", "", "", ""]),
    ("td", ["2 -> 2f: pool & connection tuning", "PgBouncer pool=30 saturated; app waits on the DB",
            "Raise PgBouncer pool 30 -> 40 -> 75 by phase and PG max_connections; lift the hardcoded worker batch limits",
            ("None - config change, same facility", "pass"), "Low", "", "", ""]),
    ("td", ["2 -> 3a: service extraction", "Payment Orchestrator, Risk or Tokenization contend with the monolith",
            "Extract core services onto dedicated boxes per the Deployment Plan",
            ("App stays out of CDE; token service isolated", "warn"), "Medium", "", "", ""]),
    ("td", ["3a -> 3b: dedicated DB tier", "One DB box cannot meet write concurrency or the HA target",
            "Move to 2x 16 GB DB boxes (primary + sync standby)",
            ("Residency preserved (both in-country)", "pass"), "Medium", "", "", ""]),
    ("td", ["3b -> 3c: observability & archive", "Monitoring, Kafka and object storage need isolation from the data plane",
            "Add a dedicated monitoring/storage box and a separate backup target",
            ("Backup target stays in-country (closes G16)", "pass"), "Medium", "", "", ""]),
    ("td", ["3 -> full: PCI DSS Level 1", "Sustained >75k concurrent / multi-service platform",
            "9-server topology; formal QSA assessment",
            ("PCI DSS Level 1 scope review (QSA); all in-country", "warn"), "High", "", "", ""]),
    ("note", ["Rule of thumb: scale the stateless app plane out (no compliance impact) and the data plane up / replicate in place. "
              "The pre-2027 managed-cloud launch (posture B) is the one sanctioned exception: a single, dated data-plane cutover back "
              "into Nigeria, rehearsed quarterly - see the Cloud First Deploy sheet. Never migrate the database across a jurisdiction "
              "outside that plan."]),
    ("spacer", []),

    ("sec", ["6. Compliance rules that make scaling cheap"]),
    ("th", ["Rule", "Why it matters", "How this plan satisfies it", "", "", "", "", ""]),
    ("td", ["Data residency (CBN)", "Payment data generated in Nigeria must be stored/managed in Nigeria by 1 Jan 2027",
            "2027 target state: DB, token DB and the offsite backup target sit in-country from day 1 of the cutover. "
            "Pre-2027 launch may run in the managed cloud only under the Cloud First Deploy sheet's dated cutover plan", "", "", "", "", ""]),
    ("td", ["Minimise PCI scope", "Every server touching card data expands the audit",
            "Hosted-fields checkout (SAQ-A); card data never hits app servers; tokenization isolated on the data plane", "", "", "", "", ""]),
    ("td", ["Site diversity", "A single facility outage must not take the DB down",
            "In-country: primary DB and P1 async standby in different facilities, backups in a third. "
            "Cloud (pre-2027): zone-redundant HA across two Joburg zones + managed PITR - no second African "
            "GCP region exists (nearest 6,256 km)", "", "", "", "", ""]),
    ("td", ["Backups in-country", "Offsite WAL/base copies are in scope for residency (gap G16)",
            "In-country: rclone target = self-hosted MinIO on a Lagos box or an in-country S3-compatible service. "
            "Cloud (pre-2027): managed PITR stays in-region and counts as temporary residency; it migrates "
            "with the data plane, and the permanent target is in-country", "", "", "", "", ""]),
    ("td", ["Change control", "Scaling events can trip DR re-test / audit obligations",
            "App scale-out is a config change; DB changes follow the release/backup gate; DR re-tested per runbook", "", "", "", "", ""]),
    ("spacer", []),

    ("sec", ["7. Why this is efficient (rightsizing rationale)"]),
    ("th", ["Dimension", "First draft", "Optimized Phase 1", "Rationale", "", "", "", ""]),
    ("td", ["App servers", "8 + 12 GB", "2 x 4 GB", "Stateless, I/O-bound; 2k concurrent needs far less RAM than provisioned", "", "", "", "", ""]),
    ("td", ["Database", "16 GB", "8 GB (grow to 16 at Phase 2)", "Phase-1 working set fits in 8 GB; RAM is the spec that grows with data", "", "", "", "", ""]),
    ("td", ["Total fleet", "20 vCPU / 36 GB / 450 GB", "8 vCPU / 16 GB / 240 GB", "Phase-1 boxes no longer sized to Phase-3 capacity", "", "", "", "", ""]),
    ("td", ["Scaling model", "Buy Phase-3 upfront", "Scale out app / scale up DB in place", "Removes ~2 orders of magnitude of idle capacity; keeps residency fixed", "", "", "", "", ""]),
    ("spacer", []),
    ("note", ["Server counts, worker slots and pool sizes match the workbook's Deployment Plan and Concurrency sheets. "
              "Prices for the optimized build are on the Local Hosting sheet (combination A)."]),
]

# ===========================================================================
# SHEET 2: Local Hosting - researched content with optimized sections 1 and 3
# ===========================================================================
SECTION1_OPT = [
    ("sec", ["1. Phase-1 server requirements (optimized - 3 servers, right-sized)"]),
    ("th", ["Server", "Role", "vCPU", "RAM (GB)", "Disk (GB)", "Est. NGN/mo*", "Notes", ""]),
    ("td", ["Server 1", "Edge / LB + app replica", ("2", "num"), ("4", "num"), ("100 SSD (40 used)", "num"), ("36,550", "num"),
            "Caddy (TLS) + app replica 1; priced as WhoGoHost C4 (2/4/100)", ""]),
    ("td", ["Server 2", "App + workers", ("2", "num"), ("4", "num"), ("100 SSD (40 used)", "num"), ("36,550", "num"),
            "Payment/risk/outbox workers; also hosts the P1 async standby", ""]),
    ("td", ["Server 3", "Database + backup staging", ("4", "num"), ("8", "num"), ("160 NVMe (100 data + 60 staging)", "num"), ("66,700", "num"),
            "PostgreSQL 17, PgBouncer, token DB, WAL + daily base backup staging; priced as ListedHosting D8 4/8/160 NVMe "
            "(confirm DC; fallback HOSTAFRICA DA2 6/12/300 at 113,100); grow to 16 GB at Phase 2", ""]),
    ("tot", ["TOTAL", "3 servers", ("8", "numtot"), ("16", "numtot"), ("360 provisioned (240 in use)", "numtot"), ("139,800", "numtot"),
             "Annual NGN 1,677,600; 3-year NGN 5,032,800 (was NGN 13,158,000 at 8/12/16)", ""]),
    ("note", ["*Right-sized to Phase-1 load plus HA floors, not Phase-3 capacity. The data plane (Server 3) is provisioned in-country in the "
              "2027 target state and scaled up in place, so growth never requires a cross-border migration. See the Server Requirements "
              "sheet for the scaling path. Disk column shows what the target plan ships: the two C4 app boxes arrive with 100 GB each but "
              "only need 40 GB, and the D8 database box's 160 GB is split 100 GB data / 60 GB staging - the rclone offsite copy stages to "
              "Server 2 at no extra cost. Requirement total 240 GB, provisioned 360 GB."]),
    ("spacer", []),
]

SECTION3_OPT = [
    ("sec", ["3. Hosting combinations - in-country 2027 target build (monthly / 3-year)"]),
    ("th", ["#", "Combination", "Server 1 (edge)", "Server 2 (app)", "Server 3 (db)", "Monthly NGN", "3-yr TCO (NGN)", "Locality"]),
    ("td", ["A", "Target - all-naira app boxes + NVMe DB (ListedHosting D8)", "C4 2/4: 36,550", "C4 2/4: 36,550", "D8 4/8/160 NVMe: 66,700",
            ("139,800", "num"), ("5,032,800", "num"), ("Likely NG - confirm DC before commit", "warn")]),
    ("td", ["B", "Fallback - larger DB box, second provider (HOSTAFRICA DA2)", "C4 2/4: 36,550", "C4 2/4: 36,550", "DA2 6/12/300: 113,100",
            ("186,200", "num"), ("6,703,200", "num"), ("Confirm DC; diversifies the DB off the app provider", "warn")]),
    ("td", ["C", "Mixed in-country + FX (AFRICLOUD database)", "C4 2/4: 36,550", "C4 2/4: 36,550", "AFRICLOUD VM6 4/8: 135,000",
            ("208,100", "num"), ("7,491,600", "num"), ("Yes - part billed USD (forex risk)", "warn")]),
    ("td", ["D", "Colocation, owned hardware", "(see section 4)", "(see section 4)", "(see section 4)",
            ("see 4", "num"), ("see 4", "num"), ("Yes - Rack Centre / Equinix Lagos", "pass")]),
    ("tot", ["", "LOWEST monthly = A (target build)", "", "", "", ("139,800", "numtot"), ("5,032,800", "numtot"),
             "Cheapest, all-naira, DC confirmation outstanding"]),
    ("note", ["Monthly = the three server line items summed; 3-yr TCO = monthly x 36 (VPS options have no meaningful one-time cost). "
              "This table is the 2027 in-country target state - launch on the managed cloud first (Cloud First Deploy sheet), then cut the "
              "data plane over before 1 Jan 2027. D8 must have its data-centre location confirmed; DA2 is the committed fallback and puts "
              "the database on a different provider from the app boxes (site diversity). Options priced in USD are labelled, not converted."]),
    ("spacer", []),
]


def replace_section(rows, prefix, new_block):
    out, i = [], 0
    while i < len(rows):
        style, cells = rows[i]
        if style == "sec" and str(cells[0]).startswith(prefix):
            out.extend(new_block)
            i += 1
            while i < len(rows) and rows[i][0] != "sec":
                i += 1
            continue
        out.append(rows[i])
        i += 1
    return out


HOSTING_ROWS = replace_section(HOST_ROWS, "1. Phase-1 server requirements", SECTION1_OPT)
HOSTING_ROWS = replace_section(HOSTING_ROWS, "3. Hosting combinations", SECTION3_OPT)


# ===========================================================================
# SHEET 3: Cloud First Deploy - GCP africa-south1 hybrid launch (posture B)
# ===========================================================================
CLOUD_COLS = [("A", 26), ("B", 30), ("C", 34), ("D", 34), ("E", 30), ("F", 16)]

CLOUD_ROWS = [
    ("title", ["Cloud First Deploy - GCP africa-south1 (Johannesburg)"]),
    ("sub", ["First deploy goes to managed cloud, not in-country: hybrid architecture - cloud-managed load balancer + cloud-managed "
             "Postgres, app tier on VMs running the existing compose stack and deploy/release.sh. Compliance posture B: full cloud now, "
             "data-plane cutover into Nigeria before 1 Jan 2027 (CBN PSS/DIR/PUB/CIR/001/004). The Local Hosting sheet stays the 2027 "
             "target state. Indicative list prices, Oct 2026, exclude VAT; FX NGN 1,500/USD."]),
    ("spacer", []),

    ("sec", ["1. Provider selection (scope: South Africa region only - no Nigerian locations)"]),
    ("th", ["Provider", "Zone HA / managed Postgres", "Managed Kafka in region", "Other factors", "Verdict"]),
    ("td", ["GCP", "3 zones (a/b/c); Cloud SQL zone-redundant HA + automated PITR + read replicas",
            "Managed Service for Apache Kafka - africa-south1 is GCP's only African region, rack-aware 3-zone",
            "Global HTTP(S) LB with HTTP/3 replaces the edge box; only 41% of VM types exist but every 2-4 vCPU type we need is "
            "present; region price rank #9/42 (+29.6% vs cheapest)", ("Selected", "pass")]),
    ("td", ["AWS", "3 AZs; RDS Multi-AZ; MSK serverless GA in af-south-1", "MSK available",
            "Close second; 34% EC2 catalogue / basket +19.9% (#32/36) barely matters at our size", ("Alternative", "warn")]),
    ("td", ["Azure", "3 zones; Flexible Server zone-redundant HA; only candidate with a second South Africa region (Cape Town) for DR",
            "Event Hubs Kafka API - not managed Kafka",
            "Cross-region DR inside Africa is the one advantage; ~+35% premium", ("DR option", "warn")]),
    ("td", ["OCI", "af-johannesburg-1 has ONE availability domain - no zone redundancy in-region",
            "Streaming (Kafka API)", "Cheapest egress, but managed HA is impossible there", ("Eliminated", "na")]),
    ("note", ["Managed depth chosen: managed LB + managed Postgres HA, app tier on VMs. That keeps every deploy/release.sh gate "
              "(pre-migrate backup -> migrate -> health gate -> synthetic checks -> auto-rollback) working unchanged."]),
    ("spacer", []),

    ("sec", ["2. Target architecture - plan server to managed service"]),
    ("th", ["Plan item (in-country)", "Cloud-first replacement", "What it retires", "Why / notes"]),
    ("td", ["Server 1 edge / Caddy",
            "Global external Application Load Balancer (TLS + HTTP/3 at Google edge; Cloud Armor optional)",
            "Edge VM, compose.edge.yaml, cert renewal, floating-IP / dual-Caddy ingress prerequisite",
            "One managed anycast VIP in front of both app VMs; 1 forwarding rule is all the plan needs"]),
    ("td", ["App boxes (Server 2 / replicas)",
            "2x e2-medium VMs (2 vCPU / 4 GB) in zones a+b running compose.yaml app services only",
            "Nothing - release.sh workflow, DB_POOL_MAX=30 and the worker pools are unchanged",
            "2 replicas x pool 30 = 60 connections: below the instance default max_connections (verify at provision); "
            "PgBouncer not required at Phase 1"]),
    ("td", ["Server 3 primary DB",
            "Cloud SQL for PostgreSQL Enterprise, db-custom-4-8192 (4 vCPU / 8 GB), regional = zone-redundant HA, automated PITR",
            "Self-managed PG + PgBouncer, P1 async standby (setup-standby.sh manual promote), WAL-ship and restore-drill timers, "
            "the Phase-2 read-replica box",
            "Failover, standby storage, backups and PITR are managed; storage grows in place to the Phase-2 size; "
            "zone diversity satisfies the site-diversity rule pre-2027"]),
    ("td", ["Private DB access", "Private IP + Cloud SQL Auth connector (sidecar in compose)",
            "Published 5432/tcp and the 'restrict to the peer app server IP' firewall rule in compose.yaml",
            "No public DB endpoint; IAM DB authentication available for the audit"]),
    ("td", ["Server 9 observability", "Cloud Monitoring + Cloud Logging", "The Phase-3 Prometheus/Grafana/ELK box",
            "The scaling path's cache-hit / CPU / disk thresholds map directly onto alerts"]),
    ("td", ["Event bus", "Managed Service for Apache Kafka (africa-south1, 3-zone) - Phase 3 only", "3-node Kafka backbone",
            "EVENT_BUS=memory stays the default; the Phase-2 single-broker step becomes 'first topic on the managed cluster'"]),
    ("td", ["Redis", "Memorystore - optional, deferred", "Self-managed Redis",
            "In-memory fallback covers Phase 1; add Memorystore only if a shared cache becomes necessary"]),
    ("td", ["Backup target", "Managed PITR + scheduled snapshots (in-region)", "backup.sh timers and MinIO staging",
            "In-region = temporary residency under posture B; the permanent target is in-country at the 2027 cutover"]),
    ("spacer", []),

    ("sec", ["3. CI/CD - already built and provider-agnostic: config to set, 3 patches to make"]),
    ("th", ["Stage / item", "Tool / file", "Config", "Patch needed"]),
    ("td", ["PR validation", ".github/workflows/ci.yml",
            "Postgres service container -> migrations -> go vet -> go test -> deploy/release_test.sh harness -> gosec",
            ("None", "pass")]),
    ("td", ["Build + sign", ".github/workflows/release.yml (tag v*)",
            "verify (tag on main + green CI) -> buildx -> ghcr.io/ekearua/easymoney + SBOM/provenance + keyless cosign by digest",
            ("None", "pass")]),
    ("td", ["Deploy gate", "release.yml deploy job (opt-in)",
            "DEPLOY_ENABLED=true (vars), production environment approval, DEPLOY_HOSTS = the 2 app-VM external IPs, "
            "DEPLOY_SSH_KEY / known-hosts secrets; OS Login + firewall 22 restricted to CI egress",
            ("None - set repo/env vars", "pass")]),
    ("td", ["Private image pulls", "deploy/release.sh:81 (APP_IMAGE_TOKEN)",
            "Host .env must hold a GHCR read token today",
            ("Patch: inject the token from a GitHub secret during the deploy job instead of requiring .env", "warn")]),
    ("td", ["Stack selection", "deploy/release.sh:84 (COMPOSE_FILES)",
            "Defaults to '-f compose.yaml'; edge/second-host selection is manual today (GO_LIVE_RUNBOOK)",
            ("Patch: set COMPOSE_FILES explicitly per host; on cloud there is no edge host - compose.edge.yaml is retired "
             "and the LB replaces it", "warn")]),
    ("td", ["Pre-migrate backup", "deploy/release.sh:361 (compose 'backup' service)",
            "Base backup via the compose backup container before 'migrate'",
            ("Patch: explicit 'gcloud sql instances backup create' (or verify PITR restore point) before migrate; "
             "auto-rollback unchanged", "warn")]),
    ("td", ["Post-deploy gates", "release.sh probe + synthetic checks + .release-state rollback",
            "Unchanged; the DB hostname switches from the Server-3 peer name to the Cloud SQL private IP",
            ("None", "pass")]),
    ("spacer", []),

    ("sec", ["4. Indicative monthly cost (list prices, Oct 2026; NGN at 1,500/USD)"]),
    ("th", ["Component", "Basis / spec", "USD/mo", "NGN/mo", "Notes"]),
    ("td", ["Global external Application LB",
            "1 forwarding rule $0.025/hr + in/out data processing ~$0.01/GB at ~100 GB each way",
            ("20", "num"), ("30,000", "num"), "TLS + HTTP/3 at the Google edge; replaces Server 1"]),
    ("td", ["App VMs (2x)", "e2-medium 2 vCPU / 4 GB @ $0.0369/hr (africa-south1)",
            ("54", "num"), ("81,000", "num"), "compose minus db/pgbouncer/backup"]),
    ("td", ["Boot disks (2x)", "100 GB pd-balanced @ $0.1098/GB",
            ("22", "num"), ("33,000", "num"), "40 GB used per plan, 100 GB provisioned"]),
    ("td", ["Public IPv4 (2x)", "In-use address $0.005/hr",
            ("7", "num"), ("10,500", "num"), "SSH from CI; OS Login + firewall"]),
    ("td", ["Cloud SQL HA (primary + standby)",
            "Enterprise db-custom-4-8192 (4 vCPU / 8 GB): zonal $0.2212/hr; HA doubles compute",
            ("323", "num"), ("484,500", "num"), "Zone-redundant HA from day 1 + automated PITR; 1-yr CUD = -25%"]),
    ("td", ["Cloud SQL storage", "100 GB SSD (~$0.17/GB)", ("17", "num"), ("25,500", "num"), "Data tier, grows in place"]),
    ("td", ["Cloud SQL backups", "PITR + snapshots ~100 GB (~$0.08/GB)", ("8", "num"), ("12,000", "num"),
            "In-region: pre-2027 temporary residency"]),
    ("td", ["Internet egress", "~100 GB @ $0.12/GB (Premium, first TB)", ("12", "num"), ("18,000", "num"),
            "Standard tier $0.085/GB if latency-tolerant"]),
    ("tot", ["TOTAL (Phase-1 cloud launch)", "Managed LB + managed HA DB + 2 app VMs", ("463", "numtot"), ("694,500", "numtot"),
             "~3.1x the in-country target build (NGN 139,800/mo): the premium buys zone-redundant HA, managed PITR and no DB runbook"]),
    ("note", ["Indicative: 730 h/mo, on-demand list, excludes VAT, support plans and CI minutes. Cloud SQL Joburg list = the us-central1 "
              "rate (bytebase, Sep 2026: Joburg priced at the cheapest tier); HA doubles compute only; LB data-processing rates vary "
              "$0.008-0.012/GB by region and are taken at ~$0.01. Levers: 1-yr CUD (-25% Cloud SQL compute), Standard egress tier, "
              "pd-standard for non-prod. Deferred: Memorystore (~$34/mo; the in-memory fallback covers Phase 1) and managed Kafka "
              "(Phase 3). Phase-2 indicative: + read replica (~$178 incl. storage) + app VM 3 (~$42) = ~$683/mo (NGN 1,024,500)."]),
    ("spacer", []),

    ("sec", ["5. Residency (posture B) - dated path to 1 Jan 2027"]),
    ("th", ["When", "Step", "Action", "Compliance impact"]),
    ("td", ["Launch", "Full stack in Joburg",
            "Cloud SQL zone-redundant HA (zones a+b) + app VMs + global LB; PITR and snapshots in-region",
            ("Temporary cross-border residence - allowed only under this dated plan; gap G16 stays open", "warn")]),
    ("td", ["Q3 2026", "Provision the in-country target build",
            "Local Hosting combination A (ListedHosting D8; committed fallback HOSTAFRICA DA2) as the rehearsal environment",
            ("Prep step; keeps app and DB latency local from the cutover onward", "pass")]),
    ("td", ["Quarterly 2026", "Rehearse the cutover in staging",
            "Logical replication Joburg -> Nigeria; measure RPO/RTO; write the DNS + release-gate runbook",
            ("Satisfies the workbook's change-control / DR re-test obligation", "pass")]),
    ("td", ["Before 1 Jan 2027", "Data-plane cutover",
            "Promote the in-country primary; system of record + offsite backups move into Nigeria (rclone -> MinIO, Lagos; closes G16); "
            "the app plane moves with the data plane - cloud keeps stateless DR/overflow replicas or retires",
            ("CBN residency satisfied. An app-in-Joburg / DB-in-Lagos split would add ~50-90 ms RTT per query - move both together", "pass")]),
    ("td", ["Post-cutover", "Local Hosting sheet = steady state",
            "The in-country scaling path on the Server Requirements sheet resumes unchanged",
            ("Site diversity returns to two facilities + backups in a third", "pass")]),
    ("spacer", []),

    ("sec", ["6. Hand-over - what stays true"]),
    ("note", ["The Server Requirements sheet is the in-country target topology (2027+); its scaling path and compliance rules were reworded "
              "for posture B in this release. The Local Hosting sheet is the 2027 target build (combination A = ListedHosting D8, fallback "
              "HOSTAFRICA DA2). After the cutover both sheets are again the live plan, and this sheet's cloud footprint shrinks to optional DR."]),
    ("note", ["Managed scope stays 'LB + Postgres': app replicas remain self-managed VMs with compose, so the tested release gates, pool "
              "sizing and worker topology carry over to any provider. The three section-3 patches are the only pipeline changes needed."]),
]


# ===========================================================================
# generic sheet builder + workbook packaging
# ===========================================================================
def col_letter(i):
    s = ""
    while i >= 0:
        s = chr(ord("A") + i % 26) + s
        i = i // 26 - 1
    return s


def _cell(ref, style_idx, text):
    if text == "":
        return '<c r="%s" s="%d"/>' % (ref, style_idx)
    esc = (str(text).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;"))
    return '<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>' % (ref, style_idx, esc)


def build_sheet(rows, cols, freeze_rows=2):
    ncols = len(cols)
    last_col = col_letter(ncols - 1)
    colxml = "".join('<col min="%d" max="%d" width="%d" customWidth="1"/>' % (i + 1, i + 1, w)
                     for i, (_, w) in enumerate(cols))
    rows_xml, merges = [], []
    for r, (style, cells) in enumerate(rows, start=1):
        if style == "spacer" or not cells:
            rows_xml.append('<row r="%d"/>' % r)
            continue
        if style in MERGED:
            merges.append("A%d:%s%d" % (r, last_col, r))
            c = _cell("A%d" % r, STYLES[style], cells[0])
            ht = ROW_HEIGHTS.get(style)
            rows_xml.append('<row r="%d"%s>%s</row>' % (r, ' ht="%d" customHeight="1"' % ht if ht else "", c))
            continue
        row_style = STYLES.get(style, STYLES["td"])
        out, cidx = [], 0
        for cell in cells:
            if cidx >= ncols:
                break
            if isinstance(cell, tuple):
                if len(cell) == 3:
                    text, cstyle, span = cell
                else:
                    text, cstyle = cell
                    span = 1
            else:
                text, cstyle, span = cell, style, 1
            si = STYLES.get(cstyle, row_style)
            out.append(_cell(col_letter(cidx) + str(r), si, text))
            if span > 1:
                merges.append("%s%d:%s%d" % (col_letter(cidx), r, col_letter(cidx + span - 1), r))
            cidx += span
        while cidx < ncols:
            out.append(_cell(col_letter(cidx) + str(r), row_style, ""))
            cidx += 1
        rows_xml.append('<row r="%d">%s</row>' % (r, "".join(out)))
    merge_xml = ""
    if merges:
        merge_xml = '<mergeCells count="%d">%s</mergeCells>' % (len(merges), "".join('<mergeCell ref="%s"/>' % m for m in merges))
    pane = ""
    if freeze_rows:
        pane = ('<pane ySplit="%d" topLeftCell="A%d" activePane="bottomLeft" state="frozen"/>'
                '<selection pane="bottomLeft" activeCell="A%d" sqref="A%d"/>' % (freeze_rows, freeze_rows + 1, freeze_rows + 1, freeze_rows + 1))
    return ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
            '<worksheet xmlns="%s" xmlns:r="%s">'
            '<dimension ref="A1:%s%d"/>'
            '<sheetViews><sheetView workbookViewId="0">%s</sheetView></sheetViews>'
            '<sheetFormatPr defaultRowHeight="15"/>'
            '<cols>%s</cols>'
            '<sheetData>%s</sheetData>'
            '%s'
            '<pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/>'
            '</worksheet>') % (NS, RELNS, last_col, len(rows), pane, colxml, "".join(rows_xml), merge_xml)


CONTENT_TYPES = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
    '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
    '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
    '<Default Extension="xml" ContentType="application/xml"/>'
    '<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>'
    '<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>'
    '%s</Types>')

RELS = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
    '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
    '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>'
    '</Relationships>')


def workbook_xml(names):
    sheets = "".join('<sheet name="%s" sheetId="%d" r:id="rId%d"/>' % (n, i + 1, i + 1) for i, n in enumerate(names))
    return ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<workbook xmlns="%s" xmlns:r="%s"><sheets>%s</sheets>'
            '<calcPr calcId="124519" fullCalcOnLoad="1"/></workbook>' % (NS, RELNS, sheets))


def wb_rels(n):
    rels = "".join('<Relationship Id="rId%d" Type="%s/worksheet" Target="worksheets/sheet%d.xml"/>' % (i + 1, RELNS, i + 1)
                   for i in range(n))
    rels += '<Relationship Id="rId%d" Type="%s/styles" Target="styles.xml"/>' % (n + 1, RELNS)
    return ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">%s</Relationships>' % rels)


def main():
    names = ["Server Requirements", "Local Hosting", "Cloud First Deploy"]
    sheets = [build_sheet(SERVER_ROWS, SERVER_COLS), build_sheet(HOSTING_ROWS, HOST_COLS),
              build_sheet(CLOUD_ROWS, CLOUD_COLS)]

    ct = CONTENT_TYPES % "".join(
        '<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>' % (i + 1)
        for i in range(len(sheets)))
    core = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" '
            'xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Xego server requirements and local hosting plan</dc:title>'
            '<dc:creator>Xego</dc:creator></cp:coreProperties>')
    app = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
           '<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"/>')

    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with zipfile.ZipFile(OUT, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", ct)
        z.writestr("_rels/.rels", RELS)
        z.writestr("docProps/core.xml", core)
        z.writestr("docProps/app.xml", app)
        z.writestr("xl/workbook.xml", workbook_xml(names))
        z.writestr("xl/_rels/workbook.xml.rels", wb_rels(len(sheets)))
        z.writestr("xl/styles.xml", STYLES_XML)
        for i, s in enumerate(sheets):
            z.writestr("xl/worksheets/sheet%d.xml" % (i + 1), s)

    # validate
    from xml.etree import ElementTree as ET
    check = zipfile.ZipFile(OUT)
    assert check.testzip() is None, "zip CRC failure"
    for name in check.namelist():
        if name.endswith(".xml") or name.endswith(".rels"):
            ET.fromstring(check.read(name))
    wb = check.read("xl/workbook.xml").decode("utf-8")
    found = re.findall(r'<sheet[^>]*name="([^"]+)"', wb)
    check.close()
    assert found == names, "sheet names mismatch: %s" % found
    print("wrote %s" % OUT)
    print("sheets: %s (server rows=%d, hosting rows=%d, cloud rows=%d)"
          % (", ".join(found), len(SERVER_ROWS), len(HOSTING_ROWS), len(CLOUD_ROWS)))


if __name__ == "__main__":
    main()
