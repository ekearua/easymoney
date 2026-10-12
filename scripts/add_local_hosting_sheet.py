#!/usr/bin/env python3
"""Add a "Local Hosting Options" sheet to reports/Xego_Production_Requirements.xlsx.

The workbook is edited through raw OOXML (openpyxl segfaults on this box):
the new worksheet is appended as its own part, registered in workbook.xml,
its rels and [Content_Types].xml via string inserts (an ET round-trip of
workbook.xml drops xmlns declarations that mc:Ignorable references, which
makes Excel repair the file).

Content: the Phase-1 server requirements, four naira-denominated hosting
combinations (AFRICLOUD/Hostraha/Melbicom Lagos VPS mixes and a colocation
option), a colocation rack-pricing table (Equinix LG1/LG2, Rack Centre
LGS1/LG2), and the data-locality decision notes behind them.

Run from the repo root:  python scripts/add_local_hosting_sheet.py
Idempotent: a re-run replaces the sheet part instead of appending a second.
"""
import re
import zipfile
from xml.sax.saxutils import escape

XLSX = "reports/Xego_Production_Requirements.xlsx"
SHEET_NAME = "Local Hosting Options"
NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
RELNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

# Full grid, row by row; empty lists are spacer rows.
GRID = [
    ["Local Hosting Options - CBN data locality (Circular PSS/DIR/PUB/CIR/001/004: payment transaction data generated in Nigeria must be stored and managed in Nigeria by 1 Jan 2027)"],
    ["Naira figures are estimate-grade list prices gathered Oct 2026; FX approx NGN 1,500 per USD. Confirm every line against a written quote before committing spend."],
    [],
    ["1. Server requirements (Deployment Plan Phase 1, unchanged)", "", "", "", "", ""],
    ["Server", "Role", "vCPUs", "RAM", "Disk", "Services running"],
    ["Server 1", "Edge/LB + app replica", "4", "8 GB", "50 GB SSD", "Caddy (TLS), app replica 1"],
    ["Server 2", "App replica + workers", "8", "12 GB", "100 GB SSD", "App replica 2, payment/risk workers"],
    ["Server 3", "Database + backup staging", "8", "16 GB", "200 GB NVMe", "PostgreSQL 17, PgBouncer, WAL + base backup staging"],
    ["TOTAL", "3 servers", "20", "36 GB", "450 GB", "2 app replicas, PITR backups (WAL + daily base, offsite, 30 days)"],
    [],
    ["2. Hosting combinations (naira-denominated; FX approx NGN 1,500/USD)"],
    ["#", "Combination", "Provider mix (topology)", "Meets CBN locality?", "Monthly (NGN)", "Monthly (USD equiv.)", "Notes"],
    ["A", "Lean 2-server + in-country backup (RECOMMENDED for launch)",
     "AFRICLOUD Lagos (Digital Realty VI, IXPN-peered, naira billing) primary 8c/16GB/200GB; Hostraha or Melbicom Lagos edge 4c/8GB/100GB; third Lagos box runs MinIO as the rclone backup target",
     "Yes - compute, standby and backup all in-country",
     "NGN 180,000-480,000", "USD 120-320",
     "Closes the G16 backup-residency gap with config-only changes (.env / rclone.conf); two providers in two facilities give site diversity for primary/standby"],
    ["B", "Phase-1 specs, all-naira VPS",
     "Naira-billed providers (Truehost / WhoGoHost class) x3 to the exact Phase-1 specs: 4c/8GB/200GB, 8c/12GB/100GB, 8c/16GB/200GB NVMe",
     "Yes, if each provider confirms its Lagos facility",
     "NGN 160,000-430,000", "USD 105-285",
     "Cheapest line items (4c/8GB/200GB approx NGN 35k/mo; entry plans from approx NGN 11k/mo); verify Docker/Postgres support on the plan"],
    ["C", "Mixed Lagos VPS (perf per naira optimum)",
     "Melbicom Lagos 8c/16GB (approx EUR 91/mo, EUR billing = forex exposure); Hostraha Lagos 4c/8GB (approx NGN 89,980/mo); AFRICLOUD for the backup box",
     "Yes",
     "NGN 230,000-520,000", "USD 155-350",
     "Best perf per naira on the big DB box; part of the bill is forex-exposed (EUR)"],
    ["D", "Colocation, owned hardware",
     "Rack Centre LGS1/LGS2 (Tier III constructed, most interconnected) or Equinix LG1/LG2 (Tier III, ISO 27001 / SOC / PCI DSS) - 2-4 servers plus a shared-to-half cabinet",
     "Yes",
     "NGN 2,100,000-4,950,000", "USD 1,400-3,300",
     "Capex-heavy; buys control, capacity headroom and predictable 3-year TCO; see rack table below"],
    [],
    ["3. Colocation rack pricing (physical racks; benchmark estimates - written quotes required)"],
    ["Item", "Equinix LG1/LG2", "Rack Centre LGS1/LGS2", "Notes"],
    ["Quarter cabinet (10U, approx 1 kW)", "USD 500-800 /mo", "USD 400-700 /mo", "Entry colocation for a single server + appliance"],
    ["Half cabinet (21U, approx 2 kW)", "USD 900-1,400 /mo", "USD 800-1,200 /mo", "Two-server footprint with power headroom"],
    ["Full cabinet 42U (approx 5 kW)", "USD 1,800-2,800 /mo", "USD 1,500-2,400 /mo", "What the 3-server Phase-1 launch needs"],
    ["Additional power per kW", "USD 150-250 /kW /mo", "USD 120-200 /kW /mo", "Billed above the cabinet base allocation"],
    ["Cross-connect", "USD 300-500 install + USD 50-100 /mo", "USD 100-300 install", "IXPN/local peering keeps Nigerian traffic in-country and cheap"],
    ["Remote hands", "USD 75-150 /hr", "USD 50-100 /hr", "Budget 2-4 hr/month for reboots and cable swaps"],
    ["One-time setup (racking, patching, labelling)", "USD 2,000-5,000", "USD 1,500-4,000", "Payable at contract start"],
    ["Effective monthly (full cabinet + power + 1 cross-connect, 3-yr TCO)", "USD 2,000-3,200 /mo (approx NGN 3.0-4.8M)", "USD 1,700-2,700 /mo (approx NGN 2.6-4.1M)", "5-10x the VPS options; justified at sustained scale, not at launch"],
    [],
    ["4. Decision notes"],
    ["Backup residency is the G16 blocker: the rclone target (base + WAL) must be in-country - self-hosted MinIO on a third Lagos VPS, or an in-country S3-compatible service; config-only change."],
    ["Naira billing is part of the compliance rationale (CBN wants payment-infrastructure spend kept in-country): AFRICLOUD and Hostraha bill naira; Melbicom bills EUR; Equinix and Rack Centre bill USD."],
    ["Hyperscaler local-zone / Outposts racks inside a Nigerian Tier III facility: interpretation pending (see CBN_DATA_LOCALITY_GAP.md). Do not architect the compliance story on it."],
    ["PCI scope is unaffected by hosting location: hosted-fields checkout keeps SAQ-A scope; ASV scans and penetration tests follow the card schemes, not the CBN."],
    ["Site diversity rule: primary and standby must sit in different physical facilities; a third facility (or MinIO box) holds the backup target."],
    [],
    ["Generated by scripts/add_local_hosting_sheet.py - re-runnable; a re-run replaces this sheet in place."],
]

