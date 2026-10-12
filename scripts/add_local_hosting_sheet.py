#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Add a styled "Local Hosting Options" sheet to reports/Xego_Production_Requirements.xlsx.

CBN data-locality planning aid: Nigerian VPS prices (Oct 2026) with server-cost
aggregation, a colocation capacity comparison and a fixed-vs-variable cost model.

Run from the repo root:  python scripts/add_local_hosting_sheet.py

The workbook is edited as raw OOXML (openpyxl is unavailable on this box) and is
idempotent: a re-run replaces the sheet in place and refreshes the appended styles.
"""

import os
import re
import zipfile
from xml.sax.saxutils import escape

SHEET_NAME = "Local Hosting Options"
XLSX = "reports/Xego_Production_Requirements.xlsx"
NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
RELNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

# A..H
COLS = [("A", 40), ("B", 30), ("C", 22), ("D", 20), ("E", 20), ("F", 18), ("G", 20), ("H", 34)]
NCOLS = len(COLS)

# style key -> appended cellXf index (existing workbook has cellXfs 0..20)
STYLES = {
    "title": 21, "sub": 22, "sec": 23, "th": 24, "td": 25, "num": 26, "tot": 27,
    "note": 28, "pass": 29, "warn": 30, "na": 31, "term": 32, "numtot": 33,
}
# rows that are a single merged cell spanning A:H
MERGED = {"title", "sub", "sec", "note"}
ROW_HEIGHTS = {"title": 30, "sub": 32, "sec": 22}

NEW_XFS = (
    '<xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment horizontal="left" vertical="center" wrapText="1"/></xf>'   # 21 title
    '<xf numFmtId="0" fontId="0" fillId="6" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>'                                       # 22 sub
    '<xf numFmtId="0" fontId="7" fillId="8" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="center" wrapText="1"/></xf>'                   # 23 sec
    '<xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment horizontal="center" vertical="center" wrapText="1"/></xf>' # 24 th
    '<xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>'                                                    # 25 td
    '<xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1" applyAlignment="1"><alignment horizontal="right" vertical="top" wrapText="1"/></xf>'                                # 26 num
    '<xf numFmtId="0" fontId="8" fillId="8" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>'                      # 27 tot
    '<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>'                                                                   # 28 note
    '<xf numFmtId="0" fontId="0" fillId="5" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>'                                     # 29 pass
    '<xf numFmtId="0" fontId="0" fillId="4" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>'                                     # 30 warn
    '<xf numFmtId="0" fontId="0" fillId="3" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>'                                     # 31 na
    '<xf numFmtId="0" fontId="8" fillId="0" borderId="1" xfId="0" applyFont="1" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>'                                    # 32 term
    '<xf numFmtId="0" fontId="8" fillId="8" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment horizontal="right" vertical="top" wrapText="1"/></xf>'   # 33 numtot
)

ROWS = [
    ("title", ["Local Hosting Options - CBN data locality (Circular PSS/DIR/PUB/CIR/001/004: payment transaction data generated in Nigeria must be stored and managed in Nigeria by 1 Jan 2027)"]),
    ("sub", ["Published list prices gathered Oct 2026 (sources in section 6). FX assumed NGN 1,500/USD and NGN 1,650/EUR; naira prices exclude VAT. Confirm renewal price and data-centre city in writing before committing spend."]),
    ("spacer", []),

    ("sec", ["1. Phase-1 server requirements (3 servers) - cost aggregation"]),
    ("th", ["Server", "Role", "vCPU", "RAM (GB)", "Disk (GB)", "Est. NGN/mo*", "Notes", ""]),
    ("td", ["Server 1", "Edge / LB + app replica", ("4", "num"), ("8", "num"), ("50 SSD", "num"), ("73,100", "num"), "Caddy (TLS) + app replica 1; priced as WhoGoHost C5 (4/8/200)", ""]),
    ("td", ["Server 2", "App + workers", ("8", "num"), ("12", "num"), ("100 SSD", "num"), ("146,200", "num"), "Payment/risk/outbox workers; also hangs the P1 async standby - size its data dir first", ""]),
    ("td", ["Server 3", "Database + backup staging", ("8", "num"), ("16", "num"), ("200 NVMe", "num"), ("146,200", "num"), "PostgreSQL 17, PgBouncer, WAL + daily base backup staging", ""]),
    ("tot", ["TOTAL", "3 servers", ("20", "numtot"), ("36", "numtot"), ("450 = 350 host + 100 staging", "numtot"), ("365,500", "numtot"), "Closest in-country plans (WhoGoHost). Annual NGN 4,386,000; 3-year NGN 13,158,000", ""]),
    ("note", ["*Reference price uses the closest in-country WhoGoHost plan per server to show a fully Nigerian, all-naira build. The combinations in section 3 optimise the mix for price, bandwidth and FX exposure."]),
    ("spacer", []),

    ("sec", ["2. VPS plan reference - researched Nigerian plans (Oct 2026, listed monthly price)"]),
    ("th", ["Provider", "Plan", "vCPU", "RAM (GB)", "Disk", "NGN/mo", "Data-centre country", ""]),
    ("td", ["WhoGoHost", "C1", ("1", "num"), ("1", "num"), ("20 GB SSD", "num"), ("11,050", "num"), ("Yes - Nigeria", "pass"), ""]),
    ("td", ["WhoGoHost", "C2", ("1", "num"), ("2", "num"), ("50 GB SSD", "num"), ("18,275", "num"), ("Yes - Nigeria", "pass"), ""]),
    ("td", ["WhoGoHost", "C4", ("2", "num"), ("4", "num"), ("100 GB SSD", "num"), ("36,550", "num"), ("Yes - Nigeria", "pass"), ""]),
    ("td", ["WhoGoHost", "C5", ("4", "num"), ("8", "num"), ("200 GB SSD", "num"), ("73,100", "num"), ("Yes - Nigeria", "pass"), ""]),
    ("td", ["WhoGoHost", "C7", ("8", "num"), ("16", "num"), ("400 GB SSD", "num"), ("146,200", "num"), ("Yes - Nigeria", "pass"), ""]),
    ("td", ["WhoGoHost", "C8", ("12", "num"), ("32", "num"), ("500 GB SSD", "num"), ("248,200", "num"), ("Yes - Nigeria", "pass"), ""]),
    ("td", ["HOSTAFRICA", "Managed DA1", ("3", "num"), ("4", "num"), ("100 GB SSD", "num"), ("81,225", "num"), ("Yes - Nigeria", "pass"), ""]),
    ("td", ["HOSTAFRICA", "Managed DA2", ("6", "num"), ("12", "num"), ("300 GB SSD", "num"), ("113,100", "num"), ("Yes - Nigeria", "pass"), ""]),
    ("td", ["HOSTAFRICA", "Managed DA4", ("8", "num"), ("16", "num"), ("300 GB SSD", "num"), ("177,700", "num"), ("Yes - Nigeria", "pass"), ""]),
    ("td", ["Hostraha", "Linux 8 GB", ("4", "num"), ("8", "num"), ("80 GB SSD", "num"), ("89,980", "num"), ("Yes - NG data-centre option", "pass"), ""]),
    ("td", ["Hostraha", "Linux 12 GB", ("6", "num"), ("12", "num"), ("120 GB SSD", "num"), ("139,980", "num"), ("Yes - NG data-centre option", "pass"), ""]),
    ("td", ["Hostraha", "Linux 16 GB", ("8", "num"), ("16", "num"), ("160 GB SSD", "num"), ("199,980", "num"), ("Yes - NG data-centre option", "pass"), ""]),
    ("td", ["QServers", "VPS-A", ("n/s", "num"), ("1", "num"), ("25 GB SSD", "num"), ("10,000", "num"), ("Not stated - confirm", "warn"), ""]),
    ("td", ["QServers", "VPS-C", ("n/s", "num"), ("4", "num"), ("75 GB SSD", "num"), ("29,500", "num"), ("Not stated - confirm", "warn"), ""]),
    ("td", ["Truehost", "Cloud VPS 3", ("4", "num"), ("8", "num"), ("200 GB SSD", "num"), ("48,000", "num"), ("No - Europe / USA", "na"), ""]),
    ("td", ["ListedHosting", "D8", ("4", "num"), ("8", "num"), ("160 GB NVMe", "num"), ("66,700", "num"), ("Likely NG - confirm", "warn"), ""]),
    ("td", ["MokoHost", "MH C4", ("8", "num"), ("16", "num"), ("320 GB NVMe", "num"), ("116,999", "num"), ("NG - confirm", "warn"), ""]),
    ("td", ["AFRICLOUD (Lagos)", "VM6", ("4", "num"), ("8", "num"), ("180 GB NVMe", "num"), ("USD 90 (~135,000)", "num"), ("Yes - Digital Realty LOS1", "pass"), ""]),
    ("td", ["AFRICLOUD (Lagos)", "VM8", ("8", "num"), ("16", "num"), ("320 GB NVMe", "num"), ("USD 160 (~240,000)", "num"), ("Yes - Digital Realty LOS1", "pass"), ""]),
    ("td", ["Melbicom (Lagos)", "VDS-6", ("6", "num"), ("12", "num"), ("120 GB SSD", "num"), ("EUR 31.20 (~51,500)", "num"), ("Yes - LOS1, Tier III", "pass"), ""]),
    ("note", ["USD/EUR rows are shown converted at the assumed FX above so they can be compared with the naira plans. Truehost lists EU/USA data centres, so it fails the in-country test despite a low price; QServers and Smartweb do not publish a server city."]),
    ("spacer", []),

    ("sec", ["3. Hosting combinations - aggregated server cost (monthly / 3-year)"]),
    ("th", ["#", "Combination", "Server 1 (edge)", "Server 2 (app)", "Server 3 (db)", "Monthly NGN", "3-yr TCO (NGN)", "Locality"]),
    ("td", ["A", "Recommended - all-naira, in-country (WhoGoHost)", "C5 4/8: 73,100", "C7 8/16: 146,200", "C7 8/16: 146,200", ("365,500", "num"), ("13,158,000", "num"), ("Yes - hosted in Nigeria", "pass")]),
    ("td", ["B", "In-country, higher bandwidth (Hostraha)", "8 GB 4/8: 89,980", "12 GB 6/12: 139,980", "16 GB 8/16: 199,980", ("429,940", "num"), ("15,477,840", "num"), ("Yes - NG DC; ISO 27001 / SOC 2", "pass")]),
    ("td", ["C", "Mixed in-country + FX (AFRICLOUD + Melbicom)", "VM6 4/8: 135,000", "VDS-6 6/12: 51,480", "Hostraha 16 GB: 199,980", ("386,460", "num"), ("13,912,560", "num"), ("Yes - part billed USD/EUR (forex risk)", "warn")]),
    ("td", ["D", "Colocation, owned hardware", "(see section 4)", "(see section 4)", "(see section 4)", ("see 4", "num"), ("see 4", "num"), ("Yes - Rack Centre / Equinix Lagos", "pass")]),
    ("tot", ["", "LOWEST monthly = A (all-in-country)", "", "", "", ("365,500", "numtot"), ("13,158,000", "numtot"), "Cheapest and fully in-country" ]),
    ("note", ["Monthly = the three server line items summed; 3-yr TCO = monthly x 36 (VPS options have no meaningful one-time cost). Colocation is priced separately in section 4 because it is capex + a fixed setup plus variable monthly charges."]),
    ("spacer", []),

    ("sec", ["4. Colocation - rack capacity and comparison"]),
    ("th", ["Rack size", "Usable height", "Servers @ 1U", "Servers @ 2U", "IT power", "Power-limited servers*", "Notes", ""]),
    ("td", ["Quarter rack", ("10 U", "num"), ("up to 10", "num"), ("up to 5", "num"), ("~1.5 kW", "num"), ("~3", "num"), "Entry footprint; 1-3 servers", ""]),
    ("td", ["Half rack", ("21 U", "num"), ("up to 21", "num"), ("up to 10", "num"), ("~3 kW", "num"), ("~6", "num"), "2-6 servers", ""]),
    ("td", ["Full rack", ("42 U", "num"), ("up to 42", "num"), ("up to 21", "num"), ("~5 kW", "num"), ("~10", "num"), "Phase-1 build (3 servers) fits many times over", ""]),
    ("td", ["Full rack, high-density", ("42-47 U", "num"), ("up to 47", "num"), ("up to 23", "num"), ("up to 70 kW", "num"), ("power-rich", "num"), "Rack Centre LGS2; AI / GPU workloads", ""]),
    ("note", ["*Power-limited count assumes about 500 W per 2U server. In a real cabinet, power - not height - is usually the limiting factor, so the power-limited figure is the practical capacity."]),
    ("spacer", []),

    ("sec", ["4b. Colocation cost model - fixed initial vs variable monthly (USD, Rack Centre-class)"]),
    ("th", ["Cost item", "Fixed / variable", "Quarter rack", "Half rack", "Full rack", "Notes", "", ""]),
    ("td", ["Rack setup (racking, patching, labelling)", "Fixed (one-time)", ("1,500-4,000", "num"), ("1,500-4,500", "num"), ("1,500-5,000", "num"), "Payable at contract start", "", ""]),
    ("td", ["Cross-connect installation", "Fixed (one-time)", ("100-500", "num"), ("100-500", "num"), ("100-500", "num"), "Per circuit, one-off", "", ""]),
    ("td", ["Cabinet / space", "Variable (monthly)", ("400-700", "num"), ("800-1,200", "num"), ("1,500-2,400", "num"), "Rack Centre estimate", "", ""]),
    ("td", ["Power (per kW above base)", "Variable (monthly)", ("120-200", "num"), ("120-200", "num"), ("120-200", "num"), "USD / kW / month", "", ""]),
    ("td", ["Cross-connect rental (IXPN peering)", "Variable (monthly)", ("50-100", "num"), ("50-100", "num"), ("50-100", "num"), "Keeps Nigerian traffic in-country", "", ""]),
    ("td", ["Remote hands", "Variable (monthly)", ("50-100/hr", "num"), ("50-100/hr", "num"), ("50-100/hr", "num"), "Budget 2-4 hr / month", "", ""]),
    ("tot", ["TOTAL monthly (variable)", "Variable", ("450-800", "numtot"), ("900-1,400", "numtot"), ("1,700-2,700", "numtot"), "Space + power + cross-connect + hands", "", ""]),
    ("tot", ["TOTAL fixed (one-time)", "Fixed", ("1,600-4,500", "numtot"), ("1,600-5,000", "numtot"), ("1,600-5,500", "numtot"), "Setup + cross-connect install", "", ""]),
    ("tot", ["3-year TCO (fixed + 36 x monthly)", "-", ("~18,000-33,000", "numtot"), ("~34,000-55,000", "numtot"), ("~63,000-103,000", "numtot"), "USD; capex of owned servers is extra", "", ""]),
    ("note", ["Equinix LG1/LG2 and Rack Centre LGS1/LGS2 both bill in USD. Equinix global average is about USD 2,538 per cabinet per month (Q2 2026). Rack Centre promo: half rack / 1.5 kW for one year + 1 month free, or full rack / 3 kW for one year + 3 months free."]),
    ("spacer", []),

    ("sec", ["5. Glossary and abbreviations"]),
    ("th", ["Term", "Meaning", "", "", "", "", "", ""]),
    ("td", ["U (rack unit)", ("44.45 mm (1.75 in) of rack height; a 42U cabinet is 42 units tall.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["1U / 2U server", ("Server is 1 or 2 rack units tall. 2U allows more drives, PCIe cards and easier cooling, but roughly half as many fit per cabinet.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["vCPU / vCore", ("A virtual CPU core allocated to a virtual machine.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["NVMe / SSD", ("NVMe is a PCIe-attached SSD (faster, the good choice for databases); SSD here means SATA SSD.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["NGN / USD / EUR", ("Nigerian naira / US dollar / euro.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["kW", ("Kilowatt of IT power draw - the main axis along which colocation is priced.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["IXPN", ("Internet Exchange Point of Nigeria - local peering that keeps Nigerian traffic cheap and in-country.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["LG1 / LG2", ("Equinix Lagos IBX data centres in the Lagos campus (Lekki / Ikeja).", "td", 7), "", "", "", "", "", ""]),
    ("td", ["LGS1 / LGS2", ("Rack Centre Lagos data centres 1 and 2 (LGS2 supports up to 70 kW per rack).", "td", 7), "", "", "", "", "", ""]),
    ("td", ["IBX", ("Equinix 'International Business Exchange' data centre.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["Tier III", ("Uptime Institute rating meaning 'concurrently maintainable' - can service one path while running.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["PUE", ("Power Usage Effectiveness = total facility power / IT power. Lower is better; Rack Centre LGS2 is designed at 1.35.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["N+1 / 2N / N+2", ("Redundancy levels for power and cooling. 2N = fully duplicated; N+2 tolerates two simultaneous failures.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["ISO 27001 / SOC 2 / PCI DSS / NDPR", ("Information-security certification; service-organisation control report; card-data security standard; Nigeria Data Protection Regulation.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["SAQ-A", ("PCI self-assessment questionnaire used when card data is handled by hosted fields, keeping scope minimal.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["ASV", ("Approved Scanning Vendor - performs the quarterly external PCI scans.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["PITR / RPO", ("Point-in-time recovery; Recovery Point Objective - the maximum data you accept losing in a failure.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["TCO", ("Total Cost of Ownership - fixed spend plus all recurring cost over the term (here 3 years).", "td", 7), "", "", "", "", "", ""]),
    ("td", ["Cross-connect", ("A physical fibre or copper link between two racks or to a carrier inside the data centre.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["Remote hands", ("On-site technicians you hire by the hour for physical tasks (reboots, cable swaps, drive replacements).", "td", 7), "", "", "", "", "", ""]),
    ("td", ["Colocation ('colo')", ("Renting rack space, power and cooling in a shared data centre to run your own hardware.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["VAT", ("Value-added tax - excluded from the listed prices above.", "td", 7), "", "", "", "", "", ""]),
    ("td", ["MinIO / rclone", ("Self-hosted S3-compatible object store (the in-country backup target) and the tool that ships WAL + base backups to it.", "td", 7), "", "", "", "", "", ""]),
    ("spacer", []),

    ("sec", ["6. Sources (fetched Oct 2026) and decision notes"]),
    ("th", ["Item", "Source", "Fetched", "", "", "", "", ""]),
    ("td", ["WhoGoHost VPS", "whogohost.com/hosting/vps", "Oct 2026", "", "", "", "", ""]),
    ("td", ["HOSTAFRICA Nigeria", "hostafrica.ng/servers/linux-vps-servers and /managed-vps-servers", "Oct 2026", "", "", "", "", ""]),
    ("td", ["Hostraha", "hostraha.ng/pricing", "Oct 2026", "", "", "", "", ""]),
    ("td", ["QServers", "qservers.ng/vps", "Oct 2026", "", "", "", "", ""]),
    ("td", ["Truehost Nigeria", "truehost.com.ng cloud VPS store", "Oct 2026", "", "", "", "", ""]),
    ("td", ["ListedHosting", "listedhosting.com/cloud-vps-hosting", "Oct 2026", "", "", "", "", ""]),
    ("td", ["MokoHost", "mokohost.com/cloud-vps-hosting", "Oct 2026", "", "", "", "", ""]),
    ("td", ["AFRICLOUD Lagos", "africloud.com/lagos", "Oct 2026", "", "", "", "", ""]),
    ("td", ["Melbicom Lagos", "melbicom.net/virtualserver/lagos", "Oct 2026", "", "", "", "", ""]),
    ("td", ["Rack Centre", "rack-centre.com (colocation, campus, promo)", "Oct 2026", "", "", "", "", ""]),
    ("td", ["Equinix Lagos LG1/LG2", "equinix.com Lagos data centers; datacentersearch.org/sites/equinix-lg1", "Oct 2026", "", "", "", "", ""]),
    ("td", ["Colocation benchmarks", "colocationscout.com (Aug 2026); Equinix Q2 2026 results", "Oct 2026", "", "", "", "", ""]),
    ("td", ["NG VPS market", "lineserve.net blog 'What VPS Hosting Costs in Nigeria' (Jul 2026)", "Oct 2026", "", "", "", "", ""]),
    ("spacer", []),
    ("note", ["G16 (see CBN_DATA_LOCALITY_GAP.md): the offsite backup / WAL bucket region is the single most important open item. The rclone target (base + WAL) must be in-country - self-hosted MinIO on a Lagos box, or an in-country S3-compatible service; a config-only change. Combination A closes it."]),
    ("note", ["Naira billing is part of the compliance rationale (CBN wants payment-infrastructure spend kept in-country): WhoGoHost, HOSTAFRICA and Hostraha bill naira; AFRICLOUD and Melbicom bill USD/EUR; Equinix and Rack Centre bill USD."]),
    ("note", ["Hyperscaler local-zone / Outposts racks inside a Nigerian Tier III facility: interpretation pending (see CBN_DATA_LOCALITY_GAP.md). Do not architect the compliance story on it."]),
    ("note", ["PCI scope is unaffected by hosting location: hosted-fields checkout keeps SAQ-A scope; ASV scans and penetration tests follow the card schemes, not the CBN."]),
    ("note", ["Site diversity rule: the DB primary and the async streaming standby (a P1 add-on on the second app server, runbook 4.1) must sit in different physical facilities; a third facility (or the MinIO box) holds the backup target."]),
    ("spacer", []),
    ("note", ["Generated by scripts/add_local_hosting_sheet.py - re-runnable; a re-run replaces this sheet and refreshes its styles in place."]),
]


def col_letter(i):
    s = ""
    while i >= 0:
        s = chr(ord("A") + i % 26) + s
        i = i // 26 - 1
    return s


LAST_COL = col_letter(NCOLS - 1)


def _cell(ref, style_idx, text):
    if text == "":
        return '<c r="%s" s="%d"/>' % (ref, style_idx)
    return ('<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>'
            % (ref, style_idx, escape(str(text))))


def build_sheet_xml():
    cols = "".join(
        '<col min="%d" max="%d" width="%d" customWidth="1"/>' % (idx + 1, idx + 1, w)
        for idx, (_, w) in enumerate(COLS)
    )
    rows_xml = []
    merges = []
    for r, (style, cells) in enumerate(ROWS, start=1):
        if style == "spacer" or not cells:
            rows_xml.append('<row r="%d"/>' % r)
            continue
        if style in MERGED:
            merges.append("A%d:%s%d" % (r, LAST_COL, r))
            c = _cell("A%d" % r, STYLES[style], cells[0])
            ht = ROW_HEIGHTS.get(style)
            if ht:
                rows_xml.append('<row r="%d" ht="%d" customHeight="1">%s</row>' % (r, ht, c))
            else:
                rows_xml.append('<row r="%d">%s</row>' % (r, c))
            continue

        row_style = STYLES.get(style, STYLES["td"])
        out = []
        cidx = 0
        for cell in cells:
            if cidx >= NCOLS:
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
        while cidx < NCOLS:
            out.append(_cell(col_letter(cidx) + str(r), row_style, ""))
            cidx += 1
        rows_xml.append('<row r="%d">%s</row>' % (r, "".join(out)))

    merge_xml = ""
    if merges:
        merge_xml = '<mergeCells count="%d">%s</mergeCells>' % (
            len(merges), "".join('<mergeCell ref="%s"/>' % m for m in merges))
    last_ref = "%s%d" % (LAST_COL, len(ROWS))
    return (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
        '<worksheet xmlns="%s" xmlns:r="%s">'
        '<dimension ref="A1:%s"/>'
        '<sheetViews><sheetView workbookViewId="0">'
        '<pane ySplit="2" topLeftCell="A3" activePane="bottomLeft" state="frozen"/>'
        '<selection pane="bottomLeft" activeCell="A3" sqref="A3"/>'
        '</sheetView></sheetViews>'
        '<sheetFormatPr defaultRowHeight="15"/>'
        "<cols>%s</cols>"
        "<sheetData>%s</sheetData>"
        "%s"
        '<pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/>'
        "</worksheet>"
    ) % (NS, RELNS, last_ref, cols, "".join(rows_xml), merge_xml)


MARK_S = "<!--LH:START-->"
MARK_E = "<!--LH:END-->"


def ensure_styles(items):
    """Append our cellXfs to xl/styles.xml, idempotently (reuses existing fonts/fills/borders)."""
    s = items["xl/styles.xml"].decode("utf-8")
    if MARK_S in s and MARK_E in s:
        block = s[s.index(MARK_S):s.index(MARK_E) + len(MARK_E)]
        n_old = block.count("<xf ")
        s = s.replace(block, "")
        s = re.sub(r'<cellXfs count="(\d+)">',
                   lambda m: '<cellXfs count="%d">' % (int(m.group(1)) - n_old), s, count=1)
    m = re.search(r'<cellXfs count="(\d+)">(.*?)</cellXfs>', s, re.S)
    if not m:
        raise SystemExit("xl/styles.xml has no cellXfs block")
    count = int(m.group(1))
    ins = MARK_S + NEW_XFS + MARK_E
    s = s[:m.end(0) - len("</cellXfs>")] + ins + "</cellXfs>" + s[m.end(0):]
    s = re.sub(r'<cellXfs count="\d+">', '<cellXfs count="%d">' % (count + NEW_XFS.count("<xf ")),
               s, count=1)
    items["xl/styles.xml"] = s.encode("utf-8")


def main():
    if not os.path.isfile(XLSX):
        raise SystemExit("run from the repo root (workbook missing)")

    zin = zipfile.ZipFile(XLSX)
    items = {n: zin.read(n) for n in zin.namelist()}
    zin.close()

    wb_xml = items["xl/workbook.xml"].decode("utf-8")
    rels_xml = items["xl/_rels/workbook.xml.rels"].decode("utf-8")
    ct_xml = items["[Content_Types].xml"].decode("utf-8")

    sheet_files = re.findall(r'<sheet[^>]*name="([^"]+)"[^>]*r:id="([^"]+)"', wb_xml)
    sheet_ids = [int(m) for m in re.findall(r'sheetId="(\d+)"', wb_xml)]
    rel_targets = dict(re.findall(r'Id="([^"]+)"[^>]*Target="([^"]+)"', rels_xml))

    if SHEET_NAME in [n for n, _ in sheet_files]:
        rid = [r for n, r in sheet_files if n == SHEET_NAME][0]
        part = "xl/" + rel_targets[rid].lstrip("/")
        if part not in items:
            part = rel_targets[rid]
        items[part] = build_sheet_xml().encode("utf-8")
        action = "replaced"
    else:
        idx = 1
        while "xl/worksheets/sheet%d.xml" % idx in items:
            idx += 1
        part = "xl/worksheets/sheet%d.xml" % idx
        items[part] = build_sheet_xml().encode("utf-8")

        rid_nums = [int(m) for m in re.findall(r'Id="rId(\d+)"', rels_xml)]
        rid = "rId%d" % (max(rid_nums) + 1)
        new_rel = ('<Relationship Id="%s" Type="%s/worksheet" '
                   'Target="worksheets/sheet%d.xml"/>' % (rid, RELNS, idx))
        rels_xml = rels_xml.replace("</Relationships>", new_rel + "</Relationships>")

        new_sheet = ('<sheet name="%s" sheetId="%d" r:id="%s"/>'
                     % (SHEET_NAME, max(sheet_ids) + 1, rid))
        wb_xml = wb_xml.replace("</sheets>", new_sheet + "</sheets>")

        override = ('<Override PartName="/%s" ContentType="application/vnd.'
                    'openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>' % part)
        ct_xml = ct_xml.replace("</Types>", override + "</Types>")

        items["xl/workbook.xml"] = wb_xml.encode("utf-8")
        items["xl/_rels/workbook.xml.rels"] = rels_xml.encode("utf-8")
        items["[Content_Types].xml"] = ct_xml.encode("utf-8")
        action = "added as %s (%s)" % (part, rid)

    ensure_styles(items)

    with zipfile.ZipFile(XLSX, "w", zipfile.ZIP_DEFLATED) as out:
        for name, data in items.items():
            out.writestr(name, data)

    from xml.etree import ElementTree as ET
    check = zipfile.ZipFile(XLSX)
    assert check.testzip() is None, "zip CRC failure"
    for name in check.namelist():
        if name.endswith(".xml") or name.endswith(".rels"):
            ET.fromstring(check.read(name))
    wb_check = check.read("xl/workbook.xml").decode("utf-8")
    assert SHEET_NAME in wb_check, "sheet not registered"
    names = re.findall(r'<sheet[^>]*name="([^"]+)"', wb_check)
    check.close()
    print("sheet %s (%d sheets now: %s)" % (action, len(names), ", ".join(names)))
    print("OK: %s rewritten and re-validated" % XLSX)


if __name__ == "__main__":
    main()
