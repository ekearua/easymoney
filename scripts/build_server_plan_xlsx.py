#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Build docs/cost-models/Xego_Server_and_Hosting_Plan.xlsx - a dedicated two-sheet workbook:

  "Server Requirements" - right-sized per-phase server plan plus a compliance-safe
                          scaling path (data plane stays in-country, app plane scales out)
  "Local Hosting"       - Nigerian VPS / colocation reference and the optimized
                          Phase-1 build (reuses the researched content)

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
             "data migration (CBN Circular PSS/DIR/PUB/CIR/001/004 - payment data generated in Nigeria stored/managed in Nigeria by 1 Jan 2027)."]),
    ("spacer", []),

    ("sec", ["1. Optimized sizing by phase"]),
    ("th", ["Phase", "Users / concurrency", "Servers", "vCPU", "RAM (GB)", "Disk", "Workload it must carry", "Compliance posture"]),
    ("td", ["Phase 1 (launch)", "10k users / ~2k concurrent", ("3", "num"), ("8", "num"), ("16", "num"),
            "280 GB host + 100 GB staging", "Monolith + payment core; 2 app replicas, 29 worker slots, pool=30",
            ("In-country DB; SAQ-A scope", "pass")]),
    ("td", ["Phase 2 (growth)", "50-100k users / ~5k concurrent", "3 -> 5 (+1 app, +1 replica)", ("16", "num"), ("32", "num"),
            "~560 GB + staging", "Extract core services; add app servers + DB RAM; add read replica",
            ("Replica in-country; app out of CDE", "pass")]),
    ("td", ["Phase 3 (scale)", "75k concurrent / 13.5B txn", ("9", "num"), ("50", "num"), ("90", "num"),
            "1,420 GB", "Dedicated LB, 4 app replicas, 2 DB boxes, monitoring/storage box",
            ("Full PCI DSS L1; all in-country", "pass")]),
    ("tot", ["TOTAL (Phase 1)", "3 servers", ("8", "numtot"), ("16", "numtot"), "280 host + 100 staging",
             "Scale out app, scale up DB in place", ""]),
    ("note", ["First draft used Phase-3-class Phase-1 boxes (20 vCPU / 36 GB / 450 GB). The optimized build is ~1/3 the compute, "
              "and Phase 1 and Phase 2 share the same data plane; Phase 3 scales out rather than replaces."]),
    ("spacer", []),

    ("sec", ["2. Phase 1 (launch) - server-by-server breakdown (3 servers)"]),
    ("th", ["Server", "Role", "vCPU", "RAM (GB)", "Disk", "Key services", "Purpose / scaling headroom", ""]),
    ("td", ["Server 1", "Edge / load balancer + app replica 1", ("2", "num"), ("4", "num"), "40 GB SSD",
            "Caddy (TLS, reverse proxy), app replica 1", "Single public entry point; add a replica on a new box - no data impact", ""]),
    ("td", ["Server 2", "App replica 2 + background workers", ("2", "num"), ("4", "num"), "40 GB SSD",
            "app replica 2, payment/risk/outbox workers; hosts the P1 async standby",
            "Second app instance for HA; scale out by adding app boxes", ""]),
    ("td", ["Server 3", "Primary database + backup staging (data plane)", ("4", "num"), ("8", "num"), "200 GB NVMe + 100 GB staging",
            "PostgreSQL 17 primary, PgBouncer, token DB, WAL + daily base backup staging",
            "System of record; scale up RAM/disk in place; later add an in-country read replica", ""]),
    ("tot", ["TOTAL", "3 servers", ("8", "numtot"), ("16", "numtot"), "280 host + 100 staging", "", ""]),
    ("note", ["Server 3 is the compliance anchor: it holds the system of record and the in-country backup target. Growing it, or adding an "
              "in-country replica, is an in-place change - it never moves the data across a border."]),
    ("note", ["Event bus: the in-memory Kafka-compatible bus is the default (EVENT_BUS=memory) and needs no broker. A single Kafka broker "
              "is optional at launch and can co-locate on Server 3 until the first external consumer exists."]),
    ("spacer", []),

    ("sec", ["3. Phase 2 (growth) - server-by-server breakdown (5 servers)"]),
    ("th", ["Server", "Role", "vCPU", "RAM (GB)", "Disk", "Key services", "What changed from Phase 1", ""]),
    ("td", ["Server 1", "Edge / load balancer + app replica 1", ("2", "num"), ("4", "num"), "40 GB SSD",
            "Caddy (TLS), app replica 1", "Unchanged", ""]),
    ("td", ["Server 2", "App replica 2 + background workers", ("2", "num"), ("4", "num"), "40 GB SSD",
            "app replica 2, payment/risk/outbox workers", "Unchanged; P1 async standby retires to the new DB replica", ""]),
    ("td", ["Server 3", "Primary database (scaled up)", ("8", "num"), ("16", "num"), "320 GB NVMe + 100 GB staging",
            "PostgreSQL 17 primary, PgBouncer (pool -> 75), token DB, WAL/base backup staging",
            "RAM 8 -> 16 GB and disk grown in place; stream replication to Server 4", ""]),
    ("td", ["Server 4 (new)", "Read replica + reporting", ("2", "num"), ("4", "num"), "120 GB NVMe",
            "In-country streaming replica; admin / reporting reads", "Offloads read traffic from the primary; residency preserved", ""]),
    ("td", ["Server 5 (new)", "App replica 3 + extracted services", ("2", "num"), ("4", "num"), "40 GB SSD",
            "app replica 3; Payment Orchestrator, Risk, Tokenization workers",
            "Begins service extraction off the monolith", ""]),
    ("tot", ["TOTAL", "5 servers", ("16", "numtot"), ("32", "numtot"), "560 host + 100 staging", "", ""]),
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
            "Add 2 app servers (4 replicas) behind Caddy",
            ("None - stateless app; hosted fields keep card data off app servers", "pass"), "Low", "", "", ""]),
    ("td", ["1 -> 2b: DB scale-up", "DB cache hit rate drops / disk > ~60%",
            "Raise Server-3 RAM to 16 GB and disk in place; tune pool",
            ("None - same box, same country", "pass"), "Low", "", "", ""]),
    ("td", ["2 -> 2c: read replica", "Reporting / admin reads compete with writes",
            "Add an in-country streaming read replica",
            ("Residency preserved (replica in Nigeria)", "pass"), "Medium", "", "", ""]),
    ("td", ["2 -> 2d: DB disk scale-up", "Data volume approaches ~60% of the 200 GB NVMe",
            "Grow Server-3 disk in place; archive aged WAL/base copies to in-country cold storage",
            ("None - same box; archive target stays in Nigeria", "pass"), "Low", "", "", ""]),
    ("td", ["2 -> 2e: backup staging growth", "Daily WAL + base backups exceed the 100 GB staging tier",
            "Expand backup staging / object storage in-country",
            ("Residency preserved (G16 target in Nigeria)", "pass"), "Low", "", "", ""]),
    ("td", ["2 -> 2f: pool & connection tuning", "PgBouncer pool=30 saturated; app waits on the DB",
            "Raise Server-3 RAM to 16 GB; raise pool toward 75 and PG max_connections; lift the hardcoded worker batch",
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
    ("note", ["Rule of thumb: scale the stateless app plane out (no compliance impact) and the data plane up / replicate in place "
              "(residency preserved). Never scale by migrating the database across a jurisdiction."]),
    ("spacer", []),

    ("sec", ["6. Compliance rules that make scaling cheap"]),
    ("th", ["Rule", "Why it matters", "How this plan satisfies it", "", "", "", "", ""]),
    ("td", ["Data residency (CBN)", "Payment data generated in Nigeria must be stored/managed in Nigeria by 1 Jan 2027",
            "DB, token DB and the offsite backup target sit in-country from day 1; growth adds in-country nodes only", "", "", "", "", ""]),
    ("td", ["Minimise PCI scope", "Every server touching card data expands the audit",
            "Hosted-fields checkout (SAQ-A); card data never hits app servers; tokenization isolated on the data plane", "", "", "", "", ""]),
    ("td", ["Site diversity", "A single facility outage must not take the DB down",
            "Primary DB and the P1 async standby sit in different physical facilities; backups in a third", "", "", "", "", ""]),
    ("td", ["Backups in-country", "Offsite WAL/base copies are in scope for residency (gap G16)",
            "rclone target = self-hosted MinIO on a Lagos box or an in-country S3-compatible service", "", "", "", "", ""]),
    ("td", ["Change control", "Scaling events can trip DR re-test / audit obligations",
            "App scale-out is a config change; DB changes follow the release/backup gate; DR re-tested per runbook", "", "", "", "", ""]),
    ("spacer", []),

    ("sec", ["7. Why this is efficient (rightsizing rationale)"]),
    ("th", ["Dimension", "First draft", "Optimized Phase 1", "Rationale", "", "", "", ""]),
    ("td", ["App servers", "8 + 12 GB", "2 x 4 GB", "Stateless, I/O-bound; 2k concurrent needs far less RAM than provisioned", "", "", "", "", ""]),
    ("td", ["Database", "16 GB", "8 GB (grow to 16 at Phase 2)", "Phase-1 working set fits in 8 GB; RAM is the spec that grows with data", "", "", "", "", ""]),
    ("td", ["Total fleet", "20 vCPU / 36 GB / 450 GB", "8 vCPU / 16 GB / 380 GB", "Phase-1 boxes no longer sized to Phase-3 capacity", "", "", "", "", ""]),
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
    ("td", ["Server 1", "Edge / LB + app replica", ("2", "num"), ("4", "num"), ("40 SSD", "num"), ("36,550", "num"),
            "Caddy (TLS) + app replica 1; priced as WhoGoHost C4 (2/4/100)", ""]),
    ("td", ["Server 2", "App + workers", ("2", "num"), ("4", "num"), ("40 SSD", "num"), ("36,550", "num"),
            "Payment/risk/outbox workers; also hosts the P1 async standby", ""]),
    ("td", ["Server 3", "Database + backup staging", ("4", "num"), ("8", "num"), ("200 NVMe", "num"), ("73,100", "num"),
            "PostgreSQL 17, PgBouncer, token DB, WAL + daily base backup staging; grow to 16 GB at Phase 2", ""]),
    ("tot", ["TOTAL", "3 servers", ("8", "numtot"), ("16", "numtot"), ("280 host + 100 staging", "numtot"), ("146,200", "numtot"),
             "Annual NGN 1,754,400; 3-year NGN 5,263,200 (was NGN 13,158,000 at 8/12/16)", ""]),
    ("note", ["*Right-sized to Phase-1 load plus HA floors, not Phase-3 capacity. The data plane (Server 3) is provisioned in-country on day 1 "
              "and scaled up in place, so growth never requires a cross-border migration. See the Server Requirements sheet for the scaling path."]),
    ("spacer", []),
]

SECTION3_OPT = [
    ("sec", ["3. Hosting combinations - optimized Phase-1 build (monthly / 3-year)"]),
    ("th", ["#", "Combination", "Server 1 (edge)", "Server 2 (app)", "Server 3 (db)", "Monthly NGN", "3-yr TCO (NGN)", "Locality"]),
    ("td", ["A", "Recommended - all-naira, in-country (WhoGoHost)", "C4 2/4: 36,550", "C4 2/4: 36,550", "C5 4/8: 73,100",
            ("146,200", "num"), ("5,263,200", "num"), ("Yes - hosted in Nigeria", "pass")]),
    ("td", ["B", "In-country NVMe database (Hostraha) + WhoGoHost apps", "C4 2/4: 36,550", "C4 2/4: 36,550", "Hostraha 8 GB 4/8: 89,980",
            ("163,080", "num"), ("5,870,880", "num"), ("Yes - NG data-centre option", "pass")]),
    ("td", ["C", "Mixed in-country + FX (AFRICLOUD database)", "C4 2/4: 36,550", "C4 2/4: 36,550", "AFRICLOUD VM6 4/8: 135,000",
            ("208,100", "num"), ("7,491,600", "num"), ("Yes - part billed USD (forex risk)", "warn")]),
    ("td", ["D", "Colocation, owned hardware", "(see section 4)", "(see section 4)", "(see section 4)",
            ("see 4", "num"), ("see 4", "num"), ("Yes - Rack Centre / Equinix Lagos", "pass")]),
    ("tot", ["", "LOWEST monthly = A (all-in-country)", "", "", "", ("146,200", "numtot"), ("5,263,200", "numtot"),
             "Cheapest, fully in-country, all-naira"]),
    ("note", ["Monthly = the three server line items summed; 3-yr TCO = monthly x 36 (VPS options have no meaningful one-time cost). "
              "Optimized Phase-1 specs cut the build from NGN 365,500 to NGN 146,200/mo (-60%). Colocation is priced in section 4."]),
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
    names = ["Server Requirements", "Local Hosting"]
    sheets = [build_sheet(SERVER_ROWS, SERVER_COLS), build_sheet(HOSTING_ROWS, HOST_COLS)]

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
    print("sheets: %s (server rows=%d, hosting rows=%d)" % (", ".join(found), len(SERVER_ROWS), len(HOSTING_ROWS)))


if __name__ == "__main__":
    main()