COLS = [("A", 52), ("B", 34), ("C", 52), ("D", 30), ("E", 24), ("F", 22), ("G", 70)]


def col_letter(i):
    s = ""
    while i >= 0:
        s = chr(ord("A") + i % 26) + s
        i = i // 26 - 1
    return s


def build_sheet_xml():
    cols = "".join(
        '<col min="%d" max="%d" width="%d" customWidth="1"/>' % (idx + 1, idx + 1, w)
        for idx, (_, w) in enumerate(COLS)
    )
    rows_xml = []
    for r, data in enumerate(GRID, start=1):
        if not data:
            rows_xml.append('<row r="%d"/>' % r)
            continue
        cells = []
        for cidx, text in enumerate(data):
            if text == "":
                continue
            ref = "%s%d" % (col_letter(cidx), r)
            cells.append(
                '<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>'
                % (ref, escape(str(text)))
            )
        rows_xml.append('<row r="%d">%s</row>' % (r, "".join(cells)))
    last_ref = "A%d" % len(GRID)
    return (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
        '<worksheet xmlns="%s" xmlns:r="%s">'
        '<dimension ref="A1:%s"/>'
        '<sheetViews><sheetView workbookViewId="0"/></sheetViews>'
        '<sheetFormatPr defaultRowHeight="15"/>'
        "<cols>%s</cols>"
        "<sheetData>%s</sheetData>"
        '<pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/>'
        "</worksheet>"
    ) % (NS, RELNS, last_ref, cols, "".join(rows_xml))


def main():
    import os
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
        # Idempotent re-run: replace the existing part.
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
                    'openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>'
                    % part)
        ct_xml = ct_xml.replace("</Types>", override + "</Types>")

        items["xl/workbook.xml"] = wb_xml.encode("utf-8")
        items["xl/_rels/workbook.xml.rels"] = rels_xml.encode("utf-8")
        items["[Content_Types].xml"] = ct_xml.encode("utf-8")
        action = "added as %s (%s)" % (part, rid)

    with zipfile.ZipFile(XLSX, "w", zipfile.ZIP_DEFLATED) as out:
        for name, data in items.items():
            out.writestr(name, data)

    # validate: zip integrity, every XML part parses, sheet is listed
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
