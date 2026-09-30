# -*- coding: utf-8 -*-
"""Build EXTERNAL_SERVICE_COST_MODEL.xlsx from editable assumption cells.

Raw-OpenXML generator (stdlib only; openpyxl segfaults on this box), same
approach as the docx builders. Workbook layout:

  sheet "Assumptions"  - every driver and unit price as an editable cell,
                         named rows so formulas reference them
  sheet "Monthly per 1k" - per-line build-up driven by Assumptions via formulas
  sheet "Scale totals" - 1k/5k/10k/50k/100k columns, per-user and per-payment
  sheet "Compliance compare" - vendor price comparison table
  sheet "Servers compare"    - hosting family comparison table

Finance edits the yellow assumption cells; every downstream total recomputes
in Excel on open (formulas, not cached numbers).
"""
import zipfile

OUT = "EXTERNAL_SERVICE_COST_MODEL.xlsx"

# ---------- minimal OpenXML helpers (same style as sync_manual_schedule) ---
NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

def col_name(i):  # 0-based -> A, B, ...
    s = ""
    i += 1
    while i:
        i, r = divmod(i - 1, 26)
        s = chr(65 + r) + s
    return s

def esc(t):
    return (t.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;"))

def cell(ref, value=None, formula=None, style=0):
    v = ""
    if formula is not None:
        v = "<f>%s</f>" % esc(formula)
    elif value is not None:
        if isinstance(value, (int, float)):
            v = "<v>%s</v>" % value
        else:
            v = "<v></v>"  # inline via t=inlineStr below
    if isinstance(value, str) and formula is None:
        return '<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>' % (ref, style, esc(value))
    return '<c r="%s" s="%d">%s</c>' % (ref, style, v)

def row(n, cells):
    return '<row r="%d">%s</row>' % (n, "".join(cells))

def sheet_xml(rows, cols=None):
    # CT_Worksheet element order matters to Excel: dimension, sheetViews,
    # cols, then sheetData. Emitting cols after sheetData makes Excel reject
    # the whole file, even though the XML is well-formed.
    colxml = ""
    if cols:
        colxml = "<cols>%s</cols>" % "".join(
            '<col min="%d" max="%d" width="%s" customWidth="1"/>' % c for c in cols)
    maxrow = max(int(r.split('"')[1]) for r in rows) if rows else 1
    return ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<worksheet xmlns="%s">'
            '<dimension ref="A1:%s%d"/>'
            '<sheetViews><sheetView workbookViewId="0"/></sheetViews>'
            '%s<sheetData>%s</sheetData>'
            '</worksheet>' % (NS, "D" if cols else "A", maxrow + 2, colxml, "".join(rows)))

# ---------- styles: 0 plain, 1 bold header, 2 editable input (yellow),
#                     3 formula/result (light gray), 4 money format -----------
STYLES = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="%s">
<numFmts count="2"><numFmt numFmtId="164" formatCode="#,##0"/><numFmt numFmtId="165" formatCode="#,##0.00"/></numFmts>
<fonts count="3">
 <font><sz val="11"/><name val="Calibri"/></font>
 <font><b/><sz val="11"/><name val="Calibri"/></font>
 <font><b/><sz val="12"/><name val="Calibri"/></font>
</fonts>
<fills count="4">
 <fill><patternFill patternType="none"/></fill>
 <fill><patternFill patternType="gray125"/></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FFFFF2CC"/><bgColor indexed="64"/></patternFill></fill>
 <fill><patternFill patternType="solid"><fgColor rgb="FFF2F2F2"/><bgColor indexed="64"/></patternFill></fill>
</fills>
<borders count="2">
 <border><left/><right/><top/><bottom/><diagonal/></border>
 <border><left style="thin"><color auto="1"/></left><right style="thin"><color auto="1"/></right><top style="thin"><color auto="1"/></top><bottom style="thin"><color auto="1"/></bottom><diagonal/></border>
</borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="6">
 <xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
 <xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0"/>
 <xf numFmtId="164" fontId="0" fillId="2" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyNumberFormat="1"/>
 <xf numFmtId="164" fontId="0" fillId="3" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyNumberFormat="1"/>
 <xf numFmtId="164" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1" applyNumberFormat="1"/>
 <xf numFmtId="165" fontId="1" fillId="3" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyNumberFormat="1"/>
</cellXfs>
</styleSheet>''' % NS

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
    sheets = "".join(
        '<sheet name="%s" sheetId="%d" r:id="rId%d"/>' % (n, i + 1, i + 1)
        for i, n in enumerate(names))
    return ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<workbook xmlns="%s" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">'
            '<sheets>%s</sheets>'
            '<calcPr calcId="124519" fullCalcOnLoad="1"/></workbook>' % (NS, sheets))

def wb_rels(n):
    rels = "".join(
        '<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>' % (i + 1, i + 1)
        for i in range(n))
    rels += ('<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>' % (n + 1))
    return ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">%s</Relationships>' % rels)

# ================= sheet 1: Assumptions (the editable model) ===============
# rows: (label, value_or_formula, unit, note, style)  style 2=editable 3=computed
ASSUMPTIONS = [
    ("DRIVERS (per 1,000 users per month)", None, "", "", 1),
    ("Active payer share", 0.40, "share", "share of registered users transacting monthly", 2),
    ("Payments per active payer", 4, "payments", "monthly", 2),
    ("Payments per 1k users", "1000*B5*B6", "payments", "= 1000 x share x frequency", 3),
    ("Average payment (naira)", 5000, "naira", "drives GMV, not opex", 2),
    ("GMV per 1k users (naira)", "B7*B8", "naira", "= payments x avg", 3),
    ("New registrations per month", 100, "users", "10% of base", 2),
    ("Tier-2 share of new users", 0.25, "share", "complete BVN+NIN in-month", 2),
    ("BVN+NIN lookups each per 1k", "B10*B11", "lookups", "same count applies to BVN and to NIN", 3),
    ("Rescreens per 1k", 50, "searches", "open high-risk cases", 2),
    ("Screening searches per 1k", "B10+B13", "searches", "onboarding + rescreens", 3),
    ("New KYB merchants per 1k", 2, "merchants", "CAC+FIRS+geo each", 2),
    ("AI sessions per 1k", 4000, "sessions", "ask-bar parse per payment + OCR/STT + assistant", 2),
    ("WhatsApp msgs per payment", 14, "messages", "receipts, status templates, service replies", 2),
    ("Free WA service msgs/month", 1000, "messages", "per business number (Meta, Oct 2026)", 2),
    ("Chargeable WA msgs per 1k", "B7*B17-B18", "messages", "= payments x msgs - free tier", 3),
    ("WA auth msgs per 1k", 200, "messages", "login-style flows", 2),
    ("SMS segments per 1k", 300, "segments", "fallback/alerts", 2),
    ("Emails per 1k", 2500, "messages", "codes, receipts, invoices", 2),
    ("Whisper minutes per 1k", 100, "minutes", "voice notes", 2),
    ("UNIT PRICES (naira)", None, "", "", 1),
    ("WA service/utility per msg", 11, "naira", "$0.0067 at 1600/dollar", 2),
    ("WA auth per msg", 23, "naira", "$0.0145", 2),
    ("SMS per segment", 4, "naira", "Termii/Africa's Talking", 2),
    ("Email per message", 0.5, "naira", "bulk SMTP relay", 2),
    ("BVN lookup", 640, "naira", "$0.40; vendor range 0.40-0.80 USD", 2),
    ("NIN lookup", 640, "naira", "$0.40", 2),
    ("Screening search", 400, "naira", "$0.25/search or vendor floor", 2),
    ("CAC query", 500, "naira", "registry API", 2),
    ("FIRS query", 200, "naira", "TIN check", 2),
    ("Geocode query", 50, "naira", "address confirmation", 2),
    ("AI session", 24, "naira", "~6k tokens mini-class vision, $0.015", 2),
    ("Whisper per minute", 9.6, "naira", "$0.006/min", 2),
]

def build_assumptions():
    rows = [row(1, [cell("A1", "Faya external service cost model - edit the yellow cells, totals recompute", style=4)]),
            row(3, [cell("A3", "Assumption", style=1), cell("B3", "Value", style=1),
                    cell("C3", "Unit", style=1), cell("D3", "Note", style=1)])]
    for i, (label, val, unit, note, st) in enumerate(ASSUMPTIONS):
        n = i + 4
        ref = "B%d" % n
        if val is None:  # section header row
            rows.append(row(n, [cell("A%d" % n, label, style=1)]))
            continue
        if isinstance(val, str) and val.startswith("="):
            val = val[1:]
        if isinstance(val, str):
            rows.append(row(n, [cell("A%d" % n, label), cell(ref, formula=val, style=3),
                                cell("C%d" % n, unit), cell("D%d" % n, note)]))
        else:
            rows.append(row(n, [cell("A%d" % n, label), cell(ref, value=val, style=st),
                                cell("C%d" % n, unit), cell("D%d" % n, note)]))
    return rows, 4 + len(ASSUMPTIONS)

# ================= sheet 2: Monthly per 1k (all formulas) ==================
# (label, driver_ref, price_ref) -- driver/price refer to Assumptions column B
LINES = [
    ("WhatsApp service/utility", "B19", "B25"),
    ("WhatsApp authentication",  "B20", "B26"),
    ("SMS",                      "B21", "B27"),
    ("Email",                    "B22", "B28"),
    ("BVN lookups",              "B12", "B29"),
    ("NIN lookups",              "B12", "B30"),
    ("Screening (onboarding + rescreens)", "B14", "B31"),
    ("CAC checks",               "B15", "B32"),
    ("FIRS TIN checks",          "B15", "B33"),
    ("Geocoding",                "B15", "B34"),
    ("AI chat/OCR sessions",     "B16", "B35"),
    ("Whisper STT",              "B23", "B36"),
    ("Telegram / Instagram / TikTok", None, None),
]

def build_monthly(total_row_out):
    rows = [row(1, [cell("A1", "Monthly variable cost per 1,000 users (all formulas - do not type over)", style=4)]),
            row(3, [cell("A3", "Line", style=1), cell("B3", "Volume", style=1),
                    cell("C3", "Rate (naira)", style=1), cell("D3", "Cost (naira)", style=1)])]
    n = 4
    for label, drv, price in LINES:
        if drv is None:
            rows.append(row(n, [cell("A%d" % n, label), cell("B%d" % n, value=0, style=0),
                                cell("C%d" % n, value=0, style=0), cell("D%d" % n, value=0, style=0)]))
        else:
            rows.append(row(n, [cell("A%d" % n, label),
                                cell("B%d" % n, formula="Assumptions!%s" % drv, style=3),
                                cell("C%d" % n, formula="Assumptions!%s" % price, style=3),
                                cell("D%d" % n, formula="B%d*C%d" % (n, n), style=3)]))
        n += 1
    tr = n
    rows.append(row(tr, [cell("A%d" % tr, "TOTAL per 1k users", style=4),
                         cell("C%d" % tr, ""), cell("D%d" % tr, formula="SUM(D4:D%d)" % (tr - 1), style=4)]))
    total_row_out.append(tr)
    checks = [
        ("Variable cost per payment", "D%d/Assumptions!B7" % tr),
        ("Variable cost per registered user", "D%d/1000" % tr),
        ("GMV per 1k users (cross-check)", "Assumptions!B9"),
        ("External cost as % of GMV", "100*D%d/Assumptions!B9" % tr),
    ]
    n = tr + 2
    for label, f in checks:
        rows.append(row(n, [cell("A%d" % n, label), cell("B%d" % n, formula=f, style=5)]))
        n += 1
    return rows

# ================= sheet 3: Scale totals ===================================
SCALES = [
    ("1k",   1000,   80000,   0.0),
    ("5k",   5000,   208000,  0.0),
    ("10k",  10000,  528000,  0.0),
    ("50k",  50000,  1600000, 0.045),
    ("100k", 100000, 4800000, 0.046),
]

def build_scale(monthly_total_ref):
    rows = [row(1, [cell("A1", "Totals at scale - infra and volume-discount cells are editable", style=4)]),
            row(3, [cell("A3", "Scale", style=1), cell("B3", "Users", style=1),
                    cell("C3", "Variable/mo (naira)", style=1), cell("D3", "Infra/mo (naira)", style=1),
                    cell("E3", "All-in/mo", style=1), cell("F3", "Per user/mo", style=1),
                    cell("G3", "Per payment", style=1), cell("H3", "Volume discount", style=1)])]
    n = 4
    for name, users, infra, disc in SCALES:
        rows.append(row(n, [
            cell("A%d" % n, name),
            cell("B%d" % n, value=users, style=2),
            cell("C%d" % n, formula="%s*B%d/1000*(1-H%d)" % (monthly_total_ref, n, n), style=3),
            cell("D%d" % n, value=infra, style=2),
            cell("E%d" % n, formula="C%d+D%d" % (n, n), style=3),
            cell("F%d" % n, formula="E%d/B%d" % (n, n), style=3),
            cell("G%d" % n, formula="E%d*1000/(Assumptions!B7*B%d)" % (n, n), style=3),
            cell("H%d" % n, value=disc, style=2),
        ]))
        n += 1
    rows.append(row(n + 1, [cell("A%d" % (n + 1),
        "Infra assumes the value-cloud/hosting mix documented in section 6 of EXTERNAL_SERVICE_COST_MODEL.md; discounts model committed-tier pricing at 50k/100k.",
        style=0)]))
    return rows

# ================= sheets 4-5: comparisons (static reference) ==============
def build_compliance():
    hdr = row(3, [cell("A3", "AML screening vendor", style=1), cell("B3", "Pricing shape", style=1),
                  cell("C3", "At 1k users (150 searches)", style=1), cell("D3", "At 10k (1.5k)", style=1),
                  cell("E3", "At 100k (15k)", style=1), cell("F3", "Tradeoff", style=1)])
    data = [
        ("ComplyAdvantage", "Starter floor $99.99-179/mo, 100 searches incl., overage above", "$179 floor (NGN 286k)", "~$180-400", "~$1.9k-3.8k", "Strongest adverse media; payment screening product new (May 2026)"),
        ("Napier AI", "Custom contract; real-time heritage", "~$1-2k floor (est.)", "custom", "custom ($2-5k/mo likely)", "Best payment-screening pedigree; one engine for both capabilities"),
        ("dilisense", "100 free/mo then EUR 0.10/call to ~0.01", "EUR 5-15 (NGN 8-24k)", "~EUR 140", "~EUR 150-1.5k", "Cheapest; thinner adverse media; weaker provenance"),
        ("OpenSanctions (self-hosted)", "Open data; infra only", "NGN 0 licence", "NGN 0", "NGN 0 (+$50-100/mo infra)", "No adverse media/PEP depth/provenance/SLA - dev only, fails CBN Baseline Standards"),
    ]
    rows = [row(1, [cell("A1", "Compliance vendor comparison (edit prices as quotes arrive; recommendation: CA primary, Napier alternate)", style=4)]), hdr]
    n = 4
    for r in data:
        rows.append(row(n, [cell("A%d" % n, r[0], style=1)] + [cell("%s%d" % (c, n), r[i + 1]) for i, c in enumerate("BCDEF")]))
        n += 1
    rows.append(row(n + 1, [cell("A%d" % (n + 1), "Identity (BVN/NIN) at 100k scale (5k lookups/mo): ninbvnportal NGN 1-2m | Dojah NGN 2.4-4.8m | Prembly NGN 4-8m | Youverify NGN 4-8m | Smile ID (full eKYC + liveness) NGN 6-16m. Hybrid: cheap record-match for Tier 2, full eKYC for Tier 3/high-risk only.", style=0)]))
    return rows

def build_servers():
    hdr = row(3, [cell("A3", "Scale", style=1), cell("B3", "Hyperscale (AWS/GCP/Azure)", style=1),
                  cell("C3", "Value cloud (Hetzner/DO/Vultr)", style=1), cell("D3", "Oracle ARM (A1)", style=1),
                  cell("E3", "NG local IaaS (Layer3)", style=1)])
    data = [
        ("1k (1 app + 1 db)", 85, 25, 12, 60),
        ("5k (2 app + db + redis/kafka)", 220, 70, 35, 150),
        ("10k (2 app, 16GB db, 3x kafka)", 500, 160, 80, 350),
        ("50k (3 app, 32GB db + replica)", 1400, 450, 220, 1000),
        ("100k (multi-AZ, 64GB db + sync replica)", 3800, 1200, 600, 2800),
    ]
    rows = [row(1, [cell("A1", "Server cost comparison (USD/month; managed Postgres on hyperscale, self-managed elsewhere)", style=4)]), hdr]
    n = 4
    for r in data:
        rows.append(row(n, [cell("A%d" % n, r[0], style=1)] +
                        [cell("%s%d" % (c, n), value=r[i + 1], style=2) for i, c in enumerate("BCDE")]))
        n += 1
    rows.append(row(n + 1, [cell("A%d" % (n + 1), "Infra is 5-10% of the all-in bill at every scale; optimize service costs first. NG local buys latency + NDPA residency; recommended shape: hyperscale credits, value cloud from 10k, NG local when residency demands.", style=0)]))
    return rows

# ================= write the workbook ======================================
def main():
    arows, _ = build_assumptions()
    total_row = []
    mrows = build_monthly(total_row)
    tr = total_row[0]
    srows = build_scale("'Monthly per 1k'!D%d" % tr)
    crows = build_compliance()
    vrows = build_servers()

    names = ["Assumptions", "Monthly per 1k", "Scale totals", "Compliance compare", "Servers compare"]
    sheets = [sheet_xml(arows, cols=[(1, 34, 30), (2, 14, 12), (3, 12, 10), (4, 60, 46)]),
              sheet_xml(mrows, cols=[(1, 36, 32), (2, 12, 10), (3, 12, 10), (4, 16, 14)]),
              sheet_xml(srows, cols=[(1, 8, 7), (2, 10, 9), (3, 20, 18), (4, 16, 14), (5, 16, 14), (6, 12, 11), (7, 12, 11), (8, 14, 12)]),
              sheet_xml(crows, cols=[(1, 22, 20), (2, 40, 34), (3, 24, 22), (4, 16, 14), (5, 18, 16), (6, 50, 44)]),
              sheet_xml(vrows, cols=[(1, 40, 36), (2, 24, 22), (3, 26, 24), (4, 18, 16), (5, 24, 22)])]

    ct = CONTENT_TYPES % "".join(
        '<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>' % (i + 1)
        for i in range(len(sheets)))
    core = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
            '<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" '
            'xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Faya external service cost model</dc:title>'
            '<dc:creator>Faya</dc:creator></cp:coreProperties>')
    app = ('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
           '<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"/>')

    with zipfile.ZipFile(OUT, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", ct)
        z.writestr("_rels/.rels", RELS)
        z.writestr("docProps/core.xml", core)
        z.writestr("docProps/app.xml", app)
        z.writestr("xl/workbook.xml", workbook_xml(names))
        z.writestr("xl/_rels/workbook.xml.rels", wb_rels(len(sheets)))
        z.writestr("xl/styles.xml", STYLES)
        for i, s in enumerate(sheets):
            z.writestr("xl/worksheets/sheet%d.xml" % (i + 1), s)
    print("written", OUT)

if __name__ == "__main__":
    main()
